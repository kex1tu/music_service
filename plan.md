# План реализации: Группировка по альбомам, построчный выбор обложек и исправление переключателя режимов загрузки

- **Дата:** 2026-09-10
- **Статус:** PROPOSED
- **Целевые компоненты:**
  - [app/schemas/tag.py](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/schemas/tag.py)
  - [app/routers/tags.py](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/routers/tags.py)
  - [app/services/tag_service.py](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/services/tag_service.py)
  - [app/services/organizer_service.py](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/services/organizer_service.py)
  - [app/static/js/downloader.js](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/static/js/downloader.js)
  - [app/static/js/tag_editor.js](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/static/js/tag_editor.js)
  - [app/templates/index.html](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/templates/index.html)
  - [tests/test_tags_router.py](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/tests/test_tags_router.py)
  - [tests/test_organizer_service.py](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/tests/test_organizer_service.py)
  - [PROGRESS.md](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/PROGRESS.md)
- **Ответственная область:** Core / UI, Downloader Engine, Metadata & Storage (согласно [AGENTS.md](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/AGENTS.md))

---

## 1. Цель и границы задачи (Objective & Scope)

### 1.1 Цель
Решить комплексную проблему разбора разнородных каталогов исполнителя (содержащих множество альбомов, EP и синглов) и исправить дефект переключения режимов ввода ссылок в загрузчике:

1. **Исправление переключателя режимов загрузчика:**
   Устранить дефект, из-за которого кнопки `[ Одна ссылка ]` и `[ Список ссылок (пакетно) ]` не переключали форму ввода или скрывали поле ввода ссылок (конфликт HTML5 `:required` валидации на скрытых `x-show` элементах и коллизии реактивных геттеров).
2. **Визуальная группировка треков по альбомам в редакторе тегов:**
   При открытии директории артиста отображать треки не сплошной простыней, а аккуратно **сгруппированными по релизам**:
   ```text
   ▼ Альбом "Bandana I" (2021) • 14 треков [Обложка альбома]
     ├── 01. Трек 1
     └── 02. Трек 2
   ▼ Альбом "Dragonborn" (2018) • 23 трека [Обложка альбома]
     ├── 01. Трек 1
     └── ...
   ▼ Синглы и отдельные треки • 5 треков [Режим синглов]
     ├── 01. Сингл A [Индивидуальный арт]
     └── 01. Сингл B [Индивидуальный арт]
   ```
   С возможностью сворачивать/разворачивать группы (аккордеон), выбирать обложку для всей группы или для отдельного трека.
3. **Построчный выбор обложек для каждого трека (сингла):**
   Квадратная миниатюра `40×40` в каждой строке таблицы с открытием единой мультисервисной галереи (Apple Music, Deezer, Яндекс, Spotify) в контексте конкретного трека (`{artist} {title}`).
4. **Пакетный автопоиск обложек синглов:**
   Кнопка над таблицей для неблокирующего фонового поиска и назначения обложек 1400×1400 для всех треков без арта.
5. **Умная организация медиатеки в OrganizerService:**
   Каждый альбом раскладывается в свою папку `{Artist}/{Album} - {Year}/`, а синглы — в `{Artist}/{Title} - Single ({Year})/01. {Title}.mp3` с сохранением персонального `cover.jpg`.

### 1.2 Границы (Scope)
- **In-Scope:**
  - Исправление реактивности и разметки переключателя одиночного/пакетного режима в `downloader.js` и `index.html`.
  - Реализация группировки `groupedReleases` в `tag_editor.js` с сохранением двустороннего связывания (`x-model`) с `tracks`.
  - Доработка интерфейса таблицы в `index.html`: секции альбомов с аккордеоном, плашки с метаданными группы, кнопка обложки группы, колонка индивидуальной обложки трека.
  - Расширение Pydantic-схем `TrackTags` (`cover_url`, `cover_base64`) и `SaveAlbumRequest` (`singles_mode`, `group_by_album`).
  - Поддержка раздельного вшивания APIC-фреймов ID3v2.4 в `PUT /tags/batch` в `tags.py`.
  - Модификация `OrganizerService`: поддержка разбиения одного батча по нескольким целевым каталогам альбомов и синглов с безопасным двухфазным стейджингом.
- **Out-of-Scope:**
  - Использование внешних СУБД (Zero-Database инвариант).
  - Сборка через Node.js/Webpack/Vite (No-build инвариант).
  - Модификация формата текстов песен (только `.lrc`/`.txt` рядом с файлом).

---

## 2. Анализ текущего состояния (Current State Analysis)

### 2.1 Дефект переключателя режимов загрузки
1. В `app/templates/index.html` (строки 485–509):
   ```html
   <div x-show="inputMode === 'single'" class="flex-1 relative">
       <input type="url" x-model="url" :required="inputMode === 'single'" ...>
   </div>
   <div x-show="inputMode === 'batch'" class="flex-1">
       <textarea x-model="batchUrlsText" :required="inputMode === 'batch'" ...></textarea>
   </div>
   ```
2. **Причина бага:**
   - В Tailwind/Alpine комбинация `x-show` (устанавливающего инлайн-стиль `display: none`) с HTML5-атрибутом `:required` в некоторых браузерах блокирует валидацию формы или вызывает сбой фокуса: скрытый элемент с `required` вызывает ошибку `An invalid form control with name='' is not focusable.`
   - Кроме того, в `downloader.js` геттер `get parsedBatchUrls()` внутри `x-data` может терять реактивность в Alpine v3, если обращение к нему происходит в `<template x-if="parsedBatchUrls.length > 0">` до первого ввода символа.
   - **Решение:** Использовать `<template x-if="inputMode === 'single'">` и `<template x-if="inputMode === 'batch'">` вместо `x-show`. Это физически удаляет неактивный контрол из DOM, исключает конфликты `required` и гарантирует чистое переключение.

### 2.2 Монолитная плоская структура списка треков в Tag Editor
1. Сейчас `tagEditorComponent()` загружает все треки в единый плоский массив `this.tracks`.
2. Если в папке артиста лежит 50 треков (3 альбома и 10 синглов), они выводятся подряд без визуального разделения.
3. Метаданные в шапке (`album.album`, `album.year`) берутся из первого попавшегося трека и навязываются всем остальным при сохранении, что разрушает дискографию.

---

## 3. Архитектурное решение и обоснование (Architecture & Rationale)

### 3.1 Схема взаимодействия компонентов

```
┌────────────────────────────────────────────────────────────────────────────────────────┐
│                                 ЗАГРУЗЧИК (Downloader)                                 │
│                                                                                        │
│   [ Одна ссылка ] ◄─── Клик ───► [ Список ссылок (пакетно) ]                          │
│          │                                    │                                        │
│          ▼ (<template x-if="single">)          ▼ (<template x-if="batch">)             │
│   ┌──────────────────────────────┐     ┌──────────────────────────────────────────┐   │
│   │ <input type="url">           │     │ <textarea rows="3">                      │   │
│   │ (Чистый DOM без конфликтов)  │     │ (Чистый DOM без конфликтов)              │   │
│   └──────────────────────────────┘     └──────────────────────────────────────────┘   │
└────────────────────────────────────────────────────────────────────────────────────────┘

┌────────────────────────────────────────────────────────────────────────────────────────┐
│                              РЕДАКТОР ТЕГОВ (Tag Editor)                               │
│                                                                                        │
│ ┌────────────────────────────────────────────────────────────────────────────────────┐ │
│ │ Панель режимов: [ ☰ Сплошной список ]  [ 🗂 Сгруппировать по альбомам ]            │ │
│ └────────────────────────────────────────────────────────────────────────────────────┘ │
│                                                                                        │
│ ▼ Секция: Альбом "Bandana I" (2021) • [🖼 Обложка альбома] • [Свернуть/Развернуть]     │
│ ┌────┬─────────┬──────────────┬─────────────┬───────────┬────────────────────────────┐ │
│ │ #  │ Обложка │ Название     │ Артист      │ Год       │ Текст                      │ │
│ ├────┼─────────┼──────────────┼─────────────┼───────────┼────────────────────────────┤ │
│ │ 01 │ [40×40] │ 99 Проблем   │ Big Baby... │ 2021      │ [LRC]                      │ │
│ └────┴─────────┴──────────────┴─────────────┴───────────┴────────────────────────────┘ │
│                                                                                        │
│ ▼ Секция: Синглы и отдельные треки (Singles) • [⚡ Найти обложки для всех синглов]    │
│ ┌────┬─────────┬──────────────┬─────────────┬───────────┬────────────────────────────┐ │
│ │ -- │ [40×40] │ Владелец     │ Big Baby... │ 2023      │ [+ Текст]                  │ │
│ │    │ (клик)  │ └► Открывает галерею для конкретного сингла (Apple/Deezer/Yandex)    │ │
│ └────┴─────────┴──────────────┴─────────────┴───────────┴────────────────────────────┘ │
└───────────────────────────────────────────┬────────────────────────────────────────────┘
                                            │ PUT /tags/batch
                                            │ { tracks: [...], singles_mode: true }
                                            ▼
┌────────────────────────────────────────────────────────────────────────────────────────┐
│                                   БЭКЕНД И ОРГАНАЙЗЕР                                  │
│                                                                                        │
│ 1. Скачивание/внедрение индивидуальных обложек в ID3v2.4 (mutagen APIC type=3).        │
│ 2. OrganizerService раскладывает треки по их фактическим релизам:                      │
│    ├─ MUSIC_DIR/Artist/Album 1 - 2021/01. Track.mp3 + cover.jpg                        │
│    └─ MUSIC_DIR/Artist/Single A - Single (2023)/01. Single A.mp3 + cover.jpg          │
└────────────────────────────────────────────────────────────────────────────────────────┘
```

### 3.2 Детальное обоснование принятых решений

1. **Реактивная группировка в Alpine.js без потери мутаций (Reference Preservation):**
   * *Проблема:* Если создать копию массива треков для каждой группы, изменения полей (название, артист, номер) в инпутах не попадут в корневой `this.tracks`, который отправляется на бэкенд при `saveTags()`.
   * *Решение:* Группировка строится методом `get groupedReleases()`, возвращающим массив групп, где каждая группа содержит прямые ссылки на объекты из `this.tracks`:
     ```javascript
     get groupedReleases() {
         const groups = {};
         this.tracks.forEach((track, originalIndex) => {
             const key = (track.album || '').trim() || '__singles__';
             if (!groups[key]) {
                 groups[key] = {
                     id: key,
                     isSingles: key === '__singles__',
                     title: key === '__singles__' ? 'Синглы и отдельные треки' : key,
                     year: track.year || null,
                     tracks: [],
                     coverUrl: null,
                     collapsed: false,
                 };
             }
             groups[key].tracks.push({ track, originalIndex });
         });
         return Object.values(groups);
     }
     ```
     Поскольку объекты `track` передаются по ссылке, любые `x-model="item.track.title"` мгновенно и реактивно обновляют корневой `this.tracks`.
2. **Замена `x-show` на `<template x-if>` в форме загрузчика:**
   * *Проблема:* `x-show` оставляет оба элемента `<input>` и `<textarea>` в DOM, переключая лишь стиль `display: none`. Браузерная валидация HTML5 падает при попытке проверить `:required` на скрытом инпуте, а верстка во флекс-контейнере может схлопываться.
   * *Решение:* `<template x-if="inputMode === 'single'">` монтирует только актуальный инпут. Никаких конфликтов стилей и валидации.
3. **Групповое и индивидуальное назначение обложек:**
   * В заголовке группы альбома кнопка «Обложка альбома» открывает галерею с запросом `"{artist} {album.title}"` и при выборе применяет обложку ко всем трекам этой конкретной группы.
   * В строке отдельного трека миниатюра `40×40` открывает галерею с запросом `"{track.artist} {track.title}"` и при выборе применяет обложку строго к этому файлу.
4. **Многоальбомная организация в OrganizerService:**
   * Если батч содержит треки из разных альбомов, органайзер группирует их по целевым папкам релиза и раскладывает каждую группу в свой каталог, сохраняя правильный `cover.jpg` в каждой папке.

---

## 4. Пошаговая декомпозиция реализации (Step-by-Step Implementation)

### Этап 1: Исправление переключателя режимов загрузчика

#### [MODIFY] [app/static/js/downloader.js](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/static/js/downloader.js)
1. Добавить явный метод переключения режима:
   ```javascript
   setMode(mode) {
       this.inputMode = mode;
   },
   ```
2. Преобразовать `parsedBatchUrls` в безопасный метод:
   ```javascript
   getParsedBatchUrls() {
       return (this.batchUrlsText || '')
           .split('\n')
           .map((u) => u.trim())
           .filter((u) => u.length > 0);
   },
   ```

#### [MODIFY] [app/templates/index.html](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/templates/index.html)
Заменить блок `x-show` на `<template x-if>` (строки 485–510):
```html
<div class="flex flex-col sm:flex-row gap-3">
    <!-- Одиночный режим ввода URL -->
    <template x-if="inputMode === 'single'">
        <div class="flex-1 relative">
            <div class="absolute inset-y-0 left-0 pl-3.5 flex items-center pointer-events-none text-gray-500">
                <svg class="w-4 h-4" viewBox="0 0 24 24" fill="currentColor">
                    <path d="M7 17q-2.075 0-3.537-1.463T2 12t1.463-3.537T7 7h3q.425 0 .713.288T11 8t-.288.713T10 9H7q-1.25 0-2.125.875T4 12t.875 2.125T7 15h3q.425 0 .713.288T11 16t-.288.713T10 17zm2-4q-.425 0-.712-.288T8 12t.288-.712T9 11h6q.425 0 .713.288T16 12t-.288.713T15 13zm5 4q-.425 0-.712-.288T13 16t.288-.712T14 15h3q1.25 0 2.125-.875T20 12t-.875-2.125T17 9h-3q-.425 0-.712-.288T13 8t.288-.712T14 7h3q2.075 0 3.538 1.463T22 12t-1.463 3.538T17 17z"/>
                </svg>
            </div>
            <input
                type="url"
                x-model="url"
                placeholder="https://music.yandex.ru/... https://open.spotify.com/... https://deezer.com/..."
                required
                class="w-full bg-gray-950 border border-gray-700 rounded-lg pl-10 pr-4 py-3 text-white placeholder-gray-500 focus:outline-none focus:border-emerald-500 focus:ring-1 focus:ring-emerald-500 transition text-sm">
        </div>
    </template>

    <!-- Пакетный режим ввода списка URL -->
    <template x-if="inputMode === 'batch'">
        <div class="flex-1">
            <textarea
                x-model="batchUrlsText"
                rows="3"
                placeholder="Вставьте по одной ссылке на строку (от 1 до 20):&#10;https://music.yandex.ru/album/...&#10;https://open.spotify.com/album/..."
                required
                class="w-full bg-gray-950 border border-gray-700 rounded-lg p-3 text-white placeholder-gray-500 focus:outline-none focus:border-emerald-500 focus:ring-1 focus:ring-emerald-500 transition text-xs font-mono"></textarea>
        </div>
    </template>
    ...
</div>
```

---

### Этап 2: Схемы Pydantic ([app/schemas/tag.py](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/schemas/tag.py)) `[MODIFY]`

1. В `TrackTags` добавить:
   - `cover_url: str | None = None`
   - `cover_base64: str | None = None`
2. В `SaveAlbumRequest` добавить:
   - `singles_mode: bool = False`
   - `group_by_album: bool = True`

---

### Этап 3: Бэкенд роутера тегов ([app/routers/tags.py](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/routers/tags.py)) `[MODIFY]`

1. В `PUT /tags/batch`:
   - Скачивание/декодирование обложек треков:
     - Если у трека задан `track.cover_url` — скачать через `tag_service.download_cover(track.cover_url)`.
     - Если задан `track.cover_base64` — декодировать.
     - Иначе если задана альбомная обложка — использовать её для треков этой группы.
     - Иначе `cover_bytes=None` (существующая обложка трека сохраняется).
   - Запись тегов через `tag_service.write_tags`.
   - Передача словаря обложек `track_covers_data` в `organizer_service.organize_album`.

---

### Этап 4: Библиотечный органайзер ([app/services/organizer_service.py](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/services/organizer_service.py)) `[MODIFY]`

1. Поддержка мультиальбомного сохранения:
   - Разделение входящих треков по релизам:
     - Треки с заполненным `album` группируются по `{Artist}/{Album} - {Year}/`.
     - Треки без альбома или помеченные как синглы (`singles_mode=True`) группируются по `{Artist}/{Title} - Single ({Year})/`.
2. Сохранение файла `cover.jpg` в целевой каталог каждого альбома и сингла.
3. Перенос ассоциированных файлов текстов `.lrc`/`.txt`.
4. Двухфазный стейджинг (`.stage_*`) и удаление пустых директорий.

---

### Этап 5: Фронтенд редактора тегов — Группировка и выбор обложек

#### [MODIFY] [app/static/js/tag_editor.js](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/static/js/tag_editor.js)
1. Свойства состояния:
   - `viewMode: 'grouped'` (`'grouped'` | `'flat'`)
   - `collapsedGroups: {}`
   - `coverGalleryTarget: { type: 'album' | 'group' | 'track', id: null }`
   - `isSearchingSinglesCovers: false`
   - `singlesCoverProgress: { current: 0, total: 0 }`
   - `singlesMode: false`
2. Геттер `get groupedReleases()`:
   - Группирует треки по `track.album` с сохранением ссылок на оригинальные объекты `this.tracks`.
3. Методы управления обложками:
   - `openGroupCoverGallery(group)`: открывает галерею с запросом `"{artist} {group.title}"`.
   - `openTrackCoverGallery(originalIndex, track)`: открывает галерею с запросом `"{track.artist} {track.title}"`.
   - `selectCover(item)`: обновляет обложку альбома, группы или конкретного трека в зависимости от контекста.
4. Пакетный автопоиск `fetchSinglesCovers()`:
   - Ищет обложки 1400×1400 для всех треков без арта.

#### [MODIFY] [app/templates/index.html](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/templates/index.html)
1. В тулбар таблицы добавить переключатель вида `[ Сгруппировать по альбомам ]` / `[ Сплошной список ]`.
2. В режиме `grouped`:
   - Рендеринг через `<template x-for="group in groupedReleases" :key="group.id">`.
   - Заголовок группы (Accordion Header):
     - Название альбома, год, бейдж количества треков.
     - Кнопка с обложкой группы.
     - Кнопка сворачивания/разворачивания.
   - Внутри группы — строки треков с колонкой `Обложка (40×40)` и всеми полями редактирования.

---

### Этап 6: Автоматизированные тесты

1. [tests/test_frontend.py](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/tests/test_frontend.py):
   - Проверка наличия шаблонов `<template x-if="inputMode === 'single'">` и `inputMode === 'batch'`.
   - Проверка элементов аккордеона групп в шаблоне редактора тегов.
2. [tests/test_tags_router.py](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/tests/test_tags_router.py):
   - Тест пакетного сохранения с индивидуальными обложками синглов.
   - Тест сохранения треков из разных альбомов в одном запросе.
3. [tests/test_organizer_service.py](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/tests/test_organizer_service.py):
   - Тест организации батча, содержащего 2 альбома и 2 сингла: проверка создания соответствующих подкаталогов и файлов `cover.jpg`.

---

## 5. Анализ рисков и краевых случаев (Risks, Edge Cases & Guardrails)

1. **Краевой случай: Потеря двусторонней привязки инпутов при группировке.**
   - *Риск:* Мутации инпутов в сгруппированном виде не отразятся в `tracks`.
   - *Митигация:* Группы содержат прямые ссылки на объекты исходного массива `this.tracks`, что гарантирует сохранение реактивности в Alpine.js.
2. **Краевой случай: Треки без названия альбома (пустой тег `TALB`).**
   - *Митигация:* Все треки без альбома автоматически собираются в специальную группу «Синглы и отдельные треки» (`__singles__`) и организуются по схеме синглов.
3. **Краевой случай: Переключатель одиночного/пакетного режима в загрузчике.**
   - *Митигация:* Использование `<template x-if>` полностью изолирует состояния, убирая неактивные инпуты из DOM и предотвращая ложные срабатывания браузерного `required`.

---

## 6. План тестирования и верификации (Verification Matrix)

| Уровень проверки | Сценарий | Ожидаемый результат | Команда запуска |
|---|---|---|---|
| **Downloader UI** | Клик по `Список ссылок (пакетно)` и `Одна ссылка` | Мгновенное и надежное появление соответствующего поля ввода без ошибок в консоли | Браузер / тесты шаблона |
| **Pydantic Validation** | Десериализация запроса `SaveAlbumRequest` с группировкой и полями `cover_url` | HTTP 200/202 без ошибок валидации | `./venv/bin/python -m pytest tests/test_tags_router.py` |
| **Tag Editor UI** | Открытие папки с треками 2 разных альбомов и 1 синглом | Отображение 3 отдельных блоков-групп с возможностью свернуть/развернуть | Проверка в UI |
| **Organizer Unit** | Реорганизация разнородного пакета треков | Раздельное размещение по каталогам `{Album} - {Year}` и `{Title} - Single` | `./venv/bin/python -m pytest tests/test_organizer_service.py` |
| **Full Regression** | Прогон всей тестовой сюиты (472 теста) | `472 passed` без регрессий | `./venv/bin/python -m pytest` |

---

## 7. Критерии приемки (Definition of Done)

- [ ] В загрузчике надежно работает переключатель между одиночным инпутом и многострочным `textarea` списка ссылок.
- [ ] При открытии каталога с разными альбомами в редакторе тегов отображаются визуальные группы с заголовками релизов (Альбом 1, Альбом 2, Синглы).
- [ ] Каждую группу можно сворачивать и разворачивать.
- [ ] Для каждой группы можно выбрать альбомную обложку, а для каждого сингла — индивидуальную обложку через галерею.
- [ ] Кнопка «Обложки синглов» производит пакетный автопоиск артов.
- [ ] При сохранении органайзер раскладывает альбомы и синглы по их собственным папкам с сохранением индивидуальных `cover.jpg`.
- [ ] Все тесты `./venv/bin/python -m pytest` зеленые.
- [ ] [PROGRESS.md](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/PROGRESS.md) актуализирован.
