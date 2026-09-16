# PROGRESS.md — Матрица статусов и дашборд реализац�- **Статус готовности:** **100% DONE** (Все 39 блоков задач полностью реализованы и протестированы).
- **Стек:** Высокопроизводительный сервис на **Pure Go (Chi)**, **yt-dlp**, **ffmpeg**, **ID3v2.4 Engine**, **SQLite (WAL)**, **No-Build Frontend (HTML5 + Tailwind CDN + Alpine.js CDN)**.
- **Размер бинарника:** 15 МБ (единый статический бинарник `bin/music-manager`, время холодного старта ~2 мс, потребление RAM ~4–24 МБ).
- **Результаты тестов:** 100% Go unit & integration tests (`go test -v ./...`, `go test -race ./...` — **0 data races**, все пакеты зеленые).
- **Продакшен-деплой:** Развернут под управлением `systemd` (`music-manager.service`) на боевых серверах `nl_vpn` (144.31.51.105) и `nl_music` (194.76.168.20).

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
5. **Строгое ID3v2.4 UTF-8 мульти-жанровое кодирование:**
   Разделение множественных исполнителей и жанров через null-байт `\x00` в фреймах `TPE1` и `TCON` (нативная поддержка Navidrome, Plex, Foobar2000, AIMP).
6. **Онлайн-сверка и бэкфилл релизов (Backfill Pipeline):**
   Автоматическая сверка неполных альбомов с официальным каталогом Deezer и пакетная дозагрузка недостающих дорожек в один клик.
7. **Декомпозиция монолитов (Go, JS и HTML):**
   Все монолитные Go-пакеты, JavaScript-модули и HTML-шаблоны (`web/templates/`) разбиты на компактные файлы по 15–250 строк (Single Responsibility Principle).

---

## 3. Сводная матрица этапов разработки

Детальные журналы изменений, списки измененных файлов и контрольные точки вынесены в специализированные файлы истории:

| Эпоха / Этап | Блоки | Статус | Документ с подробным описанием |
|---|---|---|---|
| **1. Первоначальный прототип (Python era)** | Блоки 1–20 | **DONE** | [`docs/progress/01_python_initial.md`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/docs/progress/01_python_initial.md) |
| **2. Миграция на Pure Go & SQLite WAL** | Блоки 21–30 | **DONE** | [`docs/progress/02_go_migration.md`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/docs/progress/02_go_migration.md) |
| **3. Стабилизация, оптимизация и декомпозиция** | Блоки 31–39 | **DONE** | [`docs/progress/03_go_stabilization_and_modularization.md`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/docs/progress/03_go_stabilization_and_modularization.md) |

---

### Краткий состав блоков по эпохам

#### Эпоха 1: Первоначальный прототип ([`01_python_initial.md`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/docs/progress/01_python_initial.md))
- **Блоки 1–5:** Базовое ядро, авторизация bcrypt, PathSandbox, Downloader, ID3 теги, тексты LRCLIB, файловый менеджер, systemd.
- **Блоки 6–10:** YouTube Age Gate, Deezer короткие ссылки, AudioMatcher, стриминг прогресса, Google Material Icons, DRM bypass, SoundCloud плейлисты, тексты песен.
- **Блоки 11–15:** Распознавание Shazam, Deezer Search, импорт Яндекс/Spotify/Apple Music, очистка названий, DnD/TPOS перенумерация, галерея обложек, пакетный режим, глобальный поиск `Ctrl+K`.
- **Блоки 16–20:** Группировка релизов, карточки релизов, торрент-загрузчик, стабилизация очередей.

#### Эпоха 2: Миграция на Pure Go ([`02_go_migration.md`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/docs/progress/02_go_migration.md))
- **Блоки 21–25:** Реактивность аудита, селектор дискографии, параллельное транскодирование, извлечение метаданных Deezer, бэкфилл неполных релизов.
- **Блоки 26–30:** CLI сканер `music-scanner` (NDJSON), нормализация жанров, одноуровневая структура `Альбом/Треки.mp3`, демон аудита на Go + `fsnotify` + SQLite WAL, полный вывод Python из кодовой базы, декомпозиция фронтенда на Go-шаблоны и модульные JS-файлы.

#### Эпоха 3: Стабилизация и декомпозиция Go-монолитов ([`03_go_stabilization_and_modularization.md`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/docs/progress/03_go_stabilization_and_modularization.md))
- **Блок 31 (Git Checkpoint):** Фиксация перехода на Go и деплой на боевые серверы `nl_vpn`, `nl_music`.
- **Блок 32 (Downloader Engine Stabilization):** Персистентность очереди в SQLite, SoundCloud-First Matcher, Deezer anti-403 WAF fallback на iTunes, кнопка Retry.
- **Блок 33 (YouTube PO Token & Remixes):** Обход ограничений YouTube PO Token (`--flat-playlist`), сохранение ремиксов и версий треков.
- **Блок 34 (Deterministic Queue & Progress):** Детерминированная сортировка очереди по приоритету статусов, взвешенный расчет `OverallProgress`.
- **Блок 35 (1-Level Album Layout & Singles):** Каноническая раскладка альбомов и синглов (`album = title`), независимая маршрутизация треков.
- **Блок 36 (Track Prefixes & Navidrome ID3v2.4 Multi-Genres):** Сохранение числовых префиксов артистов (`1.Kla$`), ID3v2.4 UTF-8 с null-байтами `\x00` для Navidrome.
- **Блок 37 (Track-Named Covers & Embedded APIC):** Сохранение обложек `{TrackBaseName}.jpg` для синглов и автоизвлечение встроенных APIC обложек.
- **Блок 38 (Full Monolith Decomposition):** Полная декомпозиция всех Go-пакетов (`internal/id3`, `resolver`, `downloader`, `organizer`, `scanner`, `handlers`, `tools/music-scanner`) и JS-модулей на компактные файлы по 100–300 строк.
- **Блок 39 (HTML Templates Decomposition):** Полная декомпозиция всех монолитных HTML-шаблонов (`tag_editor.html`, `audit.html`, `downloader.html`) на компактные под-шаблоны по 15–230 строк.
е, извлечение метаданных Deezer, бэкфилл неполных релизов.
- **Блоки 26–30:** CLI сканер `music-scanner` (NDJSON), нормализация жанров, одноуровневая структура `Альбом/Треки.mp3`, демон аудита на Go + `fsnotify` + SQLite WAL, полный вывод Python из кодовой базы, декомпозиция фронтенда на Go-шаблоны и модульные JS-файлы.

#### Эпоха 3: Стабилизация и декомпозиция Go-монолитов ([`03_go_stabilization_and_modularization.md`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/docs/progress/03_go_stabilization_and_modularization.md))
- **Блок 31 (Git Checkpoint):** Фиксация перехода на Go и деплой на боевые серверы `nl_vpn`, `nl_music`.
- **Блок 32 (Downloader Engine Stabilization):** Персистентность очереди в SQLite, SoundCloud-First Matcher, Deezer anti-403 WAF fallback на iTunes, кнопка Retry.
- **Блок 33 (YouTube PO Token & Remixes):** Обход ограничений YouTube PO Token (`--flat-playlist`), сохранение ремиксов и версий треков.
- **Блок 34 (Deterministic Queue & Progress):** Детерминированная сортировка очереди по приоритету статусов, взвешенный расчет `OverallProgress`.
- **Блок 35 (1-Level Album Layout & Singles):** Каноническая раскладка альбомов и синглов (`album = title`), независимая маршрутизация треков.
- **Блок 36 (Track Prefixes & Navidrome ID3v2.4 Multi-Genres):** Сохранение числовых префиксов артистов (`1.Kla$`), ID3v2.4 UTF-8 с null-байтами `\x00` для Navidrome.
- **Блок 37 (Track-Named Covers & Embedded APIC):** Сохранение обложек `{TrackBaseName}.jpg` для синглов и автоизвлечение встроенных APIC обложек.
- **Блок 38 (Full Monolith Decomposition):** Полная декомпозиция всех Go-пакетов (`internal/id3`, `resolver`, `downloader`, `organizer`, `scanner`, `handlers`, `tools/music-scanner`) и JS-модулей на компактные файлы по 100–300 строк.

---

## 4. Верификация

1. **Компиляция бинарника:**
   ```bash
   go build -ldflags="-s -w" -o bin/music-manager cmd/server/main.go
   ```
2. **Модульные и интеграционные тесты:**
   ```bash
   go test -race ./...
   ```
   **Результат:** 100% PASS, 0 race conditions.
