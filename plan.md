# План: Модульная декомпозиция HTML-шаблонов (web/templates)

**Дата:** 17 сентября 2026  
**Статус:** `PROPOSED` (Ожидает подтверждения пользователя)  
**Целевые компоненты:** `web/templates/components/`, `web/templates/modals/`, `internal/api/router.go`.

---

## 1. Цель и границы задачи (Objective & Scope)

### 1.1. Проблема
В проекте проведена успешная декомпозиция Go-кода и JavaScript-скриптов на компактные файлы по 100–300 строк. Однако в шаблонах фронтенда (`web/templates/`) остаются 3 крупных монолитных HTML-файла:
1. **[`web/templates/modals/tag_editor.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/modals/tag_editor.html)** — **979 строк** (содержит тулбар, группировку релизов, аккордеон, вложенную таблицу треков, сплошной список, модальное окно поиска Deezer и модальное окно мульти-галереи обложек).
2. **[`web/templates/components/audit.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/audit.html)** — **853 строки** (содержит дашборд 7 метрик, вкладки и 6 независимых подразделов аудита: тексты, обложки, дубликаты, неполные релизы с бэкфиллом, некорректные пути, жанры).
3. **[`web/templates/components/downloader.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/downloader.html)** — **678 строк** (содержит форму ввода с переключателями режимов, карточку активной загрузки, секцию очереди задач, карточки источников и модальное окно дискографии артиста).

Остальные 8 HTML-шаблонов (`index.html`, `header.html`, `file_manager.html`, `queue_drawer.html`, `lyrics.html`, `confirmations.html`, `login.html`, `toasts.html`) уже компактны (30–195 строк) и не требуют декомпозиции.

### 1.2. Границы задачи
- **In-Scope:**
  - Декомпозиция `tag_editor.html` (979 строк) на 5 модульных под-шаблонов по 100–250 строк.
  - Декомпозиция `audit.html` (853 строки) на 7 модульных под-шаблонов по 70–200 строк.
  - Декомпозиция `downloader.html` (678 строк) на 5 модульных под-шаблонов по 50–200 строк.
  - Обновление парсера шаблонов в `internal/api/router.go` (поддержка шаблонов в поддиректориях `templates/*/*.html`).
  - Верификация сборки, тестов и рендеринга страницы `GET /`.
- **Out-of-Scope:**
  - Изменение JavaScript-логики или стилей Tailwind.
  - Изменение API эндпоинтов.

---

## 2. Архитектурное решение и обоснование («Почему именно так»)

### 2.1. Механизм композиции Go `html/template`
Стандартная библиотека Go (`html/template`) поддерживает нативную композицию шаблонов через директивы `{{ define "name" }}` и `{{ template "name" . }}`.
При компиляции бинарника все `.html` файлы из `web/templates/` автоматически встраиваются через `go:embed` в `web.Assets`.
Разделение монолитных шаблонов на именованные подкомпоненты:
1. **Не требует сборщиков** (сохраняет 100% Zero-Node.js инвариант).
2. **Имеет нулевой оверхед по памяти и скорости рендера** (шаблоны парсятся один раз при старте сервера).
3. **Обеспечивает легкую читаемость и поддержку верстки**: разработчик или агент редактирует конкретный изолированный компонент (например, только карточку дубликатов или галерею обложек), не листая тысячестрочный файл.

---

## 3. Детальная декомпозиция шаблонов

### 3.1. Декомпозиция `web/templates/modals/tag_editor.html` (979 строк -> 5 модулей)

| Новый файл | Строк | Содержимое и назначение |
|---|---|---|
| [`web/templates/modals/tag_editor.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/modals/tag_editor.html) | ~110 | `{{ define "modal_tag_editor" }}`: контейнер окна, заголовок, верхний тулбар действий (очистка названий, перенумерация, Shazam, обложки синглов), подвал с кнопками «Сохранить» / «Отмена». Включает под-шаблоны через `{{ template "..." . }}`. |
| `web/templates/modals/tag_editor_grouped.html` | ~250 | `{{ define "tag_editor_grouped" }}`: режим отображения «По релизам» (аккордеон альбомов/синглов, карточки метаданных релиза, вложенные таблицы треков, Drag & Drop). |
| `web/templates/modals/tag_editor_flat.html` | ~160 | `{{ define "tag_editor_flat" }}`: режим отображения «Сплошной список» (плоская таблица всех треков без группировки). |
| `web/templates/modals/tag_editor_deezer.html` | ~100 | `{{ define "tag_editor_deezer_modal" }}`: модальное окно поиска официальных релизов в Deezer API с предпросмотром треклиста. |
| `web/templates/modals/tag_editor_covers.html` | ~150 | `{{ define "tag_editor_cover_gallery" }}`: модальное окно мульти-источниковой галереи обложек высокого разрешения (Apple Music, Deezer, Yandex, Spotify). |

---

### 3.2. Декомпозиция `web/templates/components/audit.html` (853 строки -> 7 модулей)

| Новый файл | Строк | Содержимое и назначение |
|---|---|---|
| [`web/templates/components/audit.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/audit.html) | ~90 | `{{ define "audit" }}`: шапка раздела «Аудит медиатеки», кнопка «Пересканировать», навигационные вкладки подразделов и переключение отображения. |
| `web/templates/components/audit_metrics.html` | ~135 | `{{ define "audit_metrics" }}`: дашборд из 7 интерактивных карточек сводки (всего треков, без текстов, без обложек, дубликаты, неполные релизы, некорректные пути, жанры). |
| `web/templates/components/audit_lyrics.html` | ~110 | `{{ define "audit_lyrics" }}`: подраздел треков без текстов с панелью фонового пакетного поиска в LRCLIB/Genius и таблицей. |
| `web/templates/components/audit_covers.html` | ~55 | `{{ define "audit_covers" }}`: подраздел релизов без обложек со ссылками на директории. |
| `web/templates/components/audit_duplicates.html` | ~115 | `{{ define "audit_duplicates" }}`: подраздел дубликатов песен с группировкой копий и кнопками безопасного удаления. |
| `web/templates/components/audit_incomplete.html` | ~205 | `{{ define "audit_incomplete" }}`: подраздел неполных релизов, карточки сверки с Deezer, прогресс-бары и кнопка пакетной дозагрузки (Backfill). |
| `web/templates/components/audit_paths.html` | ~155 | `{{ define "audit_paths" }}`: подраздел аудита неканонических путей с автоисправлением и подраздел проверки жанров. |

---

### 3.3. Декомпозиция `web/templates/components/downloader.html` (678 строк -> 5 модулей)

| Новый файл | Строк | Содержимое и назначение |
|---|---|---|
| [`web/templates/components/downloader.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/downloader.html) | ~50 | `{{ define "downloader" }}`: корневой контейнер вкладки загрузчика, объединяющий форму, статус, очередь, карточки сервисов и модал дискографии. |
| `web/templates/components/downloader_form.html` | ~160 | `{{ define "downloader_form" }}`: карточка формы ввода (одиночная ссылка, пакетный список, загрузка .torrent, селектор источника). |
| `web/templates/components/downloader_active.html` | ~105 | `{{ define "downloader_active" }}`: карточка активной задачи (прогресс, скорость, ETA, ошибка, кнопки повтора и перехода в редактор тегов). |
| `web/templates/components/downloader_queue.html` | ~165 | `{{ define "downloader_queue" }}`: секция очереди задач загрузки с карточками, статусами и кнопками управления. |
| `web/templates/components/downloader_discography.html` | ~200 | `{{ define "downloader_discography" }}`: полноэкранное модальное окно селектора дискографии артиста Deezer с фильтрами и сеткой релизов. |

---

## 4. Пошаговый план реализации

1. **Шаг 1:** Создать новые под-шаблоны для `web/templates/modals/`:
   - `tag_editor_grouped.html`, `tag_editor_flat.html`, `tag_editor_deezer.html`, `tag_editor_covers.html`.
   - Обновить `tag_editor.html` для вызова этих шаблонов.
2. **Шаг 2:** Создать новые под-шаблоны для `web/templates/components/`:
   - `audit_metrics.html`, `audit_lyrics.html`, `audit_covers.html`, `audit_duplicates.html`, `audit_incomplete.html`, `audit_paths.html`.
   - Обновить `audit.html` для вызова этих шаблонов.
3. **Шаг 3:** Создать новые под-шаблоны загрузчика в `web/templates/components/`:
   - `downloader_form.html`, `downloader_active.html`, `downloader_queue.html`, `downloader_discography.html`.
   - Обновить `downloader.html` для вызова этих шаблонов.
4. **Шаг 4:** Проверить и при необходимости уточнить шаблонный загрузчик в `internal/api/router.go`.
5. **Шаг 5:** Запустить `go test -race ./...`, собрать `bin/music-manager` и проверить рендеринг `GET /`.

---

## 5. Анализ рисков и меры защиты

| Риск | Влияние | Стратегия митигации |
|---|---|---|
| Ошибка имени шаблона (`template "..." not defined`) | Ошибка 500 при рендере `GET /` | Тщательно проверить каждое имя в `{{ define "..." }}` и вызов `{{ template "..." . }}`. Протестировать эндпоинт `GET /` через `httptest` |
| Потеря области видимости данных Alpine.js (`x-data`) | UI не реагирует | Шаблоны встраиваются на этапе Go-рендеринга до передачи в браузер, DOM-иерархия и область видимости Alpine `x-data="tagEditorComponent()"` и `x-data="downloaderComponent()"` полностью сохраняются |

---

## 6. Критерии приемки (Definition of Done)

- [ ] В проекте нет ни одного HTML-файла длиннее 250 строк.
- [ ] Все 14 новых и обновленных шаблонов используют стандартные конструкции Go `{{ define }}` / `{{ template }}`.
- [ ] Главная страница `GET /` корректно рендерится без ошибок парсинга шаблонов.
- [ ] Все тесты `go test -race ./...` проходят успешно (100% PASS).
- [ ] Статический бинарник успешно собирается без ошибок.
