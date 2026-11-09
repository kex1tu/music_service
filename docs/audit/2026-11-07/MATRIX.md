# Матрица требований

> Снимок первоначального аудита. Исправления и повторные проверки от 2026-11-08: [FIXES.md](FIXES.md).


Дата: 2026-11-07. Объект — рабочее дерево, указанное в [отчёте](REPORT.md). Все пути ниже относительно корня репозитория. Fxx — подтверждённая находка отчёта. «Частично» означает, что проверена только перечисленная часть, а не что остальные части заведомо реализованы. «Не проверено» не равно «не реализовано». Отсутствие находки не доказывает соответствие.

Доказательства: **T** = `evidence/tests.log`; **C** = `components-loopback.log` (DB — `components.log`); **R** = `race.log`; **D** = `downloader-targeted.log`; **M** = `matcher.log`; **F** = `fuzz.log`; **P** = `repro.log`. Имена существующих тестов можно найти в `evidence/test-inventory.txt`. Инвентарь сам по себе не является выполненной проверкой.

| ID | Статус | Реализация → выполненная проверка → доказательство | Отклонение / непроверенная часть |
|---|---|---|---|
| SYS-01 | частично | go.mod, web/embed.go, cmd/server/main.go → две сборки → build.log/static-build.log | CGO=0 даёт статический ELF; точный выход в bin сохранён без перезаписи. Внешний runtime: F11 |
| SYS-02 | не соответствует | README:65–72, PATH yt-dlp → file исполняемого файла | F11; EJS, автономная поставка и реальная работа источников не проверены |
| SYS-03 | не соответствует | web/embed.go, web/static/js/player.js, api/client.js → шаблонные тесты T | F12; CDN/иконки/браузерная реактивность не приняты |
| SYS-04 | частично | internal/db/sqlite.go:25, queries.go:399 → DB tests C | WAL и сохранение служебных таблиц при ResetLibraryTables проверяются тестами; crash/recovery всей системы не проверен |
| SYS-05 | частично | cmd/server/main.go, internal/features/*/feature.go, core/httputil → сборка, T, C, routes.txt | Модульная регистрация и общие хелперы есть; все границы и сохранность контрактов не доказаны |
| DL-01 | не проверено | resolver/resolver.go, API routes.txt; инвентарь resolver tests | Все источники и пакетные пользовательские сценарии не запускались |
| DL-02 | не проверено | downloader feature routes.txt; downloader_preflight_modal.html присутствует в инвентаре шаблонов | Выбор дорожек, TTL preflight, пустой выбор и дубли не проверены сквозным сценарием |
| DL-03 | частично | resolver/full_release_matcher.go:115 → тесты совпадений M | Проверена часть названий/длительностей, отбрасывание всех типов каверов/live/ремиксов и пригодность URL не приняты |
| DL-04 | частично | full_release_matcher.go:118,134,203 → threshold/wrong album/duration tests M | one-to-one, 85%, 12s проверены локально; поиск остатка и отображение всех пропусков end-to-end не проверены |
| DL-05 | не проверено | downloader/switch_version.go, routes.txt; switch_version_test.go в инвентаре | Смена источника из трёх интерфейсов, сохранение выбора, DRM/регион не приняты |
| DL-06 | частично | full_release_matcher.go:44 → CensorshipRanking test M | Есть проверка ранжирования SoundCloud/YTM; Quick-Listen, неопределённость и автопауза не проверены |
| DL-07 | не соответствует | downloader/media_processor.go:138 → D и синтетическое ffmpeg/ffprobe | F04, выход 100268 bit/s; отмена транскодирования проверена D, вся цепочка таймаутов не принята |
| DL-08 | частично | downloader/job.go, manager.go → monotonic/weighted/concurrent tests D | Реальный рестарт очереди, повторы и весь persistence не проверены; vet.log содержит copylocks |
| DL-09 | частично | organizer/organizer.go, downloader/downloader.go:68 → organizer tests T | Приёмка/организация частично покрыта; TTL, пакетная группировка, повтор при сбое не приняты |
| DL-10 | не соответствует | features/filemanager/service.go:1036 → ошибка mock queue P | F01: исходник удалён до постановки замены |
| LIB-01 | частично | id3/writer.go, reader.go → roundtrip/null/TXXX/prefix tests T, R | Базовые записи/чтение подтверждены; полный набор повреждённых файлов и все APIC/TPOS комбинации не принят |
| LIB-02 | частично | id3/album_artist_sync_test.go, organizer/navidrome_fixer_test.go → T | Проверены жанры/соавторы/синглы в фикстурах; реальная совместимость Navidrome не принята |
| LIB-03 | частично | web шаблоны/JS → PuddletagSaveButtonNotBlockedInDownloaderMode T | Браузерное сохранение после сортировки/фильтра и принятие без правок не проверены |
| LIB-04 | не проверено | tageditor feature routes.txt; тесты редактора в inventory | Regex, выбранные колонки, имена и номера дисков не проверены в полном пользовательском сценарии |
| LIB-05 | не соответствует | tageditor/handlers.go:368, service.go:1728 → анализ ветви callback | F07; сверка Deezer/MusicBrainz/Shazam и назначение пар не приняты |
| LIB-06 | частично | id3/cover_optimize_test.go, organizer/covers_test.go → T | Локальные преобразования покрыты; галерея, четыре источника и фактическое применение выбранным трекам не проверены |
| LIB-07 | частично | id3/writer.go:105 → фильтрация lyrics frames при чтении кода | В writer USLT/SYLT управляемые и не добавляются; LRCLIB/Genius, сохранение/удаление внешних текстов end-to-end не проверены |
| LIB-08 | не соответствует | organizer/path.go, companions.go:92 → T и P | Канонические пути/санитизация тестируются; F08 — потеря уникального companion |
| LIB-09 | не соответствует | filemanager/service.go:493 → C и P | F02: удаление корня через ./; поиск/списки тестируются, нагрузка >2000 не принята |
| LIB-10 | не соответствует | scanner/scanner_test.go → C; organizer/companions.go → P | Удалённые файлы, watcher и concurrent scan покрыты; F08. Сбой между ФС и SQLite не проверен |
| AUD-01 | не проверено | internal/features/audit/feature.go в routes.txt, audit tests в inventory | Все категории, степень уверенности и локальные исправления при отключённом API не проверены |
| AUD-02 | не проверено | audit routes.txt; organizer/companions.go изучен | Не подтверждено безопасное решение об удалении именно audit duplicates; F08 относится к organizer, не приписывается автоматически audit UI |
| AUD-03 | не проверено | backfill_handlers.go, соответствующие tests в inventory | Отбор отсутствующих номеров и полный треклист в Puddletag не проверены сквозным сценарием |
| PLAY-01 | не проверено | web/static/js/player.js, audio_player/player_tab шаблоны | Воспроизведение, очередь, LRC/TXT, спектр, перемотка и клавиатура в браузере не проверялись |
| PLAY-02 | частично | filemanager/service.go:698, feature.go → StreamAudio_RFC7233Range и StreamTrack tests C | Range и metadata локально PASS; все aliases/HEAD/token и внешние плееры не приняты |
| RADIO-01 | частично | radio/selector.go, streamer.go → WaveSelector/ICY tests C | Локальные фикстуры PASS; музыкальная выборка 45/30/25 и поведение реального длительного эфира не приняты |
| RADIO-02 | частично | radio/feature.go, services/radio/streamer_test.go → HEAD/EmptyLibrary/TokenValidation C | Сервисные тесты PASS; весь router+CORS+aliases+browser-session набор не проверен |
| RADIO-03 | частично | radio/navidrome.go:115 mode=ro → ReaderAndSubsonicClient tests C | Read-only SQLite и mock API покрыты; реальная интеграция/повторная регистрация станции не приняты |
| RADAR-01 | частично | services/radar/radar_test.go → ValidationAndConcurrency/ProgressUpdate C | Фикстуры управления и сканирования PASS; периодическая работа и UI не приняты |
| RADAR-02 | частично | radar/radar_test.go → MultiArtist_Deduplication/Reconcile/AutoDownload C | Локальные сценарии PASS; crash между enqueue и записью статуса, все даты/написания не приняты |
| SEC-01 | не соответствует | core/sandbox/sandbox.go → traversal tests T/R, P; filesystem-calls.txt | F02/F03. Полная проверка каждой файловой операции не завершена; только инвентаризация |
| SEC-02 | не проверено | resolver/resolver.go:25; downloader/media_processor.go; tageditor/service.go:951 | CLI использует CommandContext и --; DNS/redirect/SSRF, каждый вход обложек и лимиты ресурсов не проверены полностью |
| SEC-03 | не соответствует | config/config.go, core/auth/auth.go, tageditor callback → T/R, P, статическая трассировка | F05/F06/F07; bcrypt/HMAC и время тестируются, но это не устраняет найденные обходы |
| SEC-04 | частично | id3/reader.go:154, frame_handlers.go → T/R/F | Лимит фрейма 25 MiB, APIC fuzz PASS; декодирование изображений, все повреждения и лимиты шаблонов/regex не приняты |
| API-01 | частично | api/router.go:55–61, features/*/feature.go → routes.txt, filemanager C | Есть GET/HEAD health и частичные Range-тесты; полная сверка API.md/OpenAPI/ошибок/multipart не выполнена |
| OPS-01 | не соответствует | .env.example, config/config.go, core/auth/auth.go → статический поиск использований | F06; AUDIO_BITRATE/SAMPLE_RATE описаны как опции, но не читаются; полный конфигурационный контракт не принят |
| OPS-02 | не соответствует | .github/workflows/deploy.yml, music-manager.service, scripts/deploy.sh → чтение pipeline, две сборки | F09/F10; CI tests-before-build и CGO=0 есть. Реальный deploy/rollback и совместимость данных не проверены |
| MOB-01 | неприменимо | REQUIREMENTS §7, plan.md PROPOSED | Отложенная приёмка; мобильная навигация не реализовывалась этим аудитом |
| MOB-02 | неприменимо | REQUIREMENTS §7, plan.md PROPOSED | Предложение; перекрытия и таймкоды не проверены браузером |
| MOB-03 | неприменимо | REQUIREMENTS §7, plan.md PROPOSED | Предложение; инспектор и сетка не изменялись |
| MOB-04 | неприменимо | REQUIREMENTS §7, plan.md PROPOSED | Предложение; браузерные снимки четырёх размеров отсутствуют; общая UI-приёмка также не завершена |

## Приёмка источников

| Источник | Проверено | Статус интеграции / оставшиеся сценарии |
|---|---|---|
| YouTube / YouTube Music | Сопоставление локальных треклистов и mock-кандидатов M | не проверено: guest/cookies, EJS, playlist, регион, возраст, отмена внешнего процесса |
| SoundCloud | Целые mock-кандидаты и ранжирование M; пользовательские изменения сохранены | не проверено: реальный client_id, full track data, set/album, недоступность и rate limit |
| Deezer | Моки в scanner/radar C | не проверено: short links, каталог, ISRC/coauthors и iTunes fallback в реальном окружении |
| Yandex | Только наличие интеграции по исходникам/инвентарю | не проверено: OAuth, proxy, HTTP 451 и прямая загрузка |
| Spotify / Apple | Только наличие маршрутов/интеграционных тестов в инвентаре | не проверено: credentials, пагинация и доступность метаданных |
| BitTorrent | Только наличие torrent handler | не проверено: magnet/.torrent, лимиты объёма и ожидания, отмена, общий пайплайн |

## Контракты: проверенное подмножество

| Контракт | Доказательство | Предел |
|---|---|---|
| GET/HEAD /api/health | router.go:55–61, статически зарегистрированы | HTTP-прогон health не выполнен |
| Файлы/Range/metadata | C: filemanager suite | Не вся библиотечная OpenAPI-схема |
| Redownload | P: прямой service-вызов с фиктивными зависимостями | HTTP-код ошибки и тело отдельно не проверены |
| Радио HEAD/token/ICY | C: сервисные httptest | Не весь набор router aliases/CORS |
| Остальные маршруты | routes.txt | Наличие регистрации не означает соответствие метода, JSON, multipart, пагинации и ошибок |
