# Original User Request

## 2026-09-06T21:20:35Z

Complete the implementation of Music Downloader & Tag Manager to production-ready state by developing the remaining components from Architecture.md and PROGRESS.md (Block 3: Metadata/Tags, Lyrics, File Manager, Organizer; Block 4: No-build HTML5/Tailwind/Alpine.js Frontend; Block 5: systemd deployment), with comprehensive tests and strict adherence to architectural invariants.

Working directory: /home/kex1tu/Рабочий стол/music download/music-manager
Integrity mode: development

## Requirements

### R1. Metadata & Tag Service (`app/services/tag_service.py`, `app/schemas/tag.py`, `app/routers/tags.py`)
- Provide single-point ID3v2.4 reading and writing via `mutagen.id3.ID3`.
- Support multi-value tags (Artist, Album Artist, Genre) stored natively as null-separated (`\x00`) text frames in ID3v2.4, while parsing from and presenting to user input as double backslash (`\\`).
- Embed front album art into `APIC` frame (`type=3`), preserving image formats without re-encoding unless exceeding maximum dimensions.
- Implement two-level tag editing (Album-level defaults applied across tracklist with track-level overrides) and batch persistence (`PUT /tags/batch`, `GET /tags?path=...`).
- Route all file path operations through `PathSandbox.resolve()`.

### R2. Lyrics Service (`app/services/lyrics_service.py`, `app/schemas/lyrics.py`, `app/routers/lyrics.py`)
- Fetch synchronized lyrics from LRCLIB API (`.lrc`) with fallback to unsynchronized lyrics from Genius API (`.txt`).
- Save lyrics strictly as external standalone files (`.lrc` or `.txt`) adjacent to the target MP3 file with matching base filename; never embed lyrics inside ID3 frames.
- Provide retrieval endpoint `GET /lyrics/search` and direct save endpoint `POST /lyrics` (supporting manual input or text/lrc file upload).
- Route all file path operations through `PathSandbox.resolve()`.

### R3. File Manager Service (`app/services/file_manager_service.py`, `app/schemas/files.py`, `app/routers/files.py`)
- Implement Zero-Database library filesystem browsing rooted in `MUSIC_DIR` with dynamic directory tree building (`Artist` -> `Album - Year` -> `Tracks`).
- Provide safe directory listing (`GET /files?path=...`) and file/folder deletion (`DELETE /files?path=...`) strictly guarded by `PathSandbox.resolve()`.
- Provide album tracks reading (`get_album_tracks`) reading tag metadata on-the-fly via `TagService`.

### R4. Library Organizer Service (`app/services/organizer_service.py`)
- Implement target path generation and file migration according to the standard template:
  `MUSIC_DIR/{AlbumArtist}/{Album} - {Year}/{TrackNumber:02d}. {Title}.mp3` (and accompanying `.lrc`/`.txt`).
- Sanitize all path components against filesystem-forbidden characters (`/ \ : * ? " < > |`).
- Handle moves from temporary download buffer (`tmp_downloads/{job_id}/`) to `MUSIC_DIR` upon user confirmation, as well as library renames/reorganization when existing track metadata is modified.
- Automatically prune vacated empty directories in `MUSIC_DIR` during reorganization.
- Route all file path operations through `PathSandbox.resolve()`.

### R5. Frontend User Interface (`app/templates/index.html`, `app/static/js/`, `app/static/css/`)
- Single-page application using strictly no-build architecture: HTML5, Tailwind CSS via CDN, and Alpine.js via CDN (zero `package.json`, zero Node.js / bundlers).
- Global state management via `Alpine.store('app', ...)` holding active tab, authentication state, current modals, and cached directory trees.
- Downloader tab (`downloader.js`): URL input, long-polling `GET /downloads/{job_id}`, and transition to Tag Editor on completion.
- File Manager tab (`file_manager.js`): Lazy-loaded recursive directory tree browser (`GET /files`), quick actions to edit album/track or delete files.
- Two-level Tag Editor modal (`tag_editor.js`): Album-level header form with "Apply to all tracks below" action, tracklist table, lyrics modal trigger, and single batch save button (`PUT /tags/batch`).
- Lyrics modal (`lyrics_modal.js`): Search LRCLIB/Genius, manual textarea editing, file upload, and standalone save (`POST /lyrics`).

### R6. Deployment & System Integration (`music-manager.service`, `requirements.txt`, `.env.example`)
- Complete production-ready systemd unit file `music-manager.service` configured for bare-metal execution with venv uvicorn, `EnvironmentFile`, `Restart=on-failure`, and proper security hardening directives.
- Ensure all project dependencies are accurately specified in `requirements.txt`.
- Update `.env.example` with any additional environment variables introduced.

### R7. Progress Tracking & Documentation (`PROGRESS.md`)
- Update `PROGRESS.md` with accurate statuses, checkpoint descriptions, and test metrics for Blocks 3, 4, and 5 upon completion.

## Verification Plan & Acceptance Criteria

### Verification Commands
```bash
./venv/bin/python -m pytest tests/ -v
./venv/bin/ruff check app tests
```

### Acceptance Criteria
- [ ] **ID3v2.4 & Tag Invariants**:
  - `TagService.read_tags` and `TagService.write_tags` operate strictly in ID3v2.4 using `mutagen`.
  - Multi-value tags (e.g. `TPE1`, `TCON`) store `\x00` between values and map to `\\` in user interfaces.
  - Front cover art is embedded in `APIC` with picture type 3.
- [ ] **External Lyrics Invariant**:
  - Synced lyrics (`.lrc`) and plain text lyrics (`.txt`) are written exclusively as external files beside the `.mp3` file; no `USLT` / `SYLT` frames written to the MP3.
- [ ] **Zero-Database Invariant**:
  - No database files (SQLite/PostgreSQL/MongoDB/Redis) are created, configured, or imported. Directory and file metadata are computed dynamically from `MUSIC_DIR` and mutagen ID3 frames.
- [ ] **Path Sandbox Enforcement**:
  - Every file operation across `TagService`, `LyricsService`, `FileManagerService`, and `OrganizerService` resolves paths via `PathSandbox.resolve()`.
  - Path traversal attempts (`../../`, absolute paths outside roots, null bytes, URL encoded traversal) return HTTP 403 / raise `PathTraversalError`.
- [ ] **No-Build Frontend**:
  - No `package.json`, `package-lock.json`, or node_modules directory exists.
  - All styling and interactivity rely on Tailwind CDN and Alpine.js CDN.
  - All tabs (Downloader, File Manager) and modals (Tag Editor, Lyrics) function without frontend build steps.
- [ ] **Mocked External APIs**:
  - All test suites for Deezer, LRCLIB, Genius, YouTube/yt-dlp mock network interactions. Zero live network calls during test execution.
- [ ] **Test Coverage & Quality**:
  - Comprehensive unit and integration tests added in `tests/` covering `test_tag_service.py`, `test_lyrics_service.py`, `test_file_manager.py`, `test_organizer_service.py`, `test_tags_router.py`, `test_lyrics_router.py`, `test_files_router.py`.
  - `./venv/bin/python -m pytest` passes with 100% success rate (all existing 101 tests + all new tests).
  - `./venv/bin/ruff check app tests` produces 0 lint or formatting errors.
- [ ] **Systemd & Documentation**:
  - `music-manager.service` contains valid systemd unit definition.
  - `PROGRESS.md` updated to reflect completion of Blocks 3, 4, and 5.

## 2026-09-07T08:45:27Z

The user requested a small, focused team (one implementer with adversarial review).
Complete the implementation of Blocks 3 and 4 for the self-hosted **Music Downloader & Tag Manager** web application: build all remaining backend services (FileManagerService, OrganizerService, files router and schemas, batch save reorganization integration), wire all routers into FastAPI, build the complete no-build frontend (HTML5 + Tailwind CDN + Alpine.js CDN), and provide full test coverage.

Working directory: /home/kex1tu/Рабочий стол/music download/music-manager
Integrity mode: development

## Requirements

### R1. File Manager & Directory Navigation Service
Implement `FileManagerService` (`app/services/file_manager_service.py`), schemas in `app/schemas/files.py`, and endpoints in `app/routers/files.py`:
- Safe directory navigation and tree exploration within `MUSIC_DIR` using `PathSandbox.resolve()`.
- Lazy-loading listing of directory contents (folders vs MP3 audio files with basic metadata).
- Reading album tracks and metadata for display in the file browser.
- Safe deletion of audio files or folders within `MUSIC_DIR` with confirmation safeguards and strict sandbox isolation.
- Authentication on all endpoints via `CurrentUserDep`.

### R2. Library Organizer Service & Batch Save Integration
Implement `OrganizerService` (`app/services/organizer_service.py`) and integrate with `app/routers/tags.py`:
- Move audio files from temporary download jobs (`TMP_DIR/{job_id}/...`) into the permanent library (`MUSIC_DIR`) following the strict naming structure:
  `MUSIC_DIR/{AlbumArtist}/{Album} - {Year}/{TrackNumber:02d}. {Title}.mp3`
- Move associated lyrics files (`.lrc` or `.txt`) and optional album art (`cover.jpg`) alongside the audio tracks.
- Sanitize directory and file names using `PathSandbox.validate_filename()` to eliminate illegal filesystem characters.
- Reorganize existing library tracks when album, artist, year, or track numbers change, cleaning up empty parent directories after moves.
- Update `batch_save_tags` in `app/routers/tags.py` so that saving tags automatically invokes `OrganizerService` for both new downloads and existing library tracks.

### R3. Router Integration & Application Assembly
- Mount `tags_router`, `lyrics_router`, and `files_router` in `app/main.py`.
- Add dependency factories in `app/dependencies.py` for `FileManagerService` and `OrganizerService` with appropriate typing annotations.
- Verify all error handlers in `app/main.py` handle exceptions from the new services (`NotFoundError`, `TagError`, `PathTraversalError`, `StoragePermissionError`).

### R4. Complete Frontend UI (No-build: HTML5 + Tailwind CSS + Alpine.js CDN)
Implement the user interface adhering to the Zero-Node.js invariant (no npm, no package.json, pure CDN):
- `app/templates/index.html`: Responsive single-page application layout with dark theme, tab navigation (Downloader vs File Manager), shared modal slots, and toast notification system.
- Global application store `Alpine.store('app', ...)` for user auth status, active tab, active modal, and directory caches.
- Downloader UI (`app/static/js/downloader.js`): URL input form, source validation, download initiation (`POST /downloads`), non-blocking polling (`GET /downloads/{job_id}`), and transition to Tag Editor upon `READY`.
- Two-level Tag Editor (`app/static/js/tag_editor.js`):
  - Album-level form: Album Artist, Album Title, Year, Genre, Cover Art preview & upload, and "Apply to all tracks below" action.
  - Track-level table: Track Number, Title, Artist, lyrics status indicator, lyrics button.
  - Batch save action sending `PUT /tags/batch` and transitioning back with notification.
- Lyrics Modal (`app/static/js/lyrics_modal.js`): Fetch synced/unsynced lyrics via `GET /lyrics/search`, view/edit lyrics in textarea, save as `.lrc`/`.txt` via `POST /lyrics`, and client-side file upload.
- File Manager UI (`app/static/js/file_manager.js`): Lazy-loading tree view of artists, albums, and tracks, click to open Tag Editor, and delete confirmation modal.

## Verification Resources
- Existing test suite with 151 passing tests across auth, bootstrap, sandbox, config, deezer, downloads, lyrics, and tags.
- Virtual environment with dependencies pre-installed at `./venv`.
- System `ffmpeg` 6.1.1 installed in the environment.

## Acceptance Criteria

### Backend & Service Integrity
- [ ] `FileManagerService` lists directories, retrieves tracks, and safely deletes paths without leaving `MUSIC_DIR`.
- [ ] `OrganizerService` moves files to `MUSIC_DIR/{AlbumArtist}/{Album} - {Year}/{TrackNumber:02d}. {Title}.mp3` and handles associated lyrics and covers.
- [ ] Reorganization cleans up empty directories after moving tracks.
- [ ] `PUT /tags/batch` correctly coordinates tag writing and file relocation for both new downloads and existing albums.
- [ ] Every filesystem operation without exception passes through `PathSandbox.resolve()`.
- [ ] All new endpoints (`/files`, `/tags`, `/lyrics`) are mounted in `app/main.py` and require authentication.

### Frontend Functionality
- [ ] The web interface loads in the browser with Tailwind CSS styling and Alpine.js interactivity without console errors.
- [ ] Downloader tab allows submitting a URL, polls job status, and automatically opens Tag Editor when job is `READY`.
- [ ] Tag Editor allows editing album-level fields, applying them to tracks, and saving via batch API.
- [ ] Lyrics modal can search, display, edit, and save `.lrc`/`.txt` files.
- [ ] File Manager tab allows exploring `MUSIC_DIR`, opening albums in the Tag Editor, and deleting files with confirmation.
- [ ] No Node.js build tools, package.json, or npm packages are created or used.

### Quality & Regression Testing
- [ ] All 151 existing tests in `tests/` continue to pass (`./venv/bin/python -m pytest`).
- [ ] Comprehensive new unit and integration tests are written in `tests/` for `FileManagerService`, `OrganizerService`, and `files_router`.
- [ ] Static analysis `./venv/bin/ruff check app tests` produces 0 errors and 0 warnings.
- [ ] `PROGRESS.md` is updated to reflect the completed state of Blocks 3 and 4.

## 2026-09-08T10:18:36Z

Исправление ошибок загрузки, расширение поддержки форматов ссылок Deezer, повышение надежности сопоставления аудиопотоков, плавное отображение прогресса загрузки в реальном времени и модернизация иконок интерфейса в веб-сервисе Music Downloader & Tag Manager.

Working directory: /home/kex1tu/Рабочий стол/music download/music-manager
Integrity mode: development

## Requirements

### R1. Поддержка cookies и обхода возрастных ограничений YouTube (Age Gate) в yt-dlp
Реализовать поддержку передачи cookies для yt-dlp через файл `cookies.txt` в корне проекта или через путь, заданный в переменной окружения `YTDLP_COOKIES_PATH` в `.env`. Если файл cookies существует, он должен автоматически передаваться в опции `yt-dlp` (`--cookies`), позволяя скачивать треки с ограничением 18+ без ошибки "Sign in to confirm your age".

### R2. Распознавание мобильных коротких ссылок Deezer (`link.deezer.com`)
Добавить полную поддержку ссылок вида `https://link.deezer.com/s/...` в белый список доменов и резолвер ссылок Deezer. Сервис должен корректно переходить по редиректам, определять тип контента (трек, альбом, плейлист) и загружать соответствующие метаданные через Deezer API.

### R3. Повышение устойчивости сопоставления треков (AudioMatcher)
Устранить падения сопоставления при поиске треков с необычной пунктуацией или дополнительными символами (например, `Call Me Karizma - SAFEWORD` из альбома 1043319862). Внедрить многоэтапный поиск: при низком начальном скоре пробовать альтернативные поисковые запросы (без пунктуации, запрос с добавлением ключевого слова "audio" или "official", либо поиск в YouTube Music), обеспечивая успешный подбор аудиопотока.

### R4. Отображение прогресса загрузки и конвертации в реальном времени
Сделать отображение прогресса в загрузчике динамичным и информативным:
- Парсить вывод `yt-dlp` в реальном времени для отслеживания фактических процентов скачивания каждого трека.
- Корректно рассчитывать и плавно обновлять общий прогресс задачи `job.progress` на этапах скачивания, транскодирования через ffmpeg и тегирования.
- Устранить застревание полосы прогресса на 15% и ошибки в расчете процентов в веб-интерфейсе.

### R5. Обновление и унификация иконок пользовательского интерфейса
Заменить текущие простые иконки и плейсхолдеры на аккуратные, единообразные векторные SVG-иконки (стиль Lucide / Heroicons) во всем интерфейсе: шапка и навигация, форма ввода загрузчика, кнопки управления задачей, редактор тегов, модальное окно текстов песен и файловое дерево медиатеки.

## Acceptance Criteria

### Загрузка и резолвинг ссылок
- [ ] Ссылки формата `https://link.deezer.com/s/...` успешно распознаются, разворачиваются и парсятся без ошибки `не является распознаваемым URL Deezer`.
- [ ] Альбом `https://www.deezer.com/ru/album/1043319862` (включая трек `Call Me Karizma - SAFEWORD`) успешно находит аудиопотоки для всех дорожек релиза.
- [ ] При наличии файла cookies треки с возрастным ограничением 18+ (например, `eV-DHvPP6d8`) скачиваются без ошибки `Sign in to confirm your age`.

### Прогресс и UI
- [ ] В процессе загрузки треков процент в полосе прогресса плавно растет от 0% до 100%, точно отражая текущее состояние (загрузка потока, конвертация, тегирование).
- [ ] Веб-интерфейс содержит качественные согласованные SVG-иконки для всех действий и разделов.

### Регрессионная стабильность
- [ ] Набор тестов (`pytest tests/`) проходит на 100% без падений.
- [ ] Полное соблюдение архитектурных инвариантов проекта: zero-database, изоляция через PathSandbox, mutagen ID3v2.4, отсутствие внешних сборщиков JS/Node.js.

## 2026-09-09T00:44:08Z

Комплексная оптимизация производительности веб-сервиса Music Downloader & Tag Manager: ускорение обработки альбомов и плейлистов, исправление отображения прогресса и процентов в реальном времени при пакетной загрузке треков, а также замена всех иконок интерфейса на Google Material Icons с получением SVG через MCP Iconify.

Working directory: /home/kex1tu/Рабочий стол/music download/music-manager
Integrity mode: development

## Requirements

### R1. Оптимизация скорости обработки и загрузки альбомов и плейлистов
Устранить длительные задержки при обработке релизов из нескольких треков:
- Распараллелить этап сопоставления аудиопотоков (`AudioMatcher`) для треков альбома/плейлиста с помощью `asyncio.gather` с семафором конкурентности (3–5 параллельных запросов) вместо строго последовательного цикла, сократив первичное разрешение релиза из 30 треков в 3–5 раз.
- Оптимизировать конвейер загрузки `yt-dlp`: приоритетно использовать быстрый гостевой режим без задержек антибота YouTube, обращаясь к cookies только при обнаружении возрастных ограничений (Age Gate).

### R2. Точный расчет и непрерывное обновление прогресса мультитрековых задач в UI
Исправить отслеживание процентов и поведение прогресс-бара:
- Скорректировать парсер вывода `yt-dlp` (`progress_regex`), поддерживающий все форматы строк прогресса (включая строки с ведущими пробелами вида `  14.7%` и процентные шаблоны), чтобы вызов `on_track_progress` корректно обновлял текущий шаг и процент трека.
- Гарантировать динамический монотонный пересчет общего прогресса задачи `job.progress` на основе реального процента активного трека и доли уже завершенных треков `completed_tracks / total_tracks`.
- Обеспечить отображение в веб-интерфейсе как общего процента альбома на шкале прогресса, так и детализированного состояния текущего трека (название, номер и его индивидуальный процент скачивания).

### R3. Интеграция Google Material Icons через MCP Iconify
Заменить текущие иконки во всех разделах интерфейса на официальные Google Material Icons (`material-symbols`), полученные через MCP-сервер `iconify`:
- Получить SVG-векторы с помощью инструмента `get_icon` (коллекция `material-symbols`, стиль outline/rounded) для навигации, кнопок загрузки, управления задачами, файлового дерева, редактора тегов и модального окна текстов песен.
- Встроить аккуратные инлайн-SVG без внешних зависимостей сборщиков (no-build инвариант), сохранив стилизацию Tailwind CSS (`currentColor`, адаптивные размеры).

## Acceptance Criteria

### Производительность и скорость
- [ ] Время предварительного сопоставления (resolving) для альбома/плейлиста из 10+ треков сокращено минимум в 3 раза благодаря параллельным асинхронным запросам поиска.
- [ ] Обычные треки скачиваются без лишних задержек антибота через оптимизированный запуск `yt-dlp`.

### Прогресс-бар и индикаторы
- [ ] При пакетном скачивании альбома прогресс-бар плавно и непрерывно продвигается от 0% до 100%, точно отражая прогресс каждого скачиваемого трека и общее состояние релиза (например, 15/30).
- [ ] В текстовой строке состояния отображаются динамически изменяющиеся реальные проценты загрузки текущего трека (не зависают на 0%).

### Дизайн и иконки
- [ ] Все иконки интерфейса используют единый стиль Google Material Icons (`material-symbols`), полученный через MCP Iconify.
- [ ] Нет сломанных или отсутствующих иконок; соблюдается принцип no-build frontend (HTML5 + Tailwind CDN + Alpine.js).

### Стабильность и качество
- [ ] Полный набор автоматических тестов (`pytest tests/`) проходит на 100% (217+ тестов).
- [ ] Проверка линтером `ruff check app tests` завершается без замечаний.
- [ ] Архитектурные инварианты (`AGENTS.md`) строго соблюдены (Zero-Database, PathSandbox, mutagen ID3v2.4).

## 2026-09-09T08:39:22Z

This is a single self-contained fix; keep it small and focused.

Fix SoundCloud playlist downloads so that individual track titles, artist names, and cover artworks are properly extracted, preserved, and written into ID3v2.4 tags and output MP3 filenames.

Working directory: /home/kex1tu/Рабочий стол/music download/music-manager
Integrity mode: development

## Requirements

### R1. SoundCloud Playlist Metadata Extraction
When resolving and downloading a SoundCloud playlist or set, each track must have its actual title, artist, and duration extracted rather than placeholder values (such as "Track 1", "Track 2", or generic playlist uploader).

### R2. SoundCloud Artwork Extraction and Tagging
Each track in a SoundCloud playlist must have valid cover artwork resolved (either specific track artwork or the playlist artwork as fallback) and embedded into the MP3's ID3v2.4 APIC tag, as well as saved to the job cover path.

### R3. Preservation of Invariants and Sandbox
All file modifications and creations must strictly use `PathSandbox.resolve()` in accordance with `AGENTS.md`. No new database, no Node.js/Docker, and ID3v2.4 tags must follow the existing mutagen tag standards.

## Acceptance Criteria

### Metadata & Tagging
- [x] Resolving a SoundCloud set or playlist produces `ResolvedTrack` items with real titles and artist names (not fallback "Track N").
- [x] Resolved SoundCloud tracks have valid artwork URLs (per-track artwork or playlist artwork fallback), and downloaded MP3 files contain embedded ID3v2.4 APIC tags with the cover image.
- [x] Generated MP3 filenames in the job directory follow the `{track_number:02d}. {title}.mp3` naming scheme with clean, sanitized track names.

### Verification
- [x] Automated tests in `tests/` are added or updated to verify SoundCloud playlist resolution and tag embedding (with mocked external calls).
- [x] The entire test suite passes cleanly (`./venv/bin/python -m pytest`).
