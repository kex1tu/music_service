# Архитектурный план: Объединение демонов в единый Go-бинарник, реализация эндпоинтов аудита, фикс торрентов и очистка

**Дата:** 16 сентября 2026  
**Статус:** `DONE` (Реализовано, протестировано и задеплоено)  
**Приоритеты:**
- **P0:** Объединение `music-scanner` и `music-manager` в единый бинарник Go (нормализация жанров, проверка неполных альбомов в Deezer, декоммиссия `music-auditor.service`)
- **P1:** Реализация заглушек аудита в `internal/api/handlers/file_handlers.go` (`FixAllDuplicates`, `RetagGenres`, `BatchLyricsFetch`)
- **P2:** Исправление резолвера торрент/magnet источников в `internal/services/resolver/resolver.go`
- **P3:** Очистка мусора (локально: `deploy.sh`, `music-manager-prod.tar.gz`, `.ruff_cache/`, `COBALT_API_URL`, `GENIUS_API_TOKEN`; на серверах: остатки Python) и деплой на `nl_vpn` и `nl_music`

---

## 1. Цель и границы задачи (Objective & Scope)

### 1.1. Цель
1. Устранить дублирующий демон `music-scanner` (`music-auditor.service`), перенеся всю логику валидации/нормализации жанров и сверки с Deezer в единый бинарник `bin/music-manager` (экономия 106 МБ RAM и исключение конкуренции за SQLite базу данных).
2. Реализовать заглушенные эндпоинты аудита в `file_handlers.go`:
   - `FixAllDuplicates`: автоматический выбор канонического файла и безопасное пакетное удаление дубликатов через `PathSandbox`.
   - `RetagGenres`: автоматическая перезапись ID3v2.4 тегов жанров с русской кириллицы на канонический английский вид (`Hip-Hop`, `Rock`, `Pop`).
   - `BatchLyricsFetch` & `BatchLyricsProgress`: потокобезопасный фоновый воркер загрузки синхронизированных текстов через LRCLIB.
3. Исправить ошибку в `resolver.go:163`, когда передача источника `torrent` или magnet-ссылки приводила к возврату 0 треков и ошибке `"Релиз не содержит доступных аудиотреков"`.
4. Удалить неиспользуемые переменные `COBALT_API_URL` и `GENIUS_API_TOKEN`, удалить файлы `deploy.sh`, `music-manager-prod.tar.gz`, `.ruff_cache/` и остатки Python на серверах.

---

## 2. Анализ затронутых компонентов

| Компонент | Файлы | Действие | Назначение |
|---|---|---|---|
| **ID3 Engine** | `internal/id3/genre.go`, `internal/id3/genre_test.go` | `[NEW]` | Валидация и нормализация жанров (`AnalyzeAndNormalizeGenre`, кириллица, канонические имена) |
| **Scanner Engine** | `internal/services/scanner/scanner.go` | `[MODIFY]` | Индексация жанров, заполнение `genres`, `track_genres`, фоновая проверка неполных альбомов в Deezer |
| **File Handlers** | `internal/api/handlers/file_handlers.go` | `[MODIFY]` | Полноценная реализация `FixAllDuplicates`, `RetagGenres`, `BatchLyricsFetch`, `BatchLyricsProgress` |
| **Source Resolver** | `internal/services/resolver/resolver.go` | `[MODIFY]` | Парсинг magnet `dn=...`, поиск в Deezer и информативные сообщения об ошибках |
| **Config** | `internal/config/config.go`, `.env.example` | `[MODIFY]` | Удаление `COBALT_API_URL` и `GENIUS_API_TOKEN` |
| **Repo Cleanup** | `deploy.sh`, `music-manager-prod.tar.gz`, `.ruff_cache/` | `[DELETE]` | Удаление устаревших артефактов |
| **Deploy & Servers** | `nl_vpn`, `nl_music` | `[DEPLOY]` | Остановка и удаление `music-auditor.service`, удаление Python leftovers, обновление бинарника |

---

## 3. Архитектурное решение и обоснование

### 3.1. Объединение демонов
- **Проблема:** На сервере работали два параллельных процесса: `music-manager` (Chi веб-сервис) и `music-scanner` (фоновый демон аудита), оба обращались к одному файлу `data/library.db` в WAL-режиме, при этом `music-scanner` потреблял 106 МБ RAM и дублировал обход каталогов.
- **Решение:** Логика анализа жанров переносится в `internal/id3/genre.go`. При каждом вызове `indexFile` в `Scanner` жанр проверяется, заносится в `genres` и связывается через `track_genres`. При завершении цикла сканирования вызывается метод `CheckIncompleteAlbums`, который опрашивает Deezer API для альбомов с `is_complete = 0` и сохраняет найденные треки в `missing_tracks`. В результате сервис `music-auditor.service` становится полностью избыточным и удаляется.

### 3.2. Автоматическое исправление дубликатов (`FixAllDuplicates`)
- **Алгоритм выбора канонического файла:**
  1. Фильтр по названию файла: исключаются файлы с маркерами копий (` (1)`, ` (2)`, `_dup`, ` copy`).
  2. Фильтр по битрейту: выбирается трек с максимальным битрейтом (320 kbps предпочтительнее 128-256 kbps).
  3. Фильтр по размеру файла: при равном битрейте выбирается файл большего размера.
- **Безопасность:** Удаление файлов производится строго через `h.musicSandbox.Resolve(p, false)` с синхронным удалением companion файлов `.lrc` и `.txt` и вызовом `h.database.DeleteTrack(ctx, p)`.

### 3.3. Исправление Torrent / Magnet в Resolver
- При получении Magnet-ссылки (`magnet:?xt=...&dn=Artist+-+Album...`) парсится параметр `dn` (display name).
- Строка названия очищается от технических маркеров (`[FLAC]`, `[MP3]`, `(Vinyl)`, `24-96` и т.п.).
- Выполняется поиск альбома в `deezerClient.SearchAlbums(ctx, query)`.
- Если релиз найден — треклист разрешается через Deezer.
- Если релиз не найден — возвращается понятная ошибка с объяснением, исключая пустой треклист и краш в пайплайне загрузчика.

---

## 4. Критерии приемки (Definition of Done)
1. `go test -v ./...` проходит на 100% с новыми тестами для `genre.go`, `FixAllDuplicates`, `RetagGenres`, `BatchLyricsFetch`.
2. `go test -race ./...` чист, без состояний гонки.
3. Бинарник `bin/music-manager` успешно собирается.
4. На серверах `nl_vpn` и `nl_music`:
   - `music-auditor.service` остановлен, отключен и удален.
   - Папки Python (`app/`, `venv/`, `tests/`, `requirements.txt`, кэши) удалены.
   - Новый бинарник `bin/music-manager` запущен и отдает `200 OK` на `/api/health`.
5. `PROGRESS.md` и `plan.md` актуализированы, контрольный коммит зафиксирован в Git.
