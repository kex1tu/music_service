# Automatic Audio Resolver (SoundCloud Candidate Ranking & Version Switcher)

Архитектурный план реализации сервиса автоматического подбора доцензурных версий треков на SoundCloud при загрузке из каталогов (Deezer / iTunes), исключения цензурных версий и эдитов, сохранения кандидатов в SQLite с безопасным кэшированием и ручного переключения версий в UI.

---

## User Review Required

> [!IMPORTANT]
> **Финальные инженерные решения и исправления, включённые в план:**
>
> 1. **Исправление `remixRegex` (поддержка `Original Mix`):**
>    - В `remixRegex` остаются строго только `remix|ремикс`:
>      ```go
>      var remixRegex = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(remix|ремикс)(?:$|[^\p{L}\p{N}])`)
>      ```
>    - Слова `mix` и `edit` исключены из регулярного выражения. Это предотвращает ложную отбраковку названий вроде `Track (Original Mix)` при обычной цели. `radio edit` уже находится в `badRegex`.
>
> 2. **Исключение ложных срабатываний стоп-слов из-за метаданных артиста/трека (Кейс `Clean Bandit`):**
>    - На SoundCloud треки часто имеют вид `Artist - Title`. Если артист называется `Clean Bandit` или целевой трек содержит слово `Clean`, `badRegex` мог ошибочно счесть трек цензурным.
>    - **Решение:** Перед проверкой `badRegex` из строки кандидата исключаются совпадения, которые уже присутствуют в целевых метаданных (`targetArtist` или `targetTitle`). Если слово `clean` входит в состав имени артиста, оно игнорируется в стоп-листе.
>
> 3. **Непротиворечивая и строгая иерархия статусов:**
>    - Присвоение статусов кандидатам выполняется по строгому однозначному порядку:
>      1. Маркер-спаситель есть + эталона нет (`referenceMissing = true`) $\to$ **`marked`**;
>      2. Маркер-спаситель есть + длительность в допуске ($\le 2$ с от любого валидного релиза) $\to$ **`marked`**;
>      3. Маркер-спаситель есть + длительность разошлась ($> 2$ с) при наличии эталона $\to$ **`unverified`**;
>      4. Маркера нет + длительность в допуске ($\le 2$ с) $\to$ **`likely`**;
>      5. Маркера нет + длительность разошлась на 3–5 с $\to$ **`unverified`** (сохраняется как близкая альтернатива).
>    - **Бейджи в UI:**
>      - `marked`: **«Помечена в названии как оригинал»**
>      - `likely`: **«Та же запись (хронометраж совпал)»**
>      - `unverified`: **«Версия не подтверждена эталоном»**
>      - `manual`: **«Выбрано вручную»**
>      - `link_dead`: **«Ссылка недоступна на SoundCloud»**
>
> 4. **Корректная обработка `link_dead` и ротация кандидатов при 404:**
>    - При 404/410 для автоматической записи (`is_manual = 0`):
>      - Битая ссылка удаляется из списка `candidates_json`.
>      - Если в `candidates_json` остались альтернативные кандидаты, следующий лучший URL автоматически назначается в `selected_url`, обновляется статус и пересчитывается длительность.
>      - Только если кандидатов больше не осталось, выставляется `selected_url = ''` и `status = 'link_dead'`.
>    - Поведение `GetSoundCloudCache`:
>      - Для автоматических записей (`is_manual = 0`): если `status = 'link_dead'` или `selected_url = ''`, запись считается **кэш-промахом (MISS)**, инициируя новый поиск.
>      - Для ручных записей (`is_manual = 1`): статус `link_dead` возвращается как хит, чтобы UI отобразил предупреждение пользователю без несанкционированной подмены.
>
> 5. **Фоллбэк на YouTube при отсутствии кандидатов:**
>    - Если поиск на SoundCloud не вернул подходящих кандидатов (все отбракованы или пустая выдача), пайплайн прозрачно переключается на существующий стандартный поиск в YouTube (`matcher.go`). Существующий YouTube-пайплайн остаётся полностью работоспособным.
>
> 6. **Надёжное определение длительности `candDuration` при `switch-version`:**
>    - Если кандидат найден в `candidates_json`, его длительность берётся оттуда.
>    - Если кэша нет (прямой переход или `candidates_json` пуст), после скачивания аудио во временный файл длительность измеряется через `ffprobe` перед записью в SQLite (колонка `duration NOT NULL`).
>
> 7. **Штраф дистрибьюторов SoundCloud вместо Topic:**
>    - Понятие «Topic» специфично для YouTube. На SoundCloud официальные цензурные копии выгружают дистрибьюторы. В конфигурацию добавляется список `UNCENSORED_DISTRIBUTORS` (по умолчанию: `distrokid,believe,zvonko,the orchard,tunecore,warner,universal,sony`). При совпадении имени аплоадера начисляется штраф (-20).
>
> 8. **Парсинг даты и обновление TTL:**
>    - При пустой или нераспарсенной дате загрузки бонус за ранний релиз **не начисляется** (0 баллов).
>    - `SaveSoundCloudCache` обновляет `checked_at = CURRENT_TIMESTAMP`, продлевая TTL. Параметр в SQL формируется как `printf('-%d hours', ?)`.

---

## 1. Цель и границы задачи (Objective & Scope)

### 1.1. Проблема
При загрузке музыки из каталогов (Deezer / iTunes) многие популярные треки отдаются в цензурных версиях. На SoundCloud часто доступны оригиналы, однако выдача перегружена ремиксами, радио-эдитами и эдитами скорости (slowed/sped up).

### 1.2. Целевой результат
1. **Интеллектуальный ранжировщик кандидатов (SoundCloud Resolver):**
   - Сбор списка валидных хронометражей всех релизов альбома/сингла в Deezer/iTunes (`validDurations []int`).
   - Очистка названия от спасителей с пробелом (Anti-Glue) и проверка стоп-слов с исключением артиста/названия (Anti-Collision).
   - Скоринг с допуском $\pm 2$ сек, штрафом за дистрибьюторов (-20), бонусом за дату до 2023 года (+25, только при валидной дате) и сохранением альтернатив (3–5 сек).
   - Прозрачный фоллбэк на YouTube при отсутствии кандидатов на SoundCloud.
2. **Кэш версий в SQLite:** Таблица `soundcloud_uncensored_cache` с поддержкой TTL (30 дней), безопасным UPSERT, ротацией при 404 и защитой ручных закреплений.
3. **Интерфейс выбора версий (No-Build Alpine.js):** Отображение бейджей («Помечена в названии как оригинал», «Та же запись») и выпадающий список «Версии (N)» с ручным переключением в 1 клик.

### 1.3. Границы ответственности
- **In-Scope:**
  - База данных SQLite (таблица `soundcloud_uncensored_cache`, безопасный UPSERT, ротация кандидатов).
  - Клиенты Deezer и iTunes (сбор всех уникальных хронометражей `validDurations`).
  - Пакет скоринга `internal/services/resolver/uncensored_matcher.go`.
  - Эндпоинт `POST /api/downloads/tracks/{trackId}/switch-version` с атомарной заменой файла и `ffprobe`-замером длительности.
  - No-build фронтенд (Alpine.js + Tailwind) в `downloader.html` и `downloader.js`.
- **Out-of-Scope:**
  - Python / Whisper (строго соблюдается инвариант **Pure Go / Zero-Python** из `AGENTS.md`).
  - Изменение битрейта (MP3 320 kbps CBR фиксирован инвариантом проекта).

---

## 2. Анализ текущего состояния (Current State Analysis)

| Компонент / Файл | Текущее состояние | Что необходимо изменить |
|---|---|---|
| [`internal/config/config.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/config/config.go) | Параметры загрузок | Добавить `UncensoredDurationTolerance` (2), `UncensoredCandidatesLimit` (10), `UncensoredPreferYear` (2023), `UncensoredCacheTTLHours` (720), `UncensoredDistributors` (список дистрибьюторов) |
| [`internal/db/schema.sql`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/db/schema.sql) | Таблицы `tracks`, `downloads` | Добавить таблицу `soundcloud_uncensored_cache` с `selected_url TEXT NOT NULL DEFAULT ''` |
| [`internal/db/queries.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/db/queries.go) | Базовые SQL-операции | Добавить `GetSoundCloudCache` (с проверкой `link_dead`), `SaveSoundCloudCache` (обновляет `checked_at`), `PinSoundCloudVersion`, `InvalidateSoundCloudURL` |
| [`internal/services/resolver/client_deezer.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/client_deezer.go) | `SearchTrack` возвращает 1 трек | Добавить `SearchTrackVersions` для сбора массива хронометражей всех релизов |
| [`internal/services/resolver/matcher.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/matcher.go) | Линейный поиск SoundCloud | Интегрировать проверку кэша, запуск `RankCandidates`, ротацию кандидатов и фоллбэк на YouTube |
| [`internal/api/handlers/download_handlers.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/download_handlers.go) | Нет смены версии | Реализовать `SwitchTrackVersionHandler` с валидацией `trackId`, вычислением `cacheKey`, замером `ffprobe` и атомарной заменой |
| [`web/templates/components/downloader.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/downloader.html) | Статичный вид трека в очереди | Добавить бейджи оригинальности и выпадающее меню «Версии (N)» с кнопкой смены |

---

## 3. Архитектура решения и алгоритмы (Architecture & Algorithms)

### 3.1. Алгоритм фильтрации (Anti-Glue & Anti-Collision)
```go
var saviorRegex = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(old\s+ver(?:sion)?|старая\s+версия|до\s+цензуры|без\s+цензуры|uncensored|uncut|original\s+version|оригинал)(?:$|[^\p{L}\p{N}])`)
var badRegex = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(clean|radio\s+edit|censored|с\s+цензурой|цензурная|sped\s+up|slowed)(?:$|[^\p{L}\p{N}])`)
var remixRegex = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(remix|ремикс)(?:$|[^\p{L}\p{N}])`)
```

**Функция:** `ExtractSaviorsAndCheckBad(title, targetArtist, targetTitle string) (hasSavior bool, isBad bool)`:
1. `isRemixTarget := remixRegex.MatchString(targetTitle)` — определяем, является ли целевой трек ремиксом.
2. Поиск спасителя: `m := saviorRegex.FindStringSubmatchIndex(title)`.
   - Если найден: `hasSavior = true`, а спаситель вырезается с гарантированным пробелом: `remainder = strings.TrimSpace(title[:m[2]] + " " + title[m[3]:])`.
3. Защита от коллизий артиста (Кейс `Clean Bandit`):
   - Если в `remainder` найдено стоп-слово из `badRegex`: проверяем, не входит ли найденное слово в состав `targetArtist` или `targetTitle`. Если входит (например, слово `clean` в артисте `Clean Bandit`) $\to$ это вхождение игнорируется.
   - Если после этого в остатке строки остались нецелевые стоп-слова $\to$ `isBad = true`.
4. Проверка ремикса:
   - Если `!isRemixTarget && remixRegex.MatchString(remainder)` $\to$ `isBad = true`.
   - Если `isRemixTarget` $\to$ слово `remix` разрешено.
   - Слова `Original Mix`, `Club Mix` не затрагиваются, так как `mix` исключён из регулярки.

### 3.2. Иерархия статусов кандидата
Для каждого прошедшего фильтрацию кандидата вычисляется минимальное расхождение с эталонными длительностями:
$\Delta_{min} = \min_{ref \in validDurations} |duration - ref|$.

Статус выставляется по строгому каскаду:
1. `hasSavior && referenceMissing` $\to$ **`marked`**
2. `hasSavior && \Delta_{min} \le 2` $\to$ **`marked`**
3. `hasSavior && \Delta_{min} > 2` $\to$ **`unverified`**
4. `!hasSavior && \Delta_{min} \le 2` $\to$ **`likely`**
5. `!hasSavior && 3 \le \Delta_{min} \le 5` $\to$ **`unverified`** (добавляется как альтернатива)

### 3.3. Скоринг и взвешивание
$$\text{Score} = (\text{hasSavior} \times 50) + (\text{validDate \&\& year} < 2023 \times 25) - (\text{isDistributor} \times 20) - (\Delta_{early} \times 5)$$
- Если дата загрузки отсутствует или не распарсилась $\to$ бонус за год равен **0** (не начисляется).
- `isDistributor`: проверяется вхождение имени автора канала в список `UNCENSORED_DISTRIBUTORS`.
- Все слагаемые формулы логируются для прозрачной калибровки.

---

## 4. Пошаговая декомпозиция реализации (Step-by-Step Implementation)

### Этап 1: Конфигурация и База Данных

#### [MODIFY] [config.go](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/config/config.go)
```go
UncensoredDurationTolerance int      `env:"UNCENSORED_DURATION_TOLERANCE" envDefault:"2"`
UncensoredCandidatesLimit   int      `env:"UNCENSORED_CANDIDATES_LIMIT" envDefault:"10"`
UncensoredPreferYear        int      `env:"UNCENSORED_PREFER_YEAR" envDefault:"2023"`
UncensoredCacheTTLHours     int      `env:"UNCENSORED_CACHE_TTL_HOURS" envDefault:"720"`
UncensoredDistributors      []string `env:"UNCENSORED_DISTRIBUTORS" envSeparator:"," envDefault:"distrokid,believe,zvonko,the orchard,tunecore,warner,universal,sony"`
```
Обновить [`.env.example`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/.env.example).

#### [MODIFY] [schema.sql](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/db/schema.sql)
```sql
CREATE TABLE IF NOT EXISTS soundcloud_uncensored_cache (
    cache_key TEXT PRIMARY KEY,
    track_title TEXT NOT NULL,
    artist_name TEXT NOT NULL,
    selected_url TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,              -- marked, likely, unverified, manual, link_dead
    duration INTEGER NOT NULL,
    is_manual BOOLEAN DEFAULT 0,
    candidates_json TEXT NOT NULL DEFAULT '[]',
    checked_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_sc_cache_key ON soundcloud_uncensored_cache(cache_key);
```

#### [MODIFY] [queries.go](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/db/queries.go)
- `GetSoundCloudCache(ctx context.Context, key string, ttlHours int) (*SoundCloudCacheEntry, error)`:
  ```sql
  SELECT cache_key, track_title, artist_name, selected_url, status, duration, is_manual, candidates_json, checked_at, updated_at
  FROM soundcloud_uncensored_cache
  WHERE cache_key = ? AND (is_manual = 1 OR checked_at > datetime('now', printf('-%d hours', ?)));
  ```
  *Логика:* Если `is_manual = 0` и `(status = 'link_dead' OR selected_url = '')` $\to$ метод возвращает `nil, nil` (трактуется как MISS, инициируя новый поиск). Если `is_manual = 1` $\to$ запись возвращается всегда.
- `SaveSoundCloudCache(ctx context.Context, entry SoundCloudCacheEntry) error`:
  Вставка/обновление с обновлением `checked_at = CURRENT_TIMESTAMP`, `updated_at = CURRENT_TIMESTAMP` с условием `WHERE soundcloud_uncensored_cache.is_manual = 0`.
- `PinSoundCloudVersion(ctx context.Context, key, artist, title, url string, duration int) error`:
  ```sql
  INSERT INTO soundcloud_uncensored_cache (
      cache_key, artist_name, track_title, selected_url, duration, status, is_manual, candidates_json, updated_at
  ) VALUES (?, ?, ?, ?, ?, 'manual', 1, '[]', CURRENT_TIMESTAMP)
  ON CONFLICT(cache_key) DO UPDATE SET
      selected_url = excluded.selected_url,
      duration = excluded.duration,
      is_manual = 1,
      status = 'manual',
      updated_at = CURRENT_TIMESTAMP;
  ```
- `InvalidateSoundCloudURL(ctx context.Context, key string, failedURL string) (*SoundCloudCacheEntry, error)`:
  Загружает запись. Удаляет `failedURL` из `candidates_json`.
  Если остались другие кандидаты $\to$ выбирает следующий, обновляет `selected_url` и `status`.
  Если кандидатов не осталось $\to$ выставляет `selected_url = ''`, `status = 'link_dead'`.

---

### Этап 2: Сбор эталонных хронометражей

#### [MODIFY] [client_deezer.go](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/client_deezer.go)
- Добавить поле `Preview string` в `DeezerTrack`.
- Реализовать метод `SearchTrackVersions(ctx context.Context, artist, title string) ([]DeezerTrack, error)`: поиск в каталоге Deezer и возврат всех найденных версий трека для извлечения уникальных длительностей (`validDurations []int`).

#### [MODIFY] [client_itunes_track.go](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/client_itunes_track.go)
- Метод `SearchTracksWithCountry(ctx context.Context, query, country string) ([]ItunesTrack, error)` для расширения пула хронометражей через Apple Music US.

---

### Этап 3: Движок сопоставления и ранжирования

#### [NEW] [uncensored_matcher.go](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/uncensored_matcher.go)
1. **Канонический ключ:** `BuildCacheKey(isrc, artist, title string) string`.
2. **`ExtractSaviorsAndCheckBad`:** алгоритм Anti-Glue со вставкой пробела и Anti-Collision с проверкой вхождения в `targetArtist` и `targetTitle`.
3. **`DetermineValidDurations`:** возвращает массив секунд `validDurations []int`, длительность ранней версии `earlyDur int` и флаг `referenceMissing bool`.
4. **`RankCandidates`:** скоринг, учёт даты (бонус только при валидном парсинге), штраф за дистрибьюторов, выбор лучшего кандидата и сохранение альтернатив (включая кандидатов с дельтой 3–5 сек и кандидатов со спасителем при дельте >2 сек со статусом `unverified`).

#### [MODIFY] [matcher.go](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/matcher.go)
- Расширить `YtCandidate` полями `UploadDate string`, `Timestamp int64`, `Uploader string`.
- Интегрировать проверку кэша по `BuildCacheKey`.
- Если на SoundCloud подходящие кандидаты не найдены $\to$ **прозрачный фоллбэк на существующий поиск в YouTube**.

---

### Этап 4: Пайплайн загрузки и API переключения версий

#### [MODIFY] [download_handlers.go](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/download_handlers.go)
- Эндпоинт `POST /api/downloads/tracks/{trackId}/switch-version`:
  - Вход: JSON `{ "target_url": "https://soundcloud.com/..." }`.
  - По `trackId` получает метаданные трека, вычисляет `cacheKey = BuildCacheKey(track.ISRC, track.Artist, track.Title)`.
  - Извлекает метаданные кандидата из `candidates_json` для получения длительности. Если список пуст или кандидат не найден $\to$ после скачивания длительность замеряется через `ffprobe`.
  - Скачивает аудиопоток, перекодирует в MP3 320 kbps CBR, переносит ID3v2.4 теги, атомарно заменяет файл (`os.Rename`).
  - Вызывает `PinSoundCloudVersion(ctx, cacheKey, track.Artist, track.Title, req.TargetURL, measuredDuration)`.

#### [MODIFY] [pipeline.go](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/downloader/pipeline.go)
- Обработка 404/410:
  - Если ссылка ручная (`is_manual = 1`): выставить `status = 'link_dead'`, залогировать предупреждение, **не подменять ссылку автоматически**.
  - Если ссылка автоматическая (`is_manual = 0`): вызвать `InvalidateSoundCloudURL`, которая выбирает следующего кандидата из списка, и попытаться скачать его.

---

### Этап 5: Фронтенд (Интерфейс загрузчика)

#### [MODIFY] [downloader.html](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/downloader.html) & [downloader.js](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/downloader/downloader.js)
1. **Точные формулировки бейджей:**
   - 🟢 `marked`: «Помечена в названии как оригинал»
   - 🟡 `likely`: «Та же запись (хронометраж совпал)»
   - ⚪ `unverified`: «Версия не подтверждена эталоном»
   - 🔵 `manual`: «Выбрано вручную»
   - 🔴 `link_dead`: «Ссылка недоступна на SoundCloud»
2. **Выпадающее меню альтернатив:**
   - Кнопка «Версии (N)» открывает список кандидатов (название, автор, длительность, дельта, дата).
   - Выбор версии отправляет запрос на `switch-version`.

---

## 5. Анализ рисков и краевых случаев (Risks & Guardrails)

| Риск | Описание | Стратегия митигации |
|---|---|---|
| **Коллизия стоп-слова с именем артиста (`Clean Bandit`)** | Слово `Clean` в имени группы приводит к отбраковке трека | Проверка совпадения со словами из `targetArtist` и `targetTitle` перед маркировкой `isBad`. |
| **Отбраковка `Original Mix`** | Слово `mix` ошибочно трактуется как сторонний эдит | Регулярка `remixRegex` ограничена только `remix|ремикс`. |
| **Склейка слов при вырезании спасителя** | `Clean(old version)Clean` превращается в `CleanClean` | Обязательная вставка пробела: `title[:m[2]] + " " + title[m[3]:]`. |
| **«Мёртвая» ссылка 404 как кэш-хит** | Пустой `selected_url` отдается клиенту | Записи с `status = 'link_dead'` при `is_manual = 0` трактуются как промах кэша. |
| **Несоответствие сигнатуры `PinSoundCloudVersion`** | Ошибка передачи аргументов | Последним параметром строго передаётся `duration int`, замеренная через `ffprobe`. |
| **Недоступность SoundCloud** | Потеря возможности скачивания треков | Автоматический прозрачный фоллбэк на поиск в YouTube при отсутствии кандидатов на SoundCloud. |

---

## 6. План тестирования и верификации (Verification Matrix)

### 6.1. Модульные тесты `uncensored_matcher_test.go`
1. **Коллизии артистов и названий:**
   - `"Clean Bandit - Symphony (до цензуры)"` с `targetArtist = "Clean Bandit"` $\to$ `hasSavior = true`, `isBad = false`.
   - `"Track (Original Mix)"` при обычной цели $\to$ `isBad = false`.
   - `"Track (Remix)"` при `targetTitle = "Track (Remix)"` $\to$ `isBad = false`.
   - `"Track (Remix)"` при обычном треке $\to$ `isBad = true`.
2. **Anti-Glue разделители:**
   - `"Clean(old version)Clean"` $\to$ спаситель вырезан, `Clean Clean` на остатке бракует трек (`isBad = true`).
3. **Регистр и спасители:**
   - `"Scally Milano - Заберу (ДО ЦЕНЗУРЫ)"` $\to$ `hasSavior = true`.
   - `"Track [Old version uncut]"` $\to$ `hasSavior = true`.
4. **Хронометраж и статусы:**
   - Спаситель + совпадение $\le 2$с $\to$ `marked`.
   - Спаситель + эталона нет $\to$ `marked`.
   - Спаситель + дельта 4с $\to$ `unverified` (сохраняется в альтернативах).
   - Нет спасителя + совпадение $\le 2$с $\to$ `likely`.
   - Нет спасителя + дельта 4с $\to$ `unverified` (сохраняется как близкая альтернатива).

### 6.2. Тесты базы данных (`db_test.go`)
1. **TTL:** запись старше 30 дней с `is_manual = 0` не возвращается (протухла), а с `is_manual = 1` возвращается всегда.
2. **`link_dead`:** автоматическая запись с `status = 'link_dead'` возвращает `nil` (MISS).
3. **Ротация 404:** `InvalidateSoundCloudURL` удаляет битый URL и переключается на следующий лучший из `candidates_json`.
4. **Защита ручного пина:** автоматический резолвер не перезаписывает строку с `is_manual = 1`.
5. **Безопасный UPSERT:** вставка новой строки передаёт `'[]'`, повторное закрепление не затирает ранее сохранённый `candidates_json`.

### 6.3. Интеграционная сборка и тесты
```bash
go test -v -race ./internal/services/resolver/...
go test -v -race ./internal/db/...
go test -race ./...
go build -ldflags="-s -w" -o bin/music-manager cmd/server/main.go
```

### 6.4. Ручная верификация на слух
- Скачивание трека (*Scally Milano — Заберу*).
- Проверка отображения бейджа «Помечена в названии как оригинал».
- Проверка переключения версии в выпадающем списке «Версии (N)».
- Прослушивание результата в плеере на слух $\to$ проверка отсутствия цензурных заглушек.
