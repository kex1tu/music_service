# Music Downloader & Tag Manager

Высокопроизводительный self-hosted веб-сервис для загрузки музыки, каталогизации, пакетного тегирования и организации аудиоколлекции. Построен на **Go (Chi)**, **yt-dlp**, **ffmpeg**, встроенном **ID3v2.4 Engine** и базе данных **SQLite (WAL)** со встроенным легковесным веб-интерфейсом без сборки (**HTML5 + Tailwind CSS CDN + Alpine.js CDN**).

---

## 1. Ключевые возможности

- **Умная загрузка музыки:**
  - **Deezer (без ARL/DRM):** извлечение студийных метаданных (официальный треклист, исполнители, альбом, год, жанры, ISRC, обложка до 1400×1400) через REST API с fallback на iTunes Search API.
  - **Каскадный сопоставитель (SoundCloud-First Matcher):** приоритетный поиск оригинальных нецензурированных версий (Explicit) в SoundCloud с автоматическим переключением на YouTube / YouTube Music при отсутствии совпадений.
  - **YouTube / YouTube Music / SoundCloud:** прямая загрузка треков, альбомов и плейлистов через `yt-dlp`.
  - **Яндекс Музыка / Spotify / Apple Music:** импорт треклистов, метаданных и обложек ультравысокого качества.
  - **Персистентная очередь загрузок в SQLite:** восстановление состояния задач при перезапуске сервера, механизм повтора (`Retry`) и детерминированная сортировка очередей.
- **Эталонный аудио-пайплайн:**
  - Асинхронное транскодирование в MP3 320 kbps CBR, 44.1 kHz через системный `ffmpeg` (`libmp3lame`).
  - Построчный стриминг прогресса `yt-dlp` с защитой от скачков шкалы (монотонный прогресс).
- **Нативный ID3v2.4 Engine на Go:**
  - Строгая запись тегов по стандарту ID3v2.4 UTF-8 без внешних C/Python-библиотек.
  - Множественные исполнители и жанры сохраняются через разделитель `\x00` (null-byte) внутри фреймов (`TPE1`, `TCON`), что нативно распознается медиасерверами (Navidrome, Plex, Jellyfin, Subsonic) и плеерами (Foobar2000, AIMP, Poweramp).
  - Встраивание фронтальной обложки высокого разрешения (`APIC`, `Cover Front`).
  - Поддержка мультидисковых релизов (`TPOS` / `TRCK`).
- **Синхронизированные и текстовые тексты песен (Lyrics):**
  - Автоматический поиск синхронизированных `.lrc` через **LRCLIB API** и пакетный поиск для альбомов.
  - Резервный поиск несинхронизированных `.txt` через **Genius API**.
  - Прямая вставка текстов из буфера обмена с автодетекцией таймкодов (`LRC` / `TXT`).
  - **Инвариант Zero ID3 Lyrics:** тексты сохраняются строго отдельными файлами рядом с MP3 и никогда не вшиваются в теги.
- **Акустическое распознавание и автопоиск:**
  - **Распознавание по звуку (Shazam):** идентификация неизвестных аудиофайлов по акустическому отпечатку (`songrec`) с извлечением названия, исполнителя, альбома, года, жанра и HD-обложки (1000×1000 px).
  - **Галерея обложек:** мульти-источниковый поиск обложек ультравысокого разрешения (Deezer, Apple Music, Яндекс Музыка, Spotify).
- **Файловый менеджер, органайзер и аудит:**
  - Каноническое размещение альбомов и синглов:
    ```
    MUSIC_DIR/{Album} - {Album Artist}/{TrackNumber:02d}. {Title}.mp3
    MUSIC_DIR/{Album} - {Album Artist}/{TrackNumber:02d}. {Title}.lrc
    MUSIC_DIR/{Album} - {Album Artist}/cover.jpg
    MUSIC_DIR/{Album} - {Album Artist}/{TrackNumber:02d}. {Title}.jpg
    ```
  - Автоматическое дублирование обложки по имени трека (`{TrackBaseName}.jpg`) и извлечение встроенного ID3 APIC при отсутствии файла обложки на диске.
  - Полнотекстовый поиск SQLite WAL (`Ctrl+K`), аудит дубликатов и неканонических путей с автоисправлением.
  - **Backfill Pipeline:** онлайн-сверка неполных альбомов с официальным каталогом Deezer и дозагрузка недостающих дорожек в один клик.
- **Безопасность (Path Sandbox Guard):**
  - Все операции с файлами строго изолированы через `PathSandbox.Resolve()`.
  - Защита от Path Traversal (`../../`), symlink bypass, null-byte инъекций и запрещенных символов ФС.
- **No-Build Frontend:**
  - SPA на чистом HTML5 + Tailwind CSS CDN + Alpine.js CDN (zero `package.json`, zero Node.js/npm).
  - Единый статический бинарник со встроенными шаблонами и статикой через `go:embed`.

---

## 2. Архитектурные инварианты

1. **Pure Go / Zero-Python / Zero-Node.js:** Сервис компилируется в единый статический бинарный файл `bin/music-manager`. Никаких runtime-зависимостей от Python, Node.js, `npm`/`yarn`, Vite/Webpack.
2. **Обязательная песочница:** Любое взаимодействие с файловой системой проходит через `PathSandbox.Resolve()`. Прямой доступ по клиентским путям запрещен.
3. **No Docker:** Сервис ориентирован на запуск на хосте / VPS под управлением `systemd` (`music-manager.service`).
4. **Аудио-стандарт:** Фиксированный битрейт MP3 320 kbps CBR, 44.1 kHz. Тексты песен — строго во внешних файлах `.lrc`/`.txt`.

---

## 3. Системные требования

- **ОС:** Linux (Ubuntu 22.04 / 24.04 LTS, Debian 12, Arch Linux и др.)
- **Go:** 1.22 или новее
- **Системные утилиты:**
  - `ffmpeg` версии 6.x+ (с поддержкой кодека `libmp3lame`)
  - `yt-dlp` (рекомендуется актуальная версия)
  - `songrec` (опционально, для акустического распознавания Shazam)

Установка системных зависимостей в Ubuntu / Debian:
```bash
sudo apt update
sudo apt install -y golang ffmpeg yt-dlp
```

---

## 4. Быстрый старт (локальная разработка)

### 4.1 Сборка проекта

```bash
# Клонирование репозитория
git clone https://github.com/your-username/music-manager.git /opt/music-manager
cd /opt/music-manager

# Компиляция статического бинарника
go build -ldflags="-s -w" -o bin/music-manager cmd/server/main.go
```

### 4.2 Конфигурация `.env`

Скопируйте шаблон конфигурации:
```bash
cp .env.example .env
```

Отредактируйте параметры в `.env`:
- `MUSIC_DIR` — абсолютный путь к каталогу вашей медиатеки (например, `/var/lib/music` или `/home/user/Music`).
- `SECRET_KEY` — случайная строка длиной 32+ символов (генерация: `openssl rand -hex 32`).
- `ADMIN_LOGIN` — логин администратора (по умолчанию `admin`).
- `ADMIN_PASSWORD_HASH` — bcrypt-хэш пароля (генерация: `htpasswd -nbBC 12 "" "ВАШ_ПАРОЛЬ"` или через утилиту `mkpasswd`).
- `DATABASE_PATH` — путь к SQLite БД (по умолчанию `data/library.db`).

### 4.3 Запуск сервера разработки

```bash
./bin/music-manager
```
Сервис будет доступен по адресу: `http://127.0.0.1:8010` (или порту из `PORT` в `.env`).

### 4.4 Запуск тестов

```bash
# Запуск всех тестов проекта
go test -v ./...

# Запуск тестов с детектором состояния гонок
go test -race ./...
```

---

## 5. Развертывание в продакшене (systemd)

### 5.1 Создание пользователя и директорий

```bash
sudo useradd -r -s /bin/false musicmgr
sudo mkdir -p /opt/music-manager /var/lib/music /var/log/music-manager
sudo chown -R musicmgr:musicmgr /opt/music-manager /var/lib/music /var/log/music-manager
```

### 5.2 Настройка systemd unit-файла

Скопируйте `music-manager.service` в `/etc/systemd/system/`:

```ini
[Unit]
Description=Music Downloader & Tag Manager Service (Pure Go)
After=network.target network-online.target
Wants=network-online.target

[Service]
Type=simple
User=musicmgr
Group=musicmgr
WorkingDirectory=/opt/music-manager
ExecStart=/opt/music-manager/bin/music-manager
Restart=always
RestartSec=5s

# Переменные окружения
EnvironmentFile=/opt/music-manager/.env

# Ограничения безопасности
NoNewPrivileges=true
ProtectSystem=full
ProtectHome=read-only
ReadWritePaths=/opt/music-manager /var/lib/music /var/log/music-manager
PrivateTmp=true

# Лимиты ресурсов
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
```

### 5.3 Активация и запуск службы

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now music-manager.service
sudo systemctl status music-manager.service
```

---

## 6. Структура проекта

```
music-manager/
├── bin/                        # Скомпилированный бинарник (music-manager)
├── cmd/
│   └── server/main.go          # Точка входа веб-сервера
├── internal/
│   ├── api/                    # Chi роутер, middleware, схемы
│   │   └── handlers/           # 18 модульных HTTP-хендлеров (файлы, теги, загрузки, аудит)
│   ├── config/                 # Загрузка и валидация конфигурации .env
│   ├── core/
│   │   ├── auth/               # Bcrypt авторизация и HMAC-SHA256 сессии
│   │   └── sandbox/            # PathSandbox — изоляция путей и защита от Path Traversal
│   ├── db/                     # SQLite WAL драйвер, миграции и SQL-запросы
│   ├── id3/                    # Чистый Go ID3v2.4 парсер и генератор тегов
│   └── services/
│       ├── downloader/         # Пайплайн yt-dlp/ffmpeg, очередь и управление задачами
│       ├── lyrics/             # Клиенты LRCLIB и Genius API
│       ├── organizer/          # Перемещение файлов, обложек, текстов и очистка пустых папок
│       ├── resolver/           # SoundCloud-First Matcher, Deezer API, iTunes Search
│       └── scanner/            # Индексация медиатеки, fsnotify воркер, чекер неполных альбомов
├── web/
│   ├── static/                 # No-build JS компоненты и CSS стили
│   │   ├── css/style.css
│   │   └── js/                 # Модули (downloader, tag_editor, store, audit, lyrics)
│   ├── templates/              # Модульные HTML шаблоны компонентов
│   └── embed.go                # Встраивание статики в бинарник через go:embed
├── .env.example                # Шаблон конфигурации
├── Architecture.md             # Детальная архитектурная спецификация
├── AGENTS.md                   # Инструкции и инварианты для AI-агентов
├── PROGRESS.md                 # Журнал и матрица статусов реализации
├── plan.md                     # Текущий архитектурный план
├── API.md                      # Полная спецификация и документация REST API
└── docs/
    └── openapi.yaml            # OpenAPI 3.0.3 схема для Swagger UI / Postman
```

---

## 7. Документация REST API

Сервис предоставляет полнофункциональный REST API с поддержкой RFC 7233 HTTP Range (аудио-стриминг с перемоткой), HMAC-SHA256 сессионной авторизацией и защитой файловой системы через `PathSandbox`.

- **Полное руководство по всем эндпоинтам:** [`API.md`](API.md)
- **Спецификация OpenAPI 3.0:** [`docs/openapi.yaml`](docs/openapi.yaml) (импортируется в Postman, Insomnia, Swagger UI)

---

## 8. Лицензия

MIT License.
