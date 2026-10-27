# План реализации: Комплексное устранение 17 багов и доработок медиа-менеджера (5 фаз)

- **Дата составления:** 2026-10-26  
- **Статус плана:** `PROPOSED (Ожидает утверждения)`  
- **Целевые компоненты:**  
  - **Фаза 1 (Поиск, Резолвинг, Lyrics, Конкурентность):**  
    - [`internal/services/resolver/uncensored_matcher.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/uncensored_matcher.go) (Ранжирование кандидатов, исключение ложных «совпадений»)
    - [`internal/services/resolver/matcher.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/matcher.go) (Верификация аудио-кандидатов, порог схожести)
    - [`internal/services/resolver/client_ytmusic.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/client_ytmusic.go) (Парсинг VideoID из Innertube API)
    - [`internal/services/resolver/candidate_search.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/candidate_search.go) (Мульти-поиск кандидатов, расширенный таймаут)
    - [`internal/features/downloader/service.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/features/downloader/service.go) (Авто-поиск Lyrics, сохранение .lrc, лимит 2 воркеров)
    - [`internal/config/config.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/config/config.go) (Дефолт `MAX_CONCURRENT_DOWNLOADS=2`)
    - [`web/templates/components/downloader_active.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/downloader_active.html) (Кнопка открытия ручного выбора источника)
  - **Фаза 2 (Очередь загрузчика и переход «В теги все»):**  
    - [`web/static/js/store/queue_editor_bridge.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/store/queue_editor_bridge.js) (Группировка треков по релизам)
    - [`web/static/js/puddletag/puddletag_workstation.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/puddletag/puddletag_workstation.js) (Мульти-релизная сессия, сохранение массива jobIds)
    - [`web/static/js/tag_editor/saver.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/tag_editor/saver.js) (Удаление/принятие всех jobIds после сохранения)
    - [`web/templates/components/downloader_queue.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/downloader_queue.html) (Постоянная видимость «Принять релиз», кнопки «Принять все» и «Очистить завершённые»)
    - [`web/static/js/downloader/queue.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/downloader/queue.js) (Методы `acceptAllReadyJobs` и `clearCompletedJobs`)
  - **Фаза 3 (Теги, Каноничные жанры, Авторы альбома и Аудит):**  
    - [`internal/features/audit/service.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/features/audit/service.go) (Индивидуальное извлечение исполнителей и соавторов каждого трека)
    - [`internal/id3/genres.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/id3/genres.go) (Канонический нормализатор жанров)
    - [`internal/services/organizer/service.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/organizer/service.go) & [`internal/features/tageditor/service.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/features/tageditor/service.go) (Двустороннее правило синхронизации Album Artist и Artist)
  - **Фаза 4 (Радар релизов, даты и фильтрация):**  
    - [`internal/features/radar/discography_aggregator.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/features/radar/discography_aggregator.go) (Корректный фоллбэк даты релиза)
    - [`internal/db/radar_radio_queries.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/db/radar_radio_queries.go) (Исключение уже скачанных релизов через унифицированный `CleanReleaseTitle`)
  - **Фаза 5 (Контраст интерфейса и скорость вкладки «Файлы»):**  
    - [`web/templates/components/`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/) (Устранение белого текста на белых кнопках `text-zinc-950`)
    - [`internal/api/handlers/file_handlers.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/file_handlers.go) (Оптимизация нерекурсивного чтения, кэш метаданных SQLite, ликвидация синхронных обходов подпапок)
    - [`web/templates/components/header.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/header.html) & [`web/static/js/puddletag/puddletag_workstation.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/puddletag/puddletag_workstation.js) (Предотвращение повторных сбросов пути в корень)

- **Архитектурные инварианты (AGENTS.md):**  
  - **Pure Go / Zero-Python / Zero-Node.js:** Сборка в единый статический бинарный файл `bin/music-manager`. Никаких runtime-зависимостей от Python, Node.js, `npm`/`yarn`.
  - **Sandbox обязателен:** Любая работа с путями на диске только через `PathSandbox.Resolve()`.
  - **Аудио-пайплайн:** Скачивание — `yt-dlp`; конвертация — `ffmpeg` в MP3 320 kbps CBR / 44.1 kHz; теги — строго ID3v2.4 UTF-8 (`\x00` внутри фрейма, `\\` только в UI).
  - **Инвариант текстов песен (Lyrics):** Тексты песен сохраняются строго в виде внешних файлов `.lrc` / `.txt` рядом с MP3 файлом. **Категорически запрещено вшивать тексты в ID3-теги MP3 (USLT фрейм)**.
  - **No Docker:** Работа через локальный процесс и `systemd`.

---

## 1. Цель и границы задачи (Objective & Scope)

### 1.1. Проблематика
В текущей версии медиа-менеджера выявлено 17 дефектов и узких мест, распределенных по всему жизненному циклу аудио: от поиска и скачивания до каталогизации, редактирования метаданных и интерфейса:
1. Загрузчик помечает трек статусом «Совпадение», не проверив реальное совпадение исполнителя/названия (только по хронометражу), и скачивает чужие дорожки или завершается ошибкой.
2. Ручной поиск альтернативных источников в строке трека возвращает пустоту из-за нераспарсенного `VideoID` в Innertube API и короткого таймаута; в карточке активной загрузки кнопка ручного выбора источника вообще отсутствует.
3. При резолвинге свежих релизов (например, альбомов `h8.hood` и `апология`) матчер хватает случайные треки с одинаковой длительностью вместо реальных дорожек.
4. Тексты песен (`.lrc`) не подтягиваются автоматически в момент первичного скачивания треков.
5. Загрузчик качает строго по 1 релизу одновременно вместо 2 параллельных.
6. Кнопка «В теги все» сваливает все треки нескольких альбомов в один плоский список без сохранения структуры релизов.
7. После сохранения нескольких релизов из «В теги все» из списка загрузок удаляется только последний `job_id`.
8. Кнопка «Принять релиз» в очереди скрыта на десктопе (`opacity-0`), пока курсор не наведен на строку.
9. В очереди загрузчика отсутствует кнопка массового принятия «Принять все».
10. Отсутствует выделенная кнопка «Очистить завершённые».
11. Исправление тегов в «Аудите» берет общее имя артиста альбома с Deezer, теряя индивидуальных исполнителей и фиты отдельных треков.
12. Входящие жанры при скачивании и подстановке из каталога не приводятся к каноническим жанрам проекта.
13. Нарушается логика формирования `Album Artist`: если исполнитель присутствует на всех треках, он обязан входить в `Album Artist`, а если указан в `Album Artist`, обязан быть на каждом треке.
14. В Радаре старые альбомы отображаются с сегодняшней датой краулинга вместо реальной даты выпуска.
15. Радар предлагает скачать альбомы, которые уже есть в медиатеке, из-за различий в алгоритмах очистки строк (`CleanReleaseTitle`).
16. Недостаточный контраст текста на кнопках со светлым фоном (`text-zinc-950`).
17. Медленная загрузка вкладки «Файлы» из-за синхронного чтения ID3-тегов с диска и чтения подпапок без использования SQLite-кэша.

### 1.2. Целевой результат
Все 17 пунктов исправляются в рамках 5 последовательных фаз, полностью покрываются регрессионными Go-тестами и верифицируются через браузер с помощью Playwright MCP.

---

## 2. Анализ текущего состояния (Current State Analysis)

| № | Пункт | Затронутые файлы | Первопричина дефекта |
|---|---|---|---|
| 1 | Ложное «совпадение» | [`internal/services/resolver/uncensored_matcher.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/uncensored_matcher.go#L35-L65)<br>[`internal/services/resolver/matcher.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/matcher.go#L165-L210) | `RankCandidates` присваивает `status = "likely"` исключительно по дельте хронометража (`deltaMin <= tolerance`), игнорируя проверку текстового совпадения названия и исполнителя. В `matcher.go` совпадение длительности дает +35 баллов при общем пороге 25, пропуская абсолютно чужие видео. |
| 2 | Сбой ручного выбора источника | [`internal/services/resolver/client_ytmusic.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/client_ytmusic.go#L330-L345)<br>[`internal/services/resolver/candidate_search.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/candidate_search.go#L70-L75)<br>[`web/templates/components/downloader_active.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/downloader_active.html#L110-L130) | `parseYtResponsiveListItem` не заполнял поле `VideoID` в объекте `YtMusicTrack`. В `candidate_search.go` проверка `if tr.VideoID == ""` отбрасывала 100% треков YouTube Music. Жесткий 12-секундный таймаут приводил к отсечке поиска `yt-dlp`. В `downloader_active.html` отсутствовала кнопка вызова `$store.sourcePicker`. |
| 3 | Чужие треки свежих релизов (`h8.hood`, `апология`) | [`internal/services/resolver/matcher.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/matcher.go#L240-L310) | Недостаточная строгость скоринга при отсутствии трека в первичном источнике: при нулевом совпадении автора трек с похожим хронометражем отбирался как «лучший кандидат» вместо перехода в статус ошибки/ожидания ручного источника. |
| 4 | Автоматический поиск Lyrics при скачивании | [`internal/features/downloader/service.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/features/downloader/service.go#L540-L580) | Поиск текстов в `processSingleTrack` не вызывался для каждого скачанного трека либо вызывался со строкой `Artist 1\\Artist 2` (LRCLIB требует чистый основной артист). Не сохранялись несинхронизированные тексты `.txt` при отсутствии `.lrc`. |
| 5 | Ограничение очереди в 1 релиз | [`internal/features/downloader/service.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/features/downloader/service.go#L110-L130)<br>[`cmd/server/main.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/cmd/server/main.go#L185-L195) | Число воркеров скачивания релизов по умолчанию устанавливалось в 1 или ограничивалось семафором на 1 параллельный релиз. |
| 6 | Сваливание всех релизов в один список | [`web/static/js/store/queue_editor_bridge.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/store/queue_editor_bridge.js#L50-L85)<br>[`web/static/js/puddletag/puddletag_workstation.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/puddletag/puddletag_workstation.js#L350-L390) | Функция `openAllReadyJobsInTagEditor` объединяла треки всех готовых задач в плоский массив `allTracks`, перезаписывая метаданные альбома первым попавшимся релизом (`firstAlbum`). |
| 7 | Удаление только последнего релиза | [`web/static/js/puddletag/puddletag_workstation.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/puddletag/puddletag_workstation.js#L370-L375)<br>[`web/static/js/tag_editor/saver.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/tag_editor/saver.js#L130-L145) | В `loadJobData` сохранялся только `this.jobId = payload.jobIds[0]`. При сохранении `saveAllChanges` отправлялся запрос на удаление только одного `jobId`. |
| 8 | Скрытая кнопка «Принять релиз» | [`web/templates/components/downloader_queue.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/downloader_queue.html#L180-L195) | Кнопка «Принять» имела классы `md:opacity-0 md:group-hover:opacity-100`, из-за чего была скрыта на десктопе до наведения курсора мыши. |
| 9 | Нет кнопки «Принять все» | [`web/templates/components/downloader_queue.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/downloader_queue.html#L60-L90)<br>[`web/static/js/downloader/queue.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/downloader/queue.js) | В тулбаре очереди не было кнопки и метода `acceptAllReadyJobs()`. |
| 10 | Нет кнопки «Очистить завершённые» | [`web/templates/components/downloader_queue.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/downloader_queue.html#L80-L100) | Присутствовала только общая кнопка «Очистить», пропускавшая статус `PARTIAL_READY` и без явной визуальной семантики. |
| 11 | Потеря исполнителей треков в «Аудите» | [`internal/features/audit/service.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/features/audit/service.go#L430-L480) | При подтягивании данных альбома с Deezer брался `track.Artist.Name` из общего списка альбома (где указан только артист релиза), без параллельного запроса `GetTrack(id)` или анализа `contributors`. |
| 12 | Неканоничные жанры | [`internal/services/resolver/resolver_deezer.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/resolver_deezer.go)<br>[`internal/features/downloader/service.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/features/downloader/service.go)<br>[`web/static/js/puddletag/sources.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/puddletag/sources.js) | Сырые строковые жанры из источников (`Pop/Rock`, `Rap/Hip-Hop`) не пропускались через `id3.AnalyzeAndNormalizeGenre()`. |
| 13 | Рассинхрон Album Artist и Artist | [`internal/services/organizer/service.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/organizer/service.go)<br>[`internal/id3/tags.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/id3/tags.go) | Отсутствовало принудительное применение двустороннего инварианта: общий артист всех треков $\rightarrow$ `Album Artist`, а каждый артист из `Album Artist` $\rightarrow$ в список артистов каждого трека. |
| 14 | Неверные даты старых альбомов в Радаре | [`internal/features/radar/discography_aggregator.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/features/radar/discography_aggregator.go#L210-L245) | При парсинге дискографии дата добавления в базу ошибочно замещала `release_date`. Не отсекались релизы старше 1 года с поддельной свежей датой переиздания. |
| 15 | Не исключаются скачанные релизы из Радара | [`internal/db/radar_radio_queries.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/db/radar_radio_queries.go#L110-L150) | Разные алгоритмы санитизации названий: `CleanReleaseTitle` удалял пунктуацию (`where is the love`), а `clean_title` в SQLite базе сохранял (`where is the love?`), из-за чего `JOIN` не находил совпадения. |
| 16 | Белый текст на белых кнопках | [`web/templates/components/`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/) | Кнопки и бейджи со светлыми фонами (`bg-white`, `bg-zinc-100`) не имели явного контрастного класса `text-zinc-950`. |
| 17 | Медленная загрузка вкладки «Файлы» | [`internal/api/handlers/file_handlers.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/file_handlers.go#L348-L395)<br>[`web/static/js/puddletag/puddletag_workstation.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/puddletag/puddletag_workstation.js#L284-L315) | В нерекурсивном режиме `/files` для каждой подпапки выполнялся синхронный `os.ReadDir`, а для файлов вызывался `id3.ReadTags` с диска вместо запроса в SQLite-кэш `GetTracksMetadataCache`. Фронтенд сбрасывал путь в корень при каждом клике по вкладке. |

---

## 3. Архитектурное решение и обоснование (Architecture & Rationale)

### 3.1. Архитектурные принципы и компромиссы (Trade-off Matrix)

| Компонент / Задача | Вариант 1 (Отвергнуто) | Вариант 2 (Выбрано) | Почему именно так (Обоснование) |
|---|---|---|---|
| **Резолвинг аудио (Пункт 1, 3)** | Мягкий нечеткий поиск с принятием любого трека близкой длительности | Строгая двухфакторная верификация: нормализованный артист + сходство названия $\ge 0.65$ + $\Delta t \le 8$ сек. Если нет — статус `unverified`/`waiting_source` | Предотвращает скачивание абсолютно чужих треков под видом свежих релизов (кейсы `h8.hood`, `апология`). Лучше запросить ручной выбор источника, чем загрязнить медиатеку мусором. |
| **Кандидаты Innertube (Пункт 2)** | Только эвристический парсинг `yt-dlp` через CLI | Извлечение точного `VideoID` из `runs[0].NavigationEndpoint.WatchEndpoint.VideoID` + запасной парсинг `thumbnailOverlay` + увеличение таймаута до 25с | Нативный HTTP-запрос к Innertube работает за 150-300мс (в 10 раз быстрее запуска Python `yt-dlp`), а извлечение `VideoID` гарантирует 100% валидные ссылки `https://music.youtube.com/watch?v=...`. |
| **Lyrics Pipeline (Пункт 4)** | Запись текстов в тег ID3 (USLT фрейм) | Сохранение строго во внешние файлы `.lrc` (синхронизированный) или `.txt` (несинхронизированный) рядом с MP3 | **Жёсткий инвариант AGENTS.md:** тексты песен никогда не вшиваются в MP3. Внешний `.lrc` универсален для всех плееров и не раздувает аудиофайл. |
| **«В теги все» (Пункт 6, 7)** | Склеивать все треки в единый массив с общим фиктивным альбомом | Мульти-релизная сессия в тег-редакторе: структура `groups: [{ jobId, album, artist, tracks }]` с отдельными табами/разделителями | Сохраняет физическую структуру каталогов медиатеки `{Artist}/{Album}/...` и позволяет атомарно удалить все завершенные `jobIds` после сохранения. |
| **Кэш вкладки «Файлы» (Пункт 17)** | Читать ID3-теги с диска при каждом обращении к папке | Использование SQLite-кэша `GetTracksMetadataCache` для нерекурсивного листинга + устранение синхронного `ReadDir` подпапок | Ускоряет отклик `/files` с 1.5–3 секунд до <15 миллисекунд даже на медиатеках из десятков тысяч треков. |

---

## 4. Пошаговая декомпозиция реализации (Step-by-Step Implementation)

### Фаза 1: Поиск, Резолвинг, Lyrics и Конкурентность загрузчика (Пункты 1–5)

#### Этап 1.1: Ужесточение верификации кандидатов и устранение ложных «совпадений» (Пункты 1, 3)
- **Файлы:**
  - `[MODIFY]` [`internal/services/resolver/uncensored_matcher.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/uncensored_matcher.go)
  - `[MODIFY]` [`internal/services/resolver/matcher.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/matcher.go)
  - `[NEW]` [`internal/services/resolver/matcher_strict_test.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/matcher_strict_test.go)
- **Логика изменений:**
  - В `RankCandidates` добавить проверку сходства строк названия (`StringSimilarity(cleanCandTitle, cleanTargetTitle)`) и совпадения исполнителя.
  - Присваивать статус `"likely"` **только** если сходство названий $\ge 0.65$ И исполнитель совпадает (либо содержится в названии ролика) И $\Delta t \le 8$ секунд.
  - Если совпадает только длительность, но название или исполнитель чужие — помечать кандидата как `"unverified"` и отбрасывать из автоматического выбора.
  - В `FindYouTubeMatchWithScore`: убрать возможность набрать проходной балл ($>25$) исключительно за счет хронометража. Если схожесть названий $< 0.40$, результирующий скор обнуляется.
  - Написать модульные тесты для кейсов `h8.hood` и `апология`, подтверждающие отсечение чужих треков с идентичным хронометражем.

#### Этап 1.2: Починка ручного выбора источника (Пункт 2)
- **Файлы:**
  - `[MODIFY]` [`internal/services/resolver/client_ytmusic.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/client_ytmusic.go)
  - `[MODIFY]` [`internal/services/resolver/candidate_search.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/candidate_search.go)
  - `[MODIFY]` [`web/templates/components/downloader_active.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/downloader_active.html)
  - `[MODIFY]` [`internal/features/downloader/service.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/features/downloader/service.go)
- **Логика изменений:**
  - В `parseYtResponsiveListItem` расширить структуру и парсить `video_id`:
    ```go
    // Извлечение videoId из NavigationEndpoint первого flexColumn или overlay
    var videoID string
    if len(runs0) > 0 && runs0[0].NavigationEndpoint != nil && runs0[0].NavigationEndpoint.WatchEndpoint != nil {
        videoID = runs0[0].NavigationEndpoint.WatchEndpoint.VideoID
    }
    if videoID == "" && r.PlaylistData != nil {
        videoID = r.PlaylistData.VideoID
    }
    ```
  - В `SearchMultiCandidates` увеличить общий таймаут поиска с 12 до 25 секунд для стабильной работы внешних поисков `scsearch` и `ytsearch`.
  - В `downloader_active.html` добавить кнопку вызова `$store.sourcePicker.openForQueueTrack(job, track)` аналогично `downloader_queue.html`.
  - В `internal/features/downloader/service.go` в методе `BindTrackSource` обеспечить скачивание трека по выбранному URL и немедленное обновление состояния трека в памяти активной задачи.

#### Этап 1.3: Автоматический поиск и сохранение текстов песен (LRC) (Пункт 4)
- **Файлы:**
  - `[MODIFY]` [`internal/features/downloader/service.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/features/downloader/service.go)
  - `[NEW]` [`internal/features/downloader/lyrics_auto_test.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/features/downloader/lyrics_auto_test.go)
- **Логика изменений:**
  - В методе `processSingleTrack` после успешной конвертации MP3 вызвать:
    ```go
    primaryArtist := id3.ExtractPrimaryArtist(track.Artist)
    lrcRes, err := s.lyrics.SearchLyrics(ctx, primaryArtist, track.Title, track.Album, track.Duration)
    if err == nil && lrcRes != nil && lrcRes.Content != "" {
        _ = s.lyrics.SaveLRC(ctx, mp3Path, lrcRes.Content)
    }
    ```
  - Гарантировать: в ID3-теги текст песни **не пишется**. Рядом с файлом сохраняется `.lrc` (для таймированных текстов) или `.txt` (для обычных).

#### Этап 1.4: Увеличение параллелизма загрузчика до 2 релизов (Пункт 5)
- **Файлы:**
  - `[MODIFY]` [`internal/config/config.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/config/config.go)
  - `[MODIFY]` [`internal/features/downloader/service.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/features/downloader/service.go)
- **Логика изменений:**
  - Установить значение по умолчанию `MaxConcurrentDownloads = 2` в конфигурации.
  - В `Service.Start()` инициализировать семафор / пул горутин на 2 одновременные задачи релизов.

---

### Фаза 2: Очередь загрузчика и переход «В теги все» (Пункты 6–10)

#### Этап 2.1: Группировка релизов при «В теги все» и раздельное сохранение (Пункты 6, 7)
- **Файлы:**
  - `[MODIFY]` [`web/static/js/store/queue_editor_bridge.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/store/queue_editor_bridge.js)
  - `[MODIFY]` [`web/static/js/puddletag/puddletag_workstation.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/puddletag/puddletag_workstation.js)
  - `[MODIFY]` [`web/static/js/tag_editor/saver.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/tag_editor/saver.js)
  - `[MODIFY]` [`web/templates/components/puddletag_grid.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/puddletag_grid.html)
- **Логика изменений:**
  - `queue_editor_bridge.js`: формировать пейлоад с массивом релизов:
    ```javascript
    const payload = {
        mode: 'multi_release',
        jobIds: readyJobs.map(j => j.id),
        releases: readyJobs.map(j => ({
            jobId: j.id,
            artist: j.artist_name,
            album: j.album_title,
            targetPath: j.target_album_path,
            coverUrl: j.cover_url,
            tracks: j.tracks
        }))
    };
    ```
  - В `puddletag_workstation.js` сохранять `this.jobIds = payload.jobIds`. В таблице треков отображать заголовки/разделители релизов с указанием целевого альбома.
  - В `saver.js` при сохранении отправлять запросы `acceptJobDirectly(jobId)` по всем `jobIds` из массива, удаляя все сохраненные релизы из очереди.

#### Этап 2.2: Видимость «Принять релиз», кнопки «Принять все» и «Очистить завершённые» (Пункты 8, 9, 10)
- **Файлы:**
  - `[MODIFY]` [`web/templates/components/downloader_queue.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/downloader_queue.html)
  - `[MODIFY]` [`web/static/js/downloader/queue.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/downloader/queue.js)
- **Логика изменений:**
  - Удалить `md:opacity-0 md:group-hover:opacity-100` у кнопки «Принять» в карточке релиза: кнопка должна быть видна всегда при статусе `READY` или `PARTIAL_READY`.
  - В тулбар очереди добавить кнопку «Принять все» со счетчиком готовых:
    ```html
    <button type="button" @click="$store.app.queue.acceptAllReadyJobs()"
            x-show="$store.app.queue.readyJobsCount > 0"
            class="px-2.5 py-1 bg-emerald-500 hover:bg-emerald-400 text-zinc-950 rounded-md text-xs font-bold transition flex items-center space-x-1 shadow-xs">
        <span>Принять все (<span x-text="$store.app.queue.readyJobsCount"></span>)</span>
    </button>
    ```
  - Добавить выделенную кнопку «Очистить завершённые» (`clearCompletedJobs()`), очищающую задачи в статусах `READY`, `PARTIAL_READY`, `CANCELLED`, `ERROR`.

---

### Фаза 3: Теги, Каноничные жанры, Авторы альбома и Аудит (Пункты 11–13)

#### Этап 3.1: Индивидуальные исполнители и фиты в Аудите (Пункт 11)
- **Файлы:**
  - `[MODIFY]` [`internal/features/audit/service.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/features/audit/service.go)
  - `[NEW]` [`internal/features/audit/audit_contributors_test.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/features/audit/audit_contributors_test.go)
- **Логика изменений:**
  - При выполнении исправления тегов через каталог в `CatalogMatchAlbum`:
    - Не присваивать слепо `album.Artist` каждому треку.
    - Выполнять параллельные запросы к эндпоинту трека (`/track/{id}`) или парсить `contributors` каждого трека с помощью `s.enricher.EnrichTrackArtists`.
    - Извлекать соавторов из названий вида `Title (feat. Artist B)`.

#### Этап 3.2: Нормализация каноничных жанров (Пункт 12)
- **Файлы:**
  - `[MODIFY]` [`internal/features/downloader/service.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/features/downloader/service.go)
  - `[MODIFY]` [`internal/services/resolver/resolver_deezer.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/resolver/resolver_deezer.go)
  - `[MODIFY]` [`web/static/js/puddletag/sources.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/puddletag/sources.js)
- **Логика изменений:**
  - Прогонять сырые жанры через `id3.AnalyzeAndNormalizeGenre()` непосредственно при парсинге релиза и сохранении в Job.
  - В `sources.js` (База тегов на фронтенде) нормализовать жанры перед подстановкой в инпуты редактора тегов.

#### Этап 3.3: Двустороннее правило Album Artist и Artist (Пункт 13)
- **Файлы:**
  - `[MODIFY]` [`internal/services/organizer/service.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/organizer/service.go)
  - `[MODIFY]` [`internal/id3/tags.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/id3/tags.go)
  - `[NEW]` [`internal/id3/album_artist_sync_test.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/id3/album_artist_sync_test.go)
- **Логика изменений:**
  - Реализовать функцию синхронизации `SyncAlbumArtistAndTrackArtists(tracks []TrackMetadata, albumArtist string) ([]TrackMetadata, string)`:
    1. Найти пересечение множеств артистов всех треков: если артист присутствует на 100% треков, он включается в `Album Artist`.
    2. Если артист указан в `Album Artist`, проверить каждый трек; если его там нет, добавить в тег `Artist` трека.

---

### Фаза 4: Радар релизов, даты и фильтрация (Пункты 14–15)

#### Этап 4.1: Корректный фоллбэк даты релиза в Радаре (Пункт 14)
- **Файлы:**
  - `[MODIFY]` [`internal/features/radar/discography_aggregator.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/features/radar/discography_aggregator.go)
- **Логика изменений:**
  - Использовать строго `release_date` альбома из провайдера.
  - Если дата релиза отсутствует, брать год выпуска и формировать `YYYY-01-01`. Ни в коем случае не подставлять `time.Now()` в качестве даты релиза старого альбома.
  - Фильтровать релизы из фида «Новинки», если их реальный год выпуска старше 1 года от текущей даты.

#### Этап 4.2: Исключение уже скачанных релизов из Радара (Пункт 15)
- **Файлы:**
  - `[MODIFY]` [`internal/features/radar/discography_aggregator.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/features/radar/discography_aggregator.go)
  - `[MODIFY]` [`internal/db/radar_radio_queries.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/db/radar_radio_queries.go)
  - `[MODIFY]` [`internal/services/organizer/service.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/services/organizer/service.go)
- **Логика изменений:**
  - Унифицировать нормализацию названий релизов: удаление знаков препинания, приведение к нижнему регистру, схлопывание пробелов (`NormalizeCleanTitle`).
  - При принятии/сохранении релиза в `organizer.Service` вызывать `db.MarkRadarReleaseDownloaded(artist, album)`.

---

### Фаза 5: Контраст интерфейса и скорость вкладки «Файлы» (Пункты 16–17)

#### Этап 5.1: Контраст текста на кнопках (Пункт 16)
- **Файлы:**
  - `[MODIFY]` [`web/templates/components/downloader_queue.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/downloader_queue.html)
  - `[MODIFY]` [`web/templates/components/downloader_active.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/downloader_active.html)
  - `[MODIFY]` [`web/templates/components/downloader_source_picker_modal.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/downloader_source_picker_modal.html)
- **Логика изменений:**
  - Провести аудит всех кнопок со светлыми фонами (`bg-white`, `bg-zinc-100`, `bg-amber-400`, `bg-emerald-400`).
  - Гарантировать наличие класса `text-zinc-950 font-semibold` для обеспечения стандарта WCAG AAA по коэффициенту контрастности (> 7:1).

#### Этап 5.2: Оптимизация производительности вкладки «Файлы» (Пункт 17)
- **Файлы:**
  - `[MODIFY]` [`internal/api/handlers/file_handlers.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/file_handlers.go)
  - `[MODIFY]` [`web/templates/components/header.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/header.html)
  - `[MODIFY]` [`web/static/js/puddletag/puddletag_workstation.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/puddletag/puddletag_workstation.js)
- **Логика изменений:**
  - В `ListFiles` при `recursive == false`:
    - Использовать SQLite-кэш метаданных `h.database.GetTracksMetadataCache(r.Context())` для чтения тегов MP3-файлов текущей директории вместо медленного вызова `id3.ReadTags` с диска.
    - Убрать синхронный цикл `os.ReadDir(itemFull)` внутри каждой подпапки в корне; количество треков вычислять быстро через базу или возвращать по требованию.
  - В `header.html`:
    - Не отправлять `$dispatch('refresh-file-manager', '')` со сбросом пути, если пользователь уже находится в нужной папке.
  - В `puddletag_workstation.js`:
    - Устранить дублирующий вызов `loadDirectory` при переключении вкладки на `files`.
    - Добавить LRU-кэш на 20 последних просмотренных папок.

---

## 5. Анализ рисков и краевых случаев (Risks, Edge Cases & Guardrails)

1. **Риск ложных срабатываний фильтра кандидатов:**  
   - *Проблема:* У редких андерграундных артистов или инструменталов названия на YouTube/SoundCloud могут содержать транслит или опечатки.  
   - *Митигация:* Использовать нечеткое сопоставление с порогом 0.65 и транслитерацию RU/EN перед отклонением кандидата. Если авто-выбор не уверен, выставлять `status = "waiting_source"` и сразу давать пользователю удобный выбор из найденного списка через Source Picker.
2. **Риск превышения лимитов запросов к провайдерам:**  
   - *Проблема:* Параллельный сбор соавторов треков альбома с Deezer может вызвать Rate Limit HTTP 429.  
   - *Митигация:* Ограничить пул одновременных запросов к Deezer API до 5 горутин с экспоненциальной задержкой.
3. **Сохранение внешних файлов текстов (.lrc):**  
   - *Проблема:* Имена файлов могут содержать спецсимволы операционной системы.  
   - *Митигация:* Использовать `PathSandbox.Resolve` и функцию санитизации имен `SanitizeFilename`. Категорически не писать в ID3 тег.
4. **Удаление задач из очереди после пакетного сохранения:**  
   - *Проблема:* Если один из альбомов сохранился с ошибкой, нельзя удалять все `jobIds`.  
   - *Митигация:* Удалять `jobId` строго после успешной записи файлов конкретного релиза на диск.

---

## 6. План тестирования и верификации (Verification Matrix)

### 6.1. Автоматизированные тесты бэкенда (`go test`)
- Запуск полного набора тестов: `go test -v -race ./...`.
- Новые юнит-тесты:
  - `TestUncensoredMatcher_H8Hood_And_Apologiya`: проверка строгого отсечения чужих треков с идентичной длительностью.
  - `TestYouTubeMusicClient_ParseVideoID`: проверка корректного извлечения `VideoID` из структуры Innertube.
  - `TestDownloader_AutomaticLyrics`: проверка автоматического сохранения `.lrc` рядом с треком без записи в ID3.
  - `TestAudit_TrackContributors`: проверка обогащения треков соавторами и фитами.
  - `TestAlbumArtistSync_Bilateral`: проверка двусторонней синхронизации `Album Artist` и `Artist`.
  - `TestRadar_DownloadedExclusion`: проверка исключения скачанного альбома из фида радара.
  - `TestFileHandlers_FastDirectoryListing`: бенчмарк скорости листинга папок с кэшем метаданных.

### 6.2. Сквозное UI-тестирование через Playwright MCP
- **Сценарий 1 (Очередь и принятие):** Добавление тестового релиза $\rightarrow$ проверка постоянной видимости кнопки «Принять релиз» без наведения $\rightarrow$ проверка работы кнопки «Принять все» и «Очистить завершённые».
- **Сценарий 2 (Ручной выбор источника):** Открытие модального окна выбора источника трека из активной загрузки $\rightarrow$ мульти-поиск $\rightarrow$ отображение кандидатов YouTube Music и SoundCloud.
- **Сценарий 3 («В теги все»):** Загрузка двух тестовых релизов $\rightarrow$ клик «В теги все» $\rightarrow$ верификация отображения двух раздельных групп альбомов $\rightarrow$ сохранение $\rightarrow$ проверка исчезновения обоих релизов из очереди.
- **Сценарий 4 (Контраст):** Проверка DOM-дерева на отсутствие элементов со светлым фоном без темного текста (`text-zinc-950`).
- **Сценарий 5 (Скорость файлов):** Замер времени загрузки вкладки «Файлы» (должно быть $< 300$ мс в браузере).

---

## 7. Критерии приемки (Definition of Done)

- [ ] Все 17 пунктов из ТЗ закрыты в соответствии с планом.
- [ ] Все юнит-тесты и тесты с детектором гонок проходят: `go test -v -race ./...`.
- [ ] Бинарный файл собирается без ошибок: `go build -ldflags="-s -w" -o bin/music-manager cmd/server/main.go`.
- [ ] Проведена сквозная проверка через Playwright MCP в браузере.
- [ ] Инварианты AGENTS.md (Sandbox, Zero-Python, No Docker, No NPM, тексты песен строго в `.lrc`) не нарушены.
- [ ] Файл `PROGRESS.md` актуализирован.
