# PROGRESS.md — Матрица статусов реализации Music Downloader & Tag Manager

## Текущий статус проекта
- **Общий прогресс:** 3 / 10 компонентов завершены (30%)
- **Активный этап:** Блок 1 (Ядро и авторизация) завершен на 100%. Следующий: Блок 2 (Сервис загрузки)
- **Результаты тестов:** 78/78 passed (`pytest tests/ -v`), ruff linter — 0 замечаний.

---

## Блок 1: Ядро и авторизация

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **3.1 Core / Bootstrap** | `app/main.py`, `app/config.py`, `app/core/exceptions.py`, `app/dependencies.py` | **DONE** | Фабрика `create_app`, `Settings` (pydantic-settings), fail-fast lifespan с проверкой записи `MUSIC_DIR` и `TMP_DIR`, `SessionMiddleware` (`SameSite=Strict`, `httpOnly=True`), глобальные exception handlers (доменные исключения транслируются в HTTP 400/403/422/500), `GET /api/health`, модульные тесты в `tests/`. |
| **3.2 Auth Module** | `app/core/security.py`, `app/services/auth_service.py`, `app/routers/auth.py`, `app/schemas/auth.py`, `app/dependencies.py` | **DONE** | `bcrypt`-хеширование с защитой от тайминг-атак (`DUMMY_BCRYPT_HASH` + `secrets.compare_digest`), генерация и валидация токенов сессий через `itsdangerous.URLSafeTimedSerializer`, безопасная установка cookie (`httpOnly=True`, `SameSite=Strict`), внедрение зависимости `get_current_user` (с поддержкой cookie и `Authorization: Bearer`), маршруты `POST /login`, `POST /logout`, `GET /api/auth/me`, полное покрытие тестами (`tests/test_security.py`, `tests/test_auth_service.py`, `tests/test_auth_router.py`). |
| **3.3 Path Sandbox Guard** | `app/core/sandbox.py`, `app/dependencies.py`, `tests/test_sandbox.py` | **DONE** | Класс `PathSandbox`, методы `resolve()`, `resolve_child()`, `to_relative()`, `is_safe()`, `validate_filename()`. Предотвращение path traversal (`../../`, leading slash trap, null-byte injections `\x00`/`%00`, однократное и двойное URL-кодирование `%252e%252e%252f`, symlink bypass наружу песочницы). Зависимости FastAPI `get_music_sandbox`, `get_tmp_sandbox`. 41 новый тест в `tests/test_sandbox.py`. |


---

## Блок 2: Сервис загрузки

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **3.4 Downloader Engine** | `app/services/source_resolver.py`, `app/services/deezer_client.py`, `app/services/download_service.py`, `app/routers/downloads.py` | TODO | Интеграция с Deezer API (поиск метаданных), поиск аудиопотока через yt-dlp, конвертация ffmpeg в MP3 320kbps CBR, изолированный буфер `tmp_downloads/{job_id}/`. |

---

## Блок 3: Метаданные, тексты и файловая организация

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **3.5 Metadata / Tag Service** | `app/services/tag_service.py`, `app/routers/tags.py` | TODO | mutagen ID3v2.4, множественные теги через `\x00`, обложки APIC (type=3), двухуровневая модель (Album -> Track). |
| **3.6 Lyrics Service** | `app/services/lyrics_service.py`, `app/routers/lyrics.py` | TODO | Поиск текстов LRCLIB (синхронизированные `.lrc`) и Genius (fallback `.txt`), сохранение внешними файлами рядом с MP3. |
| **3.7 File Manager Service** | `app/services/file_manager_service.py`, `app/routers/files.py` | TODO | Навигация по `MUSIC_DIR` через `PathSandbox`, динамическое построение дерева каталогов без БД. |
| **3.8 Organizer Service** | `app/services/organizer_service.py` | TODO | Перенос файлов по шаблону `MUSIC_DIR/{AlbumArtist}/{Album} - {Year}/{TrackNumber:02d}. {Title}.mp3`, санитизация имен, удаление пустых папок. |

---

## Блок 4: Пользовательский интерфейс (Frontend no-build)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **4.1-4.6 UI & Modals** | `app/templates/index.html`, `app/static/js/` | TODO | Tailwind CDN + Alpine.js CDN, Downloader Tab, File Manager Tree, двухуровневый Tag Editor Modal, Lyrics Modal, Alpine.store('app'). |

---

## Блок 5: Деплоймент и системная интеграция

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **7 Deploy** | `music-manager.service`, `requirements.txt`, `.env.example` | IN_PROGRESS | `requirements.txt` и `.env.example` сформированы. Настройка unit-файла systemd для bare-metal запуска. |
