# Music Downloader & Tag Manager

Self-hosted веб-сервис для загрузки аудио, каталогизации и редактирования метаданных музыкальной коллекции. Построен на **FastAPI**, **yt-dlp**, **ffmpeg** и **mutagen**, с легковесным интерфейсом без сборки (**HTML5 + Tailwind CSS CDN + Alpine.js CDN**).

---

## 1. Ключевые возможности

- **Умная загрузка музыки:**
  - **Deezer (без ARL/DRM):** извлечение студийных метаданных (официальный треклист, исполнители, альбом, год, ISRC, обложка в разрешении до 1400×1400) через публичный REST API с интеллектуальным подбором наилучшего аудиопотока в YouTube / YouTube Music (`AudioMatcher` на базе RapidFuzz и проверки длительности $\pm 2$ сек).
  - **YouTube / YouTube Music / SoundCloud:** прямая загрузка аудиопотоков через `yt-dlp`.
- **Эталонный аудио-пайплайн:**
  - Асинхронное транскодирование в эталонный MP3 320 kbps CBR, 44.1 kHz через системный `ffmpeg` (`libmp3lame`).
- **Строгие ID3v2.4 теги:**
  - Нативная поддержка ID3v2.4 через `mutagen`.
  - Множественные исполнители и жанры сохраняются через разделитель `\x00` (null-byte) внутри фреймов (`TPE1`, `TPE2`, `TCON`), что корректно распознается современными плеерами (Navidrome, Plex, Foobar2000, AIMP).
  - Поддержка ввода множественных значений в UI через `\\`.
  - Встраивание фронтальной обложки высокого разрешения (`APIC`, `PictureType.COVER_FRONT`).
- **Синхронизированные и текстовые тексты песен (Lyrics):**
  - Автоматический поиск синхронизированных `.lrc` через **LRCLIB API** и пакетный поиск для всего альбома.
  - Fallback на несинхронизированные `.txt` через **Genius API**.
  - Прямая вставка текстов из буфера обмена с автоопределением таймкодов и переключением формата `LRC`/`TXT`.
  - Инвариант **Zero ID3 Lyrics**: тексты никогда не вшиваются внутрь MP3, а сохраняются строго отдельными файлами рядом с треком.
- **Акустическое распознавание и автопоиск метаданных:**
  - **Распознавание по звуку (Shazam):** идентификация неизвестных треков по аудиосигнатуре с извлечением названия, исполнителя, альбома, года, жанра и HD-обложки (1000×1000 px).
  - **Каталожный поиск релизов (Deezer):** поиск официальных альбомов по артисту или названию с предзаполнением полного треклиста и обложки.
- **Файловый менеджер и автоматический органайзер:**
  - Динамическое построение дерева медиатеки прямо из файловой структуры `MUSIC_DIR` на лету (Zero-Database).
  - Автоматическое размещение альбомов по стандартному шаблону:
    ```
    MUSIC_DIR/{AlbumArtist}/{Album} - {Year}/{TrackNumber:02d}. {Title}.mp3
    MUSIC_DIR/{AlbumArtist}/{Album} - {Year}/{TrackNumber:02d}. {Title}.lrc
    MUSIC_DIR/{AlbumArtist}/{Album} - {Year}/cover.jpg
    ```
  - Двухфазное безопасное перемещение с транзакционным откатом при смене порядка треков, исключающее коллизии и потерю файлов.
  - Автоматическая очистка опустевших папок при перемещении или удалении альбомов.
- **Безопасность (Path Sandbox Guard):**
  - Все операции чтения, записи, перемещения и удаления строго изолированы через `PathSandbox`.
  - Полная защита от Path Traversal (`../../`), symlink bypass, null-byte injection и опасных символов файловой системы.
- **No-Build Frontend:**
  - SPA-интерфейс на чистом HTML5 + Tailwind CDN + Alpine.js CDN.
  - Никаких `package.json`, Node.js, Webpack/Vite. Быстрая загрузка и минимальное потребление ресурсов.

---

## 2. Архитектурные инварианты

1. **Zero-Database:** В проекте нет СУБД (PostgreSQL, SQLite, Redis, MongoDB). Единственный источник истины — файловая система `MUSIC_DIR` и ID3-теги внутри самих MP3-файлов.
2. **Обязательная песочница:** Любое взаимодействие с файлами проходит через `PathSandbox.resolve()`. Прямой доступ по клиентским путям запрещен.
3. **Zero-Node.js:** Фронтенд работает без этапа сборки.
4. **No Docker:** Деплой ориентирован на виртуальное окружение Python (`venv`) и службу `systemd`.
5. **Аудио-стандарт:** Фиксированный битрейт MP3 320 kbps CBR, 44.1 kHz. Тексты песен — только во внешних файлах `.lrc`/`.txt`.

---

## 3. Системные требования

- **ОС:** Linux (Ubuntu 22.04 / 24.04 LTS, Debian 12, Arch Linux и др.)
- **Python:** 3.11 или 3.12
- **Системные утилиты:** `ffmpeg` версии 6.x или новее с кодеком `libmp3lame`

Установка системных зависимостей в Ubuntu/Debian:
```bash
sudo apt update
sudo apt install -y python3 python3-venv python3-pip ffmpeg
```

---

## 4. Быстрый старт (локальная разработка)

### 4.1 Клонирование и настройка окружения

```bash
cd /opt/music-manager  # или ваша рабочая директория
python3 -m venv venv
./venv/bin/pip install --upgrade pip
./venv/bin/pip install -r requirements.txt
```

### 4.2 Конфигурация `.env`

Скопируйте шаблон переменных окружения:
```bash
cp .env.example .env
```

Отредактируйте необходимые параметры в `.env`:
- `MUSIC_DIR` — абсолютный путь к вашей постоянной медиатеке.
- `SECRET_KEY` — случайная строка длиной 32+ символов (генерация: `python3 -c "import secrets; print(secrets.token_hex(32))"`).
- `ADMIN_LOGIN` — логин администратора (по умолчанию `admin`).
- `ADMIN_PASSWORD_HASH` — bcrypt-хэш пароля (генерация: `python3 -c "import bcrypt; print(bcrypt.hashpw(b'YOUR_PASSWORD', bcrypt.gensalt()).decode())"`).
- `GENIUS_API_TOKEN` — (опционально) токен Genius API для резервного поиска текстов.

### 4.3 Запуск сервера разработки

```bash
./venv/bin/uvicorn app.main:app --host 127.0.0.1 --port 8010 --reload
```

После запуска сервис доступен по адресу: **http://127.0.0.1:8010**

---

## 5. Продакшн деплоймент (systemd)

Для автономного запуска на сервере под управлением systemd:

### 5.1 Создание системного пользователя и каталогов

```bash
# Создание сервисного пользователя
sudo useradd -r -s /usr/sbin/nologin -d /opt/music-manager music

# Создание каталогов медиатеки и буфера
sudo mkdir -p /var/lib/music-manager/library
sudo mkdir -p /opt/music-manager/tmp_downloads

# Настройка владельца и прав
sudo chown -R music:music /opt/music-manager
sudo chown -R music:music /var/lib/music-manager/library
sudo chmod -R 750 /opt/music-manager
sudo chmod -R 750 /var/lib/music-manager/library
```

### 5.2 Установка systemd unit

Скопируйте unit-файл в системный каталог:
```bash
sudo cp music-manager.service /etc/systemd/system/music-manager.service
sudo systemctl daemon-reload
```

### 5.3 Запуск и автозагрузка

```bash
sudo systemctl enable --now music-manager
```

Проверка статуса и просмотр логов:
```bash
sudo systemctl status music-manager
sudo journalctl -u music-manager -f
```

---

## 6. Тестирование и проверка качества

В репозитории реализован полный набор модульных и интеграционных тестов (415 тестов), покрывающий все сценарии ядра, сервисов, изоляции путей и API-маршрутов:

```bash
# Запуск тестов
./venv/bin/python -m pytest tests/ -v

# Проверка статическим анализатором ruff
./venv/bin/ruff check app tests
```

---

## 7. Структура API

Все API-эндпоинты (кроме `/login`, `/api/health` и статики) защищены сессионной авторизацией (`get_current_user`):

| Маршрут | Метод | Описание |
|---|---|---|
| `POST /login` | POST | Вход администратора, установка защищенного session-cookie |
| `POST /logout` | POST | Сброс текущей сессии |
| `GET /api/auth/me` | GET | Проверка статуса авторизации текущего пользователя |
| `GET /api/health` | GET | Health-check сервиса |
| `POST /downloads` | POST | Запуск фоновой задачи загрузки (URL альбома/трека/плейлиста) |
| `GET /downloads/{job_id}` | GET | Polling статуса и прогресса загрузки/конвертации |
| `GET /downloads/{job_id}/tracks` | GET | Получение черновых метаданных скачанных треков |
| `DELETE /downloads/{job_id}` | DELETE | Отмена задачи и очистка временного каталога |
| `GET /tags?path=` | GET | Чтение ID3v2.4 тегов трека или альбома |
| `GET /tags/cover?path=` | GET | Получение бинарных данных обложки трека/альбома |
| `PUT /tags/batch` | PUT | Пакетное сохранение тегов + перемещение/реорганизация в библиотеку |
| `POST /tags/recognize-audio` | POST | Акустическое распознавание аудиофайла через Shazam API |
| `GET /tags/deezer/albums` | GET | Поиск альбомов и релизов в каталоге Deezer |
| `GET /tags/deezer/albums/{album_id}` | GET | Детальный треклист и метаданные альбома из Deezer |
| `GET /lyrics/search` | GET | Поиск текстов песен (LRCLIB + Genius fallback) |
| `GET /lyrics?path=` | GET | Чтение файла текста песни для трека |
| `POST /lyrics` | POST | Сохранение внешнего `.lrc` / `.txt` файла для трека |
| `DELETE /lyrics?path=` | DELETE | Удаление внешнего файла текста |
| `GET /files?path=` | GET | Листинг каталога медиатеки с классификацией файлов |
| `GET /files/tracks?path=` | GET | Получение списка треков альбома для предзаполнения |
| `DELETE /files?path=` | DELETE | Безопасное удаление трека или папки альбома |

---

## 8. Лицензия

Проект предназначен для частного домашнего использования (self-hosted). Распространяется на условиях свободной лицензии MIT.
