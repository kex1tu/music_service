# План реализации: Защита от дубликатов, предпросмотр треклиста перед скачиванием и выборочная загрузка (Pre-Flight Check & Track Selection)

- **Дата составления:** 2026-10-22  
- **Статус плана:** `PROPOSED (Ожидает утверждения)`  
- **Целевые компоненты:**  
  - `internal/features/downloader/types.go` (DTO запросов, PreflightResponse, поля SelectedTrackIndices)
  - `internal/features/downloader/repository.go` (Сверка трека с SQLite базой медиатеки: `CheckTrackInLibrary`)
  - `internal/features/downloader/service.go` (Метод `PreflightRelease`, фильтрация треков в `executeRegular`)
  - `internal/features/downloader/handlers.go` (HTTP-обработчик `POST /api/downloads/preflight`)
  - `internal/api/router.go` (Регистрация маршрутов `/downloads/preflight` и `/api/downloads/preflight`)
  - `web/templates/components/downloader_preflight_modal.html` (Модальное окно Pre-Flight Review)
  - `web/static/js/downloader/preflight.js` (Alpine.js модуль управления выбором и фильтрацией треков)
  - `web/templates/components/downloader_form.html` (Кнопка «Предпросмотр» в Command Bar)
  - `web/templates/index.html` (Подключение шаблона и скрипта с кэш-бастером)
- **Архитектурные инварианты (AGENTS.md):**  
  - **Pure Go / Zero-Python:** Реализация сверки и фильтрации треков строго на чистом Go, zero external dependencies.
  - **Sandbox Mandatory:** Все файловые операции изолированы через `PathSandbox`.
  - **SQLite WAL:** Сверка выполняется быстрыми точечными индексированными запросами к таблицам `tracks`, `artists`, `albums`.
  - **No Docker / No Node.js:** Только статический Go-бинарник и CDN Alpine.js/Tailwind.

---

## 1. Цель и границы задачи (Objective & Scope)

### 1.1. Проблематика
1. **Дублирование треков при скачивании релизов:**
   - При скачивании альбомов или сборников пользователь часто уже имеет часть композиций в своей коллекции в идеальном качестве (320 kbps). Повторное скачивание расходует сетевой трафик, время и засоряет медиатеку дубликатами.
2. **Отсутствие выборочной загрузки (All-or-Nothing):**
   - Пользователь вынужден ставить на скачивание весь альбом целиком, даже если ему нужны всего 2–3 понравившихся трека из релиза.
3. **Невозможность оценить качество треков в коллекции до загрузки:**
   - Если старый трек в коллекции записан в низком качестве (128 kbps), система должна явно предложить его обновить (*upgrade*), а для треков, уже имеющихся в 320 kbps, по умолчанию снять галочку.

### 1.2. Целевой результат
1. **Эндпоинт предполетной проверки (`POST /api/downloads/preflight`):**
   - Быстрый резолв треклиста и обложки без запуска загрузки аудиофайлов.
   - Сверка каждого трека с SQLite-базой медиатеки по очищенным названиям и авторам (`CleanTitle`, `matchesAnyArtist`).
   - Классификация статуса: `NEW` (новый трек), `EXISTS_HIGH_QUALITY` (уже есть в $\ge 310$ kbps), `EXISTS_UPGRADE_AVAILABLE` (есть, но $< 310$ kbps).
2. **Выборочное скачивание в конвейере `Job`:**
   - Передача массива выбранных индексов `selected_track_indices: []int` в `POST /downloads`.
   - В конвейере `executeRegular` скачиваются, транскодируются и тегируются строго выбранные треки.
3. **Модальное окно Pre-Flight Review (Studio Dark):**
   - Карточка релиза с обложкой и метаданными.
   - Сводные бейджи: `Всего`, `Новые`, `В коллекции (320k)`, `Доступен апгрейд`.
   - Быстрые фильтры: `«Только новые»`, `«Новые + Апгрейд»`, `«Выбрать все»`, `«Снять выбор»`.
   - Таблица с чекбоксами и статусами треков.
   - Кнопка `«Скачать выбранные (N)»`.
4. **Интеграция в Command Bar:**
   - Кнопка `«Предпросмотр»` в строке ввода URL.
   - Автоматическое открытие предполетного окна при вставке ссылок на альбомы и плейлисты.

---

## 2. Пошаговая декомпозиция реализации

```
Этап 1: Backend DTO & Repository (сверка треков с SQLite)
Этап 2: Backend Service & Pipeline (метод PreflightRelease и фильтрация в executeRegular)
Этап 3: HTTP API Handlers & Routing (эндпоинт /api/downloads/preflight и CreateJob)
Этап 4: Frontend UI Modal & Alpine.js Module (downloader_preflight_modal.html, preflight.js)
Этап 5: Интеграция в Command Bar (downloader_form.html, index.html)
Этап 6: Тестирование, стресс-проверка конкурентности и сборка
```

---

## 3. Критерии приемки (Definition of Done)

- [ ] Метод `CheckTrackInLibrary` точно сопоставляет треки по `clean_title` и артистам с учетом соавторов.
- [ ] Эндпоинт `POST /api/downloads/preflight` возвращает полный треклист со статусами медиатеки и сводкой.
- [ ] При передаче `selected_track_indices` скачиваются строго указанные треки.
- [ ] Модальное окно Pre-Flight Review отображает обложку, треклист, статусы и поддерживает фильтрацию.
- [ ] Все тесты `go test -race ./...` проходят успешно (0 data races).
- [ ] Статический валидатор шаблонов `go test ./web/...` подтверждает корректность всех JS-вызовов и атрибутов `:key`.
- [ ] Бинарник `bin/music-manager` собирается чисто.
