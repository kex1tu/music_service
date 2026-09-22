# План: Устранение отображения исполнителей через `\\` в Navidrome, стандартизация ID3v2.4 и кардинальное улучшение логики Аудита тегов

**Дата:** 22 сентября 2026  
**Статус:** `PROPOSED` (Ожидает подтверждения пользователя)  
**Целевые компоненты:** `internal/id3/`, `internal/services/downloader/`, `internal/services/organizer/`, `internal/services/scanner/`, `internal/api/handlers/`, `web/templates/components/audit_tags.html`, `web/static/js/audit/tag_audit.js`.

---

## 1. Цель и границы задачи (Objective & Scope)

### 1.1. Проблема
1. **Отображение исполнителей через `\\` в Navidrome:**  
   Исполнители треков (`TPE1`) и релизов/альбомов (`TPE2`) отображаются в Navidrome как единая строка со слешами (например, `Call Me Karizma \\ Lisp`, `SLAVA MARLOW \\ MORGENSHTERN`), так как в фрейм `TPE2` записывался плоский текст с литеральным `\\`, а старые `TXXX:ARTISTS` не очищались.
2. **Проблемная логика в разделе Аудит тегов:**  
   Текущая логика аудита метаданных вызывает раздражение и работает некорректно:
   - **Ложные срабатывания (False Positives):** Альбомы со статусом "Single" или "Singles" считаются дефектом `missing_album`, и система пытается переименовать альбом в имя трека; любые дефисы в названиях треков (например, "Spider-Man", "Part 1 - Acoustic", "Song - Remix") ложно определяются как `artist_in_title`; треки с `(feat. ...)` в названии помечаются как `unparsed_feat`, даже если соавтор уже записан в исполнителях.
   - **Слепая зона к реальным дефектам ID3:** Аудит не проверяет соответствие физических тегов стандарту ID3v2.4 — не видит литеральные `\\`, конфликтующие `TXXX` и мусор в виде номеров треков в названии ("01. Название").
   - **Опасная кнопка «Исправить все дефекты»:** Массово и вслепую перезаписывает теги без превью; в цикле выполняет синхронные внешние сетевые запросы к Yandex и YouTube Music, приводя к зависаниям и таймаутам.
   - **Ошибочная логика онлайн-сверки с каталогами:** Сортирует варианты по нелепому правилу «где больше всего артистов» — в результате каверы, трибьюты и сборники с 10 участниками побеждают оригинальные треки; при применении тегов не обновляется `AlbumArtist`.
   - **Отсутствие гранулярности в UI:** Нет чекбоксов выбора нескольких треков, нет разделения на 100% безопасные исправления и исправления, требующие проверки человеком, нет группировки по альбомам.

### 1.2. In-Scope (что входит в задачу)
1. **Исправление сериализации ID3v2.4 (`internal/id3/writer.go`):**
   - Перевод фрейма `TPE2` (Album Artist) на `writeMultiTextFrame` с нуль-разделителем `\x00`.
   - Очистка конфликтующих фреймов `TXXX:ARTISTS`, `TXXX:ALBUMARTISTS`, `TXXX:Artist`, `TXXX:AlbumArtist`.
2. **Очистка метаданных при транскодировании (`internal/services/downloader/media_processor.go`):**
   - Флаг `-map_metadata -1` в `ffmpeg`.
3. **Нормализация артистов при индексации (`internal/services/scanner/indexer.go`):**
   - Извлечение основного исполнителя трека (`resolver.ExtractPrimaryArtist`) для связей в таблице `artists`.
4. **Переработка детектора дефектов (`internal/api/handlers/tag_audit_handlers.go`):**
   - Устранение ложных срабатываний для Single, дефисов в названиях и существующих соавторов.
   - Добавление детекции реальных дефектов: литеральный `\\` в ID3 (Navidrome bug), префикс номера трека в названии ("01. Song"), суффикс `- Topic`.
   - Четкая классификация дефектов: `SAFE_AUTOFIX` (100% безопасные) и `REVIEW_NEEDED` (требующие подтверждения).
5. **Безопасное пакетное исправление (Safe Batch Fix) и пакетный выбор (Batch Selection):**
   - Кнопка «Исправить безопасные дефекты» — быстрое локальное исправление без сетевых запросов.
   - Чекбоксы в таблице треков для выборочного применения исправлений.
6. **Умная онлайн-сверка с каталогами (Smart Catalog Match):**
   - Композитный скор релевантности (Match Score 0–100%) по названию (Levenshtein), артисту и длительности (±3–5 сек) вместо правила «где больше артистов».
   - Корректное обновление `AlbumArtist` при применении каталожных данных.
7. **Миграция существующей медиатеки:**
   - Пакетное перетегирование существующих треков с записью валидных `\x00` разделителей для мгновенного подхвата Navidrome.

### 1.3. Out-of-Scope (что явно не входит)
- Изменение конфигурации стороннего сервиса Navidrome (`mappings.yaml` внутри Docker контейнера Navidrome).
- Добавление внешних сборщиков фронтенда (Node.js/npm) — только чистый Alpine.js + Tailwind CDN по `AGENTS.md`.

---

## 2. Анализ текущего состояния и первопричин (Current State Analysis)

| Компонент / Файл | Текущее поведение | Выявленный дефект / Ограничение |
|---|---|---|
| [writer.go](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/id3/writer.go#L52-L56) | `writeTextFrame(&framesBuf, "TPE2", tags.AlbumArtist)` | Пишет плоскую строку с `\\`. Navidrome не считает `\\` разделителем и показывает артиста релиза с обратными слешами. |
| [writer.go](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/id3/writer.go#L101-L115) | Копирует все сырые `TXXX` фреймы | Старые `TXXX:ARTISTS` с `\\` сохраняются и имеют приоритет в TagLib над `TPE1`. |
| [media_processor.go](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/downloader/media_processor.go#L84-L95) | `ffmpeg` без `-map_metadata -1` | Контейнерные метаданные из YouTube/SoundCloud переносятся в выходной MP3. |
| [indexer.go](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/scanner/indexer.go#L61) | `s.getOrCreateArtist(artistName)` | В таблицу `artists` базы `library.db` добавляются составные имена вида `Call Me Karizma\\Lisp`. |
| [tag_audit_handlers.go](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/tag_audit_handlers.go#L68-L98) | `analyzeTrackIssues` | **Ложные срабатывания:** альбом `single` считается ошибкой; дефис в названии трека ложно считается дублем артиста; `(feat. ...)` помечается даже при наличии в артистах. |
| [tag_audit_handlers.go](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/tag_audit_handlers.go#L349-L394) | `FixAllTagIssues` | **Слепой массовый оверрайт:** выполняет синхронные сетевые запросы к Yandex/YouTube в цикле по всем трекам, зависает и ломает теги. |
| [tag_audit_handlers.go](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/tag_audit_handlers.go#L610-L630) | `maxArtistsCount` в сверке | Отдает наивысший приоритет варианту с максимальным числом артистов (сборники и трибьюты побеждают оригиналы). |
| [audit_tags.html](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/audit_tags.html) | UI таблицы аудита | Нет чекбоксов выбора, нет градации дефектов по опасности, плоский неструктурированный список. |

---

## 3. Архитектурное решение и обоснование (Architecture & Rationale)

```
                            Входные файлы медиатеки / База данных
                                              │
                                              ▼
                    ┌──────────────────────────────────────────────────┐
                    │      Новый модуль классификации дефектов         │
                    │        internal/api/handlers/tag_audit           │
                    └─────────────────────────┬────────────────────────┘
                                              │
                   ┌──────────────────────────┴──────────────────────────┐
                   ▼                                                     ▼
      [Категория: SAFE_AUTOFIX]                             [Категория: REVIEW_NEEDED]
  1. Литеральный \\ в TPE1/TPE2 (Navidrome)              1. Нераспарсенные соавторы (нет в TPE1)
  2. Номер трека в названии ("01. Song")                 2. Артист в названии (строгая сверка)
  3. Клиповый мусор ("(Official Video)")                 3. Неизвестный альбом / Пропущенный год
  4. Канал "- Topic" в артисте                                           │
  5. Лишние концевые пробелы                                             │
                   │                                                     ▼
                   ▼                                        ┌─────────────────────────┐
      ┌─────────────────────────┐                           │   Умная онлайн-сверка   │
      │    Кнопка в UI:         │                           │   (Smart Catalog Match) │
      │ «Исправить безопасные»  │                           │ Match Score по Title +  │
      │ Мгновенно, без сети,    │                           │ Artist + Duration (±3с) │
      │ 100% детерминировано    │                           └────────────┬────────────┘
      └────────────┬────────────┘                                        │
                   │                                                     ▼
                   └──────────────────────────┬──────────────────────────┘
                                              │
                                              ▼
                                ┌──────────────────────────┐
                                │   ID3v2.4 Engine Writer  │
                                │ TPE1 -> \x00 (UTF-8)     │
                                │ TPE2 -> \x00 (UTF-8)     │
                                │ Удаление TXXX:ARTISTS    │
                                └─────────────┬────────────┘
                                              │
                                              ▼
                                ┌──────────────────────────┐
                                │   Navidrome (TagLib)     │
                                │  "Artist 1 • Artist 2"   │
                                └──────────────────────────┘
```

### 3.1. Детальное обоснование принятых решений («Почему именно так»)

1. **Почему строгая проверка `AreArtistsEquivalent` для `artist_in_title`?**  
   Музыкальные треки часто содержат дефисы: «Spider-Man», «Intro - Skit», «Song - Remix», «Part 1 - Acoustic». Считать любой дефис признаком дублирования артиста — грубая ошибка. Проверка совпадения левой части с артистом трека полностью устраняет ложные срабатывания.
2. **Почему соавторы `feat.` в названии не всегда дефект?**  
   Многие официальные релизы на стримингах намеренно сохраняют `(feat. ...)` в названии. Если соавтор **уже присутствует** во фрейме `TPE1`, теги полностью согласованы и не требуют принудительного изменения. Ошибка фиксируется только тогда, когда соавтор из названия отсутствует в метаданных исполнителей.
3. **Почему отказ от сетевых запросов в массовом исправлении?**  
   Синхронные сетевые вызовы в цикле по сотням треков приводят к блокировке HTTP-обработчика, ошибкам `context deadline exceeded` и бану по IP (HTTP 429). Массовый автофикс должен быть **строго локальным и детерминированным**. Онлайн-сверка предназначена для точечной работы и сложных случаев.
4. **Почему композитный Match Score вместо «где больше артистов»?**  
   Правило «где больше артистов» катастрофично: если для сольного трека существует ремикс-сборник или концертный трибьют с 8 исполнителями, текущая система выбирала именно его. Композитная формула (45% сходство названия + 35% совпадение артиста + 20% совпадение длительности ±3–5 сек) безошибочно находит оригинальную запись.
5. **Почему чекбоксы и гранулярный выбор в UI?**  
   Пользователь должен иметь полный контроль: либо исправить 100% безопасные дефекты одной кнопкой, либо выбрать конкретные треки галочками и нажать «Применить к выбранным».

---

## 4. Пошаговая декомпозиция реализации (Step-by-Step Implementation)

### Этап 1: Исправление ID3v2.4 движка и транскодера

#### 1.1. [MODIFY] [writer.go](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/id3/writer.go)
1. В `WriteTags` заменить запись `TPE2`:
   ```go
   // TPE2: Исполнитель альбома (поддержка null-separated \x00 для нескольких исполнителей)
   if strings.TrimSpace(tags.AlbumArtist) != "" {
       writeMultiTextFrame(&framesBuf, "TPE2", tags.AlbumArtist)
   }
   ```
2. Реализовать фильтрацию устаревших/конфликтующих `TXXX`:
   ```go
   func isArtistOrConflictingTXXX(f RawFrame) bool {
       if f.ID != "TXXX" || len(f.Data) < 2 {
           return false
       }
       desc := extractTXXXDescription(f.Data)
       descLower := strings.ToLower(strings.TrimSpace(desc))
       switch descLower {
       case "artists", "albumartists", "album artists", "artist", "album artist", "album_artist":
           return true
       default:
           return false
       }
   }
   ```
3. Исключить такие фреймы при переносе `existingFrames`.

#### 1.2. [MODIFY] [media_processor.go](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/downloader/media_processor.go)
Добавить `-map_metadata -1` в команду `ffmpeg` в методе `transcodeToMP3`.

#### 1.3. [MODIFY] [indexer.go](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/scanner/indexer.go)
Извлекать `primaryTrackArtist := resolver.ExtractPrimaryArtist(artistName)` перед вызовом `s.getOrCreateArtist()`.

---

### Этап 2: Новая логика детекции дефектов в Аудите (`tag_audit_handlers.go`)

#### 2.1. [MODIFY] [tag_audit_handlers.go](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/tag_audit_handlers.go)
1. **Обновление структуры `TagIssueItem`:**
   ```go
   type TagIssueItem struct {
       ID          int64             `json:"id"`
       FilePath    string            `json:"file_path"`
       Title       string            `json:"title"`
       Artist      string            `json:"artist"`
       Album       string            `json:"album"`
       Year        int               `json:"year"`
       TrackNumber int               `json:"track_number"`
       Duration    float64           `json:"duration"`
       Issues      []string          `json:"issues"`
       Severity    string            `json:"severity"` // "safe_autofix", "review_needed", "info"
       Proposed    ProposedTagChange `json:"proposed"`
   }
   ```
2. **Переработка `analyzeTrackIssues`:**
   - **Детекция `literal_backslash`:** Проверяем, содержит ли `artist` или теги в файле символы `\\`. При наличии — помещается в `SAFE_AUTOFIX`.
   - **Детекция `track_num_in_title`:** Проверяем префиксы `01. `, `02 - ` через `trackPrefixRegex`. Помещается в `SAFE_AUTOFIX`.
   - **Детекция `dirty_title`:** Очистка клипового мусора через `CleanYouTubeVideoTitle`. Помещается в `SAFE_AUTOFIX`.
   - **Детекция `topic_channel`:** Суффикс `- Topic`. Помещается в `SAFE_AUTOFIX`.
   - **Детекция `unparsed_feat` (только реальные дефекты):**  
     Извлекаем соавторов из названия (`ExtractFeatFromText`). Проверяем, входят ли они в `ParseMultiValue(artist)`. Если **все** соавторы уже есть в артистах — дефект **НЕ регистрируется**.
   - **Детекция `artist_in_title` (строгая):**  
     Срабатывает **только** если левая часть до дефиса совпадает с артистом через `AreArtistsEquivalent`.
   - **Очистка ложного `missing_album`:**  
     Если альбом равен `"Single"` или `"Singles"`, он **не считается дефектом**.
3. **Безопасное пакетное исправление `FixSafeTagIssues`:**
   - Эндпоинт `POST /files/audit/tag-issues/fix-safe`:  
     Находит треки, у которых есть дефекты из категории `SAFE_AUTOFIX`, и применяет строго детерминированные локальные правки (очистка мусора, номеров, слешей, топиков) без сетевых запросов.
4. **Пакетное исправление выбранных треков `FixSelectedTagIssues`:**
   - Эндпоинт `POST /files/audit/tag-issues/fix-selected`:  
     Принимает список путей файлов и применяет предложенные теги только к ним.

---

### Этап 3: Умная онлайн-сверка с каталогами (Smart Catalog Match)

#### 3.1. [MODIFY] [tag_audit_handlers.go](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/tag_audit_handlers.go)
1. **Расчет композитного Match Score (0–100%) в `CatalogMatchTrack`:**
   ```go
   func computeMatchScore(currentTitle, currentArtist string, currentDuration float64, match CatalogTrackMatch) int {
       // 1. Сходство названий (0-45 очков)
       titleSim := stringSimilarity(currentTitle, match.Title)
       score := int(titleSim * 45)

       // 2. Сходство исполнителей (0-35 очков)
       if resolver.AreArtistsEquivalent(currentArtist, match.Artist) {
           score += 35
       } else if strings.Contains(strings.ToLower(match.Artist), strings.ToLower(resolver.ExtractPrimaryArtist(currentArtist))) {
           score += 20
       }

       // 3. Совпадение длительности (0-20 очков)
       if currentDuration > 0 {
           diff := math.Abs(currentDuration - float64(match.Duration))
           if diff <= 3 {
               score += 20
           } else if diff <= 8 {
               score += 15
           } else if diff <= 15 {
               score += 8
           }
       } else {
           score += 15
       }
       return score
   }
   ```
2. Сортировка совпадений строго по `MatchScore DESC`.
3. При вызове `ApplyCatalogTags`: синхронная запись `AlbumArtist` (основной артист или соавторы альбома), предотвращающая рассинхронизацию.

---

### Этап 4: Обновление интерфейса Аудита тегов (`audit_tags.html`, `tag_audit.js`)

#### 4.1. [MODIFY] [audit_tags.html](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/audit_tags.html)
1. **Панель действий:**
   - Главная кнопка: **«Исправить безопасные дефекты (N)»** (зеленый градиент, моментальное действие).
   - Кнопка: **«Применить к выбранным (N)»** (появляется при выборе чекбоксов).
2. **Фильтры:**
   - «Все дефекты», «Безопасные (Safe)», «Требуют проверки», «Клиповый мусор», «Разделители Navidrome (\\)», «Соавторы (feat.)».
3. **Таблица треков:**
   - Колонка с чекбоксами (выбор отдельных треков и «Выбрать все»).
   - Индикация критичности дефектов цветными бейджами (`Безопасно` — зеленый, `Внимание` — янтарный).
   - Подсветка Diff изменений: зачеркнутый старый текст и зеленый новый.
4. **Модальное окно сверки:**
   - Отображение процента соответствия (`Match Score: 98%`) у каждого результата каталога.

#### 4.2. [MODIFY] [tag_audit.js](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/audit/tag_audit.js)
1. Добавление состояния выбора чекбоксов: `selectedItems: {}`, `selectAll: false`.
2. Методы `fixSafeTagIssues()` и `fixSelectedTagIssues()`.
3. Оптимистичное обновление списка и всплывающие уведомления.

---

### Этап 5: Миграция медиатеки и валидация в Navidrome

1. Прогон пакетного перетегирования существующих треков в `music_library/`.
2. Запуск рескана в Navidrome.
3. Проверка базы данных Navidrome (`navidrome.db`): отсутствие обратных слешей `\\` в таблицах `artist` и `album`.

---

## 5. Анализ рисков и краевых случаев (Risks & Edge Cases)

1. **Краевой случай: легитимные дефисы в названиях песен:**  
   *Митигация:* Проверка `AreArtistsEquivalent` на левую часть до дефиса гарантирует, что песни вроде «Self-Esteem», «Day-O», «Spider-Man» никогда не будут обрезаны.
2. **Краевой случай: синглы без отдельного альбома:**  
   *Митигация:* Полноценное признание альбомов «Single» / «Singles» валидными — исключение ложного флага `missing_album`.
3. **Безопасность пакетных правок:**  
   *Митигация:* `SAFE_AUTOFIX` изолирован от эвристик; сетевые запросы исключены из массового цикла; запись атомарна через временный файл и `os.Rename`.

---

## 6. План тестирования и верификации (Verification Matrix)

1. **Юнит-тесты `internal/id3`:**
   - Запись `AlbumArtist` с несколькими исполнителями -> валидация байтов `\x00` во фрейме `TPE2`.
   - Очистка устаревших `TXXX:ARTISTS` и `TXXX:ALBUMARTISTS`.
2. **Юнит-тесты `internal/api/handlers`:**
   - Тест `analyzeTrackIssues`:
     - Трек с дефисом ("Spider-Man") -> `issues` пустой, дефекта `artist_in_title` нет.
     - Альбом "Single" -> дефекта `missing_album` нет.
     - Трек с `(feat. B)` в названии и `Artist: A\\B` -> дефекта `unparsed_feat` нет.
     - Трек с литеральным `\\` в физическом теге -> дефект `literal_backslash_artist`, категория `safe_autofix`.
   - Тест `computeMatchScore`: оригинальный трек получает больший скор, чем 8-минутный сборник с кучей артистов.
3. **Регрессионное тестирование:**
   - `go test -v -race ./...` (все тесты зеленые).
   - Сборка бинарного файла `go build -ldflags="-s -w" -o bin/music-manager cmd/server/main.go`.
4. **Интеграционная проверка:**
   - Проверка SQLite базы Navidrome после рескана.

---

## 7. Критерии приемки (Definition of Done)

- [ ] В `TPE2` (Album Artist) стандарта ID3v2.4 множественные артисты пишутся через `\x00`.
- [ ] Старые `TXXX` фреймы с `\\` очищаются при сохранении.
- [ ] В Navidrome после рескана артисты треков и релизов отображаются корректно без `\\`.
- [ ] Устранены ложные срабатывания аудита на "Single", дефисы в названиях и существующие соавторы.
- [ ] В аудите появилась детекция физических `\\` тегов с 100% безопасным автоисправлением.
- [ ] Кнопка «Исправить безопасные дефекты» работает локально, быстро и без внешних запросов к сети.
- [ ] В таблице аудита появились чекбоксы и возможность применить исправления к выбранным трекам.
- [ ] Онлайн-сверка ранжирует результаты по Match Score (название + артист + длительность), а не по максимальному числу артистов.
- [ ] Все тесты `go test -race ./...` проходят успешно.
- [ ] Обновлены `PROGRESS.md` и документация.
