# Архитектурный план: Модернизация модуля загрузки треков (Downloader Engine)

**Дата:** 16 сентября 2026  
**Статус:** `PROPOSED` (Ожидает подтверждения пользователя)  
**Приоритеты:**
- **P0:** Внедрение приоритетного поиска в SoundCloud (SoundCloud-First Matcher) для скачивания нецензурированных версий треков (Explicit/Uncut) и обхода антибота YouTube
- **P0:** Устранение ошибки «Ни один из треков релиза не был успешно загружен» (Unicode NFD/NFC, транслитерация артистов, изоляция конфигов `yt-dlp`)
- **P0:** Обход ошибки `Deezer API returned status 403` (Akamai WAF) с браузерными заголовками, поддержкой прокси и умным Fallback на SoundCloud/YouTube
- **P1:** Сохранение очереди задач в SQLite (БД) и реализация механизма повтора упавших задач (`POST /downloads/{id}/retry` + кнопка в UI)
- **P1:** Исправление уязвимости обхода Domain Whitelist и устранение скачков прогресс-бара при параллельном скачивании треков

---

## 1. Анализ выявленных дефектов и первопричин

### 1.1. Поиск аудиопотоков без цензуры (SoundCloud-First)
- **Проблема:** Ранее сопоставитель `AudioMatcher` искал треки исключительно в YouTube (`ytsearch5:`). YouTube часто подсовывает цензурированные версии (Clean / Radio Edit), содержит агрессивный Bot Detection и проверку возраста (Age Gate), а также может скрывать официальные треки.
- **Преимущества SoundCloud:** На SoundCloud выкладываются авторские оригиналы без цензуры, клубные и полные версии треков, нет задержек YouTube bot-detection, а в метаданных авторов присутствуют точные кириллические имена исполнителей (например, официальный профиль «Макс Корж»).

### 1.2. Ошибка «Не удалось получить профиль артиста: deezer api returned status 403»
- **Первопричина:** Запросы к `api.deezer.com` блокируются защитой **Akamai GHost WAF**, так как датацентровые IP-адреса хостинга/VPN (`2a01:ecc0:400:3e1::2`) внесены в списки автоматических систем.
- **Вторичная причина в коде:** В `internal/services/resolver/client_deezer.go` методы `GetArtist`, `GetArtistAlbums`, `SearchAlbums` отправляли стандартный заголовок `User-Agent: Go-http-client/1.1` без браузерных параметров (`Accept`, `Accept-Language`, `Sec-Fetch-*`), мгновенно триггеря WAF.
- **Архитектурный пробел:** Полное отсутствие отказоустойчивого fallback-механизма. При отказе Deezer пользователь получает ошибку, хотя метаданные релиза можно получить через oEmbed/HTML-страницу или спарсить через поисковый движок.

### 1.3. Ошибка «Макс Корж — 2 типа людей 5% Ни один из треков релиза не был успешно загружен»
- **Декомпозиция Unicode (NFD vs NFC):** В YouTube заголовок официального аудио выложен с декомпозицией диакритики: `"Макс Корж - 2 типа людеи\u0306 (Official audio)"` (`и` + объединяющая бреве `\u0306`). В поисковом запросе используется предкомпонованный символ `й` (`\u0439`). Регулярное выражение `nonAlnumReg` в `matcher.go` удаляло `\u0306` как не букву, превращая слово в `людеи `, из-за чего проверка `strings.Contains("людеи", "людей")` возвращала `false`.
- **Языковое несовпадение имени артиста:** Канал на YouTube называется латиницей: `"Max Korzh"`. В метаданных имя указано кириллицей: `"Макс Корж"`. Проверка совпадения канала и артиста давала 0 баллов.
- **Заниженный балл и порог:** Из-за потери баллов на артисте и названии суммарный скор опустился до 20.0 (ниже порога 25.0), и `AudioMatcher` завершился с ошибкой `no suitable audio match found`.
- **Влияние глобального конфига `~/.config/yt-dlp/config`:** Методы `runSearch` и `resolveYtDlp` не передавали флаги `--ignore-config` и `--no-download-archive`.

### 1.4. Потеря очереди задач при перезапуске сервера
- Задачи загрузки хранятся только в оперативной памяти (`JobManager.jobs`). Перезапуск службы `music-manager.service` стирает всю историю, оставляя осиротевшие файлы в `tmp_downloads/`. Неудачные попытки загрузки невозможно запустить заново без повторного ручного ввода ссылок.

---

## 2. Архитектурное решение

```mermaid
flowchart TD
    subgraph Client ["Веб-интерфейс / API"]
        UI["Downloader UI / Queue Drawer"]
    end

    subgraph Core ["Downloader & Resolver Engine"]
        DH["DownloadHandler"]
        JM["JobManager (In-Memory + SQLite Sync)"]
        DS["downloader.Service"]
        SR["SourceResolver"]
        DZ["DeezerClient (Anti-403 + Fallback)"]
        AM["AudioMatcher (SoundCloud-First + YouTube Fallback)"]
    end

    subgraph Storage ["Персистентность и ФС"]
        DB[("SQLite WAL: download_jobs")]
        FS["tmp_downloads/ (TTL Cleaner)"]
    end

    UI -->|POST /downloads/:id/retry| DH
    DH -->|RetryJob| DS
    DS <-->|Sync State| JM
    JM <-->|Save/Load Jobs| DB
    DS -->|Resolve| SR
    SR -->|1. Try Deezer| DZ
    DZ -->|If 403 Blocked| SR
    SR -->|Fallback to Search| AM
    AM -->|1. Try SoundCloud (scsearch)| DS
    AM -.->|2. Fallback YouTube (ytsearch)| DS
    DS -->|Staging MP3 320k| FS
```

### 2.1. Каскадный сопоставитель (SoundCloud-First Audio Matching)
- При поиске аудиопотока для трека первым делом опрашивается SoundCloud: `scsearch3:%s %s`.
- Если кандидат на SoundCloud имеет высокий скор ($\ge 70.0$) и совпадение по автору/длительности, выбирается поток SoundCloud.
- Если на SoundCloud трек не найден или скор низкий (< 70.0), выполняется второй шаг: поиск в YouTube / YouTube Music (`ytsearch5:...`).
- Это обеспечивает:
  1. Скачивание нецензурированных оригиналов (Explicit).
  2. Обход YouTube Bot-detection и ограничений возраста.
  3. Максимальную надежность (fallback на YouTube всегда готов).

### 2.2. Персистентность очереди и механизм Retry
- Таблица `download_jobs` в `internal/db/schema.sql`. При каждом изменении статуса или прогресса задача асинхронно синхронизируется с базой.
- При старте сервиса `JobManager` восстанавливает состояние очереди. Зависшие на момент рестарта задачи переводятся в статус `ERROR` с возможностью мгновенного перезапуска.
- Новый эндпоинт `POST /downloads/{id}/retry`: очищает временные файлы задачи, сбрасывает статус в `QUEUED` и повторно запускает пайплайн.
- В UI на карточках ошибок и в боковой панели очереди добавляется кнопка **«Повторить»**.

### 2.3. Устойчивость к блокировкам Deezer (Akamai 403)
- В `DeezerClient` внедряется `RoundTripper` с полным комплектом современных браузерных заголовков (`Chrome 128`, `sec-ch-ua`, `Accept-Language: ru,en;q=0.9`).
- Добавляется поддержка `DEEZER_PROXY` в `.env`.
- **Anti-Fragile Fallback:** Если Deezer вернул статус 403, сервис автоматически извлекает доступные метаданные через oEmbed/HTML-страницы и переключается на прямой резолвинг через SoundCloud/YouTube Music (`resolveYtDlp`), гарантируя непрерывность скачивания.

### 2.4. Исправление сопоставителя треков (AudioMatcher)
- Приведение строк к Unicode NFC перед очисткой пунктуации (слияние диакритических знаков `\u0306` и `\u0308`).
- Внедрение двусторонней транслитерации для артистов (кириллица <-> латиница: `Макс Корж` <-> `Max Korzh`).
- Обязательная передача `--ignore-config` и `--no-download-archive` во все вызовы `yt-dlp` (`runSearch`, `resolveYtDlp`, `downloadRawStream`).
- Сохранение детализированного описания ошибки первого упавшего трека в `job.Error`.

### 2.5. Безопасность и монотонный прогресс
- Вызов `DetectSource(cleanURL)` выполняется безусловно, исключая bypass белого списка доменов.
- Расчет общего прогресса релиза через агрегирующий массив `trackProgress []float64` исключает скачки шкалы при параллельной загрузке.

---

## 3. Пошаговая декомпозиция реализации

| Этап | Компонент | Файлы | Действие |
|---|---|---|---|
| **1** | **Database** | `internal/db/schema.sql`, `internal/db/queries.go` | `[MODIFY]` Добавление таблицы `download_jobs` и CRUD-методов |
| **2** | **Resolver & Deezer** | `internal/services/resolver/client_deezer.go`, `resolver.go` | `[MODIFY]` Браузерные заголовки, proxy, fallback при 403 |
| **3** | **Matcher (SoundCloud-First)** | `internal/services/resolver/matcher.go`, `matcher_test.go` | `[MODIFY]` SoundCloud search (`scsearch`), Unicode NFC, транслитерация артистов, изоляция yt-dlp |
| **4** | **Downloader Engine** | `internal/services/downloader/job.go`, `downloader.go` | `[MODIFY]` Синхронизация с SQLite, `RetryJob`, монотонный прогресс, TTL очистка |
| **5** | **HTTP API** | `internal/api/handlers/download_handlers.go`, `internal/api/router.go` | `[MODIFY]` Эндпоинт `POST /downloads/{id}/retry`, статус 202, фикс Whitelist |
| **6** | **Frontend UI** | `web/static/js/downloader/downloader.js`, `web/static/js/store/queue_manager.js` | `[MODIFY]` Кнопка «Повторить» в карточках и в очереди |

---

## 4. Критерии приемки (Definition of Done)

1. Поиск треков в `AudioMatcher` сначала проверяет SoundCloud: при наличии качественного совпадения ($\ge 70.0$) скачивается оригинал без цензуры. При отсутствии трека на SoundCloud происходит автоматический fallback на YouTube.
2. Трек «Макс Корж — 2 типа людей» успешно находит трек на SoundCloud (или fallback YouTube) и скачивается в MP3 320k без ошибок.
3. При блокировке Deezer API (403 Forbidden) система не падает, а автоматически подтягивает метаданные и скачивает трек через fallback.
4. Очередь задач сохраняется в SQLite: после перезапуска сервиса история задач сохраняется, а упавшие задачи можно перезапустить нажатием одной кнопки «Повторить» (`POST /downloads/{id}/retry`).
5. Полоса прогресса релиза монотонно движется от 0% до 100% без скачков назад.
6. Обход белого списка доменов через `"source": "youtube"` надежно заблокирован тестами.
7. `go test -v ./...` и `go test -race ./...` проходят на 100%.
