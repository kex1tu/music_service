# План реализации: 3.4 Downloader Engine

- **Дата:** 2026-09-07
- **Статус:** PROPOSED
- **Целевые компоненты:**
  - `requirements.txt`
  - `.env.example`
  - `app/config.py`
  - `app/core/exceptions.py`
  - `app/schemas/download.py`
  - `app/services/deezer_client.py`
  - `app/services/source_resolver.py`
  - `app/services/download_service.py`
  - `app/dependencies.py`
  - `app/routers/downloads.py`
  - `app/main.py`
  - `tests/test_deezer_client.py`
  - `tests/test_source_resolver.py`
  - `tests/test_download_service.py`
  - `tests/test_downloads_router.py`
  - `PROGRESS.md`
- **Ответственная область:** Downloader Engine (согласно [AGENTS.md](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/AGENTS.md))
- **Связанные последующие компоненты:** 3.5 Metadata / Tag Service, 3.6 Lyrics Service, 3.8 Organizer Service, 4.2 Downloader UI

---

## 1. Цель и границы задачи (Objective & Scope)

### 1.1 Цель
Спроектировать и реализовать масштабируемый, отказоустойчивый и изолированный сервис загрузки аудиоконтента — **3.4 Downloader Engine**.
Сервис объединяет три сотрудничающих подсистемы:
1. **`source_resolver.py` & `deezer_client.py`**: Определение источника по URL, извлечение богатых студийных метаданных из публичного Deezer API (без ARL/DRM), интеллектуальный поиск наилучшего аудиопотока в YouTube / YouTube Music (`AudioMatcher` на базе нечеткого сопоставления строк RapidFuzz и валидации таймингов $\pm 2$ сек), а также прямая обработка ссылок SoundCloud и YouTube/YouTube Music через `yt-dlp`.
2. **`download_service.py`**: Асинхронный оркестратор конвейера загрузки (`DownloadJob`). Выполняет неблокирующее скачивание лучшего аудиопотока в изолированный каталог `TMP_DIR/{job_id}/raw/`, транскодирование в эталонный MP3 (320 kbps CBR, 44.1 kHz) через `ffmpeg` в `TMP_DIR/{job_id}/converted/`, предзаполнение черновых ID3v2.4-тегов (`mutagen`) и перевод задачи в статус `READY`.
3. **`downloads.py` (API) и Временный буфер**: HTTP-интерфейс для запуска загрузки (`POST /downloads`), опроса прогресса через long-polling (`GET /downloads/{job_id}`) и получения предзаполненных метаданных треков (`GET /downloads/{job_id}/tracks`). Фоновый сборщик мусора (TTL Garbage Collector) для очистки временных каталогов старше $N$ часов.

### 1.2 Архитектурный контекст в проекте
В соответствии с инвариантами [AGENTS.md](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/AGENTS.md) и [Architecture.md](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/Architecture.md):
1. **Инвариант 1 (Zero-Database):** Состояние задач хранится строго in-memory в потокобезопасном реестре `DownloadService` с файловым отражением в `TMP_DIR/{job_id}/`. Никаких внешних баз данных или брокеров (Redis/PostgreSQL/SQLite).
2. **Инвариант 2 (Sandbox обязателен):** Любые операции с файлами внутри `TMP_DIR/{job_id}/raw/` и `TMP_DIR/{job_id}/converted/` осуществляются исключительно через `PathSandbox.resolve()`. Прямое соединение путей или обход песочницы запрещены.
3. **Инвариант 5 (Аудио-пайплайн фиксирован):** Скачивание — `yt-dlp`; конвертация — `ffmpeg` в MP3 320 kbps CBR / 44.1 kHz; предварительные теги — строго ID3v2.4 через `mutagen` с разделителями `\x00` для множественных значений.
4. **Безопасность процессов:** Никаких блокирующих вызовов в event loop FastAPI. Запуск `yt-dlp` и `ffmpeg` производится асинхронно через `asyncio.create_subprocess_exec` или `asyncio.to_thread` с ограничением конкурентных задач (`asyncio.Semaphore`).
5. **Валидация доменов:** Проверка URL по белому списку доменов (`youtube.com`, `music.youtube.com`, `youtu.be`, `soundcloud.com`, `deezer.com`, `deezer.page.link`) до передачи в какие-либо обработчики.

### 1.3 Границы (Scope)
- **In-Scope:**
  - Добавление зависимостей `yt-dlp`, `rapidfuzz`, `mutagen` в `requirements.txt` и их интеграция.
  - Расширение `app/config.py` параметрами загрузчика (`MAX_CONCURRENT_DOWNLOADS`, `JOB_TTL_HOURS`, `COBALT_API_URL`).
  - Реализация клиента `DeezerClient` (`app/services/deezer_client.py`) для работы с публичным REST API Deezer без ARL (треки, альбомы, плейлисты, обложки 1400x1400).
  - Реализация алгоритма сопоставления `AudioMatcher` и резолвера `SourceResolver` (`app/services/source_resolver.py`).
  - Реализация `DownloadService` (`app/services/download_service.py`): управление жизненным циклом `DownloadJob`, очередь задач, запуск `yt-dlp`, конвертация `ffmpeg`, запись черновых ID3v2.4-тегов, периодическая очистка по TTL.
  - Реализация Pydantic-схем в `app/schemas/download.py`.
  - Реализация эндпоинтов в `app/routers/downloads.py` с защитой `CurrentUserDep`.
  - Подключение роутера и фонового сборщика мусора в `app/main.py`.
  - Полное покрытие unit- и интеграционными тестами с мокированием внешних сетевых вызовов (Deezer, YouTube, Cobalt) и подпроцессов (`ffmpeg`, `yt-dlp`).
- **Out-of-Scope:**
  - Финальное редактирование и перезапись тегов в медиатеку `MUSIC_DIR` (компонент 3.5 `TagService`).
  - Поиск и загрузка текстов песен LRCLIB/Genius (компонент 3.6 `LyricsService`).
  - Финальное перемещение альбома из временного буфера в `MUSIC_DIR` (компонент 3.8 `OrganizerService`).
  - Фронтенд-интерфейс загрузчика на Alpine.js (компонент 4.2 `downloader.js`).

---

## 2. Анализ текущего состояния (Current State Analysis)

1. **Существующие заготовки файлов:**
   - [app/services/deezer_client.py](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/services/deezer_client.py) — 0 байт.
   - [app/services/source_resolver.py](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/services/source_resolver.py) — 0 байт.
   - [app/services/download_service.py](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/services/download_service.py) — 0 байт.
   - [app/schemas/download.py](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/schemas/download.py) — 0 байт.
   - [app/routers/downloads.py](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/routers/downloads.py) — 0 байт.
2. **Инфраструктура ядра (Блок 1 завершен):**
   - Доступен [PathSandbox](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/core/sandbox.py) через DI `TmpSandboxDep` для гарантированной изоляции временных каталогов.
   - Доступен `get_current_user` (`CurrentUserDep`) для защиты API-маршрутов.
   - Обработчики исключений в [app/main.py](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/main.py) транслируют `DownloadError` в HTTP 502, `PathTraversalError` в HTTP 403, `NotFoundError` в HTTP 404.
3. **Бинарное окружение:**
   - Системный `ffmpeg` (версия 6.1.1 с поддержкой `libmp3lame`) установлен и проверен в ОС.
   - Пакеты `yt-dlp`, `rapidfuzz`, `mutagen` отсутствуют в [requirements.txt](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/requirements.txt) и в виртуальном окружении, их необходимо зафиксировать и установить.

---

## 3. Архитектурное решение и обоснование (Architecture & Rationale)

### 3.1 Архитектурная схема взаимодействия компонентов

```
Пользователь (UI)
      │
      │ 1. POST /downloads { url: "https://www.deezer.com/album/..." }
      ▼
┌────────────────────────────────────────────────────────────────────────┐
│ app/routers/downloads.py (FastAPI Endpoint)                            │
│ ├─ Валидация URL по доменному whitelist                                │
│ └─ DownloadService.create_job(url) ──► возвращает DownloadJob (QUEUED) │
└───────────────────────────────────┬────────────────────────────────────┘
                                    │
                                    │ 2. Фоновый воркер (asyncio.Task)
                                    ▼
┌────────────────────────────────────────────────────────────────────────┐
│ app/services/download_service.py (DownloadService)                     │
│                                                                        │
│ ┌────────────────────────────────────────────────────────────────────┐ │
│ │ Шаг 1: Определение источника и резолв (SourceResolver)             │ │
│ │ ├─ Deezer URL? ──► DeezerClient.get_album() (точные метаданные)    │ │
│ │ │                 AudioMatcher.find_best_match()                   │ │
│ │ │                 (ytsearch5 + RapidFuzz + тайминг ±2 сек)         │ │
│ │ │                 ──► Список ResolvedTrack                         │ │
│ │ └─ YouTube/SoundCloud URL? ──► yt-dlp extract_info                 │ │
│ └──────────────────────────────────┬─────────────────────────────────┘ │
│                                    │                                   │
│ ┌──────────────────────────────────▼─────────────────────────────────┐ │
│ │ Шаг 2: Изолированный файловый пайплайн                             │ │
│ │ ├─ Скачивание обложки ──► TMP_DIR/{job_id}/cover.jpg               │ │
│ │ ├─ Для каждого трека:                                              │ │
│ │ │   a. yt-dlp ──► TMP_DIR/{job_id}/raw/{track_idx}.%(ext)s        │ │
│ │ │   b. ffmpeg (async subprocess):                                  │ │
│ │ │      -i raw -codec:a libmp3lame -b:a 320k -ar 44100              │ │
│ │ │      ──► TMP_DIR/{job_id}/converted/{track_idx}. {title}.mp3     │ │
│ │ │   c. Mutagen ID3v2.4 prefill (TIT2, TPE1, TALB, APIC cover)      │ │
│ │ │   d. Удаление сырого файла raw                                   │ │
│ │ └─ job.status = JobStatus.READY                                    │ │
│ └────────────────────────────────────────────────────────────────────┘ │
└────────────────────────────────────────────────────────────────────────┘
                                    │
                                    │ 3. Polling прогресса:
                                    │    GET /downloads/{job_id}
                                    ▼
┌────────────────────────────────────────────────────────────────────────┐
│ 4. При статусе READY:                                                  │
│    GET /downloads/{job_id}/tracks                                      │
│    ──► Открытие Tag Editor с предзаполненными черновыми тегами         │
└────────────────────────────────────────────────────────────────────────┘
```

### 3.2 Детальное обоснование («Почему именно так»)

1. **Гибридный подход Deezer Metadata + YouTube Audio Stream:**
   - *Проблема:* Загрузка напрямую с Deezer требует платной подписки и валидного `ARL`-токена сессии, который регулярно протухает, блокируется и нарушает инвариант автономной работы без сторонних аккаунтов.
   - *Решение:* Публичный API Deezer (`api.deezer.com`) не требует авторизации и отдает идеальные студийные метаданные: официальные названия, точный список исполнителей, ISRC, номер трека, диск, год и несжатую обложку 1400×1400.
   - *Аудиопоток:* Ищется через `AudioMatcher` на YouTube Music / YouTube. Аудиодорожки на YouTube Music в подавляющем большинстве предоставлены самими правообладателями (каналы вида `Artist - Topic`), закодированы из lossless-мастеров в Opus 160 kbps / AAC 128 kbps с идентичной длительностью.
   - *Алгоритм сопоставления (`AudioMatcher`):*
     - Поиск: `ytsearch5:{artist} - {title}` и `ytsearch5:{artist} - {title} Official Audio`.
     - Фильтрация и взвешивание:
       $$\text{Score} = S_{\text{duration}} + S_{\text{title}} + S_{\text{artist}} + S_{\text{channel\_bonus}} - S_{\text{penalty}}$$
       - $S_{\text{duration}}$: Совпадение длительности $\le 2$ сек дает $+40$ баллов; $\le 5$ сек дает $+20$ баллов; расхождение $> 20$ сек дает штраф $-50$ баллов (отсекает клипы с длинными интро).
       - $S_{\text{title}}$: `rapidfuzz.fuzz.token_set_ratio` нормализуется до $30$ баллов.
       - $S_{\text{artist}}$: `rapidfuzz.fuzz.partial_ratio` нормализуется до $15$ баллов.
       - $S_{\text{channel\_bonus}}$: $+15$ баллов за канал ` - Topic`, $+10$ баллов за `Official` / `VEVO`.
       - $S_{\text{penalty}}$: $-40$ баллов за слова `live`, `concert`, `cover`, `remix`, `slowed`, `reverb`, если их нет в оригинальном названии Deezer.
     - Fallback: публичные шлюзы Cobalt API при отсутствии кандидатов с оценкой выше порога (60 баллов) или сетевых блокировках YouTube.

2. **In-Memory Job Manager вместо Celery/Redis/SQLite:**
   - *Обоснование:* В соответствии с правилом **Zero-Database** и развертыванием в виде единого systemd-сервиса, внедрение Redis/RabbitMQ/Celery было бы грубым нарушением KISS/YAGNI и инвариантов проекта.
   - *Реализация:* Словарь `dict[str, DownloadJob]` с `asyncio.Lock` и семафором `asyncio.Semaphore(MAX_CONCURRENT_DOWNLOADS)`. Все промежуточные данные отражаются на файловой системе в `TMP_DIR/{job_id}`. При перезапуске сервера незавершенные задачи просто признаются прерванными и вычищаются сборщиком мусора.

3. **Асинхронные неблокирующие подпроцессы (`asyncio.create_subprocess_exec`):**
   - *Обоснование:* Транскодирование `ffmpeg` и скачивание `yt-dlp` являются CPU- и I/O-интенсивными операциями. Блокирующий вызов `subprocess.run()` внутри async-функции парализует весь веб-сервер FastAPI.
   - *Реализация:* Использование нативного `asyncio.create_subprocess_exec` с чтением `stderr` по строкам для расчета процентов прогресса и таймаутами на случай зависания сети.

4. **Обязательная песочница `PathSandbox` для всех временных путей:**
   - *Обоснование:* Любое имя файла, сгенерированное на основе названия трека (например, `AC/DC - Back in Black.mp3` или `../../evil.mp3`), несет риск Path Traversal или ошибки ФС.
   - *Реализация:* `clean_filename = PathSandbox.validate_filename(...)` + `tmp_sandbox.resolve(f"{job_id}/converted/{clean_filename}")`.

5. **Черновые ID3v2.4 теги через `mutagen` на этапе `TAGGING`:**
   - *Обоснование:* Когда задача переходит в `READY`, файл в `converted/` уже является полноценным MP3 320 kbps. Запись черновых тегов на этом этапе гарантирует, что даже если пользователь нажмет «Сохранить без изменений», файл уже валиден и содержит обложку и базовые поля.

### 3.3 Сравнительная таблица альтернатив (Trade-off Matrix)

| Критерий | Вариант А: Прямой Deezer Stream (ARL) | Вариант Б: Выбранный (Deezer Meta + YouTube Stream) | Вариант В: Только yt-dlp без Deezer |
|---|---|---|---|
| **Автономность** | Низкая (нужен постоянный валидный ARL cookie) | **Максимальная** (публичные API, без аккаунтов) | Высокая (без сторонних аккаунтов) |
| **Качество тегов** | Высокое (ISRC, номера треков, cover 1400x1400) | **Идеальное** (студийные метаданные Deezer) | Низкое (названия роликов ютуба, мусор, капс) |
| **Качество обложек** | 1400x1400 | **1400x1400 (Deezer CDN)** | Превью YouTube 1280x720 с черными полосами |
| **Сложность стека** | Нестабильный реверс-инжиниринг Deezer API | **Умеренная** (Deezer REST + AudioMatcher) | Минимальная |
| **Соответствие ТЗ** | Противоречит требованию «Deezer без ARL» | **Полное соответствие п. 3.4 ТЗ** | Не выполняет требования по качеству тегов |

### 3.4 Инварианты и гарантии

- **Zero-Database:** Реестр задач — потокобезопасная in-memory структура `DownloadService._jobs`.
- **Sandbox Guard:** Ни один файл не создается и не удаляется в обход `tmp_sandbox.resolve()`.
- **ID3v2.4:** Черновые теги пишутся строго в ID3v2.4 (`mutagen.id3`), множественные исполнители — через null-separator `\x00`.
- **Long-Polling API:** Фронтенд опрашивает `GET /downloads/{job_id}` каждые 1–2 сек. Никаких WebSocket-зависимостей.
- **TTL Cleanup:** Любые задачи и каталоги в `TMP_DIR` старше `JOB_TTL_HOURS` (по умолчанию 24ч) автоматически удаляются фоновым сборщиком мусора раз в час.

---

## 4. Пошаговая декомпозиция реализации (Step-by-Step Implementation)

### Этап 1: Зависимости и конфигурация

#### [MODIFY] [requirements.txt](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/requirements.txt)
Добавление библиотек для загрузки, конвертации и нечеткого поиска:
```text
yt-dlp>=2024.8.6
rapidfuzz>=3.9.0
mutagen>=1.47.0
```

#### [MODIFY] [.env.example](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/.env.example)
Фиксация параметров загрузчика и сборщика мусора:
```env
# Параметры загрузчика
MAX_CONCURRENT_DOWNLOADS=2
JOB_TTL_HOURS=24
COBALT_API_URL=https://cobalt-backend.example.com
```

#### [MODIFY] [app/config.py](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/config.py)
Добавление полей в класс `Settings`:
- `MAX_CONCURRENT_DOWNLOADS: int = Field(default=2, ge=1, le=10)`
- `JOB_TTL_HOURS: int = Field(default=24, ge=1, le=168)`
- `COBALT_API_URL: str | None = Field(default=None)`
- `ALLOWED_DOWNLOAD_DOMAINS: tuple[str, ...] = ("youtube.com", "music.youtube.com", "youtu.be", "soundcloud.com", "deezer.com", "deezer.page.link")`

---

### Этап 2: Схемы данных и доменные исключения

#### [MODIFY] [app/core/exceptions.py](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/core/exceptions.py)
Добавление специализированных исключений загрузчика:
```python
class UnsupportedSourceError(MusicManagerError):
    """URL не поддерживается или не входит в белый список доменов."""
    status_code = 400
    error_code = "UNSUPPORTED_SOURCE"

class AudioMatchError(DownloadError):
    """Не удалось найти подходящий аудиопоток для трека."""
    status_code = 502
    error_code = "AUDIO_MATCH_FAILED"
```

#### [NEW] [app/schemas/download.py](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/schemas/download.py)
Определение Pydantic-моделей контракта:
- `JobStatus(str, Enum)`: `QUEUED`, `RESOLVING`, `DOWNLOADING`, `CONVERTING`, `TAGGING`, `READY`, `ERROR`.
- `CreateDownloadRequest(BaseModel)`: `url: str`, `source: Literal["auto", "soundcloud", "youtube", "deezer"] = "auto"`.
- `ResolvedTrack(BaseModel)`:
  - `track_id: str`
  - `track_number: int`
  - `title: str`
  - `artist: str` (формат `\\` для множественных значений)
  - `album: str`
  - `album_artist: str`
  - `year: int | None`
  - `genre: str | None`
  - `duration: int`
  - `isrc: str | None`
  - `cover_url: str | None`
  - `audio_source_url: str`
  - `converted_path: str | None` (относительный путь в песочнице)
  - `status: str`
  - `error: str | None`
- `DownloadJobResponse(BaseModel)`:
  - `id: str`
  - `status: JobStatus`
  - `source: str`
  - `url: str`
  - `progress: float` (0.0 — 100.0)
  - `current_step: str`
  - `total_tracks: int`
  - `completed_tracks: int`
  - `error: str | None`
  - `created_at: str`
- `DownloadTracksResponse(BaseModel)`:
  - `job_id: str`
  - `album_artist: str`
  - `album: str`
  - `year: int | None`
  - `genre: str | None`
  - `cover_url: str | None`
  - `tracks: list[ResolvedTrack]`

---

### Этап 3: Клиент Deezer API (`deezer_client.py`)

#### [NEW] [app/services/deezer_client.py](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/services/deezer_client.py)
**Ответственность:** Асинхронное взаимодействие с публичным REST API Deezer (`https://api.deezer.com`).

**Публичный API класса `DeezerClient`:**
```python
class DeezerClient:
    """Асинхронный клиент к публичному API Deezer."""

    def __init__(self, http_client: httpx.AsyncClient | None = None) -> None: ...

    async def parse_url(self, url: str) -> tuple[Literal["track", "album", "playlist"], str]:
        """Парсит URL Deezer (включая deezer.page.link с редиректом) и возвращает тип ресурса и ID."""

    async def get_track(self, track_id: str) -> dict[str, Any]:
        """Запрашивает метаданные одиночного трека (GET /track/{id})."""

    async def get_album(self, album_id: str) -> dict[str, Any]:
        """Запрашивает метаданные альбома и список треков (GET /album/{id})."""

    async def get_playlist(self, playlist_id: str) -> dict[str, Any]:
        """Запрашивает метаданные плейлиста и список треков (GET /playlist/{id})."""

    async def search_tracks(self, query: str, limit: int = 5) -> list[dict[str, Any]]:
        """Поиск треков в каталоге Deezer (GET /search/track?q=...)."""

    async def download_cover_bytes(self, cover_url: str) -> bytes:
        """Скачивает изображение обложки альбома в байтах."""
```

**Особенности реализации:**
- Извлечение максимального разрешения обложки (`cover_xl` = 1400×1400).
- Преобразование множественных артистов из `contributors` в формат `\\` (например, `["Daft Punk", "Pharrell Williams"]` $\to$ `"Daft Punk\\Pharrell Williams"`).
- Таймауты `httpx` (10 сек) и обработка HTTP 404/429/500 с выбросом `DownloadError`.

---

### Этап 4: Алгоритм сопоставления и резолвер источников (`source_resolver.py`)

#### [NEW] [app/services/source_resolver.py](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/services/source_resolver.py)
**Ответственность:**
1. Определение типа источника по URL (`soundcloud`, `youtube`, `deezer`).
2. Поиск наилучшего аудиопотока на YouTube для треков Deezer (`AudioMatcher`).
3. Формирование единой структуры `ResolvedTrack`.

**Публичный API класса `AudioMatcher`:**
```python
class AudioMatcher:
    """Интеллектуальный сопоставитель метаданных Deezer с аудиопотоками YouTube."""

    @staticmethod
    def calculate_score(
        target_title: str,
        target_artist: str,
        target_duration: int,
        candidate_title: str,
        candidate_channel: str,
        candidate_duration: int,
    ) -> float:
        """Вычисляет оценку соответствия кандидата (от 0 до 100 баллов)."""

    async def find_best_match(
        self,
        title: str,
        artist: str,
        duration: int,
        cobalt_fallback_url: str | None = None,
    ) -> str:
        """Формирует ytsearch-запрос к yt-dlp, оценивает кандидатов и возвращает лучший audio URL."""
```

**Публичный API класса `SourceResolver`:**
```python
class SourceResolver:
    """Сервис определения источника и извлечения нормализованных метаданных."""

    def __init__(
        self,
        deezer_client: DeezerClient,
        audio_matcher: AudioMatcher,
        cobalt_api_url: str | None = None,
    ) -> None: ...

    def detect_source(self, url: str) -> Literal["soundcloud", "youtube", "deezer"]:
        """Определяет сервис по домену URL или выбрасывает UnsupportedSourceError."""

    async def resolve(
        self,
        url: str,
        source_hint: str = "auto",
    ) -> tuple[dict[str, Any], list[ResolvedTrack]]:
        """Разрешает URL в метаданные альбома/релиза и список ResolvedTrack."""
```

**Особенности реализации:**
- Для **YouTube / SoundCloud**: `yt-dlp` запускается с параметрами `extract_flat=True`, извлекая `title`, `uploader`, `duration`, `thumbnail`. Если ссылка ведет на плейлист, разворачиваются все элементы.
- Для **Deezer**: `DeezerClient` получает студийные метаданные релиза, затем для каждого трека вызывается `AudioMatcher.find_best_match()`. Если кандидат найден с оценкой $\ge 50$, его URL становится `audio_source_url` трека.

---

### Этап 5: Оркестрация конвейера загрузки (`download_service.py`)

#### [NEW] [app/services/download_service.py](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/services/download_service.py)
**Ответственность:**
1. Управление жизненным циклом `DownloadJob`.
2. Скачивание аудиопотока через `yt-dlp` во временный буфер `TMP_DIR/{job_id}/raw/`.
3. Асинхронная конвертация через `ffmpeg` в MP3 320k CBR / 44.1 kHz в `TMP_DIR/{job_id}/converted/`.
4. Запись черновых ID3v2.4 тегов (`mutagen`) и встраивание обложки.
5. Фоновый сборщик мусора по истечении TTL.

**Публичный API класса `DownloadService`:**
```python
class DownloadService:
    """Оркестратор задач загрузки, транскодирования и управления буфером."""

    def __init__(
        self,
        settings: Settings,
        tmp_sandbox: PathSandbox,
        source_resolver: SourceResolver,
        deezer_client: DeezerClient,
    ) -> None: ...

    def create_job(self, url: str, source: str = "auto") -> DownloadJob:
        """Создает новую задачу загрузки, регистрирует в памяти и запускает фоновый процесс."""

    def get_job(self, job_id: str) -> DownloadJob:
        """Возвращает задачу по ID или выбрасывает NotFoundError."""

    def get_job_tracks(self, job_id: str) -> DownloadTracksResponse:
        """Возвращает подготовленные черновые метаданные треков для экрана проверки."""

    async def _execute_job_pipeline(self, job_id: str) -> None:
        """Основной асинхронный конвейер выполнения задачи."""

    async def _download_raw_audio(self, source_url: str, output_path: Path) -> None:
        """Загрузка аудиопотока через yt-dlp."""

    async def _transcode_to_mp3(self, input_raw: Path, output_mp3: Path) -> None:
        """Конвертация в MP3 320 kbps CBR / 44.1 kHz через ffmpeg."""

    def _apply_draft_tags(self, mp3_path: Path, track: ResolvedTrack, cover_path: Path | None) -> None:
        """Запись черновых ID3v2.4 тегов через mutagen."""

    async def cleanup_job(self, job_id: str) -> None:
        """Полная очистка файлов задачи и удаление из памяти."""

    def cleanup_expired_jobs(self) -> int:
        """Сборщик мусора: удаляет задачи и папки в TMP_DIR старше settings.JOB_TTL_HOURS."""
```

**Инварианты работы с файловой системой:**
- Все пути внутри буфера формируются строго через `self.tmp_sandbox.resolve(...)`:
  - `job_root = self.tmp_sandbox.resolve(job_id)`
  - `raw_dir = self.tmp_sandbox.resolve(f"{job_id}/raw")`
  - `converted_dir = self.tmp_sandbox.resolve(f"{job_id}/converted")`
  - `cover_file = self.tmp_sandbox.resolve(f"{job_id}/cover.jpg")`
- Имена сконвертированных файлов санитизируются через `PathSandbox.validate_filename(...)`:
  `f"{track.track_number:02d}. {clean_title}.mp3"`.

---

### Этап 6: Внедрение зависимостей и HTTP-маршруты

#### [MODIFY] [app/dependencies.py](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/dependencies.py)
Добавление синглтонов и провайдеров зависимостей:
- `get_deezer_client() -> DeezerClient`
- `get_source_resolver(...) -> SourceResolver`
- `get_download_service(...) -> DownloadService`
- Типизированные аннотации: `DownloadServiceDep = Annotated[DownloadService, Depends(get_download_service)]`.

#### [NEW] [app/routers/downloads.py](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/routers/downloads.py)
Реализация защищенных эндпоинтов:
- `POST /downloads`:
  - Принимает `CreateDownloadRequest`.
  - Валидирует домен по белому списку `settings.ALLOWED_DOWNLOAD_DOMAINS`.
  - Вызывает `download_service.create_job(req.url, req.source)`.
  - Возвращает HTTP 202 Accepted и `DownloadJobResponse`.
- `GET /downloads/{job_id}`:
  - Возвращает текущее состояние `DownloadJobResponse` (для long-polling фронтенда).
- `GET /downloads/{job_id}/tracks`:
  - Возвращает `DownloadTracksResponse` с черновыми тегами треков (доступно только в статусе `READY`).

#### [MODIFY] [app/main.py](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/main.py)
1. Регистрация `downloads_router`.
2. Интеграция фоновой периодической задачи в `lifespan`:
   - Запуск бесконечного цикла `asyncio.sleep(3600)` с вызовом `download_service.cleanup_expired_jobs()`.
   - Корректная остановка фоновой задачи при выходе (`task.cancel()`).

---

## 5. Анализ рисков и краевых случаев (Risks, Edge Cases & Guardrails)

| Риск / Краевой случай | Вероятность / Влияние | Стратегия митигации |
|---|---|---|
| **YouTube блокировки / 429 Too Many Requests при поиске через yt-dlp** | Средняя / Высокая | 1. Использование официального публичного Deezer API для метаданных (минимизирует запросы к YT до 1 целевого поиска).<br>2. Ограничение конкурентных загрузок через `asyncio.Semaphore(MAX_CONCURRENT_DOWNLOADS)`.<br>3. Опциональный fallback на публичные Cobalt-шлюзы. |
| **Выбор некорректного аудио (live/кавер вместо оригинального студийного трека)** | Средняя / Средняя | Алгоритм `AudioMatcher`: строгий штраф за несоответствие длительности ($> 2$ сек), штраф за ключевые слова `live/cover/remix/tribute` и приоритет официальным каналам `Artist - Topic` / `VEVO`. |
| **Зависание подпроцесса `ffmpeg` или `yt-dlp`** | Низкая / Высокая | Вызов через `asyncio.wait_for(..., timeout=300)`. При таймауте процесс принудительно завершается через `proc.kill()`, задача переходит в `ERROR`. |
| **Переполнение диска во временном буфере `TMP_DIR`** | Средняя / Высокая | 1. Немедленное удаление `raw` файла сразу после завершения конвертации в MP3.<br>2. Фоновый сборщик мусора раз в час очищает каталоги старше `JOB_TTL_HOURS`.<br>3. Проверка доступного места на диске. |
| **Инъекции в аргументы командной строки подпроцессов** | Высокая / Критическая | Использование `asyncio.create_subprocess_exec` со списком аргументов вместо `shell=True`. Пути передаются отдельными элементами массива без конкатенации shell-строк. |
| **Спецсимволы в названиях треков (слэши, двоеточия, эмодзи)** | Высокая / Средняя | Обязательная санитизация одиночных имен файлов через `PathSandbox.validate_filename()` с заменой запрещенных символов на безопасные `_`. |
| **Множественные артисты в тегах** | Высокая / Низкая | Конвертация `contributors` Deezer в формат `\\` для UI и `\x00` в кадр `TPE1` ID3v2.4 (`mutagen`). |

---

## 6. План тестирования и верификации (Verification Matrix)

### 6.1 Модульные и интеграционные тесты (`tests/`)

1. **`tests/test_deezer_client.py`:**
   - Парсинг URL трека, альбома, плейлиста и коротких ссылок `deezer.page.link`.
   - Мокирование ответов Deezer API (`respx` или `unittest.mock.patch` на `httpx.AsyncClient`):
     - Получение трека: проверка маппинга ISRC, артистов, длительности.
     - Получение альбома: проверка списка треков, года релиза, обложки `cover_xl`.
   - Обработка ошибок: HTTP 404, таймаут соединения, пустой результат.
2. **`tests/test_source_resolver.py`:**
   - Валидация белого списка доменов в `detect_source`:
     - Разрешенные: `youtube.com`, `music.youtube.com`, `soundcloud.com`, `deezer.com`.
     - Запрещенные: `evil.com`, `torrent.org` $\to$ `UnsupportedSourceError`.
   - Тестирование формулы оценки `AudioMatcher.calculate_score`:
     - Идеальное совпадение: канал ` - Topic`, совпадение длительности 1 сек $\to \text{Score} \ge 90$.
     - Live-запись: длительность отличается на 40 сек, в названии слово "Live" $\to \text{Score} < 30$.
   - Замоканный резолв Deezer альбома в набор `ResolvedTrack`.
3. **`tests/test_download_service.py`:**
   - Создание задачи и проверка переходов статусов (`QUEUED` $\to$ `RESOLVING` $\to$ `DOWNLOADING` $\to$ `CONVERTING` $\to$ `TAGGING` $\to$ `READY`).
   - Мокирование вызовов `yt-dlp` и `ffmpeg` через фейковые файлы:
     - Проверка создания структуры `TMP_DIR/{job_id}/converted/{track}.mp3`.
     - Проверка удаления `TMP_DIR/{job_id}/raw/`.
     - Проверка записи ID3v2.4 тегов через `mutagen.id3.ID3`: наличие кадров `TIT2`, `TPE1` (с `\x00`), `TALB`, `APIC`.
   - Тест сборщика мусора `cleanup_expired_jobs`: создание устаревшей папки и проверка ее удаления.
4. **`tests/test_downloads_router.py`:**
   - Неавторизованный запрос к `/downloads` $\to$ 401 Unauthorized.
   - Авторизованный `POST /downloads` с некорректным URL $\to$ 400 Unsupported Source.
   - Авторизованный `POST /downloads` с валидным URL $\to$ 202 Accepted и `job_id`.
   - Опрос `GET /downloads/{job_id}`: возврат прогресса и статуса.
   - `GET /downloads/{job_id}/tracks`: 400/404 при статусе `DOWNLOADING`, 200 с предзаполненными тегами при статусе `READY`.

### 6.2 Команды верификации
```bash
# 1. Прогон всех тестов (включая существующие 78)
./venv/bin/python -m pytest tests/ -v

# 2. Статический анализ и линтинг ruff
./venv/bin/ruff check app tests
```

---

## 7. Критерии приемки (Definition of Done)

1. Зависимости `yt-dlp`, `rapidfuzz`, `mutagen` зафиксированы в `requirements.txt` и установлены в `venv`.
2. Реализован `DeezerClient` с корректной обработкой треков, альбомов и скачиванием обложек 1400x1400.
3. Реализован `AudioMatcher` и `SourceResolver` с приоритизацией официальных каналов и фильтрацией по таймингам $\pm 2$ сек.
4. Реализован `DownloadService` с неблокирующим запуском `yt-dlp`/`ffmpeg`, предзаполнением ID3v2.4-тегов (`mutagen`) и гарантированной изоляцией через `PathSandbox`.
5. Эндпоинты `/downloads`, `/downloads/{job_id}`, `/downloads/{job_id}/tracks` реализованы, защищены сессионной авторизацией и задокументированы в OpenAPI.
6. Фоновый сборщик мусора очищает буфер `TMP_DIR` по TTL без утечки файловых дескрипторов.
7. Все тесты в `tests/` проходят успешно (100% pass), реальные сетевые запросы в тестах отсутствуют (полное мокирование).
8. Линтер `ruff check app tests` не выдает ошибок и предупреждений.
9. Файл `PROGRESS.md` актуализирован.
