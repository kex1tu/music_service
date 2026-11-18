# Архитектурный документ: Music Downloader & Tag Manager

**Версия:** 2.0 (Pure Go / SQLite WAL)  
**Стек:** Go (Chi) + yt-dlp + ffmpeg + ID3v2.4 Engine + SQLite WAL + No-Build Frontend (HTML5 / Tailwind CDN / Alpine.js CDN)

---

## 1. Общая архитектура системы

### 1.1. Слои приложения

```
┌─────────────────────────────────────────────────────────────────────────┐
│                           КЛИЕНТ (браузер)                              │
│         HTML5 + Tailwind CSS CDN + Alpine.js CDN (без сборки)           │
│  ─ Downloader Tab  ─ File Manager Tab  ─ Tag Editor Modal  ─ Audit Tab  │
└────────────────────────────────────┬────────────────────────────────────┘
                                     │ HTTP / REST JSON + Session Cookie
┌────────────────────────────────────▼────────────────────────────────────┐
│                      HTTP API LAYER (Chi Router)                        │
│  ┌───────────────┐ ┌────────────────┐ ┌───────────────┐ ┌────────────┐  │
│  │ Auth Handler  │ │ Download Hndlr │ │ Tag Handler   │ │ File Hndlr │  │
│  └───────┬───────┘ └────────┬───────┘ └───────┬───────┘ └─────┬──────┘  │
│          │                  │                 │               │         │
│  ┌───────▼──────────────────▼─────────────────▼───────────────▼──────┐  │
│  │                          SERVICE LAYER                            │  │
│  │  AuthService | DownloaderService | ResolverService |              │  │
│  │  OrganizerService | ScannerService | LyricsService                │  │
│  └───────┬────────────────────────────────────┬───────────────┬──────┘  │
│          │                                    │               │         │
│  ┌───────▼───────────┐             ┌──────────▼────────┐ ┌────▼──────┐  │
│  │ PathSandbox Guard │             │  ID3v2.4 Engine   │ │ SQLite WAL│  │
│  │ (Изоляция путей)  │             │ (Парсер/Генератор)│ │ (Кэш и    │  │
│  └───────────────────┘             └───────────────────┘ │  поиск)   │  │
│                                                          └───────────┘  │
└────────────────────────────────────┬────────────────────────────────────┘
                                     │
                     ┌───────────────▼──────────────┐
                     │       Файловая система       │
                     │  tmp_downloads/ (буфер)      │
                     │  MUSIC_DIR/ (медиатека)      │
                     └──────────────────────────────┘
```

---

## 2. Модульная структура репозитория

```
music-manager/
├── cmd/
│   └── server/
│       └── main.go                 # Точка входа сервера, инициализация DI, запуск HTTP-сервера
├── internal/
│   ├── config/                     # Чтение и валидация .env (Settings)
│   ├── core/
│   │   ├── auth/                   # Bcrypt пароли, HMAC-SHA256 сессии, auth middleware
│   │   └── sandbox/                # PathSandbox: абсолютная изоляция путей и защита от Traversal
│   ├── db/                         # SQLite (modernc.org/sqlite WAL), миграции, транзакции
│   │   ├── schema.sql              # Схема таблиц: tracks, albums, artists, genres, download_jobs
│   │   ├── sqlite.go               # Пул соединений и инициализация
│   │   └── queries.go              # CRUD запросы к БД
│   ├── id3/                        # Нативный Go ID3v2.4 парсер и сериализатор
│   │   ├── reader.go               # Чтение ID3 тегов и APIC обложек
│   │   ├── writer.go               # Запись ID3v2.4 UTF-8 с null-разделителями (\x00)
│   │   ├── decoder.go              # Декодирование текстовых кодировок
│   │   ├── frame_handlers.go       # Обработчики фреймов (TCON, TPE1, APIC, TPOS, TRCK, COMM)
│   │   ├── tag.go                  # Структура Tag и санитизация
│   │   └── genre.go                # Таблица канонических жанров и нормализация
│   ├── services/
│   │   ├── downloader/             # Пайплайн загрузки и управление очередью
│   │   │   ├── downloader.go       # Жизненный цикл сервиса, запуск задач
│   │   │   ├── pipeline.go         # Асинхронное выполнение yt-dlp/ffmpeg
│   │   │   ├── media_processor.go  # Транскодирование MP3 320k CBR и тегирование
│   │   │   ├── manager.go          # JobManager с синхронизацией в SQLite
│   │   │   ├── job.go              # Модель DownloadJob и монотонный прогресс
│   │   │   └── cleaner.go          # Фоновая очистка временных файлов
│   │   ├── resolver/               # Разрешение источников и сопоставление
│   │   │   ├── resolver.go         # Оркестратор SourceResolver
│   │   │   ├── matcher.go          # SoundCloud-First AudioMatcher
│   │   │   ├── client_deezer.go    # HTTP-клиент Deezer API (Anti-403)
│   │   │   ├── client_itunes.go    # Fallback клиент iTunes Search API
│   │   │   ├── score.go            # Скоринг соответствия треков
│   │   │   └── text_utils.go       # Unicode NFC и транслитерация кириллицы
│   │   ├── organizer/              # Организация файловой структуры
│   │   │   ├── organizer.go        # MoveAlbum и синхронизация директорий
│   │   │   ├── path.go             # Формирование канонических путей
│   │   │   ├── companions.go       # Перемещение сопутствующих (.lrc/.txt)
│   │   │   ├── covers.go           # Сохранение {TrackBaseName}.jpg и cover.jpg
│   │   │   └── fs_utils.go         # Безопасное удаление пустых директорий
│   │   ├── scanner/                # Фоновый сканер медиатеки
│   │   │   ├── scanner.go          # Оркестрация сканирования и fsnotify watcher
│   │   │   ├── indexer.go          # Индексация ID3 тегов в SQLite
│   │   │   ├── artist_album.go     # Управление артистами и альбомами
│   │   │   └── incomplete.go       # Чекер неполных релизов через Deezer Catalog
│   │   └── lyrics/                 # Сервис текстов песен (LRCLIB + Genius)
│   └── api/
│       ├── router.go               # Сборка Chi роутера, middleware и статики
│       └── handlers/               # 18 модульных хендлеров HTTP API
│           ├── file_handlers.go    # Дерево файлов и удаление
│           ├── audit_handlers.go   # Дашборд аудита медиатеки
│           ├── download_handlers.go# Задачи загрузки и повтор (retry)
│           ├── tag_handlers.go     # Чтение тегов
│           ├── tag_save_handlers.go# Сохранение пакетов тегов
│           ├── tag_cover_handlers.go# Галерея обложек
│           └── ...
├── web/
│   ├── templates/                  # Модульные HTML шаблоны компонентов
│   │   ├── index.html              # Базовый SPA макет
│   │   └── components/             # Компоненты (downloader, tag_editor, etc.)
│   ├── static/
│   │   ├── css/style.css           # Базовые стили
│   │   └── js/                     # Модульный JavaScript (Alpine.js)
│   └── embed.go                    # Встраивание статики в бинарник
```

---

## 3. Ключевые компоненты и конвейеры

### 3.1. Аудио-пайплайн загрузки (SoundCloud-First)

```
URL / Поисковый запрос
       │
       ▼
[SourceResolver]
       │
       ├──► Deezer API / iTunes (Получение официального треклиста и обложки 1400x1400)
       │
       ▼
[AudioMatcher] (SoundCloud-First)
       │
       ├── 1. Поиск в SoundCloud (scsearch3: ...) -> Проверка скора (>= 70.0) -> Выбор Explicit версии
       └── 2. Fallback на YouTube / YouTube Music (ytsearch5: ...) -> Сверка длительности (+-2с)
       │
       ▼
[DownloadService Pipeline]
       │
       ├── 1. yt-dlp -> Скачивание аудиопотока в tmp_downloads/{job_id}/raw/
       ├── 2. ffmpeg -> Транскодирование в MP3 320 kbps CBR, 44.1 kHz
       ├── 3. id3.Writer -> Вшивание тегов ID3v2.4 UTF-8 (\x00 multi-values) и APIC обложки
       └── 4. Сохранение в tmp_downloads/{job_id}/converted/
```

### 3.2. Стандарт тегирования ID3v2.4
- **Версия:** ID3v2.4.0 UTF-8 (`\x03`).
- **Множественные значения:** Сохраняются нативно через нулевой байт `\x00` внутри фреймов (`TPE1` — исполнители, `TCON` — жанры), что распознается Navidrome, Plex, Foobar2000, AIMP как раздельные сущности.
- **Обложки (`APIC`):** Picture Type 3 (`Cover Front`), форматы `image/jpeg` или `image/png`.
- **Номера треков и дисков:** Фреймы `TRCK` (`{track}/{total}`) и `TPOS` (`{disc}/{total}`).
- **Инвариант текстов песен:** Тексты никогда не вшиваются во фрейм `USLT` — только внешние файлы `.lrc` / `.txt` рядом с аудиофайлом.

### 3.3. Каноническая файловая структура
```
MUSIC_DIR/
└── {Album} - {Album Artist}/
    ├── 01. {Title}.mp3
    ├── 01. {Title}.lrc
    ├── 01. {Title}.jpg
    ├── 02. {Title}.mp3
    └── cover.jpg
```
Для синглов: `MUSIC_DIR/{Title} - {Artist}/01. {Title}.mp3`.

### 3.4. Изоляция и безопасность (PathSandbox)
- Любая файловая операция (`Read`, `Write`, `Delete`, `Move`, `Open`) выполняется **строго** после валидации `PathSandbox.Resolve(userPath)`.
- После `Resolve` операции проходят через `internal/core/safefs`. Для путей внутри зарегистрированных MUSIC_DIR/TMP_DIR слой использует удерживаемый `os.Root`, а не повторное открытие абсолютного пути. Так подмена симлинка между проверкой и I/O не выводит операцию за корень.
- Sandbox должен создаваться после создания корневого каталога; дескриптор привязывается к нему при инициализации. Реестр хранит слабые ссылки и не удерживает дескрипторы умерших sandbox. Неактивные зарегистрированные корни отклоняются, не переходят к обычному `os.*`.
- Служебные пути конфигурации/утилит вне медиакорней сохраняют обычные файловые операции. Они не должны использоваться для клиентских путей.
- `internal/core/mediaexec` передаёт локальные входные файлы и выходные MP3 медиапроцессам через ExtraFiles и `/proc/self/fd`. Выходные каталоги yt-dlp привязаны к дескрипторам и закрыты для других пользователей (0700). Это Linux-механизм, соответствующий целевой платформе проекта.
- Защищены traversal, нулевые байты и выходящие за корень симлинки; удаление самого корня запрещено и на уровне API, и в файловом слое.

### 3.5. Схема базы данных SQLite (WAL Mode)
- **`tracks`:** ID, FilePath, Title, Artist, Album, Year, TrackNumber, DiscNumber, Duration, Bitrate, HasCover, LyricsStatus, MTime, CreatedAt.
- **`albums`:** ID локального издания, уникальный относительный корень папки, метаданные из тегов и статус сверки треклиста.
- **`artists`:** ID, Name.
- **`track_genres`:** Связь треков и канонических жанров (`track_id`, `genre_id`).
- **`download_jobs`:** ID, Status, URL, Source, TargetAlbumPath, Title, Artist, TotalTracks, CompletedTracks, Progress, Error, CreatedAt.
- **`audit_issues`:** ID, IssueType, FilePath, ExpectedPath, Details, Resolved.
- **`missing_tracks`:** ID, AlbumID, позиция «диск, трек», ID трека Deezer, ISRC, статус и ручной источник. Задача докачки хранит выбранные каталожные позиции.

---

## 4. No-Build Frontend Архитектура

- **Принцип:** Нулевая сборка (Zero-Node.js, Zero-Webpack, Zero-Vite).
- **Стек:** HTML5 + Tailwind CSS CDN + Alpine.js CDN + Google Material Symbols SVG.
- **Модули скриптов (`web/static/js/`):**
  - `store/queue_manager.js` + `queue_editor_bridge.js` — управление очередью и мост с редактором тегов.
  - `downloader/downloader.js` + `batch.js` + `polling.js` — компонент загрузчика.
  - `tag_editor/tag_editor.js` + `loader.js` + `saver.js` + `releases.js` + `cleaner.js` + `covers.js` — модальный редактор тегов.
  - `file_manager.js` — файловый менеджер и дерево каталогов.
  - `library_audit.js` — дашборд аудита медиатеки.
  - `lyrics_modal.js` — модальное окно текстов песен.
- **Встраивание:** Шаблоны и статика вкомпилированы в бинарник `bin/music-manager` через `go:embed`.
## Уточнение 2026-11-08: ручные источники и аудит цензуры

Автоматический SoundCloud-first, применение pin/cache и переключение площадки
после ошибки заменены ручным выбором из общей выдачи. Один metadata evaluator
используется для треков, каталога и пар треклиста. Глобальное назначение с пустыми
парами не использует один URL дважды. Площадка и кириллица не доказывают цензуру;
исторические описания таких бейджей выше не являются действующим алгоритмом.

В audit slice добавлена постоянная очередь censorship_runs/items, отдельные
кэш результатов и расшифровок, таблица доказательств и дневной учёт облака.
Один worker последовательно вызывает ffmpeg и необязательный HTTP transcriber.
MP3 и LRC только читаются через PathSandbox и descriptor-relative safefs/mediaexec.
Хэш демультиплексированного аудио исключает ID3; смена тегов не вызывает новую
расшифровку. Gemini/Deepgram получают только аудио и общую инструкцию, без текста
эталона. Сравнение выполняется после расшифровки. Локальные сигналы и отличия
эталона являются гипотезами, намеренность подтверждается пользователем.
