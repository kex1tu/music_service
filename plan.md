# Архитектурный план: Полноценный цикл дозагрузки неполных релизов, исправление неканонических путей и внутрирелизный аудит дубликатов

**Дата:** 16 сентября 2026  
**Статус:** `PROPOSED` (Ожидает подтверждения пользователя)  
**Целевые компоненты:** `app/schemas/`, `app/services/`, `app/routers/`, `app/static/js/`, `app/templates/index.html`, `tests/`  

---

## 1. Цель и границы задачи (Objective & Scope)

### 1.1. Цель
Реализовать целостную систему управления качеством медиатеки по трем взаимосвязанным направлениям:
1. **Сквозной цикл обслуживания неполных альбомов:**
   - Сверка («Сверить с Deezer» в аудите и в файловом менеджере) $\rightarrow$
   - Постановка недостающих треков в очередь в виде единой связанной задачи дозагрузки (`[Дозагрузка] Artist — Album`) $\rightarrow$
   - Открытие полнофункционального редактора тегов (`TagEditor`) со **всеми треками релиза** (существующие из `MUSIC_DIR` + новые скачанные из `tmp_downloads`) в каноническом порядке $\rightarrow$
   - Сохранение и организация всего укомплектованного альбома в медиатеку в один клик.
2. **Исправление работы кнопки автоматического исправления неканонических путей:**
   - Устранить причину, по которой кнопка «Исправить все пути» в блоке «Файлы с нарушением канонической структуры» не отправляет сетевой запрос на сервер.
   - Оптимизировать серверный обработчик `fix_all_paths` (устранить вызов тяжелого `ensure_index()` на каждой из 2800+ итераций).
   - Предотвратить зависание рендеринга Alpine.js при отрисовке тысяч строк путей в DOM (внедрить лимит/виртуализацию отображения).
3. **Поиск дубликатов треков строго в рамках одного релиза и серверное удаление:**
   - Ограничить поиск дубликатов строго рамками одной папки/релиза: одинаковые треки в разных релизах (например, альбом и сингл, или альбом и Greatest Hits) больше **не расцениваются как дубликаты**.
   - Добавить кнопку «Исправить» для каждой группы дубликатов релиза и глобальную кнопку «Исправить все дубликаты».
   - На сервере реализовать алгоритм безопасного автоудаления избыточных копий (удаляются файлы с суффиксами `(2)`, дубли с худшим качеством/размером) через `PathSandbox`.

### 1.2. Границы задачи (Scope)
- **In-Scope:**
  - `app/schemas/download.py`, `app/schemas/library_audit.py`: новые поля и схемы для Backfill-задач, объединенного состава редактора тегов и пакетного удаления дубликатов.
  - `app/services/download_service.py`: поддержка связных задач дозагрузки с сохранением `target_album_path`, `is_backfill: bool`, `album_title`, `artist_name`.
  - `app/services/library_audit_service.py`:
    - Новый метод `get_backfill_editor_data()`: объединение треков из `MUSIC_DIR` и `tmp_downloads`.
    - Рефакторинг `find_duplicates()`: переход на ключ группировки по директории релиза `album_dir::title`.
    - Новый метод `fix_duplicates(group_key, album_path)` и `fix_all_duplicates()`: умный выбор сохраняемого оригинала и удаление дублей.
    - Оптимизация `fix_all_paths()`: однократный `ensure_index()` в начале и батчевая инвалидация кэша в конце.
  - `app/routers/files.py`: эндпоинты `/audit/incomplete-albums/editor-data`, `/audit/duplicates/fix`, `/audit/duplicates/fix-all`.
  - `app/static/js/library_audit.js`, `app/static/js/file_manager.js`, `app/templates/index.html`:
    - Разблокировка и исправление кнопки «Исправить все пути» (`paths`).
    - Кнопки «Исправить» в разделе «Дубликаты».
    - Кнопка «Редактировать весь релиз» в очереди и карточках аудита.
    - Кнопка «Сверить с Deezer» в файловом менеджере.
  - Написание модульных тестов в `tests/`.
- **Out-of-Scope:**
  - Внешние СУБД (строгий инвариант Zero-Database: только ФС и ID3-теги, SQLite — лишь локальный кэш).
  - Вшивание текстов песен в MP3 (только внешние `.lrc`/`.txt`).

---

## 2. Анализ текущего состояния и первопричин дефектов (Current State Analysis)

| # | Проблема | Файлы | Первопричина (Root Cause) |
|---|---|---|---|
| **P1** | **Кнопка «Исправить все пути» не отправляет запрос на сервер** | [`index.html:1930`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/app/templates/index.html#L1930), [`library_audit.js:477`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/app/static/js/library_audit.js#L477) | 1) Директива `:disabled="isFixingPaths \|\| invalidPaths.length === 0"` блокирует кнопку, если массив `invalidPaths` не успел загрузиться или вернул `[]`, даже когда на карточке горит `summary.invalid_paths_count = 2826`. Браузер глушит клик на disabled-кнопке.<br>2) Попытка отрендерить 2826 строк в `<template x-for>` намертво фризит JavaScript-поток Alpine.js.<br>3) В бэкенде `fix_all_paths` вызывает `ensure_index()` в цикле на каждый файл, что приводило к таймаутам. |
| **P2** | **Ложные дубликаты между разными альбомами/синглами** | [`library_audit_service.py:1092`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/app/services/library_audit_service.py#L1092) | Составной ключ формируется как `composite_key = f"{clean_art}::{clean_tit}"` без привязки к релизу. Песня из студийного альбома и та же песня из сборника или сингла считаются дубликатами. Удаление ломает второй релиз. |
| **P3** | **Отсутствие кнопки «Исправить» для дубликатов на сервере** | [`index.html:1640`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/app/templates/index.html#L1640), [`library_audit.js:224`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/app/static/js/library_audit.js#L224) | Существует только поштучное удаление через диалог `promptDeleteDuplicate(track)`. Нет пакетной кнопки «Исправить дубликаты релиза» и «Удалить все дубликаты» с автоматическим сохранением лучшей копии. |
| **P4** | **Невозможность отредактировать весь релиз целиком после дозагрузки** | [`downloader.js:257`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/app/static/js/downloader.js#L257), [`index.html:130`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/app/templates/index.html#L130) | При готовности скачанного трека Tag Editor открывается ТОЛЬКО с этим одиночным треком из `tmp_downloads/{job_id}`. Существующие треки из `MUSIC_DIR/{album_path}` не подгружаются, пользователь не может проверить и выровнять теги всего релиза. |
| **P5** | **Дробление дозагрузки на изолированные задачи** | [`files.py:284`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/app/routers/files.py#L284) | Создается N отдельных `DownloadJob` без привязки к `target_album_path`. Очередь захламлена, контекст альбома утерян. |
| **P6** | **Слепота к отсутствующим трекам в конце альбома без `track_total`** | [`library_audit_service.py:1376`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/app/services/library_audit_service.py#L1376) | При отсутствии `track_total` сканер ищет только разрывы (1..8 из 10 считается цельным). В файловом менеджере нет кнопки ручной сверки с Deezer. |

---

## 3. Архитектурное решение (Architecture & Rationale)

### 3.1. Исправление кнопки автоматического исправления неканонических путей
1. **Снятие ложной блокировки кнопки в UI:**
   Условие доступности кнопки в шаблоне:
   `:disabled="isFixingPaths || (invalidPaths.length === 0 && (summary.invalid_paths_count || 0) === 0)"`.
   Если счетчик на карточке показывает наличие нарушений, кнопка всегда активна. При клике гарантированно отправляется `POST /files/audit/fix-all-paths`.
2. **Ограничение рендеринга DOM в таблице путей (Anti-Freeze):**
   При выборке свыше 100 путей отображаются первые 100 записей с плашкой: *«Показано 100 из 2826 файлов. Нажмите «Исправить все пути» для полной автоматической нормализации»*. Это полностью исключает фризы браузера при рендеринге огромных массивов в Alpine.js.
3. **Оптимизация бэкенда `fix_all_paths`:**
   Вызов `await self.ensure_index()` выполняется ровно один раз перед циклом. Внутри цикла файлы перемещаются, обновляются пути, а `self.invalidate()` вызывается один раз в конце. Время выполнения сокращается с минут до нескольких секунд.

### 3.2. Внутрирелизный аудит дубликатов и серверное автоудаление
1. **Группировка строго в рамках одного релиза:**
   ```python
   # Ключ включает путь к директории альбома:
   album_dir = str(Path(rel_path).parent).replace("\\", "/")
   composite_key = f"{album_dir}::{clean_tit}"
   ```
   Треки из разных папок никогда не попадают в одну группу дубликатов.
2. **Алгоритм выбора сохраняемого оригинала (Canonical Retention):**
   Из группы дубликатов выбирается эталонный файл, а остальные удаляются:
   - Приоритет 1: имя файла без мусорных суффиксов ` (2)`, `_dup`, ` (copy)`.
   - Приоритет 2: соответствие канонической маске `{track_number:02d}. {title}.mp3`.
   - Приоритет 3: больший размер файла / более высокий битрейт (320 kbps).
   - Лишние файлы и их сопутствующие `.lrc`/`.txt` удаляются через `PathSandbox.resolve(path).unlink()`.
3. **API и UI:**
   - `POST /files/audit/duplicates/fix` (удаление дублей одной группы)
   - `POST /files/audit/duplicates/fix-all` (пакетное удаление всех дубликатов)
   - В UI: кнопка «Исправить» на каждой карточке группы дубликатов и кнопка «Удалить все дубликаты» в шапке раздела.

### 3.3. Сквозной цикл дозагрузки неполных релизов
1. **Единая Backfill-задача:**
   `DownloadJob` получает поля `is_backfill: bool = True`, `target_album_path`, `album_title`, `artist_name`. В очереди отображается компактная плашка `[Дозагрузка] Artist — Album (N треков)`.
2. **Эндпоинт подготовки полного состава релиза (`editor-data`):**
   `GET /files/audit/incomplete-albums/editor-data?path={album_path}&job_id={job_id}`:
   Считывает существующие треки из `MUSIC_DIR/{album_path}`, находит скачанные треки в `tmp_downloads/{job_id}/converted/`, объединяет их в единый список, упорядоченный по `(disc_number, track_number)`, и возвращает готовый объект для вызова `Alpine.store('app').openTagEditor(...)`.
3. **Кнопка «Редактировать весь релиз»:**
   Появляется в очереди и на карточке аудита, позволяя пользователю привычным образом проверить метаданные, обложку и тексты всего укомплектованного альбома.
4. **Сверка на лету в файловом менеджере:**
   Добавление действия «Сверить с Deezer» в проводник файлов.

---

## 4. Пошаговая декомпозиция реализации (Step-by-Step Implementation)

### Этап 1. Схемы данных (Schemas)
- `[MODIFY]` [`app/schemas/download.py`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/app/schemas/download.py)
  - Добавить в `DownloadJobResponse`: `target_album_path`, `is_backfill`, `album_title`, `artist_name`.
- `[MODIFY]` [`app/schemas/library_audit.py`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/app/schemas/library_audit.py)
  - Добавить в `DuplicateGroupItem`: `album_dir: str`, `album: str`.
  - Добавить модели `FixDuplicatesRequest`, `FixDuplicatesResponse`, `BackfillEditorDataResponse`.

### Этап 2. Бэкенд: Дубликаты, пути и дозагрузка
- `[MODIFY]` [`app/services/library_audit_service.py`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/app/services/library_audit_service.py)
  - В `find_duplicates()`: группировать строго по `album_dir::title`.
  - Реализовать `fix_duplicates(group_key)` и `fix_all_duplicates()`: выбор лучшего файла и удаление копий.
  - Оптимизировать `fix_all_paths()`: вынести `ensure_index()` из цикла.
  - Реализовать `get_backfill_editor_data(album_path, job_id)`.
  - В `match_album_catalog()`: рекурсивный `rglob("*.mp3")` и сопоставление многодисковых дорожек.
- `[MODIFY]` [`app/services/download_service.py`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/app/services/download_service.py)
  - Добавить поддержку атрибутов `target_album_path`, `is_backfill`, `album_title`, `artist_name` в `DownloadJob`.
  - Добавить метод `create_backfill_job()`.
- `[MODIFY]` [`app/routers/files.py`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/app/routers/files.py)
  - Переработать `POST /audit/incomplete-albums/download-missing` для создания единой Backfill-задачи.
  - Добавить эндпоинт `GET /audit/incomplete-albums/editor-data`.
  - Добавить эндпоинты `POST /audit/duplicates/fix` и `POST /audit/duplicates/fix-all`.

### Этап 3. Фронтенд: Разблокировка кнопок и новый UX
- `[MODIFY]` [`app/templates/index.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/app/templates/index.html)
  - Секция `paths`:
    - Исправить привязку `:disabled` на кнопке «Исправить все пути».
    - Ограничить рендеринг таблицы путей (лимит первых 100 элементов с сообщением о полном количестве).
  - Секция `duplicates`:
    - Добавить в шапку кнопку «Исправить все дубликаты».
    - Добавить на каждую группу кнопку «Исправить» (удалить копии, оставив оригинал).
  - Всплывающая очередь:
    - Для `is_backfill` задач отображать бейдж `[Дозагрузка]` и кнопку **«Редактировать весь релиз»**.
  - Файловый менеджер:
    - Добавить кнопку «Сверить с Deezer» в действиях с каталогами.
- `[MODIFY]` [`app/static/js/library_audit.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/app/static/js/library_audit.js)
  - Методы `fixDuplicates(group)`, `fixAllDuplicates()`.
  - Метод `openBackfilledAlbumInTagEditor(albumPath, jobId)`.
- `[MODIFY]` [`app/static/js/file_manager.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/app/static/js/file_manager.js)
  - Метод `matchDirectoryCatalog(item)`.

### Этап 4. Организация файлов (`OrganizerService`)
- `[MODIFY]` [`app/services/organizer_service.py`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/app/services/organizer_service.py)
  - Бесшовное перемещение файлов из `tmp_downloads` в существующую папку альбома в `MUSIC_DIR` без дублирования суффиксов `(2)`.

---

## 5. План верификации (Verification Plan)

### Автоматические тесты
1. Тест поиска дубликатов: треки с одинаковым названием в разных папках НЕ являются дубликатами.
2. Тест удаления дубликатов: в папке с `01. Track.mp3` и `01. Track (2).mp3` удаляется файл с `(2)`, а оригинал сохраняется.
3. Тест вызова `POST /files/audit/fix-all-paths`: пути корректно нормализуются без зависания.
4. Тест создания единой Backfill-задачи и эндпоинта `editor-data`: в Tag Editor попадают и старые, и новые треки.
5. Прогон полного тест-сьюта: `./venv/bin/python -m pytest` и проверка линтером `./venv/bin/ruff check app tests`.

### Ручная верификация
1. Перейти в «Некорректные пути» $\rightarrow$ нажать «Исправить все пути» $\rightarrow$ убедиться, что запрос отправляется, отображается прогресс и файлы нормализуются.
2. Перейти в «Дубликаты» $\rightarrow$ убедиться, что отображаются только дубликаты внутри одного альбома $\rightarrow$ нажать «Исправить» $\rightarrow$ убедиться, что лишние копии удаляются.
3. В «Неполных релизах» нажать «Сверить» $\rightarrow$ «Скачать недостающие» $\rightarrow$ в очереди дождаться READY $\rightarrow$ нажать «Редактировать весь релиз» $\rightarrow$ проверить единый треклист в Tag Editor и сохранить в медиатеку.
