# План реализации: Система полуавтоматического отслушивания треков по таймкодам при загрузке (Quick-Listen Spots)

- **Дата составления:** 2026-10-20  
- **Статус плана:** `DONE (Успешно реализован и протестирован)`  
- **Целевые компоненты:** `internal/services/censorcheck/` (новый пакет), `internal/services/resolver/`, `internal/features/downloader/`, `web/static/js/player.js`, `web/templates/components/downloader_queue.html`, `web/templates/components/downloader_active.html`  
- **Архитектурные инварианты (AGENTS.md):** Pure Go (Chi), Zero-Python в серверной части, Zero Node.js / Zero npm, Zero Docker, Sandbox Mandatory (`PathSandbox.Resolve()`), ID3v2.4 UTF-8 (null-separated), SQLite WAL, No-Build Frontend (Tailwind CDN + Alpine.js CDN).

---

## 1. Цель и границы задачи (Objective & Scope)

### 1.1. Проблематика
1. **Неприменимость тяжелого автоматического анализа:**
   - Полноценный ASR-анализ (Whisper) требует от 15 до 600 секунд на трек и сотен мегабайт оперативной памяти при интенсивной загрузке процессора.
   - Пользователь прямо указал: **автоматический анализатор не нужен**.
2. **Проблема скрытой цензуры стримингов (КоАП РФ ст. 6.13):**
   - Российские стриминги (Яндекс Музыка, VK, Apple Music) часто оставляют обычный мат, но точечно заглушают (Mute) или запикивают (Bleep) нарко-сленг (ст. 6.13 КоАП РФ).
   - Человеческое ухо за 2–3 секунды безошибочно определяет, присутствует ли заглушение вокала или бип на подозрительном слове, если точно знать, куда перематывать.
3. **Отсутствие фокуса в интерфейсе загрузчика:**
   - Сейчас после скачивания релиза в `tmp_downloads` пользователь видит список треков, но чтобы проверить оригинальность, ему приходится вручную открывать файл, искать текст песни и наугад перематывать дорожку.

### 1.2. Целевой результат
1. **Мгновенное извлечение контрольных точек (Quick-Listen Spots):**
   - При скачивании трека сервис автоматически подтягивает синхронизированный текст (`.lrc`) через существующий `lyrics.Service` (LRCLIB / Deezer).
   - Новый легковесный Pure Go пакет `internal/services/censorcheck` за **< 1 миллисекунды** парсит таймкоды `[mm:ss.xx]` и находит до 3 наиболее критичных точек в треке с наивысшим приоритетом:
     - Приоритет 1: Вещества и нарко-сленг (**ст. 6.13 КоАП РФ**).
     - Приоритет 2: Подозрительные маркеры и эвфемизмы (clean, radio edit, балдеет, дуреет).
     - Приоритет 3: Явный мат (хуй, пизда, ебать, блять, сука).
2. **Контекстный интеллект:**
   - Исключение ложных срабатываний: фразы вроде «у меня есть план», «по плану», «мой план», «бизнес-план» не помечаются как наркотики.
   - Исключение нейтральных слов («лирика», «марка машины») и безопасных корней с «еб» («волшебная», «скрести», «хлеб», «небе»).
3. **1-Click интерактивное отслушивание в очереди загрузок:**
   - В карточке трека в `downloader_queue.html` и `downloader_active.html` появляются кликабельные чипы контрольных точек:
     `[▶ 01:14 "косяк" (КоАП 6.13)]` `[▶ 02:05 "наебалово"]`.
   - Клик по чипу запускает встроенный веб-аудиоплеер ровно за 2 секунды до слова и воспроизводит короткий фрагмент (5 секунд) с автоматической остановкой.
   - Пользователь видит всплывающую подсказку со строкой из песни: *«Закуривая паранойю в виде косяка»*.
4. **Бесшовная замена цензурной версии:**
   - Если пользователь услышал мьют/запикивание, он тут же нажимает имеющуюся кнопку **«Выбрать источник»** (`$store.sourcePicker.openForQueueTrack`) и в 1 клик подменяет трек на альтернативную версию из SoundCloud / YouTube.

### 1.3. Границы проекта (Scope)
- **Входит в объем (In-Scope):**
  - Создание нового пакета `internal/services/censorcheck/` на чистом Go (лексикон КоАП 6.13 + мат, парсер LRC, генератор точек).
  - Расширение модели `resolver.ResolvedTrack` полем `QuickListenSpots []QuickListenSpot`.
  - Интеграция в `downloader.Service.processSingleTrack`: получение LRC через `lyrics.Service` в `tmp_downloads` и вызов генератора точек.
  - Добавление метода `playSpot(streamUrl, spot)` в `web/static/js/player.js`.
  - Обновление шаблонов `web/templates/components/downloader_queue.html` и `downloader_active.html` для отображения чипов точек.
  - Покрытие юнит-тестами на Go (`censorcheck_test.go`, тесты интеграции с очередью).
- **Не входит в объем (Out-of-Scope):**
  - Автоматический запуск нейросетей (Whisper, ASR) — от них полностью отказываемся по требованию пользователя.
  - Автоматический спектральный DSP-анализ (заменен на ухо пользователя).
  - Изменение бинарного формата ID3v2.4.

---

## 2. Анализ текущего состояния системы (Current State Analysis)

### 2.1. Затронутые компоненты кодовой базы
1. **Загрузчик треков:** [`internal/features/downloader/service.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/features/downloader/service.go#L890-L1150)
   - Метод `processSingleTrack` выполняет скачивание сырого потока, транскодирование в MP3 320k и запись ID3-тегов.
   - Сейчас он не вызывает `lyrics.Service` и не сохраняет `.lrc` рядом с MP3 во временном каталоге `tmp_downloads/{job_id}/converted/`.
2. **Модели треков очереди:** [`internal/services/resolver/resolver.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/resolver.go#L52-L75)
   - Структура `ResolvedTrack` содержит метаданные трека, статус версии (`VersionStatus`), аудиокачество (`AudioQuality`) и путь к готовому MP3 (`ConvertedPath`).
   - Поле для контрольных точек отсутствует.
3. **Сервис текстов песен:** [`internal/services/lyrics/lyrics.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/lyrics/lyrics.go#L64-L105)
   - Метод `Search(ctx, artist, title, album, duration)` обращается к LRCLIB и возвращает `SyncedLyrics` с точными таймкодами.
4. **Аудиоплеер на фронтенде:** [`web/static/js/player.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/player.js#L213-L245)
   - `Alpine.store('player')` умеет воспроизводить предпросмотры (`playPreviewStream`) и аудиодорожки через стриминг `/api/stream?path=...`.
   - Не хватает специализированного метода для позиционирования на таймкод `t - 2s` с автопаузой через 5 секунд.
5. **Интерфейс очереди загрузок:** [`web/templates/components/downloader_queue.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/downloader_queue.html#L160-L225)
   - Отображает прогресс, бейджи качества (`HQ Opus`, `True 320k`) и кнопку «Выбрать источник».
   - Место для чипов отслушивания идеально вписывается под названием трека рядом с бейджами.

---

## 3. Архитектурное решение и обоснование (Architecture & Rationale)

### 3.1. Архитектурная схема взаимодействия

```mermaid
flowchart TD
    subgraph DownloaderPipeline["Downloader Pipeline (Go)"]
        DL["processSingleTrack()"] --> MP3["Конвертация в MP3 320k"]
        MP3 --> LYRICS["lyrics.Service.Search() (LRCLIB)"]
        LYRICS -->|SyncedLyrics| SPOT_GEN["censorcheck.ExtractSpots(lyrics)"]
        SPOT_GEN -->|QuickListenSpots| TRACK["ResolvedTrack.QuickListenSpots"]
        TRACK --> JOB["Job.Tracks (in-memory + DB)"]
    end

    subgraph ClientUI["Frontend (Alpine.js + HTML5 Audio)"]
        JOB -->|/api/downloader/queue| UI["downloader_queue.html"]
        UI -->|Рендеринг чипов| CHIP["[▶ 01:14 'косяк' (КоАП 6.13)]"]
        CHIP -->|Click: playSpot()| PLAYER["player.js (Web Audio)"]
        PLAYER -->|GET /api/stream?path=tmp/...#t=72| STREAM["Range Audio Stream"]
        PLAYER -->|5 сек воспроизведения| AUTO_STOP["Автопауза + Toast"]
    end

    subgraph UserAction["Действие пользователя"]
        AUTO_STOP --> DECISION{"Слышна цензура?"}
        DECISION -->|Да: запикано/мьют| SWAP["Клик: 'Выбрать источник' (SoundCloud)"]
        DECISION -->|Нет: звучит оригинал| CONFIRM["Подтвердить перенос в медиатеку"]
    end
```

### 3.2. Сравнительный анализ альтернатив

| Параметр | Вариант А: Автоматический Whisper/DSP | Вариант Б: Только ручная перемотка трека | Вариант В (Выбранный): Точечный Quick-Listen по LRC |
|---|---|---|---|
| **Нагрузка на CPU сервера** | Экстремальная (100% CPU на 5–10 минут) | Нулевая (0%) | **Нулевая (< 0.001 сек на парсинг текста)** |
| **Точность детекции** | 85–90% (ложные срабатывания, шумы бита) | 100% (человеческий слух) | **100% (ухо человека проверяет точный срез)** |
| **Время на 1 трек** | 20–600 секунд | 60–120 секунд ручного поиска | **3–5 секунд (1 клик по готовому чипу)** |
| **Зависимости** | Python, ffmpeg C-bindings, Whisper | Нет | **Pure Go (Zero-Python, Zero-Node.js)** |
| **Удобство для пользователя** | Черный ящик, возможны ложные флаги | Утомительно искать слова вручную | **Фокусное прослушивание 1–2 ключевых слов** |

### 3.3. Почему именно такое решение («Почему именно так»)
1. **Почему Pure Go, а не Python:**
   - Соответствие инварианту #1 (`AGENTS.md`): серверная часть строго на Go. Парсер LRC и регулярные выражения на Go выполняются за микросекунды без запуска внешних интерпретаторов.
2. **Почему LRCLIB как источник таймкодов:**
   - LRCLIB — крупнейшая открытая база синхронизированных текстов, бесплатная, не требует ключей API и уже интегрирована в проект через `lyrics.Service`.
3. **Почему смещение -2 секунды и длительность 5 секунд:**
   - Таймкод в LRC указывает на начало строки или гласного звука. Смещение на 2 секунды назад дает слушателю музыкальный контекст (бит и предшествующие слова), а 5 секунд достаточно, чтобы полностью прозвучало слово и артикуляция без необходимости слушать весь куплет.
4. **Почему ограничение до 3 точек на трек:**
   - Если в треке 20 матерных слов, вывод 20 кнопок перегрузит UI. Достаточно проверить 1 слово по КоАП 6.13 (наркотики) и 1–2 мата: если в них нет цензуры, весь трек оригинальный.

---

## 4. Пошаговая декомпозиция реализации (Step-by-Step Implementation)

### Этап 1: Пакет `internal/services/censorcheck` (Pure Go) [NEW]

Создать изолированный пакет с лексиконом, контекстным фильтром и экстрактором точек.

#### Файл: `internal/services/censorcheck/types.go`
```go
package censorcheck

// SpotCategory классифицирует категорию триггерного слова.
type SpotCategory string

const (
	CategoryDrugCoAP SpotCategory = "DRUG_COAP_6_13" // ст. 6.13 КоАП РФ (нарко-сленг)
	CategorySuspect  SpotCategory = "SUSPECT_MARKER" // clean, radio edit, эвфемизмы
	CategoryProfane  SpotCategory = "PROFANITY"      // обычный мат
)

// QuickListenSpot представляет одну контрольную точку для прослушивания.
type QuickListenSpot struct {
	TimeSec   float64      `json:"time_sec"`   // Секунда в треке (например, 74.2)
	TimeStr   string       `json:"time_str"`   // Форматированное время ("01:14")
	Word      string       `json:"word"`       // Целевое слово ("косяк")
	Category  SpotCategory `json:"category"`   // DRUG_COAP_6_13 / PROFANITY
	LineText  string       `json:"line_text"`  // Полный текст строки из LRC
}
```

#### Файл: `internal/services/censorcheck/lexicon.go`
- Скомпилированные регулярные выражения и хэш-таблицы:
  - Наркотики по ст. 6.13 КоАП РФ: `мефедрон`, `меф`, `кокаин`, `кокс`, `экстази`, `мдма`, `кодеин`, `лин`, `перкосет`, `ксанакс`, `гашиш`, `бошки`, `шишки`, `блант`, `косяк`, `джойнт`, `сатива`, `индика`, `тгк`, `бульбулятор`, `бонг`, `водник`, `закладка`, `зиплок`, `амфетамин`, `спиды`, `героин`, `герыч`, `передоз`, `стафф`.
  - Проверка контекста для слова «план»: функции `isDrugPlanContext(word, line)`.
  - Мат: корни `хуй`, `пизд`, `еб` (с исключением `SAFE_EB_SUBSTRINGS`: хлеб, ребят, волшеб, небе, скреб), `бля`, `сука`.

#### Файл: `internal/services/censorcheck/extractor.go`
```go
package censorcheck

// ExtractSpots анализирует синхронизированный текст LRC и возвращает до maxSpots контрольных точек.
// Приоритет отдается словам по ст. 6.13 КоАП РФ, затем маркерам цензуры, затем мату.
func ExtractSpots(lrcContent string, maxSpots int) []QuickListenSpot
```

---

### Этап 2: Расширение моделей `resolver.ResolvedTrack` и DTO [MODIFY]

#### Файл: [`internal/services/resolver/resolver.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/resolver.go)
Добавить поле `QuickListenSpots` в `ResolvedTrack`:
```go
type ResolvedTrack struct {
    // ... существующие поля ...
    AudioQuality     *AudioQualityTelemetry        `json:"audio_quality,omitempty"`
    QuickListenSpots []censorcheck.QuickListenSpot `json:"quick_listen_spots,omitempty"`
}
```

---

### Этап 3: Интеграция в пайплайн загрузки `downloader.Service` [MODIFY]

#### Файл: [`internal/features/downloader/service.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/features/downloader/service.go)
1. В `ServiceConfig` и структуру `Service` внедрить `lyricsService *lyrics.Service`.
2. В методе `processSingleTrack` после успешной конвертации MP3 и записи тегов:
   ```go
   // Поиск синхронизированного текста через LRCLIB
   if s.lyrics != nil {
       lrcRes, _ := s.lyrics.Search(ctx, track.Artist, track.Title, meta.AlbumTitle, track.Duration)
       if lrcRes != nil && lrcRes.Synced && lrcRes.Lyrics != "" {
           // Сохраняем .lrc файл рядом с MP3 во временной папке
           lrcFilename := fmt.Sprintf("%02d. %s.lrc", track.TrackNumber, safeTitle)
           lrcPath := filepath.Join(convertedDir, lrcFilename)
           _ = os.WriteFile(lrcPath, []byte(lrcRes.Lyrics), 0644)

           // Мгновенно извлекаем до 3 контрольных точек
           spots := censorcheck.ExtractSpots(lrcRes.Lyrics, 3)
           if len(spots) > 0 {
               job.UpdateTrack(trackIdx-1, func(tr *resolver.ResolvedTrack) {
                   tr.QuickListenSpots = spots
               })
           }
       }
   }
   ```

---

### Этап 4: Веб-аудиоплеер для сниппетов в `web/static/js/player.js` [MODIFY]

#### Файл: [`web/static/js/player.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/player.js)
Добавить метод `playCensorshipSpot`:
```javascript
playCensorshipSpot(streamUrl, spot, trackTitle, trackArtist) {
    if (!streamUrl || !spot) return;

    // Смещение на 2 секунды назад для контекста
    const startTime = Math.max(0, spot.time_sec - 2.0);
    const stopDuration = 5.0; // Играем ровно 5 секунд

    this.currentTrack = {
        title: trackTitle || 'Отслушивание точки',
        artist: `${trackArtist || ''} • Слово: «${spot.word}»`,
        duration: stopDuration,
        stream_url: streamUrl,
    };
    this.isVisible = true;

    if (this._spotTimer) {
        clearTimeout(this._spotTimer);
        this._spotTimer = null;
    }

    if (this.audioElement) {
        this.audioElement.src = streamUrl;
        this.audioElement.currentTime = startTime;
        this.audioElement.play().then(() => {
            this.isPlaying = true;
            // Автоматическая остановка через 5 секунд
            this._spotTimer = setTimeout(() => {
                this.audioElement.pause();
                this.isPlaying = false;
            }, stopDuration * 1000);
        }).catch(err => console.warn('Spot play error:', err));
    }

    if (window.showToast) {
        window.showToast(`🎧 Контрольная точка: «${spot.word}» (${spot.time_str})\n${spot.line_text}`, 'info');
    }
}
```

---

### Этап 5: Отображение чипов в очереди загрузок [MODIFY]

#### Файлы: [`web/templates/components/downloader_queue.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/downloader_queue.html) и `downloader_active.html`
В блоке каждого трека под строкой названия вывести блок контрольных точек:
```html
<template x-if="track.quick_listen_spots && track.quick_listen_spots.length > 0">
    <div class="flex flex-wrap items-center gap-1.5 mt-1.5 pt-1 border-t border-surface-border/40">
        <span class="text-[10px] text-content-muted font-medium flex items-center gap-1 shrink-0">
            <svg class="w-3 h-3 text-amber-400" viewBox="0 0 24 24" fill="currentColor">
                <path d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm1 15h-2v-2h2v2zm0-4h-2V7h2v6z"/>
            </svg>
            Проверить:
        </span>
        <template x-for="spot in track.quick_listen_spots" :key="spot.time_sec">
            <button type="button"
                    @click.stop="$store.player.playCensorshipSpot('/api/stream?path=' + encodeURIComponent(track.converted_path), spot, track.title, track.artist)"
                    :title="`Строка: '${spot.line_text}'`"
                    :class="spot.category === 'DRUG_COAP_6_13' ? 'bg-rose-500/15 hover:bg-rose-500/30 text-rose-300 border-rose-500/30' : 'bg-amber-500/15 hover:bg-amber-500/30 text-amber-300 border-amber-500/30'"
                    class="px-2 py-0.5 rounded text-[10px] font-mono border transition-colors flex items-center gap-1 shadow-sm">
                <span class="text-[8px]">▶</span>
                <span x-text="spot.time_str"></span>
                <span class="font-bold font-sans" x-text="`«${spot.word}»`"></span>
                <template x-if="spot.category === 'DRUG_COAP_6_13'">
                    <span class="text-[9px] px-1 rounded bg-rose-500/30 text-rose-200">КоАП</span>
                </template>
            </button>
        </template>
    </div>
</template>
```

---

## 5. Анализ рисков и краевых случаев (Risks, Edge Cases & Guardrails)

1. **Отсутствие текста в LRCLIB:**
   - *Риск:* Для редких андеграунд-треков в LRCLIB может не быть текста.
   - *Митигация:* `quick_listen_spots` просто остается пустым `[]`. Загрузка завершается в штатном режиме, интерфейс не ломается.
2. **Рассинхронизация таймкодов в пользовательских LRC:**
   - *Риск:* Текст из LRCLIB может отставать на 1–2 секунды от конкретного рипа.
   - *Митигация:* Запуск за **2 секунды до таймкода** (`startTime = Math.max(0, spot.time_sec - 2.0)`) с окном воспроизведения 5 секунд гарантирует, что слово попадёт в окно слышимости даже при умеренном рассинхроне.
3. **Безопасность путей (Sandbox Invariant #2):**
   - *Риск:* Попытка воспроизведения через `/api/stream?path=...`.
   - *Митигация:* Хендлер `StreamTrack` уже использует `h.tmpSandbox.Resolve()` для любых путей `tmp_downloads/...`. Любые `../` или абсолютные пути отклоняются с кодом `403 Forbidden`.
4. **Утечки памяти и горутин в плеере:**
   - *Риск:* Быстрые повторные клики по кнопкам разных точек накапливают таймеры `setTimeout`.
   - *Митигация:* Ссылка на `this._spotTimer` сбрасывается через `clearTimeout(this._spotTimer)` перед каждым новым запуском.

---

## 6. План тестирования и верификации (Verification Matrix)

1. **Юнит-тесты `censorcheck` (`internal/services/censorcheck/extractor_test.go`):**
   - Тест на выявление наркотиков КоАП 6.13 («мефедрон», «косяк», «экстази»).
   - Тест контекста «план»: фраза «у меня есть план» должна дать 0 точек, а «курим чистый план» — 1 точку.
   - Тест безопасных корней с «еб»: «волшебная палочка», «на небе» не должны создавать точек.
   - Тест лимита точек: при 10 матах возвращается строго не более `maxSpots` (3).
2. **Интеграционные тесты очереди (`internal/features/downloader/service_test.go`):**
   - Тест полного цикла `processSingleTrack` с замоканным `lyrics.Service`: проверка, что готовый `ResolvedTrack` содержит корректные `QuickListenSpots` и файл `.lrc` сохранен в каталоге `converted/`.
3. **Стандартные проверки проекта (AGENTS.md):**
   - `go test -v ./...` — 100% прохождение всех тестов без гонок (`-race`).
   - `go build -ldflags="-s -w" -o bin/music-manager cmd/server/main.go` — успешная статическая бинарная сборка.

---

## 7. Критерии приемки (Definition of Done)

- [x] Создан пакет `internal/services/censorcheck` с тестами, покрывающими словарь КоАП 6.13, мат и контекст «план».
- [x] Модель `ResolvedTrack` и DTO содержат поле `quick_listen_spots`.
- [x] При скачивании трека подтягивается LRC и вычисляются 1–3 контрольные точки.
- [x] В `downloader_queue.html` и `downloader_active.html` выводятся чипы точек с таймкодами и словами.
- [x] Клик по чипу воспроизводит 5-секундный аудиофрагмент (`t - 2s`) во встроенном плеере.
- [x] `go test -v ./...` зеленый, бинарник `bin/music-manager` компилируется без ошибок.
- [x] Документ `PROGRESS.md` обновлен с фиксацией статуса задачи.
