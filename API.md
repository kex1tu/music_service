# Документация REST API: Music Downloader & Tag Manager

**Версия API:** 1.0.0  
**Базовый URL:** `http://localhost:8080` (или хост развертывания сервиса)  
**Формат данных:** JSON (`application/json; charset=utf-8`), бинарные аудиопотоки (`audio/mpeg`), изображения (`image/jpeg`, `image/png`), multipart (`multipart/form-data`)  
**Стек:** Go (Chi router) + ID3v2.4 Engine + SQLite WAL + yt-dlp + ffmpeg

---

## Содержание

1. [Аутентификация и безопасность](#1-аутентификация-и-безопасность)
   - [Механизм сессий](#11-механизм-сессий)
   - [Защита файловой системы (PathSandbox Guard)](#12-защита-файловой-системы-pathsandbox-guard)
   - [Коды ответов и формат ошибок](#13-коды-ответов-и-формат-ошибок)
2. [Системные и служебные эндпоинты](#2-системные-и-служебные-эндпоинты)
   - `GET /api/health` — Проверка статуса сервиса
   - `HEAD /api/health` — Быстрый пинг
   - `GET /` — Главная страница SPA
   - `GET /static/*` — Статические ресурсы
3. [Аутентификация (Auth API)](#3-аутентификация-auth-api)
   - `POST /login` (`POST /api/auth/login`) — Вход в систему
   - `POST /logout` (`POST /api/auth/logout`) — Выход из системы
   - `GET /api/auth/me` — Проверка текущей сессии
4. [Загрузки и очередь (Downloads API)](#4-загрузки-и-очередь-downloads-api)
   - `POST /downloads` — Создание задачи на загрузку
   - `POST /downloads/batch` — Пакетная загрузка
   - `GET /downloads/discography` — Получение дискографии артиста
   - `POST /downloads/torrent` — Загрузка .torrent файлов
   - `GET /downloads` — Список всех задач очереди
   - `GET /downloads/queue/summary` — Сводка состояния очереди
   - `POST /downloads/queue/clear` — Очистка завершенных задач
   - `DELETE /downloads/completed` — Удаление завершенных задач
   - `GET /downloads/{id}` — Статус конкретной задачи
   - `DELETE /downloads/{id}` — Отмена или удаление задачи
   - `POST /downloads/{id}/retry` — Повтор упавшей задачи
   - `GET /downloads/{id}/tracks` — Треки и метаданные задачи
5. [Теги и метаданные ID3v2.4 (Tags API)](#5-теги-и-метаданные-id3v24-tags-api)
   - `GET /tags` — Чтение ID3 тегов файла или папки
   - `POST /tags` — Сохранение тегов одного трека
   - `POST /tags/batch` / `PUT /tags/batch` — Пакетное сохранение тегов и перемещение в медиатеку
   - `POST /tags/recognize-audio` — Акустическое распознавание трека (Shazam)
   - `GET /tags/cover` — Получение обложки трека или альбома
   - `GET /tags/covers/search` — Поиск обложек ультравысокого разрешения
   - `GET /tags/search-deezer-albums` — Поиск релизов в Deezer
   - `GET /tags/deezer-album-details` — Детальная информация об альбоме в Deezer
6. [Тексты песен (Lyrics API)](#6-тексты-песен-lyrics-api)
   - `GET /lyrics` — Получение локального текста песни (.lrc / .txt)
   - `GET /lyrics/search` — Онлайн-поиск текстов (LRCLIB + Genius)
   - `POST /lyrics` — Сохранение текста песни рядом с файлом
   - `DELETE /lyrics` — Удаление файлов текстов песни
7. [Стриминг и библиотека (Tracks & Library API)](#7-стриминг-и-библиотека-tracks--library-api)
   - `GET /api/tracks/{id}/stream` — Стриминг аудио с поддержкой HTTP Range (RFC 7233)
   - `GET /api/tracks/{id}/metadata` — Быстрые метаданные трека
   - `GET /api/tracks/{id}/cover` — Обложка трека
   - `GET /api/library/tracks` — Список треков медиатеки с пагинацией
   - `GET /api/library/stream/{id}/chunks` — Обфусцированный стриминг (xHTTP)
8. [Файловый менеджер и поиск (Files API)](#8-файловый-менеджер-и-поиск-files-api)
   - `GET /files` — Дерево файлов и папок
   - `DELETE /files` — Удаление файла или папки с очисткой БД
   - `GET /files/stream` — Стриминг аудио по относительному пути
   - `GET /files/search` — Полнотекстовый поиск по медиатеке (Ctrl+K)
9. [Аудит медиатеки и автоисправление (Audit API)](#9-аудит-медиатеки-и-автоисправление-audit-api)
   - `GET /files/audit/summary` — Сводный отчет дефектов
   - `POST /files/audit/refresh` — Принудительное пересканирование
   - `GET /files/audit/invalid-paths` — Список неканонических путей
   - `POST /files/audit/fix-path` — Исправление пути одного трека
   - `POST /files/audit/fix-all-paths` — Пакетное исправление всех путей
   - `GET /files/audit/missing-lyrics` — Список треков без текстов
   - `POST /files/audit/missing-lyrics/fetch-all` — Фоновый сбор текстов песен
   - `GET /files/audit/missing-lyrics/progress` — Прогресс фонового сбора текстов
   - `GET /files/audit/missing-covers` — Список альбомов без обложек
   - `GET /files/audit/duplicates` — Поиск дубликатов треков
   - `POST /files/audit/duplicates/fix` — Удаление выбранных дубликатов
   - `POST /files/audit/duplicates/fix-all` — Автоматическая очистка всех дубликатов
   - `GET /files/audit/non-en-genres` — Поиск не-английских жанров
   - `POST /files/audit/retag-genres` — Автонормализация жанров
   - `POST /files/audit/reorganize` — Полная реорганизация медиатеки
   - `GET /files/audit/incomplete-albums` — Список неполных релизов
   - `GET /files/audit/incomplete-albums/catalog-match` — Сверка альбома с каталогом Deezer
   - `GET /files/audit/incomplete-albums/editor-data` — Подготовка данных для бэкфилла
   - `POST /files/audit/incomplete-albums/download-missing` — Дозагрузка недостающих треков
   - `GET /files/audit/tag-issues` — Анализ дефектов тегов треков
   - `POST /files/audit/tag-issues/fix` — Ручное исправление тегов трека
   - `POST /files/audit/tag-issues/fix-safe` — Быстрое безопасное автоисправление тегов
   - `POST /files/audit/tag-issues/fix-selected` — Исправление выбранных треков
   - `POST /files/audit/tag-issues/fix-all` — Пакетное автоисправление всех дефектов
   - `GET /files/audit/tag-issues/catalog-match` — Мульти-каталожный поиск трека для Diff
   - `POST /files/audit/tag-issues/apply-catalog` — Применение каталожных тегов и обложки

---

## 1. Аутентификация и безопасность

### 1.1 Механизм сессий
- Аутентификация администратора основана на cookie `session_token`.
- Формат токена: `Base64(login + ":" + HMAC_SHA256(login, SECRET_KEY))`.
- Cookie выдается со следующими атрибутами:
  - `Path=/`
  - `HttpOnly` (недоступна из клиентского JavaScript для защиты от XSS)
  - `SameSite=Lax` (или `Strict`)
  - `Max-Age=2592000` (30 суток)
- Все защищенные эндпоинты требуют передачи валидной cookie `session_token`. При её отсутствии возвращается HTTP `401 Unauthorized` с перенаправлением на экран входа.

### 1.2 Защита файловой системы (PathSandbox Guard)
- Все файловые операции (чтение, запись, перемещение, удаление, сканирование) строго ограничены корневой директорией медиатеки `MUSIC_DIR` (и временной директорией `tmp_downloads/` для задач загрузки).
- Обращение к путям с Path Traversal (`../`, `../../`, абсолютные пути `/etc/...`, `\0` null-байты, некорректные символы) немедленно пресекается на уровне `PathSandbox.Resolve()`, возвращая ошибку HTTP `403 Forbidden` (`{"error":"access denied"}`).

### 1.3 Коды ответов и формат ошибок
- `200 OK` — Запрос успешно выполнен.
- `206 Partial Content` — Успешная отдача диапазона байт при аудио-стриминге (RFC 7233).
- `400 Bad Request` — Некорректные параметры запроса или невалидный JSON: `{"error": "описание ошибки"}` или `{"detail": "..."}`.
- `401 Unauthorized` — Требуется авторизация или неверный логин/пароль: `{"error": "invalid credentials"}`.
- `403 Forbidden` — Попытка выхода за пределы песочницы или запрещенный доступ: `{"error": "access denied"}`.
- `404 Not Found` — Запрашиваемый ресурс, задача или файл не найдены: `{"error": "not found"}`.
- `451 Unavailable For Legal Reasons` — Ошибка геоблокировки зарубежного IP (при работе с Яндекс Музыкой без прокси).
- `500 Internal Server Error` — Внутренняя ошибка сервера: `{"error": "описание ошибки"}`.

---

## 2. Системные и служебные эндпоинты

### `GET /api/health`
Проверка состояния сервиса (Health Check / Liveness Probe).
- **Авторизация:** Не требуется.
- **Ответ `200 OK`:**
```json
{
  "status": "ok",
  "app": "music-manager",
  "version": "1.0.0"
}
```

### `HEAD /api/health`
Быстрая проверка доступности без передачи тела ответа.
- **Ответ `200 OK`** (только HTTP-заголовки).

### `GET /`
Главная страница Single Page Application (SPA).
- **Авторизация:** Не требуется.
- **Заголовки ответа:** `Content-Type: text/html; charset=utf-8`

### `GET /static/*`
Раздача статических файлов (CSS, модульные JS-файлы Alpine.js) из встроенной файловой системы `embed.FS`.
- **Пример:** `GET /static/css/style.css`, `GET /static/js/file_manager.js`

---

## 3. Аутентификация (Auth API)

### `POST /login` (алиас: `POST /api/auth/login`)
Авторизация администратора в системе.

- **Авторизация:** Не требуется.
- **Тело запроса (JSON):**
```json
{
  "login": "admin",
  "password": "your-password"
}
```
- **Ответ `200 OK`:**
```json
{
  "success": true,
  "message": "Авторизация успешна"
}
```
*Устанавливает HTTP-заголовок `Set-Cookie: session_token=...; Path=/; HttpOnly; Max-Age=2592000; SameSite=Lax`.*

- **Ошибки:**
  - `400 Bad Request`: `{"error":"invalid json"}`
  - `401 Unauthorized`: `{"error":"invalid credentials"}`

---

### `POST /logout` (алиас: `POST /api/auth/logout`)
Завершение сессии и сброс cookie авторизации.

- **Авторизация:** Не требуется.
- **Ответ `200 OK`:**
```json
{
  "success": true
}
```
*Устанавливает HTTP-заголовок `Set-Cookie: session_token=; Path=/; Max-Age=-1`.*

---

### `GET /api/auth/me`
Проверка текущего статуса аутентификации.

- **Авторизация:** Не требуется.
- **Ответ `200 OK` (пользователь авторизован):**
```json
{
  "login": "admin",
  "authenticated": true
}
```
- **Ответ `200 OK` (сессия отсутствует):**
```json
{
  "login": "",
  "authenticated": false
}
```

---

## 4. Загрузки и очередь (Downloads API)

Базовый путь: `/downloads`. Все эндпоинты требуют авторизации.

### `POST /downloads`
Создание задачи на скачивание трека, альбома или плейлиста. Поддерживаются ссылки Deezer, YouTube, YouTube Music, Spotify, SoundCloud, Яндекс Музыка.

- **Тело запроса (JSON):**
```json
{
  "url": "https://music.youtube.com/watch?v=dQw4w9WgXcQ",
  "source": "youtube"
}
```
*(Параметр `source` опционален, тип источника определяется автоматически).*

- **Ответ `200 OK`:** Объект `DownloadJob`
```json
{
  "id": "3fa85f64-5717-4562-b3fc-2c963f66afa6",
  "status": "queued",
  "url": "https://music.youtube.com/watch?v=dQw4w9WgXcQ",
  "source": "youtube",
  "target_album_path": "",
  "title": "Never Gonna Give You Up",
  "artist": "Rick Astley",
  "total_tracks": 1,
  "completed_tracks": 0,
  "progress": 0,
  "overall_progress": 0,
  "error": "",
  "created_at": 1727376000,
  "updated_at": 1727376000
}
```
- **Ошибки:**
  - `400 Bad Request`: `{"error":"domain not allowed"}` (домен не входит в whitelist).

---

### `POST /downloads/batch`
Пакетное создание нескольких задач загрузки по списку URL.

- **Тело запроса (JSON):**
```json
{
  "urls": [
    "https://www.deezer.com/album/302127",
    "https://music.youtube.com/playlist?list=PL..."
  ],
  "source": "batch"
}
```
- **Ответ `200 OK`:**
```json
{
  "job_ids": ["uuid-1", "uuid-2"],
  "jobs": [ { /* DownloadJob 1 */ }, { /* DownloadJob 2 */ } ],
  "total_queued": 2
}
```

---

### `GET /downloads/discography`
Получение структурированного списка всех официальных релизов артиста (альбомы, синглы, EP) по URL артиста в Deezer, Яндекс Музыке, Apple Music/iTunes или YouTube Music.

- **Query-параметры:**
  - `url` (*string*, обязательный) — URL страницы артиста.
- **Примеры ссылок:**
  - `https://www.deezer.com/artist/27`
  - `https://music.yandex.ru/artist/12345`
  - `https://music.apple.com/ru/artist/daft-punk/5468295`
  - `https://www.youtube.com/@DaftPunk/releases`
- **Ответ `200 OK`:**
```json
{
  "artist_name": "Daft Punk",
  "artist_id": 27,
  "artist_avatar": "https://e-cdns-images.dzcdn.net/images/artist/...",
  "source": "deezer",
  "total_releases": 14,
  "releases": [
    {
      "id": 302127,
      "title": "Discovery",
      "type": "album",
      "year": 2001,
      "url": "https://www.deezer.com/album/302127",
      "cover_url": "https://e-cdns-images.dzcdn.net/images/cover/.../1000x1000-000000-80-0-0.jpg",
      "track_count": 14
    }
  ]
}
```
- **Ошибки:**
  - `400 Bad Request`: `{"detail":"Нераспознанный URL артиста..."}`
  - `451 Unavailable For Legal Reasons`: При геоблокировке зарубежного IP в Яндекс Музыке (`{"detail":"Не удалось получить профиль артиста: Geoblock HTTP 451..."}`).

---

### `POST /downloads/torrent`
Загрузка торрент-файла для последующего скачивания.

- **Content-Type:** `multipart/form-data` (максимальный размер 10 МБ).
- **Поля формы:**
  - `file`: бинарное содержимое `.torrent`.
- **Ответ:** HTTP `400 Bad Request` с сообщением о временном отключении торрент-модуля и рекомендацией использовать стриминг-ссылки.

---

### `GET /downloads`
Получение полного списка задач очереди загрузок.

- **Сортировка:** По приоритету статуса (`queued` -> `resolving/downloading/converting/tagging` -> `ready` -> `error` -> `cancelled`), внутри статуса — по времени создания `created_at DESC`.
- **Ответ `200 OK`:** Массив объектов `DownloadJob`.

---

### `GET /downloads/queue/summary`
Сводка количества задач в очереди по их текущим состояниям.

- **Ответ `200 OK`:**
```json
{
  "queued": 2,
  "processing": 1,
  "ready": 15,
  "error": 0,
  "cancelled": 1,
  "total": 19
}
```

---

### `POST /downloads/queue/clear` и `DELETE /downloads/completed`
Удаление всех завершенных (`ready`) и отмененных (`cancelled`) задач из очереди.

- **Ответ `200 OK`:**
```json
{
  "status": "cleared",
  "count": 16
}
```

---

### `GET /downloads/{id}`
Получение детальной информации о конкретной задаче по её ID.

- **URL-параметры:**
  - `id` (*string*, обязательный) — UUID задачи.
- **Ответ `200 OK`:** Объект `DownloadJob`.
- **Ошибки:**
  - `404 Not Found`: `{"error":"job not found"}`.

---

### `DELETE /downloads/{id}`
Отмена выполняющейся задачи или удаление завершенной задачи вместе со всеми временными файлами из `tmp_downloads/{id}/`.

- **URL-параметры:**
  - `id` (*string*, обязательный) — UUID задачи.
- **Ответ `200 OK`:**
```json
{
  "status": "deleted",
  "job_id": "3fa85f64-5717-4562-b3fc-2c963f66afa6"
}
```

---

### `POST /downloads/{id}/retry`
Повторный перезапуск задачи, завершившейся с ошибкой или отмененной.

- **URL-параметры:**
  - `id` (*string*, обязательный) — UUID задачи.
- **Ответ `200 OK`:** Обновленный объект `DownloadJob` со статусом `queued`.

---

### `GET /downloads/{id}/tracks`
Получение треклиста и метаданных релиза скачанной задачи (используется для передачи в модальное окно редактора тегов).

- **URL-параметры:**
  - `id` (*string*, обязательный) — UUID задачи.
- **Ответ `200 OK`:**
```json
{
  "job_id": "3fa85f64-5717-4562-b3fc-2c963f66afa6",
  "album_artist": "Daft Punk",
  "album": "Discovery",
  "year": 2001,
  "genre": "Electronic",
  "disc_total": 1,
  "track_total": 14,
  "cover_url": "https://...",
  "cover_path": "tmp_downloads/3fa85f64.../converted/cover.jpg",
  "tracks": [
    {
      "track_number": 1,
      "title": "One More Time",
      "artist": "Daft Punk",
      "path": "tmp_downloads/3fa85f64.../converted/01. One More Time.mp3",
      "duration": 320.5
    }
  ]
}
```

---

## 5. Теги и метаданные ID3v2.4 (Tags API)

Базовый путь: `/tags`. Все эндпоинты требуют авторизации.

### `GET /tags`
Чтение метаданных ID3v2.4 одного аудиофайла или сводных метаданных альбома при передаче пути к директории. Поддерживает пути как из медиатеки `MUSIC_DIR`, так и из временного буфера `tmp_downloads/`.

- **Query-параметры:**
  - `path` (*string*, обязательный) — относительный путь к файлу MP3 или папке альбома.
- **Ответ `200 OK` (для директории альбома):**
```json
{
  "album": {
    "album_artist": "Daft Punk",
    "album": "Discovery",
    "year": 2001,
    "genre": "Electronic",
    "disc_total": 1,
    "track_total": 14
  },
  "tracks": [
    {
      "path": "Discovery - Daft Punk/01. One More Time.mp3",
      "title": "One More Time",
      "artist": "Daft Punk",
      "album": "Discovery",
      "album_artist": "Daft Punk",
      "year": 2001,
      "genre": "Electronic",
      "track_number": 1,
      "track_total": 14,
      "disc_number": 1,
      "disc_total": 1,
      "duration": 320,
      "bitrate": 320,
      "has_cover": true,
      "lyrics_status": "lrc"
    }
  ],
  "source_dir": "Discovery - Daft Punk",
  "cover_path": "Discovery - Daft Punk/cover.jpg",
  "has_cover": true
}
```

---

### `POST /tags`
Сохранение ID3v2.4 тегов для одного MP3 файла.

- **Тело запроса (JSON):** Объект `TrackTags`
```json
{
  "path": "Discovery - Daft Punk/01. One More Time.mp3",
  "title": "One More Time",
  "artist": "Daft Punk",
  "album": "Discovery",
  "album_artist": "Daft Punk",
  "year": 2001,
  "genre": "Electronic",
  "track_number": 1,
  "track_total": 14,
  "disc_number": 1,
  "disc_total": 1
}
```
- **Ответ `200 OK`:**
```json
{
  "success": true,
  "track": { /* сохраненные теги */ }
}
```

---

### `POST /tags/batch` (и `PUT /tags/batch`)
Пакетное сохранение тегов всего альбома, раскладка файлов в каноническую структуру медиатеки (`MUSIC_DIR/{Album} - {Album Artist}/{TrackNumber:02d}. {Title}.mp3`), вшивание обложки из Base64 / скачивание по URL и генерация файлов `cover.jpg` и `{TrackBaseName}.jpg`.

- **Тело запроса (JSON):**
```json
{
  "album": {
    "album_artist": "Daft Punk",
    "album": "Discovery",
    "year": 2001,
    "genre": "Electronic"
  },
  "tracks": [
    {
      "path": "tmp_downloads/3fa85f64.../converted/01. One More Time.mp3",
      "title": "One More Time",
      "artist": "Daft Punk",
      "track_number": 1,
      "disc_number": 1
    }
  ],
  "source_dir": "tmp_downloads/3fa85f64.../converted",
  "cover_base64": "data:image/jpeg;base64,...",
  "cover_url": "https://...",
  "reorganize": true
}
```
- **Ответ `200 OK`:**
```json
{
  "success": true,
  "destination_dir": "Discovery - Daft Punk",
  "tracks": [
    {
      "path": "Discovery - Daft Punk/01. One More Time.mp3",
      "title": "One More Time",
      "artist": "Daft Punk",
      "track_number": 1
    }
  ]
}
```

---

### `POST /tags/recognize-audio`
Акустическое распознавание неизвестного аудиофайла по звуковому отпечатку через `songrec` (Shazam Core).

- **Тело запроса (JSON):**
```json
{
  "path": "Unknown/track.mp3"
}
```
- **Ответ `200 OK`:**
```json
{
  "recognized": true,
  "title": "Get Lucky",
  "artist": "Daft Punk feat. Pharrell Williams",
  "album": "Random Access Memories",
  "year": 2013,
  "genre": "Disco",
  "cover_url": "https://cdn.shazam.com/.../1000x1000cc.jpg"
}
```
*(Если трек не распознан: `{"recognized": false}`).*

---

### `GET /tags/cover`
Получение бинарных данных обложки трека или альбома (из встроенного фрейма `APIC` или файлов `cover.jpg` / `01. Title.jpg`).

- **Query-параметры:**
  - `path` (*string*, обязательный) — относительный путь к файлу трека или папке альбома.
- **Ответ `200 OK`:** Бинарное изображение (`image/jpeg`, `image/png`, `image/webp`).
- **Заголовки:** `Cache-Control: public, max-age=86400`.

---

### `GET /tags/covers/search`
Поиск обложек ультравысокого разрешения (до 1400×1400 px) через каталог Deezer.

- **Query-параметры:**
  - `query` (*string*, обязательный) — поисковый запрос (артист и название альбома).
  - `limit` (*int*, опционально, по умолчанию 12, максимум 50) — лимит результатов.
- **Ответ `200 OK`:**
```json
[
  {
    "source": "deezer",
    "title": "Discovery",
    "artist": "Daft Punk",
    "cover_url": "https://e-cdns-images.dzcdn.net/images/cover/.../1000x1000-000000-80-0-0.jpg",
    "width": 1000,
    "height": 1000,
    "year": 2001
  }
]
```

---

### `GET /tags/search-deezer-albums` (алиас: `GET /tags/deezer/albums`)
Поиск официальных альбомов в каталоге Deezer для автозаполнения тегов релиза.

- **Query-параметры:**
  - `query` (*string*, обязательный) — строка поиска.
- **Ответ `200 OK`:**
```json
[
  {
    "id": "302127",
    "title": "Discovery",
    "artist": "Daft Punk",
    "cover_url": "https://...",
    "nb_tracks": 14,
    "year": 2001
  }
]
```

---

### `GET /tags/deezer-album-details` (алиас: `GET /tags/deezer/albums/{id}`)
Получение детального треклиста, номеров треков и длительностей релиза из Deezer API.

- **Query или URL-параметр:**
  - `id` (*string*, обязательный) — идентификатор альбома в Deezer.
- **Ответ `200 OK`:**
```json
{
  "id": "302127",
  "title": "Discovery",
  "artist": "Daft Punk",
  "year": 2001,
  "genre": "Electronic",
  "cover_url": "https://...",
  "tracks": [
    {
      "track_number": 1,
      "title": "One More Time",
      "artist": "Daft Punk",
      "duration": 320
    }
  ]
}
```

---

## 6. Тексты песен (Lyrics API)

Базовый путь: `/lyrics`. Все эндпоинты требуют авторизации.  
**Инвариант Zero ID3 Lyrics:** тексты песен сохраняются строго во внешних файлах `.lrc` / `.txt` рядом с MP3 и никогда не вшиваются в теги ID3.

### `GET /lyrics`
Получение текста песни из локального файла `.lrc` или `.txt`.

- **Query-параметры:**
  - `path` (или `track_path`, *string*, обязательный) — относительный путь к треку.
- **Ответ `200 OK`:**
```json
{
  "track_path": "Discovery - Daft Punk/01. One More Time.mp3",
  "has_lyrics": true,
  "lyrics": "[00:15.20]One more time\n[00:18.50]We're gonna celebrate...",
  "kind": "lrc",
  "synced": true
}
```

---

### `GET /lyrics/search`
Онлайн-поиск синхронизированных (`.lrc`) текстов через LRCLIB API с автоматическим резервным поиском несинхронизированных (`.txt`) через Genius API.

- **Query-параметры:**
  - `artist` (*string*, обязательный) — имя исполнителя.
  - `title` (*string*, обязательный) — название трека.
  - `album` (*string*, опционально) — название альбома.
  - `duration` (*int*, опционально) — длительность в секундах для точного сопоставления.
- **Ответ `200 OK`:**
```json
{
  "lyrics": "[00:15.20]One more time...",
  "synced": true,
  "source": "lrclib"
}
```

---

### `POST /lyrics`
Сохранение текста песни в файл рядом с MP3 (автоматически создает `.lrc` при наличии таймкодов или `.txt` для простого текста).

- **Тело запроса (JSON):**
```json
{
  "track_path": "Discovery - Daft Punk/01. One More Time.mp3",
  "content": "[00:15.20]One more time...",
  "kind": "auto"
}
```
*(Параметр `kind` может принимать значения `"auto"`, `"lrc"`, `"txt"`).*

- **Ответ `200 OK`:**
```json
{
  "success": true
}
```

---

### `DELETE /lyrics`
Удаление сопутствующих файлов текстов песен (`.lrc` и `.txt`) для указанного трека.

- **Query-параметры:**
  - `path` (или `track_path`, *string*, обязательный).
- **Ответ `200 OK`:**
```json
{
  "success": true,
  "deleted": true
}
```

---

## 7. Стриминг и библиотека (Tracks & Library API)

Все эндпоинты требуют авторизации.

### `GET /api/tracks/{id}/stream`
Потоковое воспроизведение аудиофайла по его идентификатору или хэшу с полной поддержкой стандарта RFC 7233 (HTTP Range).

- **URL-параметры:**
  - `id` (*string*, обязательный) — MD5-хэш относительного пути трека или его числовой ID в SQLite. Также допускается передача `?id=...` или `?path=...`.
- **Заголовки запроса:**
  - `Range: bytes=0-1048575` (поддерживается перемотка на любой участок аудио).
- **Ответ `206 Partial Content` (или `200 OK`):**
  - **Заголовки:**
    - `Content-Type: audio/mpeg` (или `audio/flac`, `audio/ogg`, etc.)
    - `Accept-Ranges: bytes`
    - `Content-Range: bytes 0-1048575/8388608`
    - `Content-Length: 1048576`
    - `Cache-Control: private, max-age=3600`

---

### `GET /api/tracks/{id}/metadata`
Мгновенное получение структурированных метаданных трека без накладных расходов на чтение всего аудиофайла (из кэша SQLite).

- **URL-параметры:**
  - `id` (*string*, обязательный) — идентификатор или хэш трека.
- **Ответ `200 OK`:**
```json
{
  "id": "e3b0c44298fc1c149afbf4c8996fb924",
  "title": "One More Time",
  "artist": "Daft Punk",
  "album": "Discovery",
  "duration_sec": 320,
  "bitrate_kbps": 320,
  "sample_rate": 44100,
  "track_number": 1,
  "year": 2001,
  "genre": "Electronic",
  "file_size": 12804512,
  "codec": "mp3",
  "has_cover": true,
  "stream_url": "/api/tracks/e3b0c44298fc1c149afbf4c8996fb924/stream",
  "cover_url": "/api/tracks/e3b0c44298fc1c149afbf4c8996fb924/cover",
  "file_path": "Discovery - Daft Punk/01. One More Time.mp3"
}
```

---

### `GET /api/tracks/{id}/cover`
Получение обложки трека по его ID или хэшу (встроенная APIC или файл обложки альбома).

- **URL-параметры:**
  - `id` (*string*, обязательный).
- **Ответ `200 OK`:** Бинарные данные изображения (`image/jpeg`).

---

### `GET /api/library/tracks`
Список треков медиатеки с пагинацией и фильтрацией.

- **Query-параметры:**
  - `query` (или `q`, *string*, опционально) — поисковый запрос по названию, артисту или альбому.
  - `limit` (*int*, опционально, по умолчанию 50, максимум 200).
  - `offset` (*int*, опционально, по умолчанию 0).
- **Ответ `200 OK`:**
```json
{
  "tracks": [
    {
      "id": "e3b0c44298fc1c149afbf4c8996fb924",
      "numeric_id": 42,
      "title": "One More Time",
      "artist": "Daft Punk",
      "album": "Discovery",
      "duration_sec": 320,
      "bitrate_kbps": 320,
      "path": "Discovery - Daft Punk/01. One More Time.mp3",
      "has_cover": true,
      "stream_url": "/api/tracks/e3b0c44298fc1c149afbf4c8996fb924/stream",
      "cover_url": "/api/tracks/e3b0c44298fc1c149afbf4c8996fb924/cover"
    }
  ],
  "count": 1,
  "limit": 50,
  "offset": 0
}
```

---

### `GET /api/library/stream/{id}/chunks`
Алиас стриминга с поддержкой маскировки под фрагментированные xHTTP-чанки для надежной работы через строгие корпоративные фаерволы и VPN.

---

## 8. Файловый менеджер и поиск (Files API)

Базовый путь: `/files`. Все эндпоинты требуют авторизации.

### `GET /files`
Получение содержимого папки медиатеки для файлового проводника.

- **Query-параметры:**
  - `path` (*string*, опционально, по умолчанию корень медиатеки `""`).
- **Ответ `200 OK`:**
```json
{
  "current_path": "Discovery - Daft Punk",
  "parent_path": "",
  "breadcrumbs": [
    { "name": "Корень", "path": "" },
    { "name": "Discovery - Daft Punk", "path": "Discovery - Daft Punk" }
  ],
  "items": [
    {
      "name": "01. One More Time.mp3",
      "path": "Discovery - Daft Punk/01. One More Time.mp3",
      "type": "file",
      "size": 12804512,
      "extension": ".mp3",
      "track_number": 1,
      "title": "One More Time",
      "artist": "Daft Punk",
      "album": "Discovery",
      "duration": 320,
      "has_cover": true,
      "lyrics_status": "lrc",
      "id": "e3b0c44298fc1c149afbf4c8996fb924",
      "stream_url": "/api/tracks/e3b0c44298fc1c149afbf4c8996fb924/stream"
    },
    {
      "name": "cover.jpg",
      "path": "Discovery - Daft Punk/cover.jpg",
      "type": "file",
      "size": 245120,
      "extension": ".jpg",
      "lyrics_status": "none"
    }
  ],
  "total_directories": 0,
  "total_files": 2
}
```

---

### `DELETE /files`
Удаление файла или директории с каскадной синхронизацией SQLite WAL:
- При удалении MP3: удаляются сопутствующие `.lrc`, `.txt`, `.jpg`, очищаются записи в таблицах `tracks`, `track_genres`, `audit_issues`. Если в альбоме больше не осталось треков — запись альбома автоматически удаляется.
- При удалении директории: рекурсивно удаляются все файлы, директория и все связанные записи в БД.

- **Query-параметры:**
  - `path` (*string*, обязательный) — относительный путь к файлу или папке.
- **Ответ `200 OK`:**
```json
{
  "success": true,
  "deleted_path": "Discovery - Daft Punk/01. One More Time.mp3",
  "is_directory": false
}
```

---

### `GET /files/stream`
Стриминг аудиофайла по прямому относительному пути к файлу в `MUSIC_DIR` (или `tmp_downloads/`).

- **Query-параметры:**
  - `path` (*string*, обязательный).

---

### `GET /files/search`
Глобальный полнотекстовый поиск по всей медиатеке (используется для модального окна быстрого поиска `Ctrl+K`). Ищет совпадения по артистам, альбомам и названиям треков.

- **Query-параметры:**
  - `q` (или `query`, *string*, обязательный).
- **Ответ `200 OK`:**
```json
{
  "query": "Daft",
  "artists": [ { "id": 1, "name": "Daft Punk" } ],
  "albums": [ { "id": 1, "title": "Discovery", "artist": "Daft Punk", "year": 2001 } ],
  "tracks": [ { "id": 42, "title": "One More Time", "artist": "Daft Punk", "album": "Discovery", "file_path": "..." } ],
  "total_matches": 3
}
```

---

## 9. Аудит медиатеки и автоисправление (Audit API)

Базовый путь: `/files/audit`. Все эндпоинты требуют авторизации.

### `GET /files/audit/summary`
Сводный дашборд состояния медиатеки и обнаруженных дефектов.

- **Ответ `200 OK`:**
```json
{
  "total_tracks": 1420,
  "total_albums": 115,
  "missing_lyrics_count": 42,
  "missing_covers_count": 3,
  "duplicates_count": 0,
  "incomplete_albums_count": 2,
  "invalid_paths_count": 5,
  "non_en_genres_count": 0,
  "tag_issues_count": 18
}
```

---

### `POST /files/audit/refresh`
Принудительный запуск фонового пересканирования всей медиатеки и обновление дашборда аудита.

- **Ответ `200 OK`:** Возвращает обновленный объект `summary`.

---

### `GET /files/audit/invalid-paths`
Список треков, размещенных не по каноническому шаблону (`{Album} - {Album Artist}/{TrackNumber:02d}. {Title}.mp3`).

- **Ответ `200 OK`:**
```json
[
  {
    "file_path": "Daft Punk/Discovery/1. One More Time.mp3",
    "suggested_path": "Discovery - Daft Punk/01. One More Time.mp3",
    "reason": "Неканонический путь"
  }
]
```

---

### `POST /files/audit/fix-path`
Перемещение одного трека и его сопутствующих файлов (.lrc, .jpg) в канонический путь с автоматическим удалением опустевших старых папок.

- **Тело запроса (JSON):**
```json
{
  "path": "Daft Punk/Discovery/1. One More Time.mp3",
  "overwrite": false
}
```
- **Ответ `200 OK`:**
```json
{
  "success": true,
  "source_path": "Daft Punk/Discovery/1. One More Time.mp3",
  "target_path": "Discovery - Daft Punk/01. One More Time.mp3",
  "message": "Путь успешно исправлен на канонический"
}
```

---

### `POST /files/audit/fix-all-paths`
Пакетное автоматическое перемещение всех неканонических треков медиатеки.

- **Ответ `200 OK`:**
```json
{
  "success": true,
  "total_invalid": 5,
  "fixed_count": 5,
  "errors": []
}
```

---

### `GET /files/audit/missing-lyrics`
Список треков, у которых отсутствует текст песни (`lyrics_status = 'none'`).

- **Ответ `200 OK`:**
```json
[
  {
    "path": "Discovery - Daft Punk/01. One More Time.mp3",
    "title": "One More Time",
    "artist": "Daft Punk",
    "album": "Discovery",
    "directory": "Discovery - Daft Punk"
  }
]
```

---

### `POST /files/audit/missing-lyrics/fetch-all`
Запуск фонового асинхронного воркера пакетного скачивания текстов песен через LRCLIB.

- **Ответ `200 OK`:**
```json
{
  "is_running": true,
  "total": 42,
  "current": 0,
  "found": 0,
  "failed": 0,
  "current_track": ""
}
```

---

### `GET /files/audit/missing-lyrics/progress`
Опрос статуса выполнения фонового процесса сбора текстов песен (Polling).

- **Ответ `200 OK`:**
```json
{
  "is_running": true,
  "total": 42,
  "current": 18,
  "found": 15,
  "failed": 3,
  "current_track": "Daft Punk - Aerodynamic"
}
```

---

### `GET /files/audit/missing-covers`
Список альбомов в медиатеке, в которых отсутствует обложка (`cover.jpg` или встроенный ID3 APIC).

- **Ответ `200 OK`:** Массив объектов альбомов без обложек.

---

### `GET /files/audit/duplicates`
Поиск дубликатов треков внутри альбомов по нормализованному названию.

- **Ответ `200 OK`:**
```json
{
  "groups": [
    {
      "key": "Discovery - Daft Punk::one more time",
      "title": "One More Time",
      "artist": "Daft Punk",
      "album_dir": "Discovery - Daft Punk",
      "album": "Discovery",
      "tracks": [
        {
          "path": "Discovery - Daft Punk/01. One More Time.mp3",
          "filename": "01. One More Time.mp3",
          "size_bytes": 12804512,
          "duration": 320
        },
        {
          "path": "Discovery - Daft Punk/01. One More Time (1).mp3",
          "filename": "01. One More Time (1).mp3",
          "size_bytes": 12804512,
          "duration": 320
        }
      ]
    }
  ],
  "total_duplicate_files": 1
}
```

---

### `POST /files/audit/duplicates/fix`
Удаление выбранных пользователем файлов-дубликатов.

- **Тело запроса (JSON):**
```json
{
  "group_key": "Discovery - Daft Punk::one more time",
  "album_path": "Discovery - Daft Punk",
  "paths_to_delete": [
    "Discovery - Daft Punk/01. One More Time (1).mp3"
  ]
}
```

---

### `POST /files/audit/duplicates/fix-all`
Автоматическое удаление всех найденных дубликатов по правилу выбора канонического файла: приоритет отдается файлу без суффиксов копий `(1)`, с максимальным битрейтом и большим размером.

- **Ответ `200 OK`:**
```json
{
  "success": true,
  "deleted_count": 1,
  "deleted_files": ["..."],
  "errors": []
}
```

---

### `GET /files/audit/non-en-genres`
Поиск треков, у которых жанр не переведен на английский язык или не нормализован по каноническому словарю ID3v2.4.

---

### `POST /files/audit/retag-genres`
Автоматическая нормализация жанров: перевод кириллических жанров в английские каноны («Рок» -> «Rock») с обновлением ID3-тегов и связей в таблице `track_genres`.

---

### `POST /files/audit/reorganize`
Глобальная реорганизация медиатеки: перемещение всех файлов в канонические пути, удаление всех пустых папок снизу вверх и полное пересканирование SQLite.

- **Ответ `200 OK`:**
```json
{
  "success": true,
  "total_tracks": 1420,
  "moved_tracks": 5,
  "total_albums": 1,
  "errors": []
}
```

---

### `GET /files/audit/incomplete-albums`
Список неполных релизов (альбомов, в которых присутствуют не все треки или есть разрывы в нумерации).

- **Ответ `200 OK`:**
```json
{
  "albums": [
    {
      "path": "Discovery - Daft Punk",
      "album": "Discovery",
      "artist": "Daft Punk",
      "year": 2001,
      "has_cover": true,
      "present_count": 13,
      "expected_count": 14,
      "missing_track_numbers": [14],
      "is_catalog_matched": true,
      "deezer_album_id": "302127"
    }
  ],
  "total_incomplete_albums": 1,
  "total_missing_tracks": 0
}
```

---

### `GET /files/audit/incomplete-albums/catalog-match`
Онлайн-сверка локального альбома с официальным каталогом Deezer: попарное сопоставление треков по позиции, названию и длительности, определение отсутствующих номеров дорожек и прямых ссылок для дозагрузки.

- **Query-параметры:**
  - `path` (*string*, обязательный) — относительный путь к папке альбома.
  - `deezer_album_id` (*string*, опционально) — ID альбома в Deezer (если известен).
- **Ответ `200 OK`:**
```json
{
  "path": "Discovery - Daft Punk",
  "album": "Discovery",
  "artist": "Daft Punk",
  "year": 2001,
  "has_cover": true,
  "cover_url": "https://...",
  "present_count": 13,
  "expected_count": 14,
  "missing_track_numbers": [14],
  "missing_tracks": [
    {
      "track_number": 14,
      "disc_number": 1,
      "title": "Too Long",
      "artist": "Daft Punk",
      "duration": 600,
      "deezer_id": "3135564",
      "deezer_url": "https://www.deezer.com/track/3135564",
      "is_present": false,
      "local_path": ""
    }
  ],
  "all_tracks": [ /* все 14 треков */ ],
  "is_catalog_matched": true,
  "deezer_album_id": "302127"
}
```

---

### `GET /files/audit/incomplete-albums/editor-data` (алиас: `.../backfill-editor-data`)
Сборка данных для модального редактора тегов при бэкфилле: объединение локальных файлов альбома и докачанных дорожек из временной папки `tmp_downloads/{job_id}/converted`.

- **Query-параметры:**
  - `path` (*string*, обязательный) — путь к папке альбома.
  - `job_id` (*string*, опционально) — ID завершенной задачи бэкфилла.

---

### `POST /files/audit/incomplete-albums/download-missing`
Создание задачи дозагрузки (Backfill Job) недостающих треков неполного альбома.

- **Тело запроса (JSON):**
```json
{
  "path": "Discovery - Daft Punk",
  "deezer_album_id": "302127",
  "track_urls": [
    "https://www.deezer.com/track/3135564"
  ],
  "track_numbers": [14]
}
```
- **Ответ `200 OK`:** Массив созданных задач `DownloadJob`.

---

### `GET /files/audit/tag-issues`
Анализ тегов медиатеки и классификация обнаруженных дефектов:
- `dirty_title` — клиповый мусор, слова `[Official Video]`, `(Clip)`, `HQ`.
- `track_num_in_title` — номер трека продублирован в названии (`01. Song Name`).
- `literal_backslash_artist` — литеральный слэш `\\` в поле исполнителя.
- `label_as_artist` — служебный Topic-канал или лейбл указан вместо артиста.
- `unparsed_feat` — соавторы `feat.` указаны в названии, но отсутствуют в исполнителях.
- `artist_in_title` — артист продублирован в названии (`Artist - Title`).
- `missing_year` — не указан год релиза.
- `missing_album` — отсутствует название альбома.

Каждому треку присваивается уровень серьезности `severity`:
- `safe_autofix` — 100% безопасное локальное исправление без двусмысленностей.
- `review_needed` — требует подтверждения пользователя или сверки с каталогом.
- `info` — информационное предупреждение (например, отсутствие года).

- **Query-параметры:**
  - `filter` (*string*, опционально: `all`, `safe`, `review`, `missing_tags`, конкретное имя дефекта).
- **Ответ `200 OK`:**
```json
{
  "issues": [
    {
      "id": 42,
      "file_path": "Discovery - Daft Punk/01. One More Time (Official Video).mp3",
      "title": "01. One More Time (Official Video)",
      "artist": "Daft Punk\\Romanthony",
      "album": "Discovery",
      "year": 2001,
      "track_number": 1,
      "duration": 320,
      "issues": [
        "track_num_in_title",
        "dirty_title",
        "literal_backslash_artist"
      ],
      "severity": "safe_autofix",
      "proposed": {
        "title": "One More Time",
        "artist": "Daft Punk / Romanthony",
        "artists": ["Daft Punk", "Romanthony"],
        "album": "Discovery",
        "album_artist": "Daft Punk",
        "year": 2001
      }
    }
  ],
  "total_count": 1
}
```

---

### `POST /files/audit/tag-issues/fix`
Ручное применение исправленных тегов к одному треку с дообогащением соавторов через FeatEnricher (Яндекс Музыка + YouTube Music).

- **Тело запроса (JSON):**
```json
{
  "path": "Discovery - Daft Punk/01. One More Time (Official Video).mp3",
  "title": "One More Time",
  "artist": "Daft Punk",
  "album": "Discovery",
  "year": 2001
}
```
- **Ответ `200 OK`:**
```json
{
  "success": true,
  "path": "Discovery - Daft Punk/01. One More Time (Official Video).mp3",
  "tags": { /* обновленные теги */ }
}
```

---

### `POST /files/audit/tag-issues/fix-safe`
Высокоскоростное детерминированное автоисправление всех дефектов категории `safe_autofix` (очистка названий, удаление номеров треков из имени, исправление слэшей Navidrome). Выполняется мгновенно без внешних сетевых запросов.

- **Ответ `200 OK`:**
```json
{
  "success": true,
  "fixed_count": 12,
  "total_found": 12,
  "errors": []
}
```

---

### `POST /files/audit/tag-issues/fix-selected`
Пакетное исправление выбранных пользователем треков (по чекбоксам в таблице UI).

- **Тело запроса (JSON):**
```json
{
  "items": [
    {
      "path": "...",
      "title": "...",
      "artist": "...",
      "album": "...",
      "year": 2021
    }
  ]
}
```
- **Ответ `200 OK`:**
```json
{
  "success": true,
  "fixed_count": 5,
  "total_found": 5,
  "errors": []
}
```

---

### `POST /files/audit/tag-issues/fix-all`
Пакетное автоисправление всех треков медиатеки, для которых сформировано готовое исправление.

- **Ответ `200 OK`:**
```json
{
  "success": true,
  "fixed_count": 18,
  "total_found": 18,
  "errors": []
}
```

---

### `GET /files/audit/tag-issues/catalog-match`
Параллельный поиск трека по 4 онлайн-каталогам (Яндекс Музыка, YouTube Music, Deezer, iTunes) с расчетом композитного скора соответствия `match_score` (0–100%) на основе похожести названий (45%), совпадения артистов (35%) и длительности (20%). Используется для модального окна интерактивной Diff-сверки тегов.

- **Query-параметры:**
  - `path` (*string*, опционально) — путь к треку.
  - `title` (*string*, опционально) — название трека.
  - `artist` (*string*, опционально) — исполнитель.
- **Ответ `200 OK`:**
```json
{
  "current": {
    "path": "Discovery - Daft Punk/01. One More Time.mp3",
    "title": "01. One More Time (Clip)",
    "artist": "Daft Punk",
    "album": "Discovery",
    "year": 2001,
    "track_number": 1,
    "duration": 320
  },
  "matches": [
    {
      "source": "deezer",
      "title": "One More Time",
      "artist": "Daft Punk",
      "artists": ["Daft Punk"],
      "album": "Discovery",
      "year": 2001,
      "track_number": 1,
      "duration": 320,
      "cover_url": "https://...",
      "is_best_feat": true,
      "match_score": 98
    },
    {
      "source": "yandex",
      "title": "One More Time",
      "artist": "Daft Punk",
      "artists": ["Daft Punk"],
      "album": "Discovery",
      "year": 2001,
      "track_number": 1,
      "duration": 320,
      "cover_url": "https://...",
      "is_best_feat": false,
      "match_score": 98
    }
  ]
}
```

---

### `POST /files/audit/tag-issues/apply-catalog`
Применение выбранного варианта из каталога к аудиофайлу: строгая запись ID3v2.4 UTF-8 тегов, синхронизация `AlbumArtist`, автоматическое скачивание обложки в максимальном качестве, вшивание во фрейм `APIC` и сохранение `cover.jpg` в папку релиза.

- **Тело запроса (JSON):**
```json
{
  "path": "Discovery - Daft Punk/01. One More Time.mp3",
  "title": "One More Time",
  "artist": "Daft Punk",
  "album": "Discovery",
  "year": 2001,
  "track_number": 1,
  "cover_url": "https://e-cdns-images.dzcdn.net/images/cover/.../1000x1000-000000-80-0-0.jpg"
}
```
- **Ответ `200 OK`:**
```json
{
  "success": true,
  "path": "Discovery - Daft Punk/01. One More Time.mp3",
  "tags": { /* сохраненные канонические теги */ }
}
```
