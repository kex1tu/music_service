# Архитектурный документ: Music Downloader & Tag Manager

Версия: 1.0
Основа: ТЗ "Music Downloader & Tag Manager" (self-hosted, FastAPI + yt-dlp/ffmpeg/mutagen, фронтенд без сборки)

---

## 1. Общая архитектура системы

### 1.1 Слои приложения

```
┌─────────────────────────────────────────────────────────────┐
│                      КЛИЕНТ (браузер)                        │
│  HTML5 + Tailwind CSS + Alpine.js (CDN, без сборки)          │
│  ─ Downloader Tab   ─ File Manager Tab   ─ Tag Editor Modal  │
└───────────────────────────┬───────────────────────────────────┘
                             │ HTTP/JSON + Cookie session
┌───────────────────────────▼───────────────────────────────────┐
│                    FASTAPI APPLICATION                        │
│  ┌───────────┐ ┌────────────┐ ┌──────────────┐ ┌───────────┐  │
│  │  Auth     │ │  Downloader│ │  Metadata /  │ │  Files    │  │
│  │  Router   │ │  Router    │ │  Tag Router  │ │  Router   │  │
│  └─────┬─────┘ └─────┬──────┘ └──────┬───────┘ └─────┬─────┘  │
│        │             │               │               │        │
│  ┌─────▼─────────────▼───────────────▼───────────────▼─────┐  │
│  │                    SERVICE LAYER                          │  │
│  │  AuthService | DownloadService | TagService |             │  │
│  │  LyricsService | FileManagerService | OrganizerService    │  │
│  └─────┬───────────────────────────────────────┬────────────┘  │
│        │                                       │                │
│  ┌─────▼─────┐                          ┌──────▼──────────┐    │
│  │ Path      │                          │  External APIs   │    │
│  │ Sandbox   │                          │  Deezer/LRCLIB/  │    │
│  │ Guard     │                          │  Genius/YouTube  │    │
│  └───────────┘                          └──────────────────┘    │
└───────────────────────────┬─────────────────────────────────────┘
                             │
              ┌──────────────▼──────────────┐
              │        Файловая система       │
              │  /tmp_downloads/ (буфер)      │
              │  MUSIC_DIR/ (постоянное       │
              │  хранилище, по структуре)     │
              └────────────────────────────────┘
```

### 1.2 Принцип разделения ответственности

| Слой | Ответственность | Не делает |
|---|---|---|
| Routers (API) | Валидация HTTP-запроса, авторизация, вызов сервиса, сериализация ответа | Бизнес-логику, работу с файлами напрямую |
| Services | Бизнес-логика: скачивание, тегирование, организация файлов | HTTP-специфику (коды ответов, cookies) |
| Sandbox Guard | Проверка и нормализация всех путей | Ничего, кроме проверки путей — вызывается из каждого сервиса, работающего с ФС |
| External API Clients | Обёртки над Deezer/LRCLIB/Genius/yt-dlp | Бизнес-логику соединения источников |

---

## 2. Структура репозитория

```
music-manager/
├── app/
│   ├── main.py                     # точка входа FastAPI, монтирование роутеров и статики
│   ├── config.py                   # чтение .env (pydantic-settings)
│   ├── dependencies.py             # get_current_user, get_settings и т.д.
│   │
│   ├── core/
│   │   ├── security.py             # bcrypt-хеширование, JWT/сессии
│   │   ├── sandbox.py              # PathSandbox — защита от Path Traversal
│   │   └── exceptions.py           # кастомные исключения приложения
│   │
│   ├── routers/
│   │   ├── auth.py                 # POST /login, POST /logout
│   │   ├── downloads.py            # POST /downloads, GET /downloads/{job_id}
│   │   ├── tags.py                 # GET/PUT /tags/{path}
│   │   ├── lyrics.py               # GET/POST /lyrics/{path}
│   │   └── files.py                # GET /files, DELETE /files/{path}
│   │
│   ├── services/
│   │   ├── auth_service.py
│   │   ├── download_service.py     # оркестрация yt-dlp + ffmpeg
│   │   ├── deezer_client.py        # обёртка над публичным Deezer API
│   │   ├── source_resolver.py      # логика поиска аудио-потока (аналог SpotDL)
│   │   ├── tag_service.py          # обёртка над mutagen, парсинг "\\"
│   │   ├── lyrics_service.py       # LRCLIB + Genius клиенты
│   │   ├── file_manager_service.py # навигация по MUSIC_DIR
│   │   └── organizer_service.py    # перемещение/переименование по шаблону
│   │
│   ├── schemas/
│   │   ├── auth.py
│   │   ├── download.py
│   │   ├── tag.py
│   │   ├── lyrics.py
│   │   └── files.py
│   │
│   └── templates/ + static/        # Jinja2-шаблоны, Tailwind/Alpine через CDN
│       ├── index.html
│       ├── static/js/downloader.js
│       ├── static/js/tag_editor.js
│       └── static/js/file_manager.js
│
├── tmp_downloads/                  # временный буфер (gitignore)
├── requirements.txt
├── .env.example
├── music-manager.service           # unit-файл systemd
└── README.md
```

---

## 3. Backend: детальная архитектура компонентов

### 3.1 Core / Bootstrap (`app/main.py`, `app/config.py`)

**Ответственность:** инициализация приложения, чтение конфигурации, монтирование роутеров, middleware.

**Компоненты:**
- `Settings` (pydantic-settings) — типизированная конфигурация: `MUSIC_DIR`, `TMP_DIR`, `SECRET_KEY`, `ADMIN_LOGIN`, `ADMIN_PASSWORD_HASH`, `SESSION_TTL`, ключи внешних API (Genius token, при необходимости).
- Middleware:
  - `SessionMiddleware` — подпись cookie сессии.
  - Глобальный exception handler, транслирующий внутренние исключения (`PathTraversalError`, `DownloadError`, `TagWriteError`) в корректные HTTP-статусы (400/403/422/500).
- При старте приложения: проверка существования и прав на запись `MUSIC_DIR` и `TMP_DIR`, создание при отсутствии.

**Взаимодействие:** `main.py` не содержит логики — только сборка (dependency injection) сервисов и роутеров.

---

### 3.2 Auth Module (`core/security.py`, `services/auth_service.py`, `routers/auth.py`)

**Ответственность:** аутентификация единственного администратора приложения.

**Компоненты:**
- `AuthService.verify_credentials(login, password)` — сверка с хэшем `bcrypt` из `.env`.
- `AuthService.create_session_token(user)` — генерация подписанного cookie (либо JWT с `exp`).
- `get_current_user` (FastAPI Dependency) — извлекает и валидирует cookie/JWT из каждого защищённого запроса; выбрасывает `401` при отсутствии/истечении.
- Middleware/Dependency применяется ко всем роутерам, кроме `POST /login` и статики.

**Схема данных:**
```
LoginRequest { login: str, password: str }
LoginResponse { success: bool }
```

**Поток:**
```
Client → POST /login {login, password}
       → AuthService.verify_credentials()
       → bcrypt.checkpw(password, stored_hash)
       → set-cookie(session_token, httpOnly, Secure, SameSite=Strict)
       → 200 OK
```

---

### 3.3 Path Sandbox Guard (`core/sandbox.py`)

Ключевой сквозной (cross-cutting) компонент безопасности — используется во всех сервисах, работающих с файловой системой.

**Класс `PathSandbox`:**
```python
class PathSandbox:
    def __init__(self, root: Path): ...
    def resolve(self, relative_path: str) -> Path:
        # 1. os.path.normpath / Path.resolve()
        # 2. проверка: resolved_path.is_relative_to(self.root)
        # 3. если нет — PathTraversalError
        # 4. возврат абсолютного безопасного пути
```

**Правила:**
- Любой путь, приходящий от клиента (даже параметр URL), обязан проходить через `sandbox.resolve()` до любой файловой операции (`open`, `os.remove`, `shutil.move`).
- Символические ссылки внутри `MUSIC_DIR`, ведущие наружу, также блокируются (`os.path.realpath` + повторная проверка).
- Модуль не знает о HTTP — чистая утилита, легко покрывается unit-тестами (`../../etc/passwd`, `%2e%2e%2f`, абсолютные пути и т.д.).

---

### 3.4 Downloader Engine

Состоит из трёх сотрудничающих сервисов:

#### 3.4.1 `source_resolver.py` — определение источника и получение метаданных
- **SoundCloud / YouTube Music:** `yt-dlp` используется напрямую — он же извлекает поток, он же отдаёт базовые метаданные (title, uploader, duration, thumbnail).
- **Deezer (без ARL):**
  1. `DeezerClient.search(track/album/playlist_url_or_query)` → публичный Deezer API отдаёт точные метаданные: название, исполнителей, ISRC, номер трека, год, обложку 1400×1400.
  2. Поскольку аудиопотока Deezer напрямую нет, `AudioMatcher.find_best_match(title, artist, duration)` формирует поисковый запрос к `yt-dlp` (YouTube/YouTube Music) и выбирает лучший результат по критериям: совпадение длительности (±2 сек), схожесть строк названия/исполнителя (Levenshtein/rapidfuzz), приоритет официальных каналов. Как запасной путь — публичные Cobalt-шлюзы.
  3. Итог: точные теги от Deezer + аудиопоток от YouTube — связываются в единую сущность `ResolvedTrack`.

#### 3.4.2 `download_service.py` — оркестрация загрузки
```python
class DownloadJob:
    id: str
    status: Enum[QUEUED, DOWNLOADING, CONVERTING, TAGGING, READY, ERROR]
    source: Literal["soundcloud", "youtube", "deezer"]
    tracks: list[ResolvedTrack]
```
- Асинхронная задача (FastAPI `BackgroundTasks` либо очередь на `asyncio.Queue` при нескольких одновременных загрузках).
- Шаги пайплайна на каждый трек:
  1. `yt-dlp` скачивает лучший аудиопоток во `TMP_DIR/{job_id}/raw/`.
  2. `ffmpeg -i raw -codec:a libmp3lame -b:a 320k -ar 44100` → `TMP_DIR/{job_id}/converted/`.
  3. Предзаполнение тегов из метаданных источника (ещё не финальная запись — только черновик для экрана проверки).
  4. Job переходит в `READY`, фронтенд получает список треков для проверки в Tag Editor.
- Прогресс задачи опрашивается через `GET /downloads/{job_id}` (long-polling) — упрощённый вариант вместо WebSocket, укладывается в требование «без лишней инфраструктуры».

#### 3.4.3 Временный буфер
- Структура: `tmp_downloads/{job_id}/converted/{n}.mp3`.
- Очистка: job и его файлы удаляются после подтверждения сохранения (`organizer_service` переносит файлы) либо по TTL сборщиком мусора (фоновая задача раз в час удаляет job старше N часов).

---

### 3.5 Metadata / Tag Service (`services/tag_service.py`)

**Ответственность:** единственная точка записи/чтения ID3-тегов через `mutagen`.

**Ключевые операции:**
```python
class TagService:
    def read_tags(self, path: Path) -> TrackTags: ...
    def write_tags(self, path: Path, tags: TrackTags) -> None: ...
    def embed_cover(self, path: Path, image_bytes: bytes, mime: str) -> None: ...
    def parse_multivalue(self, raw: str) -> list[str]:
        # "Daft Punk\\Pharrell Williams" -> ["Daft Punk", "Pharrell Williams"]
    def write_multivalue(self, id3_frame, values: list[str]) -> None:
        # запись через null-separated (\x00) в ID3v2.4, TPE1/TPE2/TCON
```

**Детали реализации:**
- Формат тегов — строго **ID3v2.4** (`mutagen.id3.ID3`, при необходимости — `update_to_v24()` для старых файлов).
- Множественные значения (Artist / Album Artist / Genre) хранятся как массив текстовых фреймов, разделённых `\x00` — это нативный механизм ID3v2.4, распознаваемый AIMP/Foobar2000/Plex/Navidrome. Разделитель `\\`, вводимый пользователем в UI, — это **только формат ввода**, конвертируемый в `\x00` при записи и обратно при чтении для отображения.
- Обложка пишется в кадр `APIC` с `type=3` (front cover), формат JPEG/PNG сохраняется как есть без перекодирования (либо ресайз, если исходник больше разумного лимита — конфигурируемый максимум, например 1500×1500).

**Двухуровневая логика записи (Album-level → Track-level):**
```
TagService.apply_album_tags(tracks: list[Path], album_tags: AlbumTags):
    for track in tracks:
        merged = merge(album_tags, existing_track_specific_fields)
        write_tags(track, merged)
```
Кнопка «Применить ко всем трекам ниже» на фронтенде лишь копирует значения в состояние формы; фактическая запись в файлы происходит одним batch-запросом при нажатии «Сохранить».

---

### 3.6 Lyrics Service (`services/lyrics_service.py`)

**Ответственность:** поиск/сохранение текстов как внешних файлов, отдельно от MP3.

**Компоненты:**
- `LrclibClient.search(artist, title, duration)` → синхронизированный текст (`.lrc`) при наличии.
- `GeniusClient.search(artist, title)` → обычный текст без таймкодов, используется как fallback.
- `LyricsService.save(track_path, content, kind: "lrc"|"txt")`:
  - Файл сохраняется **рядом** с MP3, с идентичным именем (без вшивания в теги), например `01. One More Time.lrc`.
  - Путь также проходит через `PathSandbox`.
- `LyricsService.load_for_track(track_path)` — при открытии карточки трека ищет соответствующий `.lrc`/`.txt` и подгружает в textarea редактора.

**Приоритет источников в UI:** LRCLIB (синхронизированный) → Genius (обычный) → ручной ввод/загрузка файла — пользователь в любой момент может переопределить результат вручную.

---

### 3.7 File Manager Service (`services/file_manager_service.py`)

**Ответственность:** безопасная навигация по `MUSIC_DIR` и предоставление данных для UI-дерева.

```python
class FileManagerService:
    def list_dir(self, rel_path: str) -> DirListing:
        # sandbox.resolve(rel_path) -> os.scandir -> различие файлов/папок
    def get_album_tracks(self, rel_album_path: str) -> list[TrackTags]:
        # читает теги всех mp3 в папке через TagService.read_tags
    def delete(self, rel_path: str) -> None:
        # опционально; повторная проверка sandbox + подтверждение на фронте
```

**Иерархия отображения:** `Артист → Альбом (Название - Год) → Треки`, строится динамически по реальной структуре папок на диске (не хранится в БД — БД в проекте отсутствует, источник истины — файловая система).

---

### 3.8 Organizer Service (`services/organizer_service.py`)

**Ответственность:** финальное размещение файлов по шаблону после нажатия «Сохранить», как для новых загрузок, так и при редактировании существующих через файловый менеджер.

**Шаблон путей:**
```
MUSIC_DIR/{AlbumArtist}/{Album} - {Year}/{TrackNumber:02d}. {Title}.mp3
MUSIC_DIR/{AlbumArtist}/{Album} - {Year}/{TrackNumber:02d}. {Title}.lrc|.txt
```

**Алгоритм:**
1. Санитизация компонентов пути (запрещённые символы ФС: `/ \ : * ? " < > |` → замена/удаление).
2. Вычисление целевого пути через `PathSandbox`.
3. Если целевая папка не существует — создаётся (`os.makedirs(exist_ok=True)`).
4. Перенос файла (`shutil.move`), включая связанный `.lrc/.txt`.
5. Если это редактирование существующего трека и артист/альбом/год/номер изменились — старый файл удаляется из прежней папки; если папка альбома опустела — папка удаляется (housekeeping).
6. Опционально — копия обложки как `cover.jpg` в папку альбома (отдельно от встроенного `APIC`).

**Важно:** Organizer вызывается **только** после подтверждения пользователем в Tag Editor — на этапе временного буфера файлы физически ещё не находятся в `MUSIC_DIR`.

---

### 3.9 API Layer — сводная таблица эндпоинтов

| Метод | Путь | Сервис | Описание |
|---|---|---|---|
| POST | `/login` | AuthService | Вход, установка cookie |
| POST | `/logout` | AuthService | Сброс сессии |
| POST | `/downloads` | DownloadService | Запуск загрузки (url + источник) |
| GET | `/downloads/{job_id}` | DownloadService | Статус/прогресс задачи |
| GET | `/downloads/{job_id}/tracks` | DownloadService | Черновые метаданные для экрана проверки |
| PUT | `/tags/batch` | TagService + OrganizerService | Сохранение тегов + финальное перемещение |
| GET | `/tags?path=` | TagService | Чтение тегов существующего файла/альбома |
| GET | `/lyrics/search` | LyricsService | Поиск в LRCLIB/Genius |
| POST | `/lyrics` | LyricsService | Сохранение текста (ручной/файл) |
| GET | `/files?path=` | FileManagerService | Листинг директории |
| DELETE | `/files?path=` | FileManagerService | Удаление файла/папки |

Все пути, кроме `/login`, защищены зависимостью `get_current_user`.

---

### 3.10 Data Models (Pydantic Schemas)

```python
class AlbumTags(BaseModel):
    album_artist: str
    album: str
    year: int
    genre: str            # "Synthwave\\Electronic" — сырой ввод пользователя
    cover: Optional[bytes]

class TrackTags(BaseModel):
    track_number: int
    title: str
    artist: str            # "Daft Punk\\Pharrell Williams"
    lyrics_status: Literal["none", "lrc", "txt"]
    path: str               # относительный путь внутри MUSIC_DIR или tmp job

class SaveAlbumRequest(BaseModel):
    album: AlbumTags
    tracks: list[TrackTags]
    source_dir: str          # tmp job dir или существующий album dir
```

---

## 4. Frontend: детальная архитектура компонентов

Стек: статический HTML + Tailwind (CDN) + Alpine.js (CDN). Jinja2 используется только для первичного рендера каркаса страницы (не для логики).

### 4.1 Layout и вкладки (`index.html`)
- Общий `x-data` корневого компонента хранит активную вкладку: `Загрузчик` / `Файлы`.
- Общий модальный слот для Tag Editor и Lyrics modal — переиспользуется обеими вкладками (один и тот же компонент редактора открывается и для новой загрузки, и для файла с диска).

### 4.2 Downloader UI (`static/js/downloader.js`)
- Форма: поле URL/поиска + селектор источника (авто-определение по URL достаточно, ручной селектор — опционально).
- `x-data` состояние: `jobId`, `status`, `progress`.
- Long-polling `fetch('/downloads/{jobId}')` каждые 1–2 сек до статуса `READY`/`ERROR`.
- При `READY` — автоматическое открытие Tag Editor с предзаполненными данными из `/downloads/{jobId}/tracks`.

### 4.3 Tag Editor (двухуровневый, `static/js/tag_editor.js`)
Общий компонент, принимающий на вход список треков (из tmp job **или** из album dir файлового менеджера) — источник данных для него абстрагирован.

- **Верхний блок (Album-level form):** обложка (превью + upload), Album Artist, Album, Year, Genre, кнопка «Применить ко всем трекам ниже» — обработчик копирует значения album-level полей в каждую строку треклиста в состоянии Alpine (`this.tracks.forEach(t => Object.assign(t, albumDefaults))`), без сетевого запроса.
- **Нижний блок (Track table):** редактируемые ячейки Track Number / Title / Artist, индикатор наличия текста (иконка + статус) и кнопка «Текст», открывающая Lyrics modal для конкретного трека.
- Кнопка «Сохранить»: единый `PUT /tags/batch` с полным телом `SaveAlbumRequest` — сервер сам решает: если источник — tmp job, вызывается `OrganizerService.move_to_library`; если источник — существующий альбом, вызывается `OrganizerService.reorganize_if_needed`.

### 4.4 Lyrics Modal (`static/js/lyrics_modal.js`)
- При открытии — запрос `GET /lyrics/search?artist=&title=&duration=` (LRCLIB → Genius fallback на бэкенде, фронт получает уже готовый результат + флаг источника).
- Textarea с полученным (или пустым) текстом, доступна для ручного редактирования.
- Кнопка загрузки файла — `<input type=file>` с чтением на клиенте и передачей содержимого в `POST /lyrics`.
- Сохранение текста **не требует** пересохранения всего альбома — отдельный лёгкий запрос, независимый от Tag Editor.

### 4.5 File Manager UI (`static/js/file_manager.js`)
- Компонент дерева: рекурентная Alpine-структура (артисты → альбомы → треки), данные подгружаются лениво (`GET /files?path=`) при разворачивании узла — не грузится весь каталог целиком.
- Клик по альбому → открытие Tag Editor (см. 4.3) с `source_dir = album_path`, предзаполнение из `GET /tags?path=album_path` (батч чтение всех треков папки).
- Клик по треку → тот же Tag Editor, но с единственной строкой в треклисте.
- Кнопка удаления — модальное подтверждение перед `DELETE /files`.

### 4.6 Управление состоянием
- Глобальный Alpine `store` (`Alpine.store('app', {...})`) хранит: текущего пользователя (факт логина), активный модал, кэш последнего открытого каталога файлового менеджера — минимизирует повторные запросы при переключении вкладок.
- Никакого клиентского роутинга (SPA-фреймворка) не требуется — одна страница, вкладки переключаются через `x-show`.

---

## 5. Ключевые сквозные сценарии (sequence flow)

### 5.1 Сценарий: загрузка нового альбома с Deezer

```
User → [Downloader UI] вставляет Deezer-ссылку → POST /downloads
FastAPI → DownloadService.create_job()
        → DeezerClient.fetch_album_metadata()      (точные теги, обложка)
        → for each track: source_resolver.find_best_match() → yt-dlp download
        → ffmpeg convert → 320kbps mp3 в tmp_downloads/{job_id}/
        → job.status = READY
[Downloader UI] long-polling получает READY
        → GET /downloads/{job_id}/tracks (черновые теги от Deezer)
        → открывается Tag Editor, предзаполненный точными данными Deezer
User → правит теги при необходимости → «Сохранить»
        → PUT /tags/batch
FastAPI → TagService.write_tags() для каждого трека (mutagen, ID3v2.4, \\-парсинг)
        → OrganizerService.move_to_library() → MUSIC_DIR/Artist/Album - Year/...
        → LyricsService (если пользователь добавил текст в модалке) уже сохранил .lrc/.txt рядом на этапе tmp — переносится вместе с треком
        → tmp job очищается
```

### 5.2 Сценарий: редактирование альбома через Файловый менеджер

```
User → [File Manager] разворачивает Artist → Album
     → клик «Редактировать альбом»
FastAPI → GET /tags?path=Artist/Album - Year
        → FileManagerService.get_album_tracks() → TagService.read_tags() для каждого mp3
        → возврат AlbumTags + list[TrackTags]
User → меняет Album Artist / Year → «Сохранить»
FastAPI → PUT /tags/batch (source_dir = существующий путь)
        → TagService.write_tags()
        → OrganizerService.reorganize_if_needed():
             если Artist/Year/Album изменились → shutil.move в новую папку,
             старая папка при опустошении удаляется
```

---

## 6. Безопасность: сводка по компонентам

| Компонент | Угроза | Механизм защиты |
|---|---|---|
| Auth | Брутфорс логина | Rate-limiting на `/login` (например `slowapi`), задержка при неверном пароле |
| Auth | Хранение пароля | `bcrypt` с достаточным cost-фактором, никогда plaintext |
| Sandbox | Path Traversal | `PathSandbox.resolve()` перед каждой файловой операцией, проверка симлинков |
| Downloader | SSRF через произвольный URL | Whitelist доменов (soundcloud.com, youtube.com, music.youtube.com, deezer.com) перед передачей в yt-dlp |
| File Manager | Удаление вне MUSIC_DIR | Тот же `PathSandbox`, плюс подтверждение на фронте |
| Tag Service | Порча файла при некорректных тегах | try/except вокруг `mutagen`, откат/логирование при ошибке записи |
| Cookies | Кража сессии | `httpOnly`, `Secure` (при HTTPS), `SameSite=Strict`, ограниченный TTL |

---

## 7. Деплой-архитектура

```
systemd (music-manager.service)
   └── ExecStart: /opt/music-manager/venv/bin/uvicorn app.main:app --host 127.0.0.1 --port 8010
   └── Restart=on-failure
   └── EnvironmentFile=/opt/music-manager/.env

(опционально, если сервер уже используется под другие self-hosted сервисы)
Reverse-proxy (nginx/caddy, вне рамок ТЗ) → 127.0.0.1:8010
```

- Установка: `python3.11 -m venv venv && pip install -r requirements.txt`, системные пакеты — `ffmpeg`.
- Не используется Docker/Node.js — соответствует требованию ТЗ о минимальной инфраструктуре и стабильности генерации кода нейросетью.
- Логи — через стандартный вывод systemd (`journalctl -u music-manager`).

---

## 8. Соответствие компонентов пунктам плана разработки (раздел 9 ТЗ)

| Блок ТЗ | Компоненты из данного документа |
|---|---|
| Блок 1 (Ядро и авторизация) | 3.1 Core/Bootstrap, 3.2 Auth Module, 3.3 Path Sandbox |
| Блок 2 (Сервис загрузки) | 3.4 Downloader Engine (source_resolver, download_service, DeezerClient) |
| Блок 3 (Метаданные и тексты) | 3.5 Tag Service, 3.6 Lyrics Service, 3.8 Organizer Service |
| Блок 4 (Интерфейс) | Раздел 4 (Frontend), Tag Editor, Lyrics modal, File Manager UI |
| Блок 5 (Деплой) | Раздел 7 (systemd, venv) |

Такое соответствие позволяет генерировать проект теми же блоками, что предложены в ТЗ, но каждый блок теперь имеет чёткие границы модулей, интерфейсы между сервисами и точки, которые обязаны проходить через `PathSandbox`.