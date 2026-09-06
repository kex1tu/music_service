# AGENTS.md — Music Downloader & Tag Manager

Правила и границы ответственности для агентов (Claude Code / других coding-агентов), работающих над этим репозиторием. Файл читается перед началом любой задачи.

---

## 1. О проекте

Self-hosted веб-сервис на **FastAPI + yt-dlp + ffmpeg + mutagen**, фронтенд без сборки (**HTML5 + Tailwind CDN + Alpine.js CDN**). Без реляционных/документных СУБД — источник истины — файловая система `MUSIC_DIR` и ID3-теги в самих файлах.

Подробная архитектура компонентов — в `Architecture.md`. Текущий план работ — в `plan.md`. Статус задач — в `PROGRESS.md`. Этот файл (`AGENTS.md`) не дублирует их содержимое, а фиксирует жёсткие правила и границы.

---

## 2. Технологические инварианты (без исключений)

1. **Zero-Database.** Никаких PostgreSQL/SQLite/Redis/MongoDB. Каталог строится из `MUSIC_DIR` и ID3-тегов на лету.
2. **Sandbox обязателен.** Любое чтение/запись/удаление/переименование файла — только через `PathSandbox.resolve()`. Прямая работа с путём от клиента запрещена.
3. **Zero-Node.js / no-build frontend.** Никаких `package.json`, `npm`/`yarn`, Vite/Webpack. Только CDN-версии Tailwind и Alpine.js.
4. **No Docker.** Деплой — `venv` + `systemd` (`music-manager.service`). Dockerfile/docker-compose создавать нельзя.
5. **Аудио-пайплайн фиксирован.** Скачивание — `yt-dlp`; конвертация — `ffmpeg` в MP3 320 kbps CBR / 44.1 kHz; теги — строго ID3v2.4 через `mutagen`, множественные значения — `\x00` внутри фрейма, `\\` — только формат пользовательского ввода на фронте.
6. **Тексты песен не вшиваются в MP3** — только внешние `.lrc`/`.txt` рядом с треком.

Любое расхождение кода с этими пунктами — блокирующая ошибка, а не предмет для обсуждения в рамках отдельной задачи.

---

## 3. Зоны ответственности по областям кода

Перед изменением файла проверь, к какой области он относится, и не выходи за её рамки без явной необходимости, отражённой в `plan.md`.

| Область | Пути | Что входит | Что запрещено трогать |
|---|---|---|---|
| **Core / Auth / Sandbox** | `app/core/`, `app/config.py`, `app/dependencies.py`, `app/main.py`, `app/schemas/auth.py` | Конфигурация, bcrypt-аутентификация, `PathSandbox`, DI, обработка исключений | Бизнес-логика загрузки/тегов, фронтенд |
| **Downloader Engine** | `app/services/download_service.py`, `app/services/deezer_client.py`, `app/services/source_resolver.py`, `app/routers/downloads.py` | yt-dlp/ffmpeg-пайплайн, поиск источника, интеграция с Deezer API | Прямая запись в `MUSIC_DIR` — только через `tmp_downloads/{job_id}/` |
| **Metadata & Storage** | `app/services/tag_service.py`, `app/services/lyrics_service.py`, `app/services/file_manager_service.py`, `app/services/organizer_service.py`, `app/routers/tags.py`, `app/routers/lyrics.py`, `app/routers/files.py` | mutagen, LRCLIB/Genius, навигация по ФС, перемещение/переименование файлов | Любой вызов `open/os.remove/shutil.move` в обход `PathSandbox` |
| **Frontend** | `app/templates/`, `app/static/js/`, `app/static/css/` | Downloader/File Manager tabs, Tag Editor, Lyrics modal, `Alpine.store('app', …)` | Python-код, добавление Node.js-зависимостей |
| **Tests** | `tests/` | Unit/integration тесты, моки внешних API | Правки бизнес-логики в `app/` ради «зелёных» тестов — вместо этого баг-репорт в `PROGRESS.md` |
| **DevOps / Deploy** | `music-manager.service`, `requirements.txt`, `.env.example` | systemd unit, зависимости, переменные окружения | Dockerfile, хардкод пользовательских путей вида `/home/username/...` |

---

## 4. Команды разработки

```bash
# установка окружения
python3.11 -m venv venv
./venv/bin/pip install -r requirements.txt

# запуск сервера в разработке
./venv/bin/uvicorn app.main:app --reload --port 8010

# тесты
./venv/bin/python -m pytest

# линт (если добавлен в requirements)
./venv/bin/ruff check app tests
```

Все команды выполняются из корня репозитория. Не запускать сервисные/деструктивные команды за пределами рабочей директории проекта.

---

## 5. Правила по областям (кратко)

**Core / Auth / Sandbox**
- Секреты (`SECRET_KEY`, хэш пароля) — только из `.env`, никогда в коде.
- `app/main.py` — только сборка приложения (роутеры, DI, exception handlers), без бизнес-логики.
- Cookie сессии — обязательно `httpOnly`, `SameSite=Strict`.

**Downloader Engine**
- URL проверяется по whitelist доменов (youtube.com, music.youtube.com, soundcloud.com, deezer.com) до передачи в `yt-dlp`.
- `ffmpeg`/`yt-dlp` вызываются асинхронно (`asyncio.create_subprocess_exec` / `asyncio.to_thread`) — не блокировать event loop.
- Итоговые файлы остаются в `tmp_downloads/{job_id}/` до подтверждения пользователем; перенос в `MUSIC_DIR` — только через `organizer_service`.

**Metadata & Storage**
- Теги — строго ID3v2.4, множественные значения — null-separated (`\x00`), не устаревшие ID3v1/v2.3.
- Тексты песен — только `.lrc`/`.txt` рядом с треком, никогда в тег.
- Любая операция с путём — сначала `PathSandbox.resolve()`, потом файловая операция. Без исключений даже для «внутренних» вызовов.

**Frontend**
- Никаких сборщиков и `package.json`. Только `<script src="https://cdn...">`.
- Общее состояние (текущий пользователь, кэш навигации, открытый модал) — в `Alpine.store('app', …)`, не размазывать по компонентам.
- Ожидание загрузки — через polling `fetch('/downloads/{jobId}')`, не блокирующий UI.

**Tests**
- Все внешние HTTP-вызовы (Deezer, LRCLIB, Genius, YouTube) в тестах — замоканы, реальных сетевых запросов быть не должно.
- Обязательны тесты `PathSandbox` на `../../`, null-byte инъекции, обход через симлинки.

**DevOps / Deploy**
- Не хардкодить пути пользователя — только через `.env`/`Settings`.
- Не добавлять Docker/Compose ни при каких условиях.

---

## 6. Глобальные запреты

- Обход `PathSandbox.resolve()` при вызове `open`, `os.remove`, `os.unlink`, `shutil.move`, `shutil.copy`, `os.scandir`.
- Деструктивные shell-команды (`rm -rf` вне временных подкаталогов проекта, работа с блочными устройствами).
- `npm`/`yarn`/`pnpm`, любые frontend-сборщики.
- Хардкод реальных секретов, токенов, паролей, хэшей в репозитории — только плейсхолдеры в `.env.example`.
- Dockerfile / docker-compose.
- `git push --force` и коммиты без прогона тестов.

---

## 7. Перед тем как считать задачу завершённой

1. Код не выходит за границы своей области (раздел 3), либо изменение в чужой области согласовано и отражено в `plan.md`.
2. Все файловые операции с пользовательским вводом путей проходят через `PathSandbox`.
3. `./venv/bin/python -m pytest` — зелёный.
4. Новые зависимости добавлены в `requirements.txt` (для Python) — сверься с ним перед импортом незнакомой библиотеки.
5. `.env.example` обновлён при появлении новых переменных окружения (без реальных значений).
6. `PROGRESS.md` отражает фактическое состояние: если что-то не доделано или заблокировано — это явно написано, а не «замолчано».

---

## 8. Проверка перед действием (anti-hallucination)

- Перед импортом стороннего модуля — проверь `requirements.txt`; если пакета нет, сначала добавь его туда, а не полагайся на то, что он «наверное установлен».
- Перед изменением или ссылкой на файл — открой его и убедись, что он существует и выглядит так, как ты предполагаешь.
- Перед вызовом метода из другого сервиса (например, `TagService` из `organizer_service.py`) — открой сигнатуру метода в файле-источнике, а не угадывай по названию.

---

## 9. Эскалация

Если задача из `plan.md` противоречит инвариантам из раздела 2 или структуре `Architecture.md` — не проектировать обходной путь самостоятельно. Зафиксировать конфликт в `PROGRESS.md` (что именно противоречит чему) и остановиться на этом месте до прояснения.