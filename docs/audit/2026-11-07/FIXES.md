# Исправления аудита F01–F12

Дата: 2026-11-08. Основание — явный запрос исправить все находки REPORT.md.
Исходный отчёт и матрица сохранены как исторический снимок; эта запись описывает
изменённую реализацию. Пользовательские изменения SoundCloud matcher сохранены.
Мобильная переработка остаётся PROPOSED.

| Находка | Изменение | Проверка |
|---|---|---|
| F01 | Redownload создаёт staging-задачу без удаления существующего файла/релиза. Устаревший delete_existing игнорируется, deleted=false. Ошибка очереди сохраняет исходник. Форма больше не предлагает удалять файлы. Обычная приёмка сохраняет разные версии с суффиксом, не делает молчаливую замену. | TestRedownloadQueueFailurePreservesOriginal, filemanager redownload tests, TestUserJourney_RedownloadTrackAndRelease; fixes-evidence/repro.log |
| F02 | Проверяется канонический корень, Remove/RemoveAll в файловом слое также запрещают сам корень. | TestDeleteAllRootAliasesDenied: ./, a/.., URL-кодирование и другие варианты |
| F03 | Встроен слой safefs поверх удерживаемого os.Root. Go-операции чтения, записи, перечисления, переименования, удаления, временные ID3-файлы, обходы, glob и аудиопотоки переведены на него. Каждый медиапуть проходит Resolve. После этого I/O выполняется относительно дескриптора корня. Mediaexec передаёт локальные входы и выходные MP3 через ExtraFiles; для yt-dlp закрепляется приватный staging-каталог. | TestOperationsRejectSwapAfterResolve, TestRootDescriptorSurvivesRename, TestInputDescriptorSurvivesDirectorySwap, TestRegisteredMP3OutputUsesPinnedDescriptor, существующие traversal/symlink тесты и Range |
| F04 | Убран -q:a 0 в обоих используемых реализациях транскодирования: services/downloader и features/downloader. | Реальный ffmpeg/ffprobe проверяет MP3 320000 bit/s и 44100 Hz; проверяется также дескрипторный выход |
| F05 | Удалены запасные SECRET_KEY и bcrypt-хэш. Config отклоняет отсутствующий/короткий ключ, плейсхолдер и невалидный bcrypt до доступа к storage. Добавлена pure Go утилита cmd/passwordhash. | TestMissingSecretsFailBeforeStorageAccess; обычные auth/config tests |
| F06 | Cookie name/TTL/Secure передаются через общую политику в login/logout/me, middleware и радио. Secure включён при TLS либо явной конфигурации reverse proxy; SameSite фиксирован Strict. | TestConfiguredCookiePolicy, TestConfiguredTTLAndCookieName, TestLoginHTTPSUsesConfiguredCookie; auth/radio suites |
| F07 | В обоих OAuth callback проверяются state query, cookie, срок и одноразовый серверный реестр. State потребляется до ExchangeCode; missing/mismatch/expired/replay отклоняются. | TestOAuthStateSingleUseAndBinding, тесты MusicBrainz handlers, callback без state получает 403 |
| F08 | Дедупликация сравнивает сопутствующие файлы. При разных текстах сохраняется отдельный вариант. Ошибки переноса не скрываются; при отказе перенос уже перемещённых companions и аудио откатывается, а ошибка отката возвращается вызывающему коду. | TestDedupPreservesDifferentLyrics, TestCompanionFailurePreservesAudio; organizer suite; repro.log сохраняет оба текста |
| F09 | Live-тест MaloletkaRemix заменён фиксированными кандидатами через SetSearchFn, без Skip. Torrent-not-found использует мок. В тестовых процессах API/downloader/resolver установлен блокирующий внешний трафик MockTransport; Deezer использует внедрённый transport. Поправлены пауза и безопасное чтение snapshot в backfill-тесте. Отменённая очередь не продолжает работу после Close. | Полные обычный/race suites; TestFindBestMatch_MaloletkaRemix; гонка исходного теста устранена |
| F10 | Условие выбранного target стоит на каждом deploy-step, включая SSH, upload, restart и health/rollback. | fixes-evidence/deploy-check.txt: push, all, nl_music, server2; YAML разобран локальным парсером |
| F11 | README описывает автономный Linux ELF yt-dlp, встроенный EJS и Deno >=2.3, проверку релиза и PATH systemd. Проверка перед запуском yt-dlp отклоняет Python-launcher/непригодный runtime; локальная библиотека продолжает работать. | TestPythonLauncherRejectedBeforeExecution; изолированный сервер с неподходящим yt-dlp сохранил вход и доступ к медиатеке |
| F12 | Backend-запросы из JS идут через window.api.raw либо типизированные методы общего клиента. Общий raw сохраняет Response, так что прежние JSON/text/blob-разборы работают. Обновлены версии изменённых JS в HTML для сброса браузерного кэша. | TestJavaScriptNetworkingUsesAPIClient; шаблонные тесты; браузерные вход, список файлов, открытие и отмена формы |

## Проверки и артефакты

- `go test -v ./...`: PASS, `fixes-evidence/tests.log`.
- `GOFLAGS=-p=1 go test -race ./...`: PASS, `fixes-evidence/race.log`. Все пакеты проверяются, внутри SQLite-теста остаются 50 горутин. Параллельные прогоны пакетов нестабильны по старому 5-секундному порогу SQLite (5,14 и 5,33 с); исходные неуспешные логи сохранены в race-initial.log и race-parallel.log. Порог теста не ослаблялся, race detector не отключался.
- `go vet ./...`: PASS. Дополнительно исправлен возврат копии mutex в DownloadJob.Snapshot: возвращается отдельный указатель на снимок.
- `CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/music-manager cmd/server/main.go`: PASS; file подтверждает статический ELF. Бинарник не развёртывался и рабочий сервис не перезапускался.
- `git diff --check`: PASS. YAML workflow/OpenAPI разобран без синтаксических ошибок.
- Воспроизведения F01/F02/F03/F06/F08: fixes-evidence/repro.log.
- Браузер: отдельные SQLite и синтетический MP3, вход тестовым пользователем, «Файлы» → «Скачать заново», открытие/отмена формы. На 1280×800 и 375×812 текст помещается; на мобильном scrollWidth=370 при innerWidth=375. Снимки получены инструментом браузера в чате. Временный сервер остановлен, viewport сброшен. Это проверка изменённой формы, не полная WCAG/MOB-приёмка.

## Границы результата

Исправлены подтверждённые F01–F12 в репозитории. Это не объявляет все 46 требований
полностью принятыми: оставшиеся условия исходного аудита (реальные поставщики,
полная контрактная/нагрузочная/WCAG/crash-recovery приёмка) не превращаются в PASS.

Системные инструменты за пределами workspace не заменялись. Обнаруженный в исходной
среде Python-launcher yt-dlp теперь отклоняется. Для фактических загрузок оператору
нужно установить автономный yt-dlp и Deno согласно README в tools/ установки;
изменения и проверки поставки подготовлены, production provisioning не выполнялся.
Медиакорни и родительские каталоги конфигурации должны администрироваться доверенным
оператором. Защита os.Root не предназначена для противодействия привилегированному
процессу, который может менять mounts или память сервера.
