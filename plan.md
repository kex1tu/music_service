# Архитектурный план: Фиксация контрольной точки, недостающие Go-эндпоинты и продакшен-деплой

**Дата:** 16 сентября 2026  
**Статус:** `PROPOSED` (Ожидает подтверждения пользователя)  
**Приоритеты:**
- **P0:** Фиксация контрольной точки в Git (Conventional Commits)
- **P1:** Недостающие сервисные эндпоинты в Go (Паритет фронтенда: Discography, Recognition, Torrent)
- **P2:** Продакшен-деплой (nl_vpn 144.31.51.105 и nl_music 194.76.168.20)

---

## 1. Цель и границы задачи (Objective & Scope)

### 1.1. Цель
Завершить переход проекта на полностью автономный стек на Go, закрыть оставшиеся пробелы в функционале фронтенда и вывести новую версию в промышленную эксплуатацию на боевые серверы.

### 1.2. Декомпозиция по приоритетам
1. **Приоритет P0: Фиксация контрольной точки в Git (Git Checkpoint)**
   - Фиксация в ветку `main` масштабных архитектурных изменений: удаление Python, создание Pure Go ядра, распил монолитных шаблонов на 11 partials и распил JS на 23 модуля.
   - Формат коммита: Conventional Commits (`feat(core): complete Go migration, decommission Python, decompose templates and modularize frontend`).
   - Гарантия: чистый прогон тестов (`go test -v ./...`) и успешная сборка бинарника перед созданием коммита.

2. **Приоритет P1: Недостающие сервисные эндпоинты в Go (Паритет фронтенда)**
   - `GET /downloads/discography`:
     - Парсинг ссылки на артиста Deezer (`deezer.com/artist/123`).
     - Получение списка альбомов, синглов и сборников с пагинацией через `DeezerClient`.
     - Возврат структурированного JSON для `web/static/js/downloader/discography.js`.
   - `POST /tags/recognize-audio`:
     - Валидация аудиофайла через `PathSandbox`.
     - Запуск `songrec audio-file-to-recognized-song` (Rust CLI клиент Shazam, установленный в `/usr/bin/songrec`) с таймаутом 15 секунд.
     - Разбор метаданных (название, артист, альбом, год, жанр, URL обложки высокого разрешения).
     - При отсутствии совпадений или недоступности утилиты — возврат `{ "recognized": false }` (HTTP 200), предотвращающий 404 и сбои в UI `recognition.js`.
   - `POST /downloads/torrent`:
     - Прием `.torrent` файлов (`multipart/form-data`, поле `file`).
     - Валидация формата файла.
     - Информативная заглушка / обработчик с понятным ответом пользователю в toast (`{ "message": "Загрузка через .torrent временно недоступна в текущей версии Go бэкенда..." }`), исключающая ошибку 404.
   - Регистрация эндпоинтов в `internal/api/router.go` и покрытие тестами в `internal/api/router_test.go`.

3. **Приоритет P2: Продакшен-деплой (Production Deployment)**
   - Сборка продакшен-бинарника под Linux x86_64: `go build -ldflags="-s -w" -o bin/music-manager cmd/server/main.go`.
   - Синхронизация бинарника и unit-файла `music-manager.service` на серверы:
     - `nl_vpn` (144.31.51.105)
     - `nl_music` (194.76.168.20)
   - Обновление systemd (`systemctl daemon-reload`) и перезапуск службы (`systemctl restart music-manager.service`).
   - Верификация доступности сервиса: `curl -I http://127.0.0.1:8010/api/health` и просмотр логов `journalctl -u music-manager.service -n 20`.

---

## 2. Анализ затронутых компонентов

| Компонент | Файлы | Изменение | Назначение |
|---|---|---|---|
| **Git / Repo** | Весь репозиторий | `COMMIT` | Фиксация завершенного этапа миграции |
| **Deezer Client** | `internal/services/resolver/client_deezer.go` | `[MODIFY]` | Добавление `GetArtist` и `GetArtistAlbums` с пагинацией |
| **Download Handler** | `internal/api/handlers/download_handlers.go` | `[MODIFY]` | Обработчики `GetDiscography` и `UploadTorrent` |
| **Tag Handler** | `internal/api/handlers/tag_handlers.go` | `[MODIFY]` | Обработчик `RecognizeAudio` через `songrec` + fallback |
| **Router** | `internal/api/router.go`, `cmd/server/main.go` | `[MODIFY]` | Регистрация новых маршрутов в Chi |
| **Tests** | `internal/api/router_test.go` | `[MODIFY]` | Интеграционные тесты для 3 новых эндпоинтов |
| **Deploy** | `music-manager.service`, серверы `nl_vpn`, `nl_music` | `[DEPLOY]` | Развертывание бинарника и перезапуск служб |

---

## 3. Критерии приемки (Definition of Done)

- [ ] Контрольная точка P0 зафиксирована в Git ветки `main`.
- [ ] Все 3 эндпоинта P1 (`/downloads/discography`, `/tags/recognize-audio`, `/downloads/torrent`) возвращают валидные ответы.
- [ ] `go test -v ./...` проходит на 100% со всеми пакетами.
- [ ] Бинарник `bin/music-manager` собран без ошибок.
- [ ] Сервис `music-manager.service` успешно перезапущен и отвечает на `api/health` на обоих серверах (`nl_vpn`, `nl_music`).
- [ ] `PROGRESS.md` актуализирован.
