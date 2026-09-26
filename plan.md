# План: Audio Streaming & Web Audio Player Pipeline (Блок 44)

**Дата:** 26 сентября 2026  
**Статус:** `COMPLETED`  
**Целевые компоненты:** `internal/api/handlers/`, `internal/api/`, `internal/db/`, `web/templates/components/`, `web/static/js/`, `web/templates/index.html`.

---

## 1. Цель и границы задачи (Objective & Scope)

### 1.1. Проблема
На текущий момент в сервисе реализован полный пайплайн загрузки (yt-dlp, ffmpeg), редактирования ID3v2.4 тегов, поиска текстов песен (LRCLIB), навигации по файловой системе и аудита медиатеки.
Однако полноценного воспроизведения аудио нет:
1. В интерфейсе отсутствует аудиоплеер — пользователь не может прослушать скачанный трек или проверить качество аудиофайла непосредственно в браузере.
2. Существующий эндпоинт `GET /files/stream?path=...` принимает сырой путь к файлу через query-параметр, что неудобно для плееров, требует сложного URL-экранирования и не предоставляет структурированного API для сторонних клиентов или мобильных приложений.
3. Отсутствуют эндпоинты получения легковесных метаданных трека и обложки по стабильному идентификатору (`track_id`).
4. Отсутствует поддержка эндпоинта-алиаса `/api/library/stream/{id}/chunks`, необходимого для xHTTP-маскировки VPN-трафика под аудиостриминг на боевом сервере `nl_vpn`.

### 1.2. In-Scope (что входит в задачу)
1. **Детерминированная идентификация треков (`TrackID`):**
   - Вычисление стабильного 16-символьного хэша `TrackHash(relPath) = sha256(relPath)[:16]`.
   - Универсальный резолвер `resolveTrackPath(idOrHash)`: поддержка числового ID из SQLite (`tracks.id`), 16-символьного hex-хэша и пути файла.
   - Строгая изоляция через `PathSandbox.Resolve()` с возвратом HTTP 403 Forbidden при любых попытках path traversal.
2. **HTTP Range Audio Streaming (RFC 7233 / RFC 9110):**
   - Высокопроизводительный Zero-Copy стриминг через `http.ServeFile` на ядре Linux (`sendfile(2)`).
   - Поддержка заголовков `Range: bytes=start-end`, `bytes=start-`, `206 Partial Content`, `Content-Range`, `Accept-Ranges: bytes`, `416 Range Not Satisfiable`.
   - Эндпоинт `GET /api/tracks/{id}/stream`.
   - VPN-алиас `GET /api/library/stream/{id}/chunks` для маскировки xHTTP-трафика.
3. **Метаданные и обложки по ID:**
   - Эндпоинт `GET /api/tracks/{id}/metadata`: мгновенный ответ из SQLite WAL (`duration`, `bitrate`, `title`, `artist`, `album`, `has_cover`) с fallback на парсер ID3v2.4.
   - Эндпоинт `GET /api/tracks/{id}/cover`: извлечение встроенного APIC-фрейма или папочной обложки `cover.jpg` с `Cache-Control: public, max-age=86400`.
   - Эндпоинт листинга `GET /api/library/tracks?query=&limit=&offset=`.
4. **No-Build Web Audio Player (Alpine.js CDN + Tailwind CSS CDN):**
   - Компонент плеера [`web/templates/components/audio_player.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/audio_player.html): фиксированная нижняя панель (Sticky Bottom Bar) в стиле modern dark UI (Spotify/Navidrome).
   - Обложка трека (48×48), название, артист, тайминги (`01:23 / 03:45`).
   - Кнопки управления: Play/Pause, Next/Prev, ползунок перемотки с перемоткой по клику (Range requests), регулятор громкости и Mute.
   - Глобальное состояние `Alpine.store('player', ...)` в [`web/static/js/player.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/player.js).
   - Интеграция кнопки воспроизведения ▶ в таблицу файлов [`web/templates/components/file_manager.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/file_manager.html) с подсветкой активного трека.
5. **Тестирование и валидация:**
   - Полноценные тесты эндпоинтов стриминга с различными вариациями `Range`, некорректными диапазонами, проверкой `403 Forbidden` и валидацией метаданных.
   - Полный прогон `go test -race ./...`.

### 1.3. Out-of-Scope (что явно не входит)
- Транскодинг на лету через ffmpeg pipe (вся библиотека уже стандартизирована в MP3 320 kbps CBR по инварианту №4 `AGENTS.md`; транскодинг без seek ломает перемотку и создает оверинжиниринг).
- Внедрение внешних JS-библиотек плееров (Howler.js, Video.js и т.д.) — строго нативный HTML5 `<audio>` + Alpine.js без сборки.
- Плейлисты произвольного состава в базе данных (в первой версии очередь воспроизведения формируется на лету из треков текущей директории или результатов поиска).

---

## 2. Анализ текущего состояния (Current State Analysis)

| Компонент | Текущее состояние | Что требуется добавить |
|---|---|---|
| [`stream_search_handlers.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/stream_search_handlers.go) | Содержит простой `StreamAudio(w, r)`, принимающий `?path=...` и вызывающий `http.ServeFile` | Вынести логику треков по ID: `StreamTrack`, `GetTrackMetadata`, `GetTrackCover`, `ListLibraryTracks` |
| [`router.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/router.go) | Только `/files/stream` внутри защищенной группы `/files` | Маршруты `/api/tracks/{id}/*` и `/api/library/stream/{id}/chunks` |
| [`schema.sql`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/db/schema.sql) | Поле `file_hash TEXT` уже присутствует в таблице `tracks`, но не индексировано | Индекс `idx_tracks_hash` и заполнение `file_hash` при сканировании для $O(1)$ поиска |
| [`file_manager.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/file_manager.html) | Таблица треков отображает метаданные и кнопки «Сверить», «Теги», «Удалить» | Добавить кнопку Play ▶ для аудиофайлов и индикатор текущего воспроизведения |
| [`index.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/index.html) | Модальные окна и экраны загрузчика/менеджера/аудита | Включение шаблона `audio_player` и подключение скрипта `player.js` |

---

## 3. Архитектурное решение и обоснование (Architecture & Rationale)

### 3.1. Архитектурная схема взаимодействия

```text
┌────────────────────────────────────────────────────────────────────────┐
│                          Браузер / Web UI                              │
│                                                                        │
│   [Таблица файлов / Поиск] ──(клик Play)──► Alpine.store('player')    │
│                                                     │                  │
│                                                     ▼                  │
│   [Sticky Bottom Player] ◄─────────────── HTML5 <audio>               │
└───────────────────────────────────────────────┬────────────────────────┘
                                                │ HTTP GET
                                                │ Range: bytes=1048576-
                                                ▼
┌────────────────────────────────────────────────────────────────────────┐
│                        Go-бэкенд (Chi Router)                          │
│                                                                        │
│  /api/tracks/{id}/stream ──► FileHandler.StreamTrack                   │
│  /api/library/stream/{id}/chunks (VPN alias)                           │
│                                    │                                   │
│                 ┌──────────────────┴──────────────────┐                │
│                 ▼                                     ▼                │
│      1. resolveTrackPath(id)                2. musicSandbox.Resolve    │
│         - in-memory fast cache                 - защита от traversal   │
│         - SQLite query (id / file_hash)        - 403 Forbidden         │
│                 │                                     │                │
│                 └──────────────────┬──────────────────┘                │
│                                    ▼                                   │
│                     3. http.ServeFile(w, r, path)                     │
│                        - RFC 7233 Range parser                         │
│                        - Linux sendfile(2) Zero-Copy                   │
│                        - 206 Partial Content / 416                     │
└────────────────────────────────────┬───────────────────────────────────┘
                                     │ sendfile
                                     ▼
                            [Файловая система]
                        MUSIC_DIR/Artist/Album/01.mp3
```

### 3.2. Детальное обоснование принятых решений («Почему именно так»)

1. **Использование `http.ServeFile` вместо ручной отдачи чанков:**
   - *Почему:* Стандартный `http.ServeFile` в Go протестирован десятилетиями в высоконагруженных системах. Он реализует полную спецификацию RFC 7233 (включая многодиапазонные Range, open-ended Range, валидацию ETag и Last-Modified). В среде Linux `http.ServeFile` использует системный вызов `sendfile(2)`, при котором ядро ОС перенаправляет страницы памяти из дискового кэша напрямую в сокет без участия userspace. Это обеспечивает минимальное использование CPU и памяти (< 4 КБ на соединение).
2. **Гибридный `TrackID` (Числовой ID + 16-hex хэш пути):**
   - *Почему:* Фронтенду и сторонним клиентам удобнее оперировать короткими строками без слэшей. Хэш `sha256(relPath)[:16]` полностью детерминирован и не зависит от пересоздания базы SQLite. При этом проверка числового ID (`tracks.id`) сохраняет обратную совместимость с результатами поиска SQLite.
3. **Хранение состояния плеера в выделенном `Alpine.store('player')`:**
   - *Почему:* Разделение ответственности. `Alpine.store('app')` отвечает за навигацию по вкладкам, модальные окна и тосты. Вынос плеера в отдельный стор `player` изолирует логику событий аудио (`timeupdate`, `ended`, `error`, `volumechange`) и предотвращает лишние перерисовки других компонентов.
4. **Отказ от внешних JS-библиотек воспроизведения:**
   - *Почему:* Соответствие инварианту №1 из `AGENTS.md` (No-Build, zero npm). Нативный тег `<audio>` в современных браузерах безупречно поддерживает seek и Range-запросы, если сервер отвечает `206 Partial Content` и `Accept-Ranges: bytes`.

### 3.3. Сравнительная таблица альтернатив

| Критерий | Вариант 1: Предложенный (Go http.ServeFile + ID resolver + Alpine store) | Вариант 2: Ручной чанкинг в Go (io.CopyN по 1MB) | Вариант 3: Оставить только `?path=...` без ID и без плеера |
|---|---|---|---|
| **Производительность** | **Экстремальная** (Zero-Copy `sendfile(2)`) | Средняя (буферизация 1MB в памяти на чанк) | Экстремальная |
| **Поддержка Range/Seek** | **100% стандарт RFC 7233** (все клиенты) | Требует ручного парсинга и обработки краевых случаев | 100% стандарт |
| **Удобство URL** | **Отличное** (`/api/tracks/a1b2c3d4/stream`) | Отличное | Плохое (слэши, пробелы в URL) |
| **Безопасность** | **Двойная** (ID lookup + `PathSandbox.Resolve`) | Двойная | Только `PathSandbox.Resolve` |
| **UX для пользователя** | **Встроенный плеер в браузере** | Встроенный плеер | Нет возможности слушать |

---

## 4. Пошаговая декомпозиция реализации (Step-by-Step Implementation)

### Этап 1: Доработка базы данных и идентификации треков
- **`[MODIFY]` [`internal/db/schema.sql`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/db/schema.sql)**:
  Добавление индекса `CREATE INDEX IF NOT EXISTS idx_tracks_hash ON tracks(file_hash);`.
- **`[MODIFY]` [`internal/db/queries.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/db/queries.go)**:
  Добавление методов:
  - `GetTrackByIDOrHash(ctx context.Context, idOrHash string) (*TrackEntity, error)`
  - `UpdateTrackHash(ctx context.Context, trackID int64, hash string) error`

### Этап 2: Бэкенд-хендлеры стриминга, метаданных и обложек
- **`[NEW]` [`internal/api/handlers/stream_handlers.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/stream_handlers.go)**:
  - Функция `TrackHash(relPath string) string` (детерминированный SHA256 [:16]).
  - Метод `(h *FileHandler) resolveTrack(idOrHash string) (fullPath string, relPath string, track *db.TrackEntity, err error)`.
  - Метод `(h *FileHandler) StreamTrack(w http.ResponseWriter, r *http.Request)`.
  - Метод `(h *FileHandler) GetTrackMetadata(w http.ResponseWriter, r *http.Request)`.
  - Метод `(h *FileHandler) GetTrackCover(w http.ResponseWriter, r *http.Request)`.
  - Метод `(h *FileHandler) ListLibraryTracks(w http.ResponseWriter, r *http.Request)`.
- **`[MODIFY]` [`internal/api/handlers/file_handlers.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/file_handlers.go)**:
  - Добавление `trackCache sync.Map` в `FileHandler` для мгновенного резолвинга без обращения к диску.
  - Обогащение ответа `fileItem` полями `id` и `stream_url` для каждого `.mp3`.

### Этап 3: Регистрация маршрутов в Chi Router
- **`[MODIFY]` [`internal/api/router.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/router.go)**:
  Регистрация эндпоинтов:
  ```go
  r.Route("/api/tracks", func(r chi.Router) {
      r.Use(authMiddleware)
      r.Get("/{id}/stream", p.FileHandler.StreamTrack)
      r.Get("/{id}/metadata", p.FileHandler.GetTrackMetadata)
      r.Get("/{id}/cover", p.FileHandler.GetTrackCover)
  })
  r.Route("/api/library", func(r chi.Router) {
      r.Use(authMiddleware)
      r.Get("/tracks", p.FileHandler.ListLibraryTracks)
      r.Get("/stream/{id}/chunks", p.FileHandler.StreamTrack) // xHTTP VPN obfuscation alias
  })
  ```
  Поддержка передачи токена сессии через query-параметр `?token=` в middleware для внешних плееров (VLC, mpv).

### Этап 4: No-Build фронтенд (Alpine.js + Tailwind)
- **`[NEW]` [`web/templates/components/audio_player.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/audio_player.html)**:
  - Шаблон фиксированной панели плеера (Sticky Bottom Bar).
  - Скрытый `<audio x-ref="audio">` элемент.
  - Полоса прогресса с визуализацией буферизации и ползунком перемотки.
  - Индикатор громкости с поддержкой Mute.
- **`[NEW]` [`web/static/js/player.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/player.js)**:
  - `Alpine.store('player', { ... })` со всеми методами воспроизведения.
  - Горячие клавиши (Space: Play/Pause, Стрелки: перемотка +/- 5 сек, M: Mute).
- **`[MODIFY]` [`web/templates/components/file_manager.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/file_manager.html)**:
  - Добавление кнопки Play ▶ в каждой строке MP3-файла.
  - Подсветка строки играющего трека.
- **`[MODIFY]` [`web/templates/index.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/index.html)**:
  - Подключение `player.js` и шаблона `audio_player`.

### Этап 5: Автоматизированные тесты
- **`[NEW]` [`internal/api/handlers/stream_handlers_test.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/stream_handlers_test.go)**:
  - Тест `TestStreamTrack_Range206`: запрос с `Range: bytes=0-100` возвращает 206 и корректный `Content-Range`.
  - Тест `TestStreamTrack_Full200`: запрос без `Range` возвращает 200 и весь файл.
  - Тест `TestStreamTrack_RangeNotSatisfiable416`: запрос `Range: bytes=99999999-` возвращает 416.
  - Тест `TestStreamTrack_PathTraversalBlocked`: проверка защиты от `../`.
  - Тест `TestGetTrackMetadata`: проверка валидности JSON.
  - Тест `TestGetTrackCover`: проверка извлечения обложки.
  - Тест `TestStreamTrack_VPNAlias`: проверка роута `/api/library/stream/{id}/chunks`.

---

## 5. Анализ рисков и краевых случаев (Risks, Edge Cases & Guardrails)

| Риск | Описание | Стратегия митигации |
|---|---|---|
| **Path Traversal через ID** | Передача пути вида `../../etc/passwd` в качестве `track_id` | Вызов `musicSandbox.Resolve(relPath, false)` обязателен для каждого разрешённого пути. При `errors.Is(err, sandbox.ErrPathTraversal)` немедленно отдавать HTTP 403 Forbidden. |
| **Утечка файловых дескрипторов** | Браузер резко разрывает соединение при перемотке трека | В Go `http.ServeFile` корректно обрабатывает закрытие контекста `r.Context().Done()` и закрывает дескриптор файла. |
| **Трек отсутствует в SQLite** | Пользователь только что скопировал файл в папку, сканер ещё не отработал | Резолвер осуществляет fallback: если ID совпадает с хэшем файла на диске или передан валидный путь — файл отдаётся, а теги читаются на лету через `id3.ReadTags`. |
| **Аудиофайлы без ID3-тегов** | Файл поврежден или не содержит тегов | Метаданные формируются из имени файла и размера; fallback на `audio/mpeg` MIME-тип. |

---

## 6. План тестирования и верификации (Verification Matrix)

1. **Модульные и интеграционные тесты:**
   - Запуск: `go test -v -race ./internal/api/handlers/ -run TestStream`
   - Запуск полного набора: `go test -race ./...` (все пакеты должны оставаться зелёными, 0 data races).
2. **Сборка бинарного файла:**
   - Команда: `go build -ldflags="-s -w" -o bin/music-manager cmd/server/main.go`
3. **Ручная верификация UI:**
   - Открытие веб-интерфейса в браузере.
   - Запуск трека по кнопке ▶ в файловом менеджере.
   - Проверка перемотки трека по клику на прогресс-бар (проверка отправки браузером Range-запросов и ответа 206).
   - Проверка регулировки громкости и переключения треков.

---

## 7. Критерии приёмки (Definition of Done)

- [ ] Все эндпоинты (`/api/tracks/{id}/stream`, `/api/tracks/{id}/metadata`, `/api/tracks/{id}/cover`, `/api/library/stream/{id}/chunks`) работают и протестированы.
- [ ] Запросы с `Range: bytes=...` корректно возвращают HTTP 206 Partial Content с правильным `Content-Range`.
- [ ] При попытке traversal возвращается HTTP 403 Forbidden.
- [ ] В UI встроен современный адаптивный аудиоплеер на Alpine.js.
- [ ] `go test -race ./...` проходит на 100% без ошибок и гонок.
- [ ] Бинарник успешно собирается командой `go build -ldflags="-s -w" -o bin/music-manager cmd/server/main.go`.
