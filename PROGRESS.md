# PROGRESS.md — Матрица статусов и дашборд реализации

- **Статус готовности:** **100% DONE** (Все 68 блоков задач полностью реализованы и протестированы).
- **Стек:** Высокопроизводительный сервис на **Pure Go (Chi)**, **yt-dlp**, **ffmpeg**, **ID3v2.4 Engine**, **SQLite (WAL)**, **No-Build Frontend (HTML5 + Tailwind CDN + Alpine.js CDN)**.
- **Размер бинарника:** 16 МБ (единый статический бинарник `bin/music-manager`, время холодного старта ~2 мс, потребление RAM ~4–24 МБ).
- **Результаты тестов:** 100% Go unit, fuzz & integration tests (все тесты пройдены, `go test -race ./...` — **0 data races**, все 13 пакетов зеленые).
- **Безопасность:** Проведен глубокий аудит безопасности и фаззинг, сформирован [`SECURITY_AUDIT_REPORT.md`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/SECURITY_AUDIT_REPORT.md).
- **Продакшен-деплой:** Развернут под управлением `systemd` (`music-manager.service`) на боевых серверах `nl_music` (194.76.168.20 / `https://musicdownloader.verymad.net/`) и Сервер 2 (13.143.193.207 / `https://mymusicdownload.verymad.net/`).


---

## 2. Ключевые архитектурные вехи

1. **Каноническая файловая структура:**
   `MUSIC_DIR/{Album} - {Album Artist}/{TrackNumber:02d}. {Title}.mp3` (и `{Title} - {Artist}/01. {Title}.mp3` для синглов) с сопутствующими `.lrc`, `cover.jpg` и `{TrackBaseName}.jpg`.
2. **Синхронизация БД SQLite (WAL):**
   Удаление и перемещение файлов атомарно синхронизирует таблицы `tracks`, `albums`, `track_genres`, `audit_issues`, `missing_tracks`, обновляет статусы и очищает осиротевшие записи.
3. **SoundCloud-First Downloader & Anti-403 Fallback:**
   Приоритетный поиск оригинальных нецензурированных треков в SoundCloud с fallback на YouTube Music / iTunes Search при блокировках Deezer (Akamai WAF).
4. **Персистентная очередь и Retry:**
   Хранение задач в SQLite, восстановление при рестарте сервера, кнопка повтора упавших задач (`POST /downloads/{id}/retry`) и детерминированная сортировка.
5. **Строгое ID3v2.4 UTF-8 мульти-жанровое и мульти-артист кодирование:**
   Разделение множественных исполнителей и жанров через null-байт `\x00` в фреймах `TPE1`, `TPE2` и `TCON` (нативная поддержка Navidrome, Plex, Foobar2000, AIMP) и полная фильтрация конфликтных `TXXX` фреймов.
6. **Онлайн-сверка и бэкфилл релизов (Backfill Pipeline):**
   Автоматическая сверка неполных альбомов с официальным каталогом Deezer и пакетная дозагрузка недостающих дорожек в один клик.
7. **Декомпозиция монолитов (Go, JS и HTML):**
   Все монолитные Go-пакеты, JavaScript-модули и HTML-шаблоны (`web/templates/`) разбиты на компактные файлы по 15–250 строк (Single Responsibility Principle).

---

## 3. Сводная матрица этапов разработки

Детальные журналы изменений, списки измененных файлов и контрольные точки вынесены в специализированные файлы истории:

| Эпоха / Этап | Блоки | Статус | Документ с подробным описанием |
|---|---|---|---|
| **1. Первоначальный прототип (Python era)** | Блоки 1–20 | **DONE** | Консолидировано в PROGRESS.md |
| **2. Миграция на Pure Go & SQLite WAL** | Блоки 21–30 | **DONE** | Консолидировано в PROGRESS.md |
| **3. Стабилизация, оптимизация и декомпозиция** | Блоки 31–42 | **DONE** | Консолидировано в PROGRESS.md |

---

### Краткий состав блоков по эпохам

#### Эпоха 1: Первоначальный прототип
- **Блоки 1–5:** Базовое ядро, авторизация bcrypt, PathSandbox, Downloader, ID3 теги, тексты LRCLIB, файловый менеджер, systemd.
- **Блоки 6–10:** YouTube Age Gate, Deezer короткие ссылки, AudioMatcher, стриминг прогресса, Google Material Icons, DRM bypass, SoundCloud плейлисты, тексты песен.
- **Блоки 11–15:** Распознавание Shazam, Deezer Search, импорт Яндекс/Spotify/Apple Music, очистка названий, DnD/TPOS перенумерация, галерея обложек, пакетный режим, глобальный поиск `Ctrl+K`.
- **Блоки 16–20:** Группировка релизов, карточки релизов, торрент-загрузчик, стабилизация очередей.

#### Эпоха 2: Миграция на Pure Go
- **Блоки 21–25:** Реактивность аудита, селектор дискографии, параллельное транскодирование, извлечение метаданных Deezer, бэкфилл неполных релизов.
- **Блоки 26–30:** CLI сканер `music-scanner` (NDJSON), нормализация жанров, одноуровневая структура `Альбом/Треки.mp3`, демон аудита на Go + `fsnotify` + SQLite WAL, полный вывод Python из кодовой базы, декомпозиция фронтенда на Go-шаблоны и модульные JS-файлы.

#### Эпоха 3: Стабилизация и декомпозиция Go-монолитов
- **Блок 31 (Git Checkpoint):** Фиксация перехода на Go и деплой на боевой сервер `nl_music`.
- **Блок 32 (Downloader Engine Stabilization):** Персистентность очереди в SQLite, SoundCloud-First Matcher, Deezer anti-403 WAF fallback на iTunes, кнопка Retry.
- **Блок 33 (YouTube PO Token & Remixes):** Обход ограничений YouTube PO Token (`--flat-playlist`), сохранение ремиксов и версий треков.
- **Блок 34 (Deterministic Queue & Progress):** Детерминированная сортировка очереди по приоритету статусов, взвешенный расчет `OverallProgress`.
- **Блок 35 (1-Level Album Layout & Singles):** Каноническая раскладка альбомов и синглов (`album = title`), независимая маршрутизация треков.
- **Блок 36 (Track Prefixes & Navidrome ID3v2.4 Multi-Genres):** Сохранение числовых префиксов артистов (`1.Kla$`), ID3v2.4 UTF-8 с null-байтами `\x00` для Navidrome.
- **Блок 37 (Track-Named Covers & Embedded APIC):** Сохранение обложек `{TrackBaseName}.jpg` для синглов и автоизвлечение встроенных APIC обложек.
- **Блок 38 (Full Monolith Decomposition):** Полная декомпозиция всех Go-пакетов (`internal/id3`, `resolver`, `downloader`, `organizer`, `scanner`, `handlers`) и JS-модулей на компактные файлы по 100–300 строк. Устаревший `tools/music-scanner` выведен из репозитория.
- **Блок 39 (HTML Templates Decomposition):** Полная декомпозиция всех монолитных HTML-шаблонов (`tag_editor.html`, `audit.html`, `downloader.html`) на компактные под-шаблоны по 15–230 строк.
- **Блок 40 (YouTube & Yandex Cross-Referencing, Max Artists Wins & Tag Audit):**
  - Клиент YouTube Music Innertube API (`client_ytmusic.go`) на Pure Go без API-ключей.
  - Клиент Яндекс Музыки (`client_yandex.go`) для треков, альбомов и дискографий артистов.
  - Парсинг YouTube заголовков (`yt_title_parser.go`) с отделением фитов (`feat.`), удалением мусора (клипы, премьеры) и нормализацией Topic-каналов.
  - Детекция соавторов (`feat_enricher.go`) с параллельной кросс-сверкой по YouTube Music, Яндекс Музыке и iTunes по правилу "где больше артистов то и указывается" (Max Artists Wins).
  - Интеграция сверки фитов в загрузчики (YouTube, Yandex Music, Deezer) и подсистему Аудита медиатеки (`tag_audit_handlers.go`, `audit_tags.html`, `tag_audit.js`).
  - Раздел «Сверка тегов» в аудите с бейджами проблем, автоисправлением и Diff-модалом со сравнением с онлайн-каталогами.
- **Блок 41 (Yandex Music Geoblocking HTTP 451 & Proxy Support):**
  - Поддержка `YANDEX_MUSIC_PROXY` (HTTP/HTTPS/SOCKS5) и `YANDEX_MUSIC_TOKEN` в `Config` и `YandexMusicClient`.
  - Информативная диагностика ошибки HTTP 451 Geoblock (зарубежный IP-адрес сервера) во всех эндпоинтах Яндекс Музыки.
  - Структурированный ответ HTTP 451 в обработчике дискографий с рекомендациями по настройке прокси или переходу на Deezer/YouTube.
  - Документация переменных в `.env.example`.
- **Блок 42 (Navidrome Multi-Artist Backslash Fix & Tag Audit Overhaul):**
  - **Устранение `\\` в Navidrome:**
    - `internal/id3/writer.go`: перевод записи `TPE2` (Album Artist) на `writeMultiTextFrame` с null-байтами `\x00` в ID3v2.4 UTF-8 (ранее ошибочно вызывался `writeTextFrame`, сохранявший литеральный `\\`).
    - Фильтрация устаревших/конфликтных `TXXX` фреймов (`TXXX:ARTISTS`, `TXXX:ALBUMARTISTS`, etc.) в `writer.go`, перебивавших чистые `TPE1`/`TPE2` в TagLib / Navidrome.
    - `internal/services/downloader/media_processor.go`: флаг `"-map_metadata", "-1"` в ffmpeg-пайплайне конвертации, предотвращающий просачивание мусорных тегов контейнеров.
    - `internal/services/scanner/indexer.go`: индексация первичного исполнителя (`primaryTrackArtist := resolver.ExtractPrimaryArtist(artistName)`) в таблице `artists` SQLite вместо составных строк с `\\`.
  - **Переработка логики Аудита тегов:**
    - Устранение ложных срабатываний: альбомы с именами `"Single"` / `"Singles"` признаны валидными; проверка дефиса в названии стала строгой (только если префикс совпадает с исполнителем или исполнителем является Topic-канал/лейбл); существующие соавторы в теге не вызывают ложный `unparsed_feat`.
    - Классификация дефектов по серьезности (`Severity`): `safe_autofix` (клиповый мусор, номер трека в названии, topic-каналы, `\\`), `review_needed` (соавторы, артист в названии, отсутствие альбома), `info` (отсутствие года).
    - Качественный алгоритм подбора в каталогах: замена сортировки "по максимуму артистов" на композитный скоринг `computeMatchScore` (0–100%) на основе сходства названий (45%), артистов (35%) и длительности трека (20%).
    - Высокопроизводительные эндпоинты: быстрый локальный `POST /tag-issues/fix-safe` без внешних HTTP-запросов и зависаний, селективный `POST /tag-issues/fix-selected`, синхронизация `AlbumArtist` при применении каталожных тегов (`apply-catalog`).
  - **No-Build фронтенд (Alpine.js + Tailwind):**
    - Добавлены чекбоксы строк и "Выбрать все" в шапке таблицы тегов с подсветкой выбранных строк.
    - Кнопки "Безопасные (N)" со счетчиком и "Применить к выбранным (N)".
    - Бейджи серьезности (`Безопасно (Авто)`, `Требует внимания`, `Инфо`) и дефектов.
    - Бейджи процента совпадения (`MatchScore`) и длительности трека в карточках каталогов модального окна Diff-сверки.
  - **Блок 43: Устранение оверинжиниринга, стабилизация конкурентности и декомпозиция архитектуры Go-бэкенда:**
    - **Concurrency Hardening & Goroutine Leak Fix:** Фоновый таймер `startCleaner` в `downloader.Service` переведён на `ctx context.Context` и снабжён методом `Close()`, полностью устранив утечки горутин при создании сервисов и в тестах. В `pipeline.go` захват семафора `trackSem` перенесён до `go func()` с поддержкой ранней отмены через `select { case <-ctx.Done() }`. В `manager.go` внедрён дебаунс `syncJob` через `time.AfterFunc(500ms)`, кардинально снизивший частоту параллельных записей в SQLite WAL при обновлении прогресса.
    - **Вынос доменной логики в `internal/services/resolver/`:**
      - Реализован переиспользуемый клиент `ITunesClient` (`client_itunes_track.go`) с возможностью подмены `SetBaseURL` для тестирования.
      - Алгоритм ранжирования каталогов вынесен в `catalog_score.go` (`ComputeCatalogMatchScore`) отдельно от аудио-скоринга `score.go`.
    - **Декомпозиция монолита `tag_audit_handlers.go` (1 116 строк) на 4 компактных файла:**
      - `tag_audit_models.go`: структуры данных (`TagIssueItem`, `ProposedTagChange`, `CatalogTrackMatch`, `FixTagIssueRequest`, etc.).
      - `tag_audit_analyzer.go`: чистая функция обнаружения дефектов `analyzeTrackIssues`.
      - `tag_audit_fix_handlers.go`: HTTP-обработчики автоисправления (`FixTagIssue`, `FixSafeTagIssues`, `FixSelectedTagIssues`, `FixAllTagIssues`).
      - `tag_audit_handlers.go`: компактные эндпоинты (`TagIssues`, `CatalogMatchTrack`, `ApplyCatalogTags`).
    - **Parameter Object & Внедрение зависимостей:** Конструктор `NewFileHandler` переведён на `FileHandlerParams` по аналогии с `RouterParams`, исключив Constructor Bloat (8 -> 1 параметр) и позволив внедрить каталожные клиенты `Yandex`, `YTMusic`, `ITunes` и переиспользуемый `httpClient` для обложек.
    - **Усиление обработки ошибок:** Проверка `errors.Is(err, sandbox.ErrPathTraversal)` во всех обработчиках файлов возвращает точный HTTP-статус `403 Forbidden` вместо `400/404`. Подавленные ошибки SQL в `scanner.RemoveFile` и `batch_lyrics_handlers` заменены на диагностическое логирование.
    - **100% изоляция сети в тестах:** Добавлены модульные тесты с моками (`httptest.Server`) для `ITunesClient`, `ComputeCatalogMatchScore` и `CatalogMatchTrack`. Тестовое покрытие выросло со 127 до 135 тестов (все 12 пакетов зеленые, 0 data races).
- **Блок 44 (Audio Streaming & Web Audio Player Pipeline):**
  - **Zero-Copy HTTP Range Streaming:** Эндпоинт `GET /api/tracks/{id}/stream` на базе `http.ServeFile` и системного вызова `sendfile(2)` ядра Linux. Полная поддержка RFC 7233 (206 Partial Content, перемотка, `416 Not Satisfiable`, `Accept-Ranges: bytes`, ETag).
  - **VPN-маскировка (xHTTP):** Алиас-маршрут `GET /api/library/stream/{id}/chunks` и `GET /api/library/stream/{id}/chunks/*` для маскировки туннельного трафика xHTTP под стриминг аудиофрагментов.
  - **TrackID Resolver & Fast In-Memory Cache:** Детерминированный 16-hex хэш `TrackHash(relPath) = sha256(relPath)[:16]`, числовой ID SQLite, индекс `idx_tracks_hash` и кэш `trackCache sync.Map`. Обязательная валидация через `PathSandbox.Resolve()`.
  - **Метаданные и обложки по ID:** Эндпоинты `GET /api/tracks/{id}/metadata` (мгновенная отдача из SQLite WAL с fallback на `id3.ReadTags`), `GET /api/tracks/{id}/cover` (встроенный APIC или папочная обложка с `Cache-Control: public, max-age=86400`) и каталог `GET /api/library/tracks`.
  - **Поддержка query-токена в Auth:** `auth.Middleware` проверяет `?token=` наряду с cookie сессии и `Authorization: Bearer`, позволяя стримить в сторонние плееры (VLC, mpv).
  - **No-Build Alpine.js Аудиоплеер:**
    - Компонент `web/templates/components/audio_player.html`: фиксированная нижняя панель (Sticky Bottom Bar) в стиле modern dark UI с мини-обложкой, названием, артистом, таймлайном с перемоткой по клику, регулятором громкости и Mute.
    - Реактивный стор `Alpine.store('player')` в `web/static/js/player.js` с управлением воспроизведением, горячими клавишами (Space, стрелки, M) и сохранением громкости.
    - Кнопки ▶ в строках файлов `file_manager.html` и в окне глобального поиска `header.html` с подсветкой играющего трека.
  - **Тесты:** 11 новых unit-тестов в `stream_handlers_test.go` с проверкой Range 200/206/416, VPN-алиаса, защиты от path traversal и метаданных. Общее количество тестов достигло 146/146 (100% зеленые).
- **Блок 45 (Dedicated Studio Hi-Fi Player Tab — Teenage Engineering Style):**
  - **Выделенная вкладка «Плеер»:** 4-я вкладка в главной навигации `header.html` со студийной иконкой и пульсирующим индикатором сигнала. Бесшовное открытие по клику на нижний мини-плеер `audio_player.html`.
  - **Индустриальная дизайн-система (Teenage Engineering):** Глубокий матовый графит (`#0d0f12`, `#14171f`), прецизионная лазерная разметка 1px, аппаратная моноширинная телеметрия (`font-mono tracking-wider`), тактильные переключатели, аналоговые янтарные (`#f59e0b`) и изумрудные (`#10b981`) индикаторы, виниловый картридж обложки.
  - **Трехпалубная архитектура (3-Deck Rack):**
    - *Left Deck (Cassette & Master Transport):* Картридж альбома, название/артист/альбом, аналоговый стерео VU-метр (L/R каналы с динамической шкалой и пиками), миллисекундный таймлайн (`02:14.85 / 03:45.00`), кнопки Play/Pause, Prev/Next, Loop (None/All/One), Shuffle, Mute и слайдер уровня громкости.
    - *Center Deck (Multi-Monitor CRT Display):* Трехрежимный переключаемый экран:
      1. `SPECTRUM`: Аппаратный реалтайм эквалайзер на `<canvas>` с Web Audio API `AnalyserNode` (32 частотных бина от 32 Гц до 16 кГц с гравитационным peak-hold и динамическими VU-уровнями). Энергоэффективный цикл (0% нагрузки при скрытой вкладке).
      2. `LYRICS`: Движок синхронизированного караоке-текста `.lrc` с плавной центровкой активной строки (`scrollIntoView`), кликабельными таймкодами (мгновенный seek по клику на строчку), тумблером автопрокрутки и фоллбэком на plain text.
      3. `PASSPORT`: Аудиофильский паспорт трека (MP3 CBR 320 kbps, 44.1 kHz 16-bit, ID3v2.4 UTF-8 с null-разделителями, размер файла, 16-hex хэш, статус текстов).
    - *Right Deck (Tape Queue Rack):* Интерактивная очередь воспроизведения с живым анимированным эквалайзером у играющего трека, счетчиком треков/длительности, кнопками перемещения (вверх/вниз), удаления и полной очистки.
  - **Обогащение бэкенда:** Расширение `TrackMetadataResponse` полями `sample_rate`, `track_number`, `year`, `genre`, `file_size` в `stream_handlers.go`.
  - **Тесты:** Проверены поля метаданных в `stream_handlers_test.go`, все 146 тестов зеленые (`go test -race ./...` — 0 data races).

- **Блок 46 (Full Frontend Redesign — SoundCloud & Deezer Aesthetic):**
  - **Ликвидация «AI-слопа»:** Полный отказ от шаблонной зелено-серой палитры Tailwind (`gray-950/emerald-500`) в пользу глубокой чернильно-обсидиановой основы (`#0B0C0E`), теплого студийного оранжевого акцента SoundCloud (`#FF5500`) и структурированной потоковой консоли Deezer.
  - **Дизайн-система токенов (Tokens & Foundation):**
    - `brand`: `#FF5500`, hover `#FF6E1A`, active `#E64D00`, muted `rgba(255, 85, 0, 0.12)`, glow `rgba(255, 85, 0, 0.25)`.
    - `surface`: `surface-base` (`#0B0C0E`), `surface-panel` (`#13151A`), `surface-elevated` (`#1B1E26`), `surface-input` (`#0F1014`), `surface-border` (`#242834`), `surface-borderSubtle` (`#1C1F28`).
    - `content`: `content-primary` (`#F4F4F6`), `content-secondary` (`#A2A4B0`), `content-muted` (`#636779`).
  - **Типографика:** Подключение `Plus Jakarta Sans` для интерфейсных элементов и `JetBrains Mono` для моноширинной телеметрии (битрейты 320 kbps, таймкоды, пути, теги).
  - **Стилизация плеера и визуализатора:** Оранжевый прогресс-бар, стильные градиенты холста спектроанализатора в `web/static/js/player.js`, глубокие бархатные тени `shadow-panel` и тонкие скроллбары в `web/static/css/style.css`.
  - **Полный рефакторинг всех компонентов (`web/templates/`):**
    - Шапка и навигация `header.html`: градиентный логотип-волна, стриминговый поиск `Ctrl+K`.
    - Нижняя консоль плеера `audio_player.html` и полноэкранная вкладка `player_tab.html`.
    - Загрузчик и очередь: `downloader_form.html`, `downloader_active.html`, `downloader_queue.html`, `downloader_sources.html`, `downloader_discography.html`.
    - Файловый менеджер `file_manager.html`: Deezer-треклист, Play-кнопки по наведению, моно-бейджи `[320 kbps]`, `[LRC]`.
    - Аудит целостности библиотеки: `audit.html`, `audit_metrics.html`, `audit_tags.html`, `audit_covers.html`, `audit_duplicates.html`, `audit_genres.html`, `audit_incomplete.html`, `audit_lyrics.html`, `audit_paths.html`.
    - Модальные окна: `login.html`, `confirmations.html`, `lyrics.html`, `tag_editor.html`, `tag_editor_covers.html`, `tag_editor_deezer.html`, `tag_editor_flat.html`, `tag_editor_grouped.html`, `tag_editor_grouped_tracks.html`.
  - **No-Build соблюдение инвариантов:** Zero-Node.js, Zero-npm, чистые inline SVG Google Material Symbols (`viewBox="0 0 24 24"`), Alpine.js CDN и Tailwind Play CDN.
  - **Тесты и сборка:** Все 146 тестов зеленые (`go test -race ./...`), бинарник `bin/music-manager` собран без ошибок.

- **Блок 47 (File Manager Responsive Layout & Width Overflow Fix):**
  - **Устранение горизонтального переполнения в просмотре файлов:**
    - Расширение глобального контейнера приложения с `max-w-7xl` (1280px) до `max-w-[1600px]` в `index.html`, `header.html` и `audio_player.html` для полноценного использования пространства широкоформатных мониторов (1080p, 1440p, 4K) без пустых полей по бокам.
    - Перевод таблицы файлов на `table-fixed w-full`: исключение расползания колонок из-за длинных названий треков и каталогов, гарантированное срабатывание CSS `truncate` с многоточием.
    - Оптимизация ширин и адаптивности колонок:
      - `#` (номер трека / кнопка воспроизведения): `w-10 sm:w-12 text-center`
      - `Композиция`: гибкая колонка `w-auto min-w-0 truncate` с мобильным субтитром артиста и статусом текста песни
      - `Исполнитель`: скрыта на мобильных, адаптируется `hidden md:table-cell md:w-36 lg:w-44 xl:w-56 truncate`
      - `Альбом`: скрыта на мобильных и планшетах, адаптируется `hidden lg:table-cell lg:w-36 xl:w-52 truncate`
      - `Текст`: скрыта на узких экранах, адаптируется `hidden sm:table-cell sm:w-16 lg:w-20 text-center`
      - `Время`: компактная моноширинная колонка `w-16 sm:w-20 lg:w-24 text-right font-mono`
      - `Действия`: компактная колонка `w-24 sm:w-28 md:w-32 lg:w-36 text-right whitespace-nowrap`
    - Рефакторинг кнопок действий (Actions): компактные иконки `p-1.5` с подсказками (tooltips) и адаптивными метками на XL-экранах вместо громоздких текстовых кнопок, занимавших до 300px на строку.
    - Подгонка внутренних отступов ячеек (`py-2.5 sm:py-3 px-2 sm:px-4`) для высвобождения полезной рабочей площади.
  - **Тесты и сборка:** Все тесты пройдены (`go test -v ./...`), бинарник `bin/music-manager` собран без ошибок.

- **Блок 48 (Frontend Performance & Zero-Freeze File Manager Optimization):**
  - **Ликвидация зависаний браузера (Browser Freeze Fix):**
    - В медиатеке `/var/lib/music` находится 2 756 каталогов/альбомов. При открытии вкладки «Файлы» Alpine.js создавал 113 920 DOM-элементов одновременно со множеством вложенных `<template x-if>`, что приводило к лавине событий `MutationObserver` в Tailwind CDN и 100% блокировке UI-потока браузера.
    - **Клиентская пагинация и фильтрация:**
      - Внедрена плавная пагинация в `fileManagerComponent`: 50 элементов на страницу по умолчанию (выбор 25, 50, 100, 200, Все). Количество DOM-узлов снижено со 113 920 до ~1 100–3 200 (-97.1%).
      - Интегрирован быстрый фильтр по названию/артисту/альбому в шапку каталога с моментальным откликом (<30 мс на 2 756 записей).
      - Создана панель навигации по страницам (`Показано 1–50 из 2756`, кнопки «, Назад, Стр. X из Y, Вперед, », выбор лимита).
      - Устранены десятки избыточных `<template x-if>` внутри строк таблицы файлов: кнопки воспроизведения и бейджи переведены на прямое переключение SVG-путей (`:d="..."`) и `x-show`.
    - **Устранение реактивного шторма плеера:**
      - В `player.js` исключено присвоение реактивных полей `$store.player.vuMeterL`/`R` на каждый `requestAnimationFrame` (60–144 FPS). Анимация аналогового VU-метра выведена напрямую в DOM через `getElementById`, ликвидировав нагрузку на реактивный движок Alpine.
    - **Устранение утечки таймеров опроса очереди:**
      - В `queue_manager.js` исправлен двойной вызов `scheduleNextPoll()` внутри `fetchQueue()`.
    - **Кулдаун автообновления:**
      - В `file_manager.js` добавлен 30-секундный троттлинг на событие `window.focus`, предотвращающий холостые повторные запросы при переключении окон.
  - **Тесты и сборка:** 100% тестов пройдены (`go test -race ./...`), бинарник `bin/music-manager` пересобран.
  - **Деплой на боевой сервер:** Коммит `c3337d8` запушен в `main` и раскатан на `nl_music` (194.76.168.20), сервис перезапущен. Инструментальные замеры в реальном браузере подтвердили молниеносный отклик интерфейса.

- **Блок 49 (Comprehensive Server REST API Documentation & OpenAPI 3.0 Specification):**
  - **Полная спецификация REST API в `API.md`:**
    - Задокументированы все 43 эндпоинта сервера, сгруппированные по 8 функциональным областям: Системные, Аутентификация, Загрузки, Теги ID3v2.4, Тексты песен, Стриминг и библиотека, Файловый менеджер и Аудит медиатеки.
    - Описан механизм сессий на HMAC-SHA256 и HttpOnly-cookie `session_token`.
    - Описаны гарантии безопасности файловой системы через `PathSandbox Guard` (изоляция путей, защита от Path Traversal, symlink escape, null-byte инъекций).
    - Для каждого эндпоинта зафиксированы: HTTP-метод, URL, query-параметры, форматы тела запроса (JSON, multipart), коды ответов (200, 206, 400, 401, 403, 404, 451, 500) со структурами данных и подробными примерами.
  - **Спецификация OpenAPI 3.0.3 (`docs/openapi.yaml`):**
    - Создана интерактивная схема в формате OpenAPI 3.0.3 со всеми тэгами, путями, схемами компонентов (`DownloadJob`, `TrackTags`, `DiscographyResponse`, `HealthResponse`, `LoginRequest`, etc.), заголовками Range и cookie-авторизацией `sessionCookie` для импорта в Swagger UI, Postman и Insomnia.
  - **Интеграция в README.md:**
    - В дерево проекта и структуру документации добавлены ссылки на `API.md` и `docs/openapi.yaml`.

- **Блок 50 (Fix Login Modal Visibility & Balanced Template Divs):**
  - **Устранение бага скрытого окна авторизации:**
    - Исправлен незакрытый тег `</div>` в `web/templates/modals/tag_editor.html`, из-за которого модальное окно авторизации (`modal_login`) и последующие компоненты парсились браузером как дочерние элементы редактора тегов и оставались невидимыми (`display: none`) при клике на «Войти».
    - Восстановлена строгая иерархия DOM верхнего уровня для всех модальных окон в `<body>`.
  - **Регрессионные тесты и верификация:**
    - В `internal/api/router_test.go` добавлен тест на сбалансированность тегов `<div>` / `</div>` в результирующем HTML главной страницы и присутствие `loginModalComponent()`.
    - Проведено тестирование в headless Chrome через CDP со снятием скриншота: клик по кнопке «Войти» корректно открывает модальное окно авторизации.

- **Блок 51 (Core Stability & Quality-of-Life: Censorship, Covers, Permissions, Artist Resolution & Tag Audit):**
  - **Приоритет Explicit-версий и отсечение цензуры:**
    - Добавлен жесткий штраф `-40` за ключевые слова цензуры (`censored`, `clean version`, `clean`, `radio edit`, `radio version`, `clean edit`).
    - Добавлен бонус `+25` за нецензурированные явные релизы (`explicit`, `dirty`, `uncensored`, `album version`, `explicit version`) и бонус `+25` за официальные каналы ` - Topic`.
    - Переупорядочен приоритет выбора источников (Topic-каналы -> Explicit треки -> Audio-видео -> общие результаты).
  - **Исправление прав доступа для Navidrome (0644/0755):**
    - Устранена первопричина `0600` прав: `id3.WriteTags` теперь явно выполняет `os.Chmod(tmpName, 0644)` перед атомарным `os.Rename` и повторный `os.Chmod(filePath, 0644)`.
    - В `fs_utils.go` (`moveFile`, `copyFile`) и `organizer.go` директории создаются с `0755`, а файлы перемещаются с `0644`.
    - В `music-manager.service` установлен `UMask=0022`.
    - Реализована автоматическая фоновая проверка и исправление прав доступа медиатеки при старте сервера (`organizer.FixLibraryPermissions`).
  - **Корректный резолвинг артистов со спецсимволами и псевдонимами:**
    - Регулярное выражение санитизации поисковых запросов в `text_utils.go` обновлено для сохранения знака доллара `$`: `[^\p{L}\p{N}\s\$]`, что предотвратило искажение имен артистов вроде `Toxi$`.
    - Добавлен реестр двунаправленных синонимов и транслитераций (`ЛСП` <-> `LSP`, `Toxi$` <-> `Toxis`) с автоматическим fallback при поиске в Deezer, YouTube Music и SoundCloud.
  - **Пакетная и одиночная дозагрузка обложек (Covers Backfill Pipeline):**
    - Реализован HTTP API: `POST /files/audit/missing-covers/backfill` (одиночный), `POST /files/audit/missing-covers/backfill-all` (пакетный), `GET /files/audit/missing-covers/progress`, `POST /files/audit/missing-covers/cancel`.
    - Вкладка «Релизы без обложек» обогащена кнопкой «Докачать все» с динамическим прогресс-баром и кнопками «Скачать арт» для каждой строки.
    - Автопоиск обложек высокого разрешения (1400×1400) через цепочку iTunes -> Deezer -> Яндекс Музыка с сохранением `cover.jpg`, поквартирных `{Track}.jpg` и вшиванием APIC в MP3.
  - **Сверка метаданных на уровне альбома/релиза:**
    - Реализован эндпоинт `GET /files/audit/tag-issues/catalog-match-album` для сопоставления локального релиза с официальным треклистом Deezer / Apple Music.
    - Реализован эндпоинт `POST /files/audit/tag-issues/apply-catalog-album` для пакетного применения чистых тегов ко всем трекам альбома в один клик.
    - Во вкладку «Сверка тегов» добавлен переключатель режимов «По трекам» / «По релизам (альбомам)» и модальное окно Album Diff с предпросмотром треклиста и обложки.
  - **Синхронизация библиотеки для Navidrome (`\\` -> `\x00`):**
    - Реализована утилита нормализации `NormalizeNavidromeLibrary`, рекурсивно исправляющая права доступа (0644/0755), расщепляющая устаревшие разделители `\\` в `TPE1`/`TPE2`, удаляющая паразитные `TXXX` и перезаписывающая чистые теги ID3v2.4 с null-байтами `\x00`.
    - В заголовок раздела «Аудит медиатеки» добавлена кнопка «Для Navidrome».

- **Блок 52 (SoundCloud Candidate Ranking & Version Switcher — Automatic Audio Resolver):**
  - **Этап 1: Config & Database:**
    - Добавлены параметры SoundCloud резолвера в `internal/config/config.go` (`UncensoredDurationTolerance`, `UncensoredCandidatesLimit`, `UncensoredPreferYear`, `UncensoredCacheTTLHours`, `UncensoredDistributors`) и секция `4c` в `.env.example`.
    - Создана SQLite таблица `soundcloud_uncensored_cache` с индексом `idx_sc_cache_key`.
    - Реализованы методы в `internal/db/queries.go`: `GetSoundCloudCache`, `SaveSoundCloudCache`, `PinSoundCloudVersion`, `InvalidateSoundCloudURL` с защитой ручных пинов (`manual = 1`), TTL экспирацией и ротацией при 404/410. Покрыто тестами `TestSoundCloudCache`.
  - **Этап 2: Сбор эталонных хронометражей (Catalog Durations):**
    - Метод `SearchTrackVersions` в `internal/services/resolver/client_deezer.go` для извлечения всех версий трека (синглы, альбомы, делюксы, explicit).
    - Метод `SearchTracksWithCountry` в `internal/services/resolver/client_itunes_track.go` для поиска по региональным витринам iTunes (US, GB, DE) с юнит-тестами.
  - **Этап 3: Движок сопоставления и ранжирования (Matching & Ranking Engine):**
    - Создан модуль `internal/services/resolver/uncensored_matcher.go`:
      - Алгоритм Anti-Glue с отступами пробелов и Anti-Collision для артистов (Clean Bandit, The Cleaners from Venus и т.д.).
      - Определение допустимого диапазона хронометражей по эталонам Deezer/iTunes с настраиваемым допуском (`tolerance`).
      - Каскад статусов оригинальности: `marked` > `likely` > `unverified`.
      - Штрафы за сторонних дистрибьюторов (TuneCore, DistroKid, CDBaby и др.) и бонус за соответствие года релиза.
      - Прозрачный fallback на YouTube при отсутствии подходящих SoundCloud кандидатов.
    - Реализованы юнит-тесты `internal/services/resolver/uncensored_matcher_test.go`.
  - **Этап 4: Пайплайн загрузки и API переключения версий (Download Pipeline & Version Switch API):**
    - Пайплайн загрузки (`pipeline.go`) обновлен: проброс статусов оригинальности и кандидатов в `ResolvedTrack`, обработка 404/410 с инвалидацией и ротацией кандидатов / fallback на YouTube.
    - Метод `SwitchTrackVersion` в `internal/services/downloader/switch_version.go`: загрузка выбранного SoundCloud URL, перекодирование в MP3 320k CBR, замер ffprobe, перенос ID3v2.4 тегов и обложки через `PathSandbox.Resolve()`, атомарная замена файла и фиксация ручного пина в SQLite.
    - Эндпоинты `POST /api/downloads/tracks/{trackId}/switch-version` и `POST /downloads/tracks/{trackId}/switch-version`.
    - Юнит-тесты `TestDownloadHandler_SwitchTrackVersion`.
  - **Этап 5: Фронтенд (UI статусов и селектор версий):**
    - Модуль `web/static/js/downloader/versions.js` (`window.DownloaderVersions`): переключение версий в 1 клик, бейджи оригинальности (🟢 marked, 🟡 likely, ⚪ unverified, 🔵 manual, 🔴 link_dead), форматирование дельты хронометража и дат.
    - Интеграция в `downloader.js`, `downloader_active.html`, `downloader_queue.html` (аккордеон «Треки (N)» и выпадающий список кандидатов «Версии (N)» с кнопкой «Выбрать»), а также отображение бейджей в Tag Editor (`tag_editor_flat.html`, `tag_editor_grouped_tracks.html`).

- **Блок 53 (Search Bug Fix, In-Folder Tag Editor & Integration Test):**
  - **Устранение инверсии результатов глобального поиска (`Ctrl+K`):**
    - Корень бага: в `stream_search_handlers.go` переменные `SearchLibrary` распаковывались в обратном порядке (`artists, albums, tracks` вместо `tracks, albums, artists`), из-за чего треки показывались как артисты и наоборот.
    - `internal/db/queries.go`: монолитная структура `SearchResultItem` заменена на три типизированные: `SearchTrackItem` (добавлено поле `lyrics_status`), `SearchAlbumItem` (добавлены `Album`, `AlbumArtist`, `Artist`, `TrackCount`), `SearchArtistItem` (добавлены `Name`, `TrackCount`, `FolderPath`, `FirstPath`). Метод `SearchLibrary` возвращает `(*SearchLibraryResult, error)` вместо 4-кортежа. SQL-запросы переписаны с правильными JOIN и алиасами.
    - `internal/api/handlers/stream_search_handlers.go`: обработчик теперь вызывает `result, err := h.database.SearchLibrary(...)` и кодирует `result` напрямую.
    - `web/static/js/header_search.js`: добавлен хелпер `getTrackDir(path)`, безопасный `path || ''` в `navigateToDir`.
    - `web/templates/components/header.html`: исправлены шаблоны артистов (`art.name || art.title`, `art.folder_path || art.first_path || ''`), альбомов (`alb.album || alb.title`, безопасный `alb.album_artist || alb.artist`) и треков (`getTrackDir(trk.path)`, безопасный `trk.artist`).
  - **Кнопка «Редактировать теги» внутри открытой папки:**
    - `web/static/js/file_manager.js`: добавлен геттер `hasAudioTracksInCurrentFolder` и метод `editCurrentFolderTags()`, вызывающий `openInTagEditor({ type: 'directory', path: this.currentPath, ... })`.
    - `web/templates/components/file_manager.html`: добавлена кнопка «Редактировать теги» в панель инструментов папки (видна только при `currentPath && currentPath.length > 0`), рядом с кнопкой «Назад».
  - **Тесты:**
    - `internal/db/db_test.go`: добавлен `TestDB_SearchLibrary` (3 кейса: поиск по артисту, альбому, треку) — всё PASS.
    - `internal/api/handlers/stream_handlers_test.go`: добавлен `TestFileHandler_SearchLibrary_HTTP` с двумя сабтестами (поиск по треку и по артисту через HTTP); проверяет `title`, `artist`, `path`, `name`, `track_count`. PASS.
    - `go test -race ./...` — **0 data races**, 153/153 тестов зеленые.
  - **Деплой:** Бинарник пересобран и раскатан на `nl_music` (194.76.168.20), сервис перезапущен.

- **Блок 54 (Censored Tracks Detection & Deletion in Tag Audit):**
  - **Детекция цензурированных версий на бэкенде:**
    - В [`internal/api/handlers/tag_audit_analyzer.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/tag_audit_analyzer.go) добавлен регулярный выразитель `badRegexAudit` (`clean`, `radio edit`, `censored`, `censored version`, `radio version`, `tv edit`, `с цензурой`, `цензурная`) по границам слов `[^\p{L}\p{N}]`.
    - Добавлен тип дефекта `censored_version` в `analyzeTrackIssues` с классификацией серьезности `review_needed` (amber бейдж «Требует внимания»).
    - В [`internal/api/handlers/file_handlers.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/file_handlers.go) метод `DeleteFile` расширен поддержкой извлечения пути из JSON-тела `{ "path": "..." }` при отсутствии query-параметра с сохранением обратной совместимости.
  - **Интерфейс и взаимодействие (Alpine.js + Tailwind):**
    - В [`web/templates/components/audit_tags.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/audit_tags.html) добавлена кнопка фильтрации «Цензурированные» (`filterTagIssues('censored_version')`), бейдж дефекта (`bg-red-600/10 text-red-400 border border-red-600/20`) и кнопка «Удалить» в колонке действий только для цензурированных треков.
    - В [`web/static/js/audit/tag_audit.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/audit/tag_audit.js) реализован метод `deleteCensoredTrack(item)` с диалогом подтверждения `confirm(...)`, вызовом `DELETE /files` с query и JSON-телом, тостом уведомления и оптимистичным удалением строки из таблицы.
  - **Тесты и верификация:**
    - Добавлен unit-тест `TestAnalyzeTrackIssues_CensoredDetection` в [`internal/api/handlers/tag_audit_analyzer_test.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/tag_audit_analyzer_test.go), проверяющий детекцию маркеров цензуры, спасители (`Uncensored`, `Uncut`), кейс артиста `Clean Bandit` и статус `review_needed`.
    - Добавлен интеграционный тест `TestFileHandler_DeleteFile_JSONBody` в [`internal/api/handlers/audit_handlers_test.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/audit_handlers_test.go), проверяющий физическое удаление файлов с диска и каскадную синхронизацию базы данных SQLite.
  - **Деплой:** Бинарник пересобран со статической линковкой, синхронизирован валидный `cookies.txt` и раскатан на боевые серверы `nl_music` (194.76.168.20) и `mymusicdownload.verymad.net`.
  - **Документация:** Секция `4c` в [`.env.example`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/.env.example) актуализирована.

- **Блок 55 (Deezer Album Contributors Parallel Resolution & YouTube Music Shelf Support):**
  - **Параллельное извлечение соавторов альбомов Deezer:**
    - В [`internal/services/resolver/resolver_deezer.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/resolver_deezer.go) внедрен конкурентный пул горутин (семафор на 8 воркеров с таймаутом контекста), который параллельно запрашивает `/track/{id}` для всех треков альбома, подтягивая оригинальный массив `track.Contributors` и `track.ISRC` прямо из Deezer.
    - В [`internal/services/resolver/client_deezer.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/client_deezer.go) конструктор `NewDeezerClient` расширен поддержкой вариативного `customTransport ...http.RoundTripper` для полной изоляции сетевых тестов.
  - **Поддержка актуального контейнера `musicShelfRenderer` в YouTube Music:**
    - В [`internal/services/resolver/client_ytmusic.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/client_ytmusic.go) выделена функция `parseYtResponsiveListItem` и добавлена поддержка парсинга элементов выдачи из `musicShelfRenderer` (актуальный формат YouTube Music Innertube API).
  - **Тесты и верификация:**
    - Добавлен юнит-тест `TestResolve_DeezerAlbumContributors` в [`internal/services/resolver/resolver_test.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/resolver_test.go) с моковым транспортом Deezer API (проверяет извлечение всех соавторов и ISRC).
    - Добавлен юнит-тест `TestYouTubeMusicClient_MusicShelfRenderer` в [`internal/services/resolver/client_ytmusic_test.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/client_ytmusic_test.go).
    - `go test -race ./...` — **100% PASS** (0 race conditions во всех 13 пакетах).

- **Блок 56 (Full Contributors Resolution in Tag Editor Deezer Search & Track Search):**
  - **Полная детекция соавторов для альбомов Deezer в редакторе тегов:**
    - В [`internal/api/handlers/deezer_search_handlers.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/deezer_search_handlers.go) метод `GetDeezerAlbumDetails` расширен параллельным пулом горутин (семафор на 8 воркеров, таймаут 4с на контекст), запрашивающим эндпоинт `/track/{id}` для каждого трека альбома.
    - Извлекаются оригинальные соавторы `track.Contributors` (`Main`, `Featured`), обогащаются через `FeatEnricher` и форматируются через `id3.FormatMultiValue` (`Artist 1\\Artist 2`). Сохраняются оригинальные `isrc` и `disc_number`.
  - **Поиск и выбор отдельных треков в каталоге Deezer:**
    - Реализован обработчик `SearchDeezerTracks` (`GET /tags/deezer/tracks` и алиас `/tags/search-deezer-tracks`) для поиска треков в Deezer с автоматическим форматированием соавторов.
    - Реализован обработчик `GetDeezerTrackDetails` (`GET /tags/deezer/tracks/{id}` и алиас `/tags/deezer-track-details`) для получения полной карточки трека (соавторы через `\\`, обложка, альбом, год, жанр, номер диска и трека, ISRC).
    - Маршруты зарегистрированы в [`internal/api/router.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/router.go).
    - В [`cmd/server/main.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/cmd/server/main.go) и [`internal/api/handlers/tag_handlers.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/tag_handlers.go) `TagHandler` подключен к `sourceResolver.Enricher()`.
  - **Интерфейс Alpine.js и No-Build шаблоны:**
    - В [`web/templates/modals/tag_editor_deezer.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/modals/tag_editor_deezer.html) добавлены сегментированные табы `[Альбомы]` / `[Треки]`, адаптивный поиск и карточки треков с подсветкой соавторов и кнопкой «Применить».
    - В [`web/templates/modals/tag_editor_grouped_tracks.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/modals/tag_editor_grouped_tracks.html) и [`web/templates/modals/tag_editor_flat.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/modals/tag_editor_flat.html) добавлена кнопка Deezer-поиска трека рядом с кнопкой очистки названия на каждой строке трека.
    - В [`web/static/js/tag_editor/deezer.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/tag_editor/deezer.js) добавлены методы `openDeezerSearchForTrack`, `searchDeezerTracks`, `setDeezerSearchType`, `applyDeezerTrack` и обновлен `applyDeezerAlbum`.
  - **Тесты и верификация:**
    - Добавлен unit-тест `TestTagHandler_GetDeezerAlbumDetails_ResolvesContributors` в `internal/api/handlers/tag_handlers_test.go` (проверяет корректное подтягивание соавторов и разделение `\\`).
    - Добавлен unit-тест `TestTagHandler_SearchDeezerTracks_And_GetDetails` в `internal/api/handlers/tag_handlers_test.go` (проверяет поиск треков и применение соавторов).
    - `go test -race ./...` — **100% PASS** (0 race conditions во всех 13 пакетах).

- **Блок 57 (Single Tracks Metadata Propagation to Release & Tag Manager):**
  - **iTunes Client & Feat Enricher (`internal/services/resolver/`):**
    - В [`client_itunes_track.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/client_itunes_track.go) добавлено поле `Genre` (`PrimaryGenreName`) в структуру `ITunesTrackMatch`.
    - В [`feat_enricher.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/feat_enricher.go) реализована функция `EnrichTrackDetails`, возвращающая `EnrichedTrackDetails` (соавторы, жанр и год релиза) с перекрестным поиском по Yandex Music и iTunes Search API.
  - **Резолверы источников (`internal/services/resolver/`):**
    - В [`resolver_deezer.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/resolver_deezer.go) для одиночных треков (`case "track"`) запрашивается родительский альбом для получения `PrimaryGenre()`, проставляются `AlbumArtist = formattedArtist`, `AlbumTitle = cleanTitle`, `Genre`, `Year`.
    - В [`resolver_yandex.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/resolver_yandex.go) для одиночных треков извлекаются `Genre` и `Year` из `tr.Albums[0]`, передаются в `ReleaseMetadata` и `ResolvedTrack`, синглу назначается альбом `cleanTit`.
    - В [`resolver_ytdlp.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/resolver_ytdlp.go) для одиночных треков вызывается `EnrichTrackDetails`, заполняются жанр, год и соавторы, проставляются `meta.AlbumArtist = cleanArt`, `tr.AlbumArtist = cleanArt`, `tr.Genre = genre`.
  - **Пайплайн загрузки и тегирования (`internal/services/downloader/pipeline.go`):**
    - В `processTracksPipeline` добавлена строгая двунаправленная синхронизация при `len(tracks) == 1`: трек и релиз делят `Artist <-> AlbumArtist`, `Genre <-> Genre`, `Year <-> Year`, `Title <-> AlbumTitle`.
    - При записи ID3v2.4 тегов для синглов `albArt` устанавливается равным `art` (со всеми соавторами).
  - **API обработчики (`internal/api/handlers/`):**
    - В [`download_handlers.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/download_handlers.go) метод `GetDownloadTracks` дополнен фоллбэком при `len(snap.Tracks) == 1`: если релизные поля пусты, они наследуются из единственного трека.
    - В [`tag_save_handlers.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/tag_save_handlers.go) метод `BatchSaveTags` синхронизирует поля релиза и трека при `len(req.Tracks) == 1`.
  - **Органайзер и сканер библиотеки (`internal/services/organizer/`, `internal/services/scanner/`):**
    - В [`organizer.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/organizer/organizer.go) методы `SaveBatch` и `MoveAlbum` при `len(tracks) == 1` подставляют название трека и артиста трека вместо пустых или фиктивных ("Single"/"Singles") значений альбома.
    - В [`indexer.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/scanner/indexer.go) при индексации файлов, если `tags.Album` пустой или равен "Single"/"Singles", в качестве альбома используется `tags.Title`.
  - **Фронтенд модули (No-Build Alpine.js):**
    - [`polling.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/downloader/polling.js) и [`queue_editor_bridge.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/store/queue_editor_bridge.js): при открытии сингла в Tag Editor объект `album` наследует `album_artist`, `album`, `year`, `genre` из трека.
    - [`loader.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/tag_editor/loader.js): `TagLoader.loadData` синхронизирует поля `component.album` и трека при 1 треке.
    - [`releases.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/tag_editor/releases.js): `buildReleasesFromTracks` формирует полноценную карточку релиза с названием трека для сингла вместо общей папки "Синглы".
    - [`deezer.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/tag_editor/deezer.js): `applyDeezerTrack` обновляет `this.album` при применении метаданных к синглу.
  - **Тесты и верификация:**
    - Добавлен `TestSaveBatch_SingleTrackMetadataPropagationToRelease` в `organizer_test.go`.
    - Добавлен `TestDownloadHandler_GetDownloadTracks_SingleTrackFallback` в `download_handlers_test.go`.
    - Добавлен `TestTagHandler_BatchSaveTags_SingleTrackPropagation` в `tag_handlers_test.go`.
    - `go test -race ./...` — **100% PASS** (0 race conditions во всех пакетах).
- **Блок 58 (Batch Normalization of Existing Single Tracks in «Для Navidrome»):**
  - **Organizer Service (`internal/services/organizer/navidrome_fixer.go`):**
    - Добавлено поле `SinglesFixed int` в структуру `NavidromeFixReport`.
    - В методе `NormalizeNavidromeLibrary` реализована детекция треков-синглов (`Album` пустой, `"Single"`, `"Singles"`, `"Unknown Album"`, `"Single - ..."` или совпадает с названием трека).
    - Для синглов выполняется синхронизация метаданных релиза: `tags.Album = tags.Title`, `tags.AlbumArtist = tags.Artist` (включая всех соавторов).
    - Реализовано автоматическое извлечение соавторов `(feat. ...)` из названия трека через `resolver.ExtractFeatFromText` с очисткой заголовка и добавлением артистов в `tags.Artist` и `tags.AlbumArtist`.
    - Многотрековые альбомы гарантированно не затрагиваются (`Album != Title` и не `"Single"`).
    - При нормализации сингла вызывается `onTrackNormalized(relPath)` для мгновенного обновления индекса SQLite через `scanner.IndexFile`.
  - **Аудит тегов и API обработчики (`internal/api/handlers/`):**
    - В [`tag_audit_analyzer.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/tag_audit_analyzer.go) дополнена детекция `missing_album`: значения `"single"` / `"singles"` классифицируются как требующие исправления альбома на название трека, а для синглов предлагается `AlbumArtist = Artist` со всеми соавторами.
    - В [`tag_audit_fix_handlers.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/tag_audit_fix_handlers.go) методы `FixSafeTagIssues`, `FixSelectedTagIssues` и `FixAllTagIssues` обновлены для корректной перезаписи альбома и альбомного артиста для синглов.
  - **Интерфейс Alpine.js и No-Build шаблоны:**
    - В [`fixes.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/audit/fixes.js) метод `normalizeNavidrome()` сопоставляет поля ответа бэкенда (`total_scanned`, `files_perms_fixed + directories_fixed`, `tags_normalized`, `singles_fixed`) и выводит детальный тост: `...проверено X файлов, исправлено прав: Y, нормализовано тегов: Z, исправлено синглов: W`.
    - В [`audit.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/audit.html) обновлен тултип кнопки «Для Navidrome», отражающий нормализацию метаданных синглов.
  - **Тесты и верификация:**
    - Добавлен `TestNormalizeNavidromeLibrary_SingleTrackPromotion` в `navidrome_fixer_test.go` (проверяет исправление альбома "Single" на название песни, перенос соавторов из feat в Artist и AlbumArtist).
    - Добавлен `TestNormalizeNavidromeLibrary_MultiTrackUntouched` в `navidrome_fixer_test.go` (гарантирует неизменность полноценных альбомов).
    - Дополнена проверка синглов в `TestFileHandler_NormalizeNavidrome` в `batch_covers_handlers_test.go`.
    - `go test -race ./...` — **100% PASS** (0 race conditions во всех пакетах).
    - `go build -ldflags="-s -w" -o bin/music-manager cmd/server/main.go` — успешная сборка.

- **Блок 59 (Cutoff Audio Quality Telemetry, Download Queue Bulk Actions & Tag Editor Multi-Selection Bulk Edit):**
  - **1.5. Детекция реального качества звука (Cutoff & Spek Telemetry):**
    - В [`internal/services/resolver/resolver.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/resolver.go) добавлена структура `AudioQualityTelemetry` (`SourceCodec`, `SourceBitrate`, `CutoffHz`, `QualityTier`, `Description`) и поле `AudioQuality *AudioQualityTelemetry` в `ResolvedTrack`.
    - В [`internal/services/downloader/media_processor.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/downloader/media_processor.go) реализованы:
      - `probeAudioStream`: парсинг входного потока через `ffprobe -v quiet -print_format json -show_streams` (кодек, битрейт, частота дискретизации).
      - `measureSpectralCutoff`: мгновенный анализ частотного среза (~190ms на 5-секундном слайсе через `ffmpeg -ss 15 -t 5 -af aspectralstats=measure=rolloff,ametadata=print:key=lavfi.aspectralstats.1.rolloff:file=- -f null -`).
      - `classifyAudioQuality`: градация качества на уровни `lossless` (FLAC/ALAC/>21kHz), `true_320k` (>=19.5kHz), `hq_opus` (Opus/AAC >=17kHz, например SoundCloud HQ) и `upscaled_128k` (YouTube 128k апскейл со срезом на ~15.5kHz).
      - `analyzeAudioQuality`: интеграция в конвейер `pipeline.go` перед перекодированием в MP3 320k.
    - В [`web/templates/components/downloader_queue.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/downloader_queue.html) на карточках треков очереди выведен телеметрический бейдж реального качества (цветовая индикация: фиолетовый для Lossless, изумрудный для True 320k, голубой для HQ Opus/AAC, янтарный для апскейла 128k со всплывающим тултипом с точным срезом в kHz).
  - **1.6. Массовые действия над очередью загрузок (Bulk Queue Actions):**
    - В [`internal/services/downloader/downloader.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/downloader/downloader.go) реализованы:
      - Потокобезопасная пауза/возобновление очереди (`PauseQueue()`, `ResumeQueue()`, `IsQueuePaused()`) на базе `sync.Cond` и `pauseMu` без накладных расходов процессорного времени.
      - Метод ожидания `waitIfPaused(ctx)` внедрен в воркеры конвейера `pipeline.go` с немедленным пробуждением при завершении сервиса (`Close()`).
      - `ClearCompletedJobs()`: пакетное удаление завершенных и ошибочных задач.
      - `RetryAllFailedJobs()`: пакетный перезапуск всех упавших задач с повторной постановкой в очередь.
    - Добавлены HTTP эндпоинты в [`internal/api/handlers/queue_handlers.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/queue_handlers.go) и зарегистрированы в [`router.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/router.go):
      - `POST /downloads/pause` и `POST /downloads/resume`.
      - `POST /downloads/clear-completed` и `POST /downloads/retry-failed`.
      - В ответе `GET /downloads/summary` добавлено поле `is_paused: bool`.
    - В [`web/static/js/store/queue_manager.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/store/queue_manager.js) и [`web/templates/components/downloader_queue.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/downloader_queue.html) добавлены кнопки быстрого управления:
      - Переключатель Пауза/Возобновление с баннером состояния очереди.
      - Кнопка «Повторить ошибки (N)» (активна при наличии упавших задач).
      - Кнопка «Очистить завершённые (N)» с мгновенной синхронизацией счетчиков.
  - **2.3. Массовое редактирование выбранных треков в Tag Editor (Multi-Selection & Bulk Edit):**
    - Разработан модуль [`web/static/js/tag_editor/selection.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/tag_editor/selection.js) (`window.TagSelection`):
      - Хранение выбранных индексов `selectedIndices`, поддержка выбора отдельных чекбоксов и непрерывных диапазонов через `Shift+Click`.
      - Быстрый выбор всех / сброс выбора (`toggleSelectAllTracks`).
      - Пакетное применение метаданных: жанр (`applyBulkGenre`), год (`applyBulkYear`), номер диска (`applyBulkDisc`), альбомный артист (`applyBulkAlbumArtist`), добавление фитов (`applyBulkFeat`), очистка названий (`cleanTitlesForSelected`), исключение треков из сохранения (`removeSelectedTracks` с коррекцией индексов).
    - Разработан плавающий тулбар [`web/templates/modals/tag_editor_bulk_toolbar.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/modals/tag_editor_bulk_toolbar.html):
      - Индикатор «Выбрано X из Y треков».
      - Быстрые кнопки пакетных действий со встроенными попапами быстрого ввода.
    - В шаблонах [`tag_editor_grouped_tracks.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/modals/tag_editor_grouped_tracks.html) и [`tag_editor_flat.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/modals/tag_editor_flat.html) добавлена колонка с чекбоксами строк, выбор всех в шапке таблицы и визуальное выделение выбранных строк.
    - Интеграция модулей в [`tag_editor.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/tag_editor/tag_editor.js), [`loader.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/tag_editor/loader.js), [`tag_editor.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/modals/tag_editor.html) и [`index.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/index.html).
  - **Тесты и верификация:**
    - Добавлен unit-тест `TestClassifyAudioQuality` в `internal/services/downloader/media_processor_test.go` (проверка всех градаций: lossless, true 320k, hq opus, upscaled 128k).
    - Добавлены unit-тесты `TestService_PauseResumeQueue`, `TestService_ClearCompletedJobs`, `TestService_RetryAllFailedJobs` в `internal/services/downloader/downloader_test.go`.
    - Добавлены интеграционные тесты HTTP API `TestQueueHandlers_PauseResumeSummary`, `TestQueueHandlers_ClearAndRetry` в `internal/api/handlers/queue_handlers_test.go`.
    - `go test -race ./...` — **100% PASS (0 data races)** во всех пакетах.
    - `go build -ldflags="-s -w" -o bin/music-manager cmd/server/main.go` — успешная компиляция бинарника.

- **Блок 55 (Puddletag-Style Workstation & Unified Tag Editor Engine):**
  - **55.1. Бэкенд и Sandbox изоляция:**
    - Поддержка рекурсивного обхода каталогов `GET /files?path=...&recursive=true` с ограничением глубины (20), порогом треков (2000), проверкой `PathSandbox.ToRelative()` и сбором всех метаданных `.mp3` в `internal/api/handlers/file_handlers.go`.
    - Эндпоинт пакетного безопасного переименования `POST /files/rename-pattern` в `internal/api/handlers/file_rename_handlers.go`: проверка коллизий имен, dry-run предпросмотр, атомарный `os.Rename`, синхронное переименование `.lrc`/`.txt` текстов песен и актуализация SQLite БД.
    - Монтирование маршрута в `internal/api/router.go`.
    - Unit-тесты `file_handlers_recursive_test.go` и `file_rename_handlers_test.go` с проверкой на race conditions и path traversal.
  - **55.2. Клиентский движок шаблонов и действий:**
    - Разработан `web/static/js/puddletag/patterns.js` (`window.PuddletagPatterns`): компиляция шаблонов `%artist% - %title%` в регулярные выражения, парсинг имен файлов в теги, форматирование с санитизацией запрещенных символов файловой системы и расчет Diff «Было → Станет».
    - Разработан `web/static/js/puddletag/actions.js` (`window.PuddletagActions`): преобразование регистра (Title Case, UPPERCASE, lowercase, Sentence case), мастер автонумерации треков (01..N, сброс по дискам), очистка от мусора и извлечение `feat` в поле `Artist` с разделителем `\\`, поиск и замена текста с поддержкой Regex.
  - **55.3. Интерактивная рабочая станция Puddletag UI:**
    - Контроллер `web/static/js/puddletag/puddletag_workstation.js`: инлайн-редактирование ячеек (Enter/Tab/Esc), мультиселект диапазонов (Shift+Click, Ctrl+Click), подсветка staged dirty state (янтарный цвет), синхронизация Tag Panel (<Разные значения>), сохранение по `Ctrl+S`.
    - Шаблоны: `puddletag_toolbar.html` (тулбар с конвертерами и рекурсией), `puddletag_tag_panel.html` (левая панель с драг-н-дропом обложек), `puddletag_grid.html` (таблица-сетка), `puddletag_diff_modal.html` (предпросмотр Diff) и `puddletag_actions_modals.html` (модалы регистра, нумерации и замены).
    - Унификация: библиотека `file_manager.html` и модальное окно скачанных релизов `tag_editor.html` переведены на единый Puddletag Engine с кнопкой «Принять в медиатеку» для очереди загрузок.
    - Навигация по подкаталогам в пустом состоянии: добавлены карточки папок исполнителей с количеством треков при пустом корневом каталоге и кнопка «⚡ Загрузить все треки в таблицу (Включая подпапки)».
    - Мгновенная синхронизация модального редактора тегов при передаче данных загрузки через `CustomEvent('puddletag-load-job')`.
    - Раскатка на оба боевых сервера: `nl_music` (194.76.168.20) и Сервер 2 (`13.143.193.207` / `mymusicdownload.verymad.net`).
- **Блок 60 (Comprehensive Security Audit, Native Go Fuzzing, Race & Concurrency Verification):**
  - **Устранение Path Traversal в `PathSandbox.Resolve` (SEC-01):**
    - Увеличение предела итераций URL-декодирования до 10 для нейтрализации deep multi-level URL encoding (`%2525252e%2525252e`).
    - Замена `strings.TrimPrefix` на `strings.TrimLeft(cleaned, "/")` для блокировки обходов через множественные слэши (`////../../etc/passwd`).
    - Нормализация Unicode fullwidth слэшей и точек (`\uff0f`, `\uff3c`, `\uff0e`) и немедленный возврат `ErrPathTraversal` при ведущих `../`.
  - **Защита от бессрочных сессионных токенов (SEC-02):**
    - В [`ValidateSessionToken`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/core/auth/auth.go) добавлена проверка метки времени из будущего (`ts > now + 300`), исключающая уязвимость бессрочной жизни сессий. Санитизация двоеточих в логинах.
  - **Нейтрализация SSRF и Argument Injection в загрузчике (SEC-03):**
    - В [`SwitchTrackVersion`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/downloader/switch_version.go) добавлена валидация схемы `http/https`, белого списка доменов `soundcloud.com` и запрет флагов, начинающихся с дефиса `-`.
    - В [`media_processor.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/downloader/media_processor.go) перед позиционным `sourceURL` в `yt-dlp` добавлен разделитель `--`.
    - В [`download_handlers.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/download_handlers.go) ошибка невалидного URL сопоставлена со статусом HTTP 400 Bad Request.
  - **Нативный фаззинг и защита ID3v2.4 от DoS/OOM (SEC-04, SEC-05):**
    - Создан сьют [`fuzz_test.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/id3/fuzz_test.go) с нативными Go fuzz-тестами `FuzzParseAPIC`, `FuzzDecodeText`, `FuzzReadTags`.
    - Ограничение максимального размера фрейма обложки до 25 МБ в `parseFrames` и `ReadRawFrames` для защиты от APIC-бомб декомпрессии.
    - Добавлена проверка `len(b) < 4` в `decodeSynchsafe` против паник `slice bounds out of range`.
    - Более 500 000 итераций фаззинга завершились с 0 паник и падений.
  - **Стресс-тестирование SQLite WAL под высокой конкуренцией (PERF-01):**
    - В [`db_test.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/db/db_test.go) реализован `TestDB_ConcurrentWALStress` (20 горутин, 500 транзакций записи/чтения задач и кэша). 0 ошибок блокировки базы данных, 0 гонок данных.
  - **Клиентская безопасность Puddletag (SEC-06):**
    - В [`patterns.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/puddletag/patterns.js) ограничен размер строки шаблона (500 символов) для предотвращения ReDoS; подтверждено полное отсутствие опасных директив `x-html` и `innerHTML` во всем интерфейсе.
  - **Документация и отчетность:**
    - Сформирован сводный документ [`SECURITY_AUDIT_REPORT.md`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/SECURITY_AUDIT_REPORT.md).
- **Блок 61 (Puddletag Workstation Bugfix & Performance Optimization in Files Tab):**
  - **Устранение коллизии скриптов и сбоя инициализации компонента:**
    - В `web/templates/index.html` скрипт `file_manager.js` загружался после `puddletag_workstation.js` и перезаписывал функцию `fileManagerComponent()` устаревшей моделью данных, что приводило к `displayTracks is undefined` и отсутствию рендера строк таблицы.
    - В `web/static/js/file_manager.js` и `web/static/js/tag_editor/tag_editor.js` реализованы безопасные алиас-мосты к `puddletagWorkstationComponent()`.
    - Версии кэш-бастеров обновлены на `v=20261013_02`.
  - **Включение рекурсивного режима по умолчанию для медиатек с альбомами:**
    - В медиатеках треки разложены по подкаталогам `{Album} - {Artist}/`, из-за чего прямой вызов `GET /files?path=` без рекурсии возвращал 0 треков при наличии тысяч альбомов.
    - Установлен `recursiveMode: true` по умолчанию при открытии корневого каталога.
  - **Оптимизация производительности рендеринга и пагинация:**
    - Внедрена постраничная навигация (pagination): `currentPage: 1`, `pageSize: 100` (варианты: 50, 100, 200, 500, все), навигационные кнопки «, ‹, стр/всего, ›, » в нижней строке состояния.
    - В пустом состоянии каталога добавлен фильтр поиска папок `dirFilter` и ограничение рендера кнопок папок (slice до 60 элементов) для исключения зависания браузера при 2600+ альбомах.
    - Маппинг оригинального индекса трека `_trackIndex` во избежание рассинхронизации редактируемых ячеек и чекбоксов при фильтрации, сортировке и пагинации.
    - Корректный URL превью обложки `/tags/cover?path=...`.
  - **Полный цикл тестирования и деплоя:**
    - Все тесты `go test -race ./...` пройдены успешно (0 data races).
    - Развернуто на боевых серверах `nl_music` (194.76.168.20) и Сервер 2 (`13.143.193.207`).
- **Блок 62 (Full Library Scaling: Remove 2000 Track Truncation, SQLite Fast Metadata Cache & Workstation Optimization):**
  - **Снятие ограничения в 2000 треков:**
    - Устранен жестко зафиксированный лимит `maxTracks = 2000` в `ListFiles` (`GET /files?recursive=true`), приводивший к неполному отображению медиатек с более чем 2000 треками.
    - Введен настраиваемый параметр `limit` со значением по умолчанию `100 000` (охватывает любые масштабные коллекции) и возможностью запроса без ограничений (`limit=0`).
  - **Высокоскоростной SQLite кэш метаданных:**
    - В `internal/db/queries.go` добавлен метод `GetTracksMetadataCache(ctx)`: предзагрузка метаданных всех треков (`track_number`, `track_total`, `disc_number`, `disc_total`, `title`, `artist`, `album`, `album_artist`, `year`, `genre`, `duration`, `has_cover`, `lyrics_status`) за один SQLite запрос (~40-70 мс для всей медиатеки).
    - В `internal/api/handlers/file_handlers.go`: во время обхода `filepath.WalkDir` метаданные файлов мгновенно обогащаются из кэша по относительному пути (0 дисковых обращений к тегам). Новые неиндексированные файлы читаются напрямую через `id3.ReadTags`.
    - Структура `fileItem` дополнена полями `AlbumArtist`, `Year`, `Genre`, `TrackTotal`, `DiscNumber`, `DiscTotal`.
  - **Оптимизация производительности в `puddletag_workstation.js`:**
    - Свойства `_trackIndex` проставляются один раз при загрузке данных, устраняя паразитное пересоздание 8000+ объектов на каждый вызов геттера `displayTracks`.
    - Поддержка мгновенного O(1) поиска по `_selectedSet` при выделении любого количества треков.
    - `toggleSelectAll` и `isAllSelected` адаптированы под отфильтрованные списки.
  - **Тестирование и деплой:**
    - Добавлен тест `TestDB_GetTracksMetadataCache` в `db_test.go`.
    - Все тесты `go test -race ./...` пройдены успешно (0 data races).
    - Развернуто на боевых серверах `nl_music` (194.76.168.20) и Сервер 2 (`13.143.193.207`).
- **Блок 63 (Multi-Source Release & Tag Search for Puddletag Workstation: Deezer, MusicBrainz, Shazam):**
  - **63.1. Unified Models & MusicBrainz Client (`internal/services/resolver/`):**
    - Унифицированные DTO модели `CatalogReleaseSummary`, `CatalogTrackDetail`, `CatalogReleaseDetails`, `CatalogSearchResponse` в [`catalog_models.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/catalog_models.go).
    - Разработан клиент MusicBrainz API v2 [`musicbrainz.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/musicbrainz.go): поиск `SearchReleases` и деталей `GetRelease`, rate-limiting (с защитой от 503/429), in-memory кэширование на 5 минут, интеграция с Cover Art Archive (`coverartarchive.org`), парсинг соавторов через `\\`, дат, номеров дисков и треков.
    - Добавлена переменная `MUSICBRAINZ_USER_TOKEN` в `Config` и `.env.example`.
    - Герметичные unit-тесты [`musicbrainz_test.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/musicbrainz_test.go) с `httptest.Server`.
  - **63.2. Shazam Hybrid Provider & Unified Catalog Handlers (`internal/services/resolver/`, `internal/api/handlers/`):**
    - Реализован гибридный провайдер Shazam [`shazam.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/shazam.go): акустический фингерпринтинг локального файла через `songrec` + каталог Apple/Shazam через `ITunesClient`.
    - Unit-тесты [`shazam_test.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/shazam_test.go) с мокированием команд и API.
    - Контроллеры [`catalog_unified_handlers.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/catalog_unified_handlers.go):
      - `GET /api/catalog/search`: параллельный fan-out горутинами к Deezer, MusicBrainz и Shazam с агрегацией результатов.
      - `GET /api/catalog/deezer/album/{id}`: параллельный 8-воркерный пул дозапросов `/track/{id}` для гарантированного получения всех соавторов (`contributors`) и ISRC.
      - `GET /api/catalog/musicbrainz/release/{id}`: детальный просмотр релиза с треклистом и обложкой Cover Art Archive.
      - `GET /api/catalog/shazam/track/{id}`: акустическое или текстовое разрешение трека.
    - Регистрация маршрутов в Chi роутере ([`internal/api/router.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/router.go)) под `authMiddleware`.
    - Unit и race-тесты [`catalog_unified_handlers_test.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/catalog_unified_handlers_test.go) с проверкой изоляции PathSandbox от `../../`.
  - **63.3. Puddletag UI & No-Build Frontend (`web/templates/`, `web/static/js/`):**
    - Разработано модальное окно [`puddletag_source_modal.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/puddletag_source_modal.html) по утвержденному визуальному макету: поиск, кнопки «🔍 Искать во всех базах» и «🎵 Распознать по аудио (Shazam)», 3 вкладки с бейджами совпадений (`🟣 Deezer`, `🟠 MusicBrainz`, `🔵 Shazam`), левая панель карточек релизов, правая панель сопоставления треклиста и чекбоксы полей (`☑ Названия и авторы`, `☑ Номера треков и дисков`, `☑ Альбом и год`, `☑ Жанры`, `☑ Обложка`).
    - Модуль [`web/static/js/puddletag/sources.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/puddletag/sources.js) (`window.PuddletagSources`): клиентский движок поиска, сопоставления и применения тегов.
    - Кнопка «🌐 Базы тегов» на командном тулбаре [`puddletag_toolbar.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/puddletag_toolbar.html).
    - Интеграция в [`puddletag_workstation.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/puddletag/puddletag_workstation.js): перенос выбранных тегов в staged dirty state (янтарная подсветка ячеек таблицы), обновление превью обложки, возможность ручной доработки и надежное сохранение по `Ctrl+S`.
    - Подключение шаблона модала в [`file_manager.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/file_manager.html) и [`tag_editor.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/modals/tag_editor.html), подключение скрипта `sources.js` в [`index.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/index.html).
  - **Тестирование и верификация:**
    - `go test -race ./...` — **100% PASS (0 data races)** во всех пакетах.
    - `go build -ldflags="-s -w" -o bin/music-manager cmd/server/main.go` — успешная компиляция бинарника.

- **Блок 64 (MusicBrainz OAuth 2.0 Web Application Integration & Persistence):**
  - **64.1. Конфигурация и окружение (`internal/config/`, `.env.example`):**
    - В `Config` добавлены поля `MusicBrainzClientID`, `MusicBrainzClientSecret`, `MusicBrainzRedirectURI`.
    - В `.env.example` задокументированы параметры подключения официального Web Application MusicBrainz OAuth 2.0 с объяснением регистрации и scope (`profile tag rating collection`).
  - **64.2. MusicBrainz OAuth 2.0 Engine (`internal/services/resolver/musicbrainz.go`):**
    - В `MusicBrainzClient` реализованы методы:
      - `GetAuthorizationURL`: генерация ссылки авторизации с передачей `response_type=code`, `client_id`, `redirect_uri`, `scope`, `access_type=offline`, `state`.
      - `ExchangeCode`: обмен authorization code на access_token и refresh_token через `POST /oauth2/token`.
      - `RefreshToken`: автоматическое и ручное обновление токена через `grant_type=refresh_token`.
      - `FetchUserInfo`: запрос `sub` (имени пользователя) через `GET /oauth2/userinfo`.
      - `SetTokens`, `SetAccessToken`, `SetRefreshToken`, `Disconnect`, `GetOAuthStatus`.
      - Автоматическое упреждающее обновление access токена (`ensureToken`) при оставшемся сроке жизни менее 1 минуты перед выполнением запросов к каталогу.
      - Повышение rate limit с 1 req/s (1050ms) до 4 req/s (250ms) при активной авторизации.
    - Герметичные unit-тесты `musicbrainz_test.go` с mock-сервером `httptest.Server` (100% PASS c `-race`).
  - **64.3. Персистентность сессий в SQLite (`internal/db/`):**
    - В схему SQLite добавлена таблица `app_settings` (key, value, updated_at).
    - В `internal/db/queries.go` добавлены методы `GetSetting`, `SetSetting`, `DeleteSetting` (покрыты тестом `TestAppSettings`).
    - В `cmd/server/main.go` реализован `dbTokenPersistence`, сохраняющий токены в SQLite при авторизации и обновлении, и автоматически восстанавливающий активную сессию пользователя MusicBrainz при перезапуске сервиса.
  - **64.4. HTTP API и обработчики (`internal/api/handlers/`, `internal/api/router.go`):**
    - Создан файл `musicbrainz_oauth_handlers.go`:
      - `GET /api/auth/musicbrainz/login`: валидация конфигурации, установка cookie с CSRF state, генерация ссылки авторизации (с поддержкой браузерного редиректа и JSON ответа).
      - `GET /api/auth/musicbrainz/callback`: обработка обратного вызова MusicBrainz, обмен кода на токены, безопасное закрытие всплывающего окна с `postMessage` или редирект в приложение.
      - `GET /api/auth/musicbrainz/status`: статус подключения (`configured`, `connected`, `username`, `expires_at`).
      - `POST /api/auth/musicbrainz/disconnect`: сброс авторизации и удаление токенов.
    - Обработчик обратного вызова также перехватывает запросы с параметром `code` на корневом пути `/`, обеспечивая надежность даже при указании корневого URL в настройках приложения MusicBrainz.
    - Unit-тесты `musicbrainz_oauth_handlers_test.go` с проверкой всех сценариев (100% PASS c `-race`).
  - **64.5. Фронтенд интерфейс (`web/templates/`, `web/static/js/`):**
    - В `web/static/js/puddletag/sources.js` добавлены методы `getMusicBrainzOAuthStatus` и `disconnectMusicBrainzOAuth`.
    - В `puddletag_workstation.js` добавлено реактивное состояние `mbOAuth`, методы `checkMusicBrainzOAuth()`, `openMusicBrainzOAuth(event)` (с открытием центрированного всплывающего окна и отслеживанием `postMessage`) и `disconnectMusicBrainzOAuth()`.
    - В шапку вкладок `puddletag_source_modal.html` добавлен статус авторизации:
      - При активном подключении: бейдж `🟢 OAuth подключен (Повышенные лимиты)` с отображением никнейма пользователя и кнопкой отключения.
      - При отсутствии подключения: кнопка `🔗 Подключить MusicBrainz (OAuth)` с красивой подсветкой.
  - **Тестирование и верификация:**
    - `go test -race ./...` — **100% PASS (0 data races)** во всех пакетах репозитория.
- **Блок 65 (Intelligent Matching & Manual Track Selection in Puddletag Source Search):**
  - **65.1. Алгоритм нечеткого сопоставления (`web/static/js/puddletag/sources.js`):**
    - Реализована функция `normalizeTitleForMatching`: отрезание расширений аудиофайлов (`.mp3`, `.flac` и т.д.), ведущих номеров дорожек (`^\d+[\.\-\s]+`), префиксов исполнителей (`Artist - `), клипового мусора в скобках, приведение к нижнему регистру и нормализация `ё` -> `е`.
    - Реализована функция `calculateTitleSimilarity`: оценка совпадений по подстрокам (containment) + биграммный коэффициент Дайса (Dice's coefficient).
    - Модернизирован `matchTracks`: жадное сопоставление (Best Match First) по матрице сходства названий и имен файлов с безопасным порогом уверенности `>= 0.50`. Если совпадений нет, строка остаётся со статусом `⚠️ Без файла (пропустить)` без риска повреждения чужих тегов.
  - **65.2. Управление состоянием и уникальностью (`web/static/js/puddletag/puddletag_workstation.js`):**
    - Создан массив `targetTrackOptions` со списком доступных локальных файлов.
    - Реализован метод `onMatchedFileChange(pairIndex, newOrigIndexStr)` со строго уникальным сопоставлением 1-к-1: при выборе файла в другой строке он автоматически освобождается в предыдущей.
    - Реализованы методы `toggleAllMatched(checked)`, `isAllMatchedChecked()`, `reMatchTracks()` (кнопка «Авто-сопоставить заново»).
    - В `applySourceTags()` теги применяются только к парам, где `isMatched && isChecked`.
  - **65.3. Интерфейс окна поиска (`web/templates/components/puddletag_source_modal.html`):**
    - В каждой строке треклиста добавлен чекбокс `[✓]` для выборочного включения/отключения применения тегов.
    - В столбце «Файл таблицы» размещен выпадающий список `<select>` с опцией `⚠️ Без файла (пропустить)` и списком локальных файлов.
    - В шапку таблицы добавлены: мастер-чекбокс в `<th>`, информационный бейдж `Сопоставлено: X из Y файлов` и кнопка `⚡ Авто-сопоставить`.
    - В нижней панели динамический счётчик отображает точное количество выбранных сопоставленных дорожек (`matchedPairs.filter(p => p.isMatched && p.isChecked).length`).
- **Блок 66 (Интерактивный поиск и ручной фоллбэк для ненайденных треков / Manual Fallback & Source Picker):**
  - **66.1. База данных SQLite и персистентность ручных пинов (`internal/db/`):**
    - Добавлены поля `pinned_source_url` в таблицы `tracks` и `missing_tracks`.
    - Создана таблица `pinned_track_sources` (`cache_key`, `artist_name`, `track_title`, `isrc`, `pinned_url`, `source_type`, `duration`, `updated_at`) и индекс `idx_pinned_sources_artist_title`.
    - Реализованы запросы `SavePinnedSource`, `GetPinnedSource`, `UpdateTrackPinnedURL`, `UpdateMissingTrackPinnedURL`.
  - **66.2. Резолвер и мульти-поиск кандидатов (`internal/services/resolver/`):**
    - Разработан параллельный Fast Fan-Out модуль `SearchMultiCandidates` (`candidate_search.go`): одновременный опрос YouTube Music (Innertube API), SoundCloud (`scsearch10`) и YouTube (`ytsearch10`) через горутины и `sync.WaitGroup`.
    - Реализован скоринг кандидатов `calculateCandidateScore` (0–100) с бонусом за официальные Topic/YTMusic релизы (+25), дельту хронометража ($\le 2$с: +25, $\le 5$с: +15, $> 30$с: -30), совпадение названия/автора и штрафами за live/remix/slowed/reverb.
    - Добавлена поддержка домена `bandcamp.com` в `IsAllowedURL`, `DetectSource` и `Resolve`.
    - Проверка `GetPinnedSource` в `FindBestMatchWithDetails` с наивысшим приоритетом перед онлайн-поиском.
  - **66.3. Стриминг предпрослушивания (Preview Stream Proxy):**
    - Эндпоинт `GET /api/downloads/preview?url=...` (`download_preview_handler.go`): извлечение прямого потока через `yt-dlp -g` и кодирование фрагмента 15 секунд со сдвигом на 30с (`ffmpeg -ss 30 -t 15 -f mp3 -`) потоково в `ResponseWriter` с `Content-Type: audio/mpeg` без сохранения на диск.
    - Добавлен метод `playPreviewStream(streamUrl, title, artist)` в `player.js` для мгновенного прослушивания через встроенный Web Audio Player.
  - **66.4. Движок загрузчика и статус `PARTIAL_READY` («Пакетная докачка»):**
    - Внедрен статус задачи `StatusPartialReady = "PARTIAL_READY"` и метод `SetPartialReady(completed, total)`.
    - При сбое поиска отдельных треков альбома они помечаются как `waiting_source`, а задача переходит в статус `PARTIAL_READY` (например, "9 из 10 треков готовы").
    - Кнопки «В теги» и «Весь релиз» разблокированы для `PARTIAL_READY`: пользователь может сразу отправить готовые треки в Tag Editor, не ожидая проблемные.
    - Метод `SwitchTrackVersion` адаптирован для любых разрешенных источников (YouTube, YouTube Music, SoundCloud, Bandcamp): прямое скачивание, транскодирование в MP3 320 kbps CBR, накат канонических ID3v2.4 тегов альбома и оригинальной обложки, обновление статуса и сохранение пина в базу данных.
  - **66.5. HTTP API & Маршрутизация:**
    - `GET /api/downloads/candidates`: параллельный поиск кандидатов по `{Artist} - {Title}` или произвольному запросу.
    - `GET /api/downloads/preview`: проксирование превью аудиопотока.
    - `POST /api/downloads/tracks/{trackId}/bind-source`: ручная привязка источника для трека из очереди.
    - `POST /api/downloads/backfill/bind-source`: привязка источника и докачка отсутствующего трека альбома из аудита.
  - **66.6. No-Build фронтенд (Alpine.js + Tailwind):**
    - Создан компонент модального окна `downloader_source_picker_modal.html`:
      - Вкладка 1 (Прямая ссылка): инпут для вставки ссылки на YouTube, YouTube Music, SoundCloud, Bandcamp + кнопка «Привязать и скачать».
      - Вкладка 2 (Мульти-поиск кандидатов): карточки с иконками источников (SoundCloud / YouTube / Topic), название и автор, дельта длительности $\Delta$ с цветовой подсветкой ($\le 3$с зеленый, $\le 30$с желтый, $> 30$с красный), битрейт/кодек потока, кнопка ▶ Preview и кнопка «Выбрать».
      - Инлайн-индикатор прогресса внутри модального окна с отображением шагов обработки и автозакрытием при успехе.
    - Создан стор `Alpine.store('sourcePicker')` (`source_picker.js`).
    - Добавлены кнопки «🔍 Выбрать источник» / «Источник» в карточки очереди (`downloader_queue.html`) и таблицу неполных альбомов (`audit_incomplete.html`).
    - Поддержка `PARTIAL_READY` во всех компонентах очередей (`downloader_queue.html`, `queue_manager.js`, `queue_editor_bridge.js`, `polling.js`).
  - **66.7. Тестирование:**
    - Добавлены юнит-тесты для `SavePinnedSource`/`GetPinnedSource`/`UpdateTrackPinnedURL` в `db_test.go`.
    - Добавлены юнит-тесты `IsAllowedURL`, `calculateCandidateScore`, `SearchMultiCandidates` в `candidate_search_test.go`.
    - Добавлены юнит-тесты валидации эндпоинтов `PreviewAudio`, `GetCandidates`, `BindBackfillSource` в `download_handlers_test.go`.
    - Добавлены юнит-тесты `SetPartialReady` и сводки очереди в `job_test.go`.
    - Все 13 пакетов репозитория успешно проходят `go test -race ./...` (0 data races), бинарник компилируется чисто.

  - **Блок 67: Release Radar, Gap Detector, «Моя Волна», ICY Audio Streamer & Navidrome Integration:**
    - **67.1. Конфигурация и персистентная схема SQLite:**
      - Добавлены настройки в `internal/config/config.go` и `.env.example`: `RadioStreamToken`, `RadioWaveSize`, `NavidromeDBPath`, `NavidromeURL`, `NavidromeUser`, `NavidromePassword`, `LastfmAPIKey`, `RadarScanIntervalHours`, `RadarAutoDownload`.
      - В схему `internal/db/schema.sql` добавлены персистентные таблицы: `tracked_artists`, `radar_releases`, `artist_relations`, `radio_history`, с индексами. Настроено их сохранение при вызове `ResetLibraryTables`.
      - Автоматическое добавление артистов в `tracked_artists` при фоновом сканировании медиатеки (`artist_album.go`).
      - В `internal/db/radar_radio_queries.go` реализован полный набор методов выборки и мутаций: `UpsertTrackedArtist`, `GetTrackedArtists`, `GetTrackedArtistByName`, `SetArtistFollowed`, `SetArtistAutoDownload`, `UpdateArtistCustomLinks`, `UpdateArtistLastChecked`, `UpsertRadarRelease`, `GetRadarFeed`, `GetArtistDiscography`, `SetRadarReleaseStatus`, `GetRadarUnreadCount`, `GetWaveCandidateTracks`, `GetTracksByGenre`, `GetTracksByArtists`, `GetAvailableGenres`, `RecordRadioHistory`, `UpsertArtistRelation`, `GetRelatedArtistsFromDB`.
    - **67.2. Каталожные клиенты, агрегатор дискографий и движок схожести:**
      - Расширен `DeezerClient` (`client_deezer.go`): реализованы методы `SearchArtist` и `GetRelatedArtists`.
      - Расширен `MusicBrainzClient` (`musicbrainz.go`): добавлен метод `GetArtistReleaseGroups`.
      - Создан чистый HTTP-клиент `LastfmClient` (`client_lastfm.go`) для метода `artist.getsimilar`.
      - Создан агрегатор `DiscographyAggregator` (`discography_aggregator.go`): нормализация названий (`CleanReleaseTitle`), отсечение Deluxe/Remaster маркеров, объединение дубликатов по дельте года $|\Delta \text{Year}| \le 1$, сверка с локальной медиатекой и присвоение статусов (`in_library`, `version_exists`, `new`, `missing`).
      - Создан движок связей `SimilarityEngine` (`similarity.go`): композитный скоринг схожести исполнителей на базе Deezer и Last.fm с кэшированием в SQLite.
    - **67.3. Радио, Моя Волна, ICY Streamer и Navidrome:**
      - Создан селектор `WaveSelector` (`selector.go`): алгоритм генерации очереди (45% comfort / любимые, 30% forgotten / забытые, 25% discovery / открытия), защита от повторов (6 часов для трека, 3 трека подряд для одного артиста), станции по жанру и артисту.
      - Создан `NavidromeReader` (`navidrome.go`): безопасное read-only (`mode=ro`) чтение `navidrome.db` для учета избранного (`starred`) и счетчиков прослушиваний.
      - Создан `NavidromeSubsonicClient` (`navidrome.go`): автоматическая регистрация потока как интернет-радиостанции в Navidrome Subsonic API (`createInternetRadioStation`).
      - Создан потоковый сервер `IcyStreamer` (`streamer.go`): Zero-Transcoding MP3 320 kbps CBR прямое чтение с диска, пропуск ID3v2 заголовка, burst-буфер 120 КБ (3 сек), пейсинг-таймер 40 КБ/с (1.0x realtime), инжекция метаданных `StreamTitle` блоками по 16384 байт при наличии заголовка `Icy-MetaData: 1`.
      - Создан генератор плейлистов `M3UGenerator` (`m3u.go`): атомарная генерация `#EXTM3U` с относительными путями (`Playlists/Моя Волна.m3u`). Фоновый ежедневный запуск в 05:00 и начальная генерация при старте сервера.
    - **67.4. Сервис Release Radar & Gap Detector:**
      - Создан `RadarService` (`internal/services/radar/service.go`): периодический опрос каталогов с настраиваемым интервалом, Paced Worker с задержкой 1.5 сек между артистами для защиты от 429 Too Many Requests, отслеживание живого прогресса (`ScanProgress`), автозагрузка новых релизов через `downloader.Service`.
    - **67.5. HTTP API & Маршрутизация:**
      - Создан `RadarHandler` (`radar_handlers.go`) и маршруты `/api/radar/*`: `GET /feed`, `GET /unread-count`, `GET /artists`, `POST /artists`, `POST /artists/{name}/follow`, `POST /artists/{name}/auto-download`, `POST /artists/{name}/custom-links`, `GET /artists/{name}/discography`, `POST /scan`, `GET /progress`, `POST /cancel`, `POST /releases/{id}/status`.
      - Создан `RadioHandler` (`radio_handlers.go`) и маршруты `/api/radio/*`: публичные/токен-авторизованные потоки `GET /stream/wave`, `GET /stream/genre/{name}`, `GET /stream/artist/{name}`; защищенные эндпоинты `GET /wave/queue`, `GET /wave/stats`, `GET /genres`, `POST /m3u/generate`, `POST /navidrome/sync-radio`.
      - Сборка и запуск сервисов в `cmd/server/main.go` с graceful shutdown.
    - **67.6. No-Build фронтенд (Alpine.js + Tailwind + Material Symbols):**
      - Добавлены вкладки `📡 Радар` (с бейджем непросмотренных новинок) и `📻 Радио` в `header.html`.
      - Создан шаблон `web/templates/components/radar.html`: лента релизов с фильтрами типов (`album`, `single`, `ep`, `compilation`), поиск, таблица отслеживаемых артистов, прогресс-бар сканирования.
      - Создано модальное окно `web/templates/modals/radar_discography_modal.html`: полная раскладка дискографии со статусами пробелов и настройкой кастомных ID каталогов.
      - Создан шаблон `web/templates/components/radio.html`: неоморфический баннер Моей Волны, кнопки запуска потока, генерации M3U и экспорта в Navidrome, станции по жанрам и артистам, живая очередь кандидатов.
      - Созданы клиентские сторы `web/static/js/radar/radar.js` и `web/static/js/radio/radio.js`.
      - В `web/static/js/player.js` внедрен метод `playRadioStream` с интеграцией в спектроанализатор Web Audio API.
    - **67.7. Тестирование:**
      - Написаны и успешно пройдены модульные тесты для всех компонентов: `db_test.go`, `discography_similarity_test.go`, `radio_test.go`, `radar_test.go`, `radar_handlers_test.go`, `radio_handlers_test.go`.
      - `go test -v -race ./...` — **100% PASS, 0 data races**. Бинарник компилируется чисто.

  - **Блок 68 (CI/CD Automated Deployment via GitHub Actions & systemd):**
    - **Выделенный SSH Deploy Key:**
      - Сгенерирована отдельная ключевая пара ED25519 (`github-actions-music-manager-deploy`).
      - Открытый ключ безопасно прописан в `~/.ssh/authorized_keys` на обоих боевых серверах (`nl_music` и `Server 2`).
      - Закрытый ключ сохранен в GitHub Actions Secrets (`DEPLOY_SSH_KEY`) через GitHub CLI.
    - **GitHub Actions Workflow (`.github/workflows/deploy.yml`):**
      - Автоматический запуск при каждом `git push` в ветку `main` и ручной запуск через `workflow_dispatch` с выбором целевого сервера (`all`, `nl_music`, `server2`).
      - Очередь `concurrency: deploy-production` с `cancel-in-progress: false` против коллизий параллельных деплоев.
      - CI Job: полный прогон модульных тестов (`go test -v ./...`).
      - Build Job: кросс-компиляция статического бинарника `CGO_ENABLED=0 GOOS=linux GOARCH=amd64` со встроенными шаблонами и статикой через `go:embed`.
      - CD Matrix Job: параллельная раскатка на `nl_music` (порт 56924, `/opt/music-manager`, пользователь `music:media`) и `Server 2` (порт 56924, `/root/music_service`, пользователь `root:root`).
      - Атомарная замена исполняемого файла с созданием бэкапа `bin/music-manager.bak`, перезапуск `systemd` и опрос эндпоинта `/api/health` с авто-откатом при ошибках.
    - **Локальный скрипт быстрой раскатки (`scripts/deploy.sh`):**
      - Скрипт `./scripts/deploy.sh [all|nl_music|server2]` для быстрого деплоя из терминала в один клик.
    - **Документация:**
      - Обновлен `README.md` (раздел 5.4).

---

## 4. Верификация и Продакшен-Деплой

1. **Компиляция бинарника:**
   ```bash
   go build -ldflags="-s -w" -o bin/music-manager cmd/server/main.go
   ```
2. **Модульные и интеграционные тесты:**
   ```bash
   go test -race ./...
   ```
   **Результат:** 100% PASS, 0 race conditions во всех 13 пакетах.

3. **Развертывание на боевых серверах (коммиты `00d8afa`, `5ec223c`):**
   - **Сервер 1 (`nl_music`):** `https://musicdownloader.verymad.net/`
     - Git: актуализирован до `main` (`5ec223c`).
     - Бинарный файл: синхронизирован в `/opt/music-manager/bin/music-manager`.
     - Переменные окружения: `MUSICBRAINZ_CLIENT_ID`, `MUSICBRAINZ_CLIENT_SECRET`, `MUSICBRAINZ_REDIRECT_URI` сконфигурированы в `.env`.
     - Сервис: `systemctl restart music-manager.service` (active/running).
     - Проверка эндпоинтов:
       - `https://musicdownloader.verymad.net/api/health` — `200 OK`.
       - `https://musicdownloader.verymad.net/api/auth/musicbrainz/callback` — `400 Bad Request`.
   - **Сервер 2:** `https://mymusicdownload.verymad.net/`
     - Git: актуализирован до `main` (`5ec223c`).
     - Бинарный файл: синхронизирован в `/root/music_service/bin/music-manager`.
     - Переменные окружения: `MUSICBRAINZ_CLIENT_ID`, `MUSICBRAINZ_CLIENT_SECRET`, `MUSICBRAINZ_REDIRECT_URI` сконфигурированы в `.env`.
     - Nginx: настроен сквозной проброс статус-кодов приложения (`proxy_intercept_errors off`).
     - Сервис: `systemctl restart music-manager.service` (active/running).
     - Проверка эндпоинтов:
       - `https://mymusicdownload.verymad.net/api/health` — `200 OK`.
       - `https://mymusicdownload.verymad.net/api/auth/musicbrainz/callback` — `400 Bad Request`.




