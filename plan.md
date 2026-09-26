# План: Dedicated Studio Hi-Fi Player Tab (Блок 45)

**Дата:** 26 сентября 2026  
**Статус:** `COMPLETED`  
**Целевые компоненты:** `web/templates/components/`, `web/templates/`, `web/static/js/`, `internal/api/handlers/`.

---

## 1. Цель и границы задачи (Objective & Scope)

### 1.1. Проблема
В Блоке 44 был успешно внедрен бэкенд стриминга (HTTP RFC 7233 Range, Zero-Copy sendfile, детерминированные TrackID) и базовый нижний sticky-миниплеер.
Однако для полноценного, эстетически выверенного взаимодействия с медиатекой пользователю требуется **выделенная вкладка «Плеер»** с бескомпромиссным, профессиональным дизайном («не нейрослоп», отказ от шаблонных градиентных карточек, характерный индустриальный Hi-Fi стиль **Teenage Engineering / Studio Console**).

### 1.2. Решения пользователя по интерфейсу и функционалу
По результатам интерактивного брифа согласованы следующие требования:
1. **Эстетическое направление:** Студийный Hi-Fi / Teenage Engineering — глубокий матовый графит (`#0d0f12`, `#14171f`), тактильные переключатели с физической окантовкой, точная моноширинная телеметрия, CRT-дисплей с частотным спектром и аналоговые светодиодные янтарно-изумрудные индикаторы.
2. **Интерактивные модули (все 4 согласованы):**
   - **Synced Scrolling Lyrics (`.lrc`):** Караоке-подсветка текущей строчки, плавный автоскролл, перемотка по клику на строчку.
   - **Real-time Canvas Audio Visualizer:** Web Audio API (`AudioContext`, `AnalyserNode`), 32 частотные полосы с пик-холдом (падающие маркеры пиков), частотная сетка (`32Hz` – `16kHz`).
   - **Интерактивная очередь (Queue / Tape Rack):** Треклист с удалением, перемещением выше/ниже, очисткой, прямым переходом и анимированным мини-эквалайзером на активном треке.
   - **Технический паспорт трека (Audiophile Specs):** Битрейт (320 kbps CBR), частота дискретизации (44.1 kHz), размер файла, стандарт тегов (ID3v2.4 UTF-8 с null-разделителями), 16-символьный SHA-256 хэш трека.
3. **Поведение мини-плеера:** Липкая нижняя плашка остаётся на всех вкладках, а при клике по ней (или кнопке разворота) открывается вкладка «Плеер».

### 1.3. In-Scope (что входит в задачу)
1. **Навигация:** Добавление 4-й вкладки «Плеер» в шапку [`web/templates/components/header.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/header.html) с сохранением состояния `$store.app.activeTab = 'player'`.
2. **Шаблон студийной консоли:** Создание [`web/templates/components/player_tab.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/player_tab.html) с 3-палубной архитектурой (Left Deck: Cassette & Transport, Center Deck: Multi-monitor CRT, Right Deck: Tape Queue).
3. **Включение в базовый шаблон:** Подключение компонента в [`web/templates/index.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/index.html).
4. **Реактивный движок плеера:** Расширение [`web/static/js/player.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/player.js):
   - Инициализация Web Audio API `AudioContext` + `AnalyserNode` (обработка политик автоплея и однократного создания `createMediaElementSource`).
   - Рендер-луп спектроанализатора на HTML5 Canvas (32 бара + peak hold).
   - Парсер LRC-таймкодов и караоке-трекер с кликабельным `seek(time)`.
   - Операции управления очередью (`removeFromQueue`, `moveInQueue`, `clearQueue`).
   - Переход во вкладку плеера (`openPlayerTab()`).
5. **Связка с мини-плеером:** Добавление кнопки «Развернуть консоль» и клика по обложке/названию в [`web/templates/components/audio_player.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/audio_player.html).
6. **Обогащение метаданных бэкенда:** Добавление полей `file_size` и `track_number` в `TrackMetadataResponse` в [`internal/api/handlers/stream_handlers.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/stream_handlers.go).

### 1.4. Out-of-Scope (что явно не входит)
- Внедрение внешних NPM-библиотек или frontend-сборщиков (строго инвариант №1: Pure Go + No-Build Alpine.js + Tailwind CDN).
- Транскодинг на лету (все файлы в медиатеке соответствуют стандарту MP3 320 kbps).

---

## 2. Анализ текущего состояния (Current State Analysis)

| Файл | Текущее состояние | Что необходимо изменить |
|---|---|---|
| [`header.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/header.html) | Вкладки: Загрузчик, Файлы, Аудит | Добавить кнопку вкладки «Плеер» с иконкой студийной деки и индикатором воспроизведения |
| [`index.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/index.html) | Включает `downloader`, `file_manager`, `audit` | Добавить `{{ template "player_tab" . }}` в блок `<main>` |
| [`player.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/player.js) | Управляет `audioElement`, базовым воспроизведением и громкостью | Добавить Web Audio API Analyser, LRC-парсер, караоке-скроллер, расширенное управление очередью |
| [`audio_player.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/audio_player.html) | Стики-миниплеер внизу экрана | Добавить кнопку открытия вкладки «Плеер» и клик по треку |
| [`player_tab.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/player_tab.html) | Отсутствует | **[NEW]** Создать полноразмерный студийный интерфейс в стиле Teenage Engineering |
| [`stream_handlers.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/stream_handlers.go) | `TrackMetadataResponse` отдает базовые поля | Добавить `file_size`, `track_number`, `sample_rate` |

---

## 3. Архитектурное решение и обоснование (Architecture & Rationale)

### 3.1. Дизайн-система: Teenage Engineering / Studio Console Hi-Fi
Вместо типового «нейрослопа» (мягкие розово-фиолетовые градиенты, полупрозрачные карточки с размытием, размазанные тени) интерфейс строится по законам индустриального дизайна профессионального звукового оборудования:
- **Палитра:**
  - Шасси (Base): `#0d0f12` — глубокий матовый графит.
  - Модульные рэковые панели: `#14171f` — фактурная металлическая поверхность.
  - Углубления регуляторов (Wells): `#191e28` — тактильные гнезда.
  - Линии разметки: `#262d3d` и `border-white/10` — строгая гравировка 1px.
  - Индикаторы Amber: `#f59e0b` / `#d97706` — лампа накаливания / винтажный индикатор записи.
  - Индикаторы Emerald: `#10b981` / `#059669` — студийный светодиод активности / пик-метр.
- **Типографика:**
  - Чистый гротеск для названий треков и артистов.
  - Строгий моноширинный шрифт (`font-mono tracking-wider`) для всех параметров: `CH-01`, `03:42.18`, `320 KBPS / 44.1 KHZ`, `ID3v2.4 UTF-8`, `43.8 MB`.
- **Компоновка (3-Deck Rack):**
  - **Left Deck (Cassette & Master Controls):** Физический картридж трека (крупная обложка с виниловым акцентом), название, артист, блок тяжелых тактильных кнопок управления, стерео-VU пиковые индикаторы, шкала воспроизведения с миллисекундной точностью, ползунок уровня громкости.
  - **Center Deck (Multi-Function Monitor):** Переключаемый CRT-экран с тремя режимами:
    1. *SPECTRUM:* Реалтайм эквалайзер на Canvas с пик-холдом.
    2. *LYRICS:* Синхронизированный караоке-текст песни с интерактивной перемоткой.
    3. *PASSPORT:* Технический паспорт файла и спецификация тегов.
  - **Right Deck (Tape Queue Rack):** Очередь треков с живым эквалайзером, кнопками смены порядка, удаления и счетчиками длительности.

### 3.2. Архитектура Web Audio API & Canvas Visualizer
```text
┌───────────────────────────────────────────────────────────────┐
│                    HTML5 <audio> Element                      │
└──────────────────────────────┬────────────────────────────────┘
                               │ createMediaElementSource() (once)
                               ▼
┌───────────────────────────────────────────────────────────────┐
│              Web Audio API: AnalyserNode (fftSize=64)         │
│                 32 частотных бина (0 - 22.05 kHz)             │
└──────────────┬────────────────────────────────┬───────────────┘
               │ connect                        │ getByteFrequencyData()
               ▼                                ▼
┌──────────────────────────────┐ ┌──────────────────────────────┐
│ AudioContext.destination     │ │   Canvas Render Loop (rAF)   │
│ (Аудиовыход на динамики)     │ │ - 32 изумрудных полосы       │
└──────────────────────────────┘ │ - Peak-hold с гравитацией    │
                                 │ - Частотная шкала (Гц/кГц)   │
                                 └──────────────────────────────┘
```
**Важные технические гарантии:**
1. Метод `audioCtx.createMediaElementSource(audioEl)` вызывается строго один раз и кэшируется в `$store.player.audioSourceNode` во избежание ошибки `InvalidStateError`.
2. Перед возобновлением воспроизведения проверяется состояние `audioCtx.state === 'suspended'`: вызывается `audioCtx.resume()` по жесту пользователя.
3. Цикл `requestAnimationFrame` активен только тогда, когда открыта вкладка `player` и `isPlaying === true`. При переходе на другую вкладку или паузе рендеринг засыпает, гарантируя 0% паразитной нагрузки на CPU/GPU.

### 3.3. Архитектура движка Synced LRC Lyrics
1. При смене трека `$store.player` вызывает `GET /lyrics?path={path}`.
2. Текст парсится регулярным выражением `/\[(\d{2}):(\d{2}(?:\.\d{2,3})?)\](.*)/g`.
3. Каждая строка преобразуется в объект `{ time: seconds, text: string }`.
4. В процессе воспроизведения (`timeupdate`) вычисляется индекс активной строки `currentLyricIndex`.
5. Автоскролл: контейнер плавно центрирует активную строку через `scrollIntoView({ behavior: 'smooth', block: 'center' })`.
6. Пользовательский скролл не блокируется; при клике на любую строку вызывается `seek(line.time)` с мгновенным переходом.

### 3.4. Сравнительная таблица альтернатив
| Критерий | Вариант 1 (Студийный 3-Deck Hi-Fi) | Вариант 2 (Шаблонный Tailwind SaaS плеер) | Вариант 3 (Только мини-плеер внизу) |
|---|---|---|---|
| **Уникальность стиля** | **Бескомпромиссная (Teenage Engineering)** | Низкая («нейрослоп», типовые карточки) | Минимальная |
| **Визуализатор** | **Реалтайм Canvas с пик-холдом** | Фейковый CSS-вейвформ | Отсутствует |
| **Синхронный караоке-текст** | **Интерактивный с кликом и автоскроллом** | Статичный блок текста | Нет |
| **Управление очередью** | **Полнофункциональный рэк** | Простой выпадающий список | Только кнопки след/пред |
| **Нагрузка на браузер** | **Минимальная (отключение при скрытии)** | Низкая | Минимальная |

---

## 4. Пошаговая декомпозиция реализации (Step-by-Step Implementation)

### Этап 1: Доработка бэкенда метаданных
- **`[MODIFY]` [`internal/api/handlers/stream_handlers.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/stream_handlers.go)**:
  - Добавить поля `file_size`, `track_number`, `sample_rate` в структуру `TrackMetadataResponse`.
  - Заполнить их в `GetTrackMetadata` из `TrackEntity` или через `id3.ReadTags` и `os.Stat`.

### Этап 2: Расширение JavaScript движка плеера
- **`[MODIFY]` [`web/static/js/player.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/player.js)**:
  - Добавить поля состояния:
    - `audioCtx`, `analyserNode`, `audioSourceNode`, `visualizerRunning`
    - `centerDeckMode` (`'spectrum'`, `'lyrics'`, `'specs'`)
    - `lyricsLines`, `currentLyricIndex`, `lyricsLoading`, `lyricsError`, `lyricsType`
    - `stereoPeakL`, `stereoPeakR` (VU levels)
  - Методы:
    - `initAudioVisualizer(canvasEl)`: подключение Web Audio API к `<audio>` и запуск Canvas loop.
    - `loadLyrics(track)`: загрузка и парсинг `.lrc`/`.txt`.
    - `updateLyrics(currentTime)`: подсветка строки и плавный скролл.
    - `removeFromQueue(index)`, `moveInQueue(fromIdx, toIdx)`, `clearQueue()`
    - `openPlayerTab()`: переключение на `$store.app.activeTab = 'player'`.

### Этап 3: Навигация в шапке
- **`[MODIFY]` [`web/templates/components/header.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/header.html)**:
  - Добавить 4-ю кнопку «Плеер» в навигационный блок:
    ```html
    <button
        @click="$store.app.activeTab = 'player'; $store.player.onTabOpened()"
        :class="$store.app.activeTab === 'player' ? 'bg-emerald-600 text-white font-medium shadow' : 'text-gray-400 hover:text-gray-200'"
        class="px-3 sm:px-4 py-1.5 rounded-md text-sm transition-all flex items-center space-x-2">
        <svg class="w-4 h-4" ...>...</svg>
        <span>Плеер</span>
        <span x-show="$store.player.isPlaying" class="w-2 h-2 rounded-full bg-emerald-400 animate-pulse"></span>
    </button>
    ```

### Этап 4: Разработка шаблона выделенной вкладки плеера
- **`[NEW]` [`web/templates/components/player_tab.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/player_tab.html)**:
  - Корневой контейнер с условием `x-show="$store.app.activeTab === 'player'"`.
  - Студийная рамка с аппаратной гравировкой, светодиодными индикаторами питания и стереоканала.
  - **Левая палуба:** Кассета трека, большой виниловый диск/обложка, кнопки воспроизведения, VU-метр, слайдер времени и громкости.
  - **Центральная палуба:** CRT-монитор с тумблером режимов (Спектроанализатор / Караоке LRC / Паспорт аудио).
  - **Правая палуба:** Очередь треков с динамическим эквалайзером, драгом/перемещением и мгновенным запуском.

### Этап 5: Интеграция в index.html и мини-плеер
- **`[MODIFY]` [`web/templates/index.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/index.html)**:
  - Включение `{{ template "player_tab" . }}` внутри тега `<main>`.
- **`[MODIFY]` [`web/templates/components/audio_player.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/audio_player.html)**:
  - Добавление кнопки «Развернуть консоль» и клика по названию/обложке для вызова `$store.player.openPlayerTab()`.

---

## 5. Анализ рисков и краевых случаев (Risks, Edge Cases & Guardrails)

| Риск | Описание | Стратегия митигации |
|---|---|---|
| **InvalidStateError в Web Audio API** | Повторный вызов `createMediaElementSource(audioEl)` выбрасывает исключение | Сохранять `audioSourceNode` в инстансе стора и вызывать конструктор источника строго один раз. |
| **Блокировка AudioContext политикой автоплея** | Браузер переводит `AudioContext` в состояние `suspended` до клика | Автоматический вызов `audioCtx.resume()` внутри обработчика `playTrack()` и `togglePlay()`. |
| **Утечка ресурсов при фоновом рендеринге** | Canvas перерисовывается в цикле rAF при скрытой вкладке | Проверять флаг `$store.app.activeTab === 'player' && $store.player.isPlaying`. Если вкладка скрыта — прерывать rAF-луп. |
| **Трек без LRC (только TXT или без слов)** | Нет таймкодов для караоке | Отображать структурированный чистый текст с моноширинными номерами строк или плашку «Текст отсутствует» с кнопкой поиска в LRCLIB. |
| **Пустая очередь воспроизведения** | Пользователь открыл вкладку до запуска какого-либо трека | Отображать элегантный standby-экран студийной деки («NO TAPE LOADED», индикаторы в режиме ожидания, кнопка выбора трека из медиатеки). |

---

## 6. План тестирования и верификации (Verification Matrix)

1. **Автоматизированные тесты бэкенда:**
   - Запуск `go test -v -race ./...` (все тесты зеленые, 0 гонок).
2. **Сборка бинарного файла:**
   - `go build -ldflags="-s -w" -o bin/music-manager cmd/server/main.go`
3. **Ручная верификация UI:**
   - Переход на вкладку «Плеер» через шапку и через клик по мини-плееру.
   - Запуск трека: проверка анимации частотного спектра на Canvas.
   - Переключение в режим «Текст»: проверка плавного караоке-скроллинга и перемотки по клику на строчку.
   - Переключение в режим «Паспорт»: проверка корректности битрейта, размера файла и ID3-тегов.
   - Управление очередью: переключение треков, удаление трека из очереди, проверка живого эквалайзера рядом с текущим треком.
   - Проверка энергоэффективности: сворачивание вкладки «Плеер» останавливает rAF-цикл.

---

## 7. Критерии приёмки (Definition of Done)

- [x] Вкладка «Плеер» доступна в шапке сайта и открывается по клику на нижний мини-плеер.
- [x] Дизайн выполнен строго в индустриальном стиле Teenage Engineering / Studio Console (без шаблонного градиентного «нейрослопа»).
- [x] Реалтайм эквалайзер на Canvas работает через Web Audio API с частотной сеткой и пик-холдом.
- [x] Синхронизированный караоке-текст плавно скроллится и позволяет мгновенно перематывать трек по клику.
- [x] Очередь треков поддерживает интерактивное управление и индикацию активного трека.
- [x] Аудиофильский паспорт трека отображает полные технические параметры.
- [x] `go test -race ./...` проходит на 100% без ошибок.
- [x] Бинарный файл успешно компилируется.

