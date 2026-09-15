# PROGRESS.md — Матрица статусов реализации Music Downloader & Tag Manager

## Текущий статус проекта
- **Текущий статус проекта:** 100% готовность, все проверки и тесты пройдены.
- **Активный этап:** Сквозной пайплайн дозагрузки неполных релизов (Сверить -> Загрузить недостающее -> Отредактировать теги всего релиза в Tag Editor), скоупинг дубликатов строго в рамках одного релиза (`album_dir::title`) с кнопками точечного и пакетного удаления (`/files/audit/duplicates/fix` и `/files/audit/duplicates/fix-all`), фикс кнопки и виртуализация отрисовки неканонических путей («Исправить все пути» с пакетным сохранением без избыточной инвалидации кэша).
- **Результаты тестов:** 562/562 passed (`pytest`), Go unit tests passed (100% pass), ruff linter — 0 замечаний (All checks passed!).
- **Деплой:** Развёрнуто, синхронизировано и активно на обоих серверах (`nl_vpn` / `144.31.51.105` и `nl_music` / `194.76.168.20`). Некорректные пути на `nl_vpn` приведены к каноническому виду (0 нарушений), на `nl_music` дубликаты исправлены.

---

## Блок 1: Ядро и авторизация

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **3.1 Core / Bootstrap** | `app/main.py`, `app/config.py`, `app/core/exceptions.py`, `app/dependencies.py` | **DONE** | Фабрика `create_app`, `Settings` (pydantic-settings), fail-fast lifespan с проверкой записи `MUSIC_DIR` и `TMP_DIR`, `SessionMiddleware` (`SameSite=Strict`, `httpOnly=True`), глобальные exception handlers (доменные исключения транслируются в HTTP 400/403/422/500), `GET /api/health`, модульные тесты в `tests/`. |
| **3.2 Auth Module** | `app/core/security.py`, `app/services/auth_service.py`, `app/routers/auth.py`, `app/schemas/auth.py`, `app/dependencies.py` | **DONE** | `bcrypt`-хеширование с защитой от тайминг-атак (`DUMMY_BCRYPT_HASH` + `secrets.compare_digest`), генерация и валидация токенов сессий через `itsdangerous.URLSafeTimedSerializer`, безопасная установка cookie (`httpOnly=True`, `SameSite=Strict`), внедрение зависимости `get_current_user` (с поддержкой cookie и `Authorization: Bearer`), маршруты `POST /login`, `POST /logout`, `GET /api/auth/me`, полное покрытие тестами (`tests/test_security.py`, `tests/test_auth_service.py`, `tests/test_auth_router.py`). |
| **3.3 Path Sandbox Guard** | `app/core/sandbox.py`, `app/dependencies.py`, `tests/test_sandbox.py` | **DONE** | Класс `PathSandbox`, методы `resolve()`, `resolve_child()`, `to_relative()`, `is_safe()`, `validate_filename()`. Предотвращение path traversal (`../../`, leading slash trap, null-byte injections `\x00`/`%00`, однократное и двойное URL-кодирование `%252e%252e%252f`, symlink bypass наружу песочницы). Зависимости FastAPI `get_music_sandbox`, `get_tmp_sandbox`. 41 тест в `tests/test_sandbox.py`. |

---

## Блок 2: Сервис загрузки

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **3.4 Downloader Engine** | `app/services/source_resolver.py`, `app/services/deezer_client.py`, `app/services/download_service.py`, `app/routers/downloads.py`, `app/schemas/download.py` | **DONE** | Асинхронный клиент `DeezerClient` (публичный API без ARL, треки, альбомы, плейлисты, обложки 1400×1400), `AudioMatcher` (нечеткое сопоставление RapidFuzz, фильтрация по длительности ±2с, каналы `- Topic`), `SourceResolver` с белым списком доменов, `DownloadService` (очередь с `asyncio.Semaphore`, скачивание `yt-dlp` в `raw/`, транскодирование `ffmpeg` в MP3 320 kbps CBR / 44.1 kHz, запись черновых ID3v2.4 через `mutagen`, TTL сборщик мусора), маршруты `POST /downloads`, `GET /downloads/{job_id}`, `GET /downloads/{job_id}/tracks`, `DELETE /downloads/{job_id}`. 23 теста в `tests/`. |

---

## Блок 3: Метаданные, тексты и файловая организация

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **3.5 Metadata / Tag Service** | `app/services/tag_service.py`, `app/routers/tags.py` | **DONE** | mutagen ID3v2.4, множественные теги через `\x00`, обложки APIC (type=3), двухуровневая модель (Album -> Track), чтение/запись тегов, извлечение обложек, обнаружение треков мультидисковых альбомов (CD1/CD2) через `rglob`, очистка временных обложек base64, `GET /tags`, `GET /tags/cover`, `PUT /tags/batch`. 14 тестов в `tests/test_tags_router.py` и 14 тестов в `tests/test_tag_service.py`. |
| **3.6 Lyrics Service** | `app/services/lyrics_service.py`, `app/routers/lyrics.py` | **DONE** | Поиск текстов LRCLIB (синхронизированные `.lrc`) и Genius (fallback `.txt`), сохранение внешними файлами рядом с MP3, маршруты `GET /lyrics/search`, `GET /lyrics`, `POST /lyrics`, `DELETE /lyrics`. 20 тестов в `tests/test_lyrics_service.py` и 6 тестов в `tests/test_lyrics_router.py`. |
| **3.7 File Manager Service** | `app/services/file_manager_service.py`, `app/routers/files.py`, `app/schemas/files.py` | **DONE** | Навигация по `MUSIC_DIR` через `PathSandbox`, динамическое построение дерева каталогов без БД, ленивая подгрузка содержимого, чтение треков альбомов, безопасное удаление файлов/папок с защитой корня, маршруты `GET /files`, `GET /files/tracks`, `DELETE /files`. 14 тестов в `tests/test_file_manager_service.py` и 7 тестов в `tests/test_files_router.py`. |
| **3.8 Organizer Service** | `app/services/organizer_service.py` | **DONE** | Перенос файлов по шаблону `MUSIC_DIR/{AlbumArtist}/{Album} - {Year}/{TrackNumber:02d}. {Title}.mp3`, двухфазный перенос с защитой от потери данных при смене порядка треков и коллизий с существующими файлами на диске, потокобезопасная синхронизация каталогов, сохранение обложек при `reorganize=False`, поддержка переименований регистра (case-only rename), перенос прилежащих `.lrc`/`.txt` и обложек `cover.jpg`, санитизация имен через `PathSandbox.validate_filename()`, удаление пустых папок, интеграция в `PUT /tags/batch`. 14 тестов в `tests/test_organizer_service.py`. |

---

## Блок 4: Пользовательский интерфейс (Frontend no-build)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **4.1-4.6 UI & Modals** | `app/templates/index.html`, `app/static/js/`, `app/static/css/style.css` | **DONE** | Чистый no-build стек (HTML5 + Tailwind CDN + Alpine.js CDN). Главный SPA-макет с темной темой, вкладки Загрузчик и Файловый менеджер, глобальный стор `Alpine.store('app')`, компонент загрузчика с long-polling (`downloader.js`), двухуровневый модальный редактор тегов (`tag_editor.js`), модальное окно текстов песен (`lyrics_modal.js`), компонент дерева медиатеки (`file_manager.js`), модал подтверждения удаления, тосты уведомлений. Интеграционные тесты статики в `tests/test_bootstrap.py`. |

---

## Блок 5: Деплоймент и системная интеграция

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **7 Deploy** | `music-manager.service`, `requirements.txt`, `.env.example`, `README.md` | **DONE** | `requirements.txt` и `.env.example` сформированы. Unit-файл systemd (`music-manager.service`) настроен с максимальной изоляцией безопасности (NoNewPrivileges, ProtectSystem=full, PrivateTmp, ProtectControlGroups). Руководство по установке, запуску и администрированию задокументировано в `README.md`. |

---

## Блок 6: Исправления, стабилизация и UI-модернизация (2026-09-08T10:18:36Z)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1. YouTube Age Gate & Cookies** | `app/config.py`, `app/dependencies.py`, `app/services/download_service.py`, `app/services/source_resolver.py`, `tests/test_config.py` | **DONE** | Поддержка передачи cookies в yt-dlp через опцию `cookiefile` (`--cookies`). Настройка параметра `YTDLP_COOKIES_PATH` в `Settings`, автоматическое обнаружение `cookies.txt` в корне проекта (`PROJECT_ROOT`) или текущей рабочей директории (CWD). Устранение блокировок возрастных ограничений 18+ ("Sign in to confirm your age"). Автоматический fallback на чистый гостевой режим без cookies при ошибках антибота YouTube ("The page needs to be reloaded") с изоляцией бинарника virtualenv (`venv/bin/yt-dlp`) и поддержкой современных клиентов visionos/ios. Тесты автоматического поиска и валидации путей cookies в `tests/test_config.py`. |
| **R2. Deezer Short Links (`link.deezer.com`)** | `app/config.py`, `app/services/source_resolver.py`, `app/services/deezer_client.py`, `tests/test_source_resolver.py`, `tests/test_deezer_client.py` | **DONE** | Добавление домена `link.deezer.com` в белый список `ALLOWED_DOWNLOAD_DOMAINS`. Поддержка формата мобильных коротких ссылок `https://link.deezer.com/s/{code}` в `DeezerClient.parse_url`. Реализация асинхронного перехода по HTTP-редиректам (`HEAD` с fallback на `GET` и `follow_redirects=True`) для разворачивания коротких ссылок в канонические URL треков, альбомов и плейлистов Deezer. Модульные тесты парсинга и следования редиректам. |
| **R3. Устойчивость сопоставления треков (AudioMatcher)** | `app/services/deezer_client.py`, `tests/test_deezer_client.py` | **DONE** | Модернизация алгоритма сопоставления аудиопотоков `AudioMatcher`: использование `RapidFuzz default_process` для нормализации регистра и нечувствительности к пунктуации; внедрение строгих границ слов `\b` в регулярные выражения штрафных ключевых слов (защита от ложных штрафов корректных названий); дедупликация префикса артиста в названии; 6-ступенчатая лестница резервного поиска (fallback search ladder: исходный запрос -> очищенный от пунктуации -> "official audio" -> "audio" -> YouTube Music -> fallback). Успешное сопоставление треков с нестандартной пунктуацией (в т.ч. `Call Me Karizma - SAFEWORD`, альбом 1043319862). |
| **R4. Потоковый прогресс загрузки и конвертации** | `app/services/download_service.py`, `app/routers/downloads.py`, `app/static/js/downloader.js`, `tests/test_download_service.py` | **DONE** | Построчный асинхронный стриминг stdout процесса `yt-dlp` через `asyncio.subprocess` с регулярным выражением для извлечения реального процента, скорости и ETA. Монотонное распределение бюджета прогресса задачи от 0% до 100%: скачивание аудиопотока (0–70%), перекодирование ffmpeg в MP3 320 kbps (70–90%), тегирование mutagen (90–100%). Взвешенный расчет для мультитрековых релизов, защита от регрессии прогресса (`clamp`), корректная блокировка и разблокировка кнопки отправки в UI (`downloader.js`), устранение зависания прогресс-бара на 15%. |
| **R5. Модернизация и унификация SVG-иконок UI** | `app/templates/index.html`, `app/static/js/downloader.js`, `app/static/js/file_manager.js`, `app/static/js/tag_editor.js`, `app/static/js/lyrics_modal.js`, `app/static/css/style.css` | **DONE** | Полная замена устаревших текстовых бейджей, псевдосимволов и эмодзи на единообразные современные векторные SVG-иконки в контурном стиле Lucide / Heroicons outline (`stroke-width="2"`, `fill="none"`, `stroke="currentColor"`). Обновлены элементы навигационной панели, форма загрузчика, карточки заданий и метаданных, файловый проводник (папки, треки, файлы текстов, удаление), двухпанельный редактор тегов, модальное окно текстов песен и система всплывающих тостов. |

---

## Блок 7: Оптимизация производительности, расчет мультитрекового прогресса и интеграция Google Material Icons (2026-09-09T00:44:08Z)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Оптимизация скорости обработки и загрузки альбомов и плейлистов** | `app/config.py`, `app/services/source_resolver.py`, `app/services/download_service.py`, `tests/test_source_resolver.py`, `tests/test_source_resolver_stress.py`, `tests/test_challenger_m1_2.py` | **DONE** | Параллельный `AudioMatcher` с `asyncio.Semaphore(4)` через настройку `MATCHER_CONCURRENCY` в `Settings`, сокращающий время разрешения треков альбома/плейлиста в 3–5 раз с сохранением строгого порядка дорожек и надежной изоляцией ошибок отдельных треков. Приоритетный быстрый гостевой режим `yt-dlp` (`player_client=ios,visionos`) без антибот-задержек YouTube. Автоматический обход возрастных ограничений (Age Gate: "Sign in to confirm your age") с безопасным fallback на cookies только при необходимости. |
| **R2: Расчет и непрерывное обновление прогресса мультитрековых задач** | `app/services/download_service.py`, `app/templates/index.html`, `app/static/js/downloader.js`, `tests/test_download_service.py`, `tests/test_progress_regex_stress.py`, `tests/test_challenger_m2_2.py` | **DONE** | Универсальный regex `progress_regex` (`r"(?:\[download\]|\bdownload:)?\s*(\d+(?:\.\d+)?)%"`) с поддержкой ведущих пробелов и шаблонов без префиксов. Стриппинг ANSI кодов перед сопоставлением. Динамический монотонный пересчет `job.progress` по формуле `min(100.0, max(job.progress, round(10.0 + ((completed_tracks + active_pct / 100.0) / total_tracks) * 90.0, 1)))`. Детальное отображение активного трека (номер, название, индивидуальный процент) в UI с защитой от переполнения верстки (`truncate`, `min-w-0`). |
| **R3: Интеграция Google Material Icons через MCP Iconify** | `app/templates/index.html`, `tests/test_frontend.py` | **DONE** | Замена всех 45 SVG-иконок в `app/templates/index.html` на официальные Google Material Symbols outline/rounded, полученные через MCP Iconify (`get_icon`). Все иконки имеют `viewBox="0 0 24 24"`, `fill="currentColor"`, сохраняют существующие Tailwind классы и строго соблюдают no-build архитектуру без Node.js. |

---

## Блок 8: Устойчивость мультитрековых загрузок и пропуск DRM-защищенных треков (2026-09-09T07:38:00Z)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Пропуск сбойных и DRM-защищенных треков в плейлистах** | `app/services/download_service.py`, `app/static/js/downloader.js`, `tests/test_download_service.py` | **DONE** | Детекция DRM-защиты через функцию `is_drm_protected_error` (включая SoundCloud Go+ "This video is DRM protected"). Изоляция исключений на уровне отдельного трека в конвейере `_execute_pipeline`: при ошибке скачивания, транскодирования или тегирования трек помечается как `status="error"`, временные `.part`-файлы удаляются, а цикл продолжает скачивание остальных треков плейлиста без аварийной остановки всей задачи. Для одиночных треков ошибка немедленно всплывает в `JobStatus.ERROR`. В UI статус и тост показывают количество успешно скачанных и пропущенных треков. 3 новых теста в `tests/test_download_service.py`. |

---

## Блок 9: Извлечение метаданных и обложек для плейлистов SoundCloud (2026-09-09T09:05:00Z)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Извлечение метаданных треков плейлистов SoundCloud** | `app/services/source_resolver.py`, `tests/test_source_resolver.py` | **DONE** | Патч `SoundcloudPlaylistBaseIE._extract_set` для извлечения реальных названий, авторов, длительностей и URL обложек с пакетной дозагрузкой недостающих треков через API v2 (`/tracks?ids=...`). Поддержка списочной и словарной схем (`collection`). Дедупликация и разделение `Artist - Title` (дефис, en-dash, em-dash), защита от присвоения playlist uploader отдельным трекам, slug fallback для плейсхолдеров `Track N`. Автоматическое повышение качества обложек (`-large.` -> `-t500x500.`) с сохранением query-параметров. 11 тестов в `tests/test_source_resolver.py`. |
| **R2: Загрузка и вшивание обложек в ID3v2.4 APIC для плейлистов SoundCloud** | `app/services/download_service.py`, `app/services/tag_service.py`, `tests/test_download_service.py`, `tests/test_tag_service.py` | **DONE** | Индивидуальная загрузка обложки для каждого трека плейлиста с безопасным fallback на обложку плейлиста или первого трека. Автоматическая транскодировка WebP обложек в стандартный JPEG через `ffmpeg` для 100% совместимости с ID3v2.4 APIC и плеерами. Вшивание обложки в ID3v2.4 APIC фрейм каждого MP3 через mutagen с детекцией mime-типов (`image/jpeg`, `image/png`, `image/webp`). Сохранение обложки задачи в `{job.id}/cover.jpg` (`job.cover_relative_path`). Полная очистка временных файлов обложек в `raw/` в блоках `finally`. 8 тестов в `tests/test_download_service.py` и 2 теста в `tests/test_tag_service.py`. |
| **R3: Архитектурные инварианты, песочница и санитизация имен файлов** | `app/services/download_service.py`, `app/services/source_resolver.py`, `app/services/tag_service.py`, `app/config.py` | **DONE** | Строгое использование `PathSandbox.resolve()` для всех создаваемых файлов и обложек. Санитизация `clean_title` с заменой опасных символов (`[\x00-\x1f/\\:*?"<>|%]`) на `_`, защита от падений URL-декодера `PathSandbox.resolve` на `%00` и создания ложных подкаталогов на `%2f`. Полная поддержка нелатинских символов (японский, кириллица, арабский). Полное удаление устаревших ID3v1 тегов (`v1=0`). Добавление `on.soundcloud.com` в `ALLOWED_DOWNLOAD_DOMAINS`. Соблюдение стандарта mutagen ID3v2.4, Zero-Database, отсутствие Node.js/Docker. Все 407 тестов проходят успешно (`pytest`), линтер `ruff` чист. |

---

## Блок 10: Пакетный поиск текстов для альбомов и прямая вставка текстов (2026-09-09T16:30:00Z)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Кнопка «Найти тексты для альбома» в редакторе тегов** | `app/templates/index.html`, `app/static/js/tag_editor.js` | **DONE** | Добавлена кнопка пакетного поиска текстов песен для всех треков альбома/папки в шапку списка треков окна редактирования тегов (`tagEditorComponent`). Автоматический опрос LRCLIB/Genius для недостающих треков с защитой от спама API (задержка между запросами), динамическим счетчиком прогресса (`lyricsProgress.current / total`), мгновенным реактивным обновлением бейджей `LRC`/`TXT` в строках треков и итоговым toast-уведомлением. |
| **R2: Прямая вставка текста песни и ручной выбор формата в Lyrics Modal** | `app/templates/index.html`, `app/static/js/lyrics_modal.js` | **DONE** | В модальное окно текстов песен добавлена кнопка «Вставить текст» (чтение из буфера обмена через `navigator.clipboard` с fallback на горячие клавиши `Ctrl+V`), слушатели ввода и вставки (`@input`, `@paste`) с автоопределением наличия таймкодов `[mm:ss` и формата `lrc`/`txt`, кнопка «Очистить», а также интерактивный переключатель формата `LRC`/`TXT` в шапке модала. Текст можно как загрузить файлом, так и напрямую вставить из буфера или клавиатурой в поле редактирования. |
| **R3: Верификация и интеграционные тесты** | `tests/test_frontend.py` | **DONE** | Добавлен комплексный тест `test_lyrics_features_in_tag_editor_and_lyrics_modal` в `tests/test_frontend.py`, верифицирующий разметку, кнопки, обработчики событий и методы в JavaScript-файлах. Все 408 тестов успешно пройдены (`pytest`). |

---

## Блок 11: Автопоиск метаданных через Shazam (аудиоотпечаток) и Deezer API (поиск альбомов) (2026-09-09T19:40:00Z)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Акустическое распознавание через Shazam (`shazamio`)** | `app/services/recognition_service.py`, `app/schemas/tag.py`, `app/routers/tags.py`, `tests/test_recognition_service.py`, `tests/test_tags_router.py` | **DONE** | Внедрен сервис `RecognitionService` на базе `shazamio` для распознавания неизвестных MP3-треков по аудиосигнатуре (Shazam fingerprint). Полная изоляция через `PathSandbox` (поддержка `music_library` и `tmp_downloads`), извлечение названия, артиста, альбома, года выпуска, жанра и автоматический апскейл обложки до 1000x1000 px (`1000x1000cc.jpg`). Эндпоинт `POST /tags/recognize-audio`. Модульные и интеграционные тесты с моками. |
| **R2: Поиск альбомов и треклистов в Deezer API** | `app/services/deezer_client.py`, `app/schemas/tag.py`, `app/routers/tags.py`, `tests/test_tags_router.py` | **DONE** | Реализован метод `DeezerClient.search_albums` и эндпоинты `GET /tags/deezer/albums` и `GET /tags/deezer/albums/{album_id}`. Позволяет искать официальные релизы по названию/артисту, получать валидированные треклисты с нумерацией, исполнителями (`format_artists`), годами, жанрами и обложками `cover_xl`. |
| **R3: Интеграция в UI редактора тегов (Tag Editor)** | `app/templates/index.html`, `app/static/js/tag_editor.js`, `tests/test_frontend.py` | **DONE** | 1) Добавлена кнопка «Найти в Deezer» в блоке общих тегов альбома, открывающая модальное окно поиска с живым предпросмотром альбомов, автозаполнением всех метаданных альбома, треклиста и обложки через Base64 Data URL. 2) Добавлена кнопка «Распознать всё (Shazam)» в шапку списка треков для пакетной идентификации неизвестных треков с прогресс-баром и защитой от перегрузки. 3) Добавлены индивидуальные кнопки «Shazam» в строке каждого трека таблицы для точечного распознавания. Тесты UI-элементов в `tests/test_frontend.py`. |
| **R4: Верификация, линтинг и тесты** | `requirements.txt`, `tests/` | **DONE** | Библиотека `shazamio` добавлена в `requirements.txt`. Все 415 тестов успешно проходят (`pytest`), линтер `ruff check` чист (0 ошибок). |

---

## Блок 12: Импорт плейлистов, альбомов и треков из Яндекс Музыки, Spotify и Apple Music (2026-09-09T20:30:00Z)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Клиент Яндекс Музыки (`YandexMusicClient`)** | `app/services/yandex_client.py`, `tests/test_yandex_client.py` | **DONE** | Асинхронный клиент Яндекс Музыки: парсинг URL треков в альбомах, одиночных треков, альбомов и плейлистов (включая редиректы UUID); извлечение метаданных и треклистов без авторизации через JSON Web Handlers (`handlers/*.jsx`); форматирование студийных обложек 1000×1000 (`%%` -> `1000x1000`); генерация прямых ссылок на оригинальные MP3 320 kbps с CDN Яндекса при наличии `YANDEX_MUSIC_TOKEN`. 11 тестов в `tests/test_yandex_client.py`. |
| **R2: Клиент Spotify (`SpotifyClient`)** | `app/services/spotify_client.py`, `tests/test_spotify_client.py` | **DONE** | Асинхронный клиент Spotify: парсинг треков, альбомов, плейлистов и коротких ссылок `spotify.link`; режим out-of-the-box без API-ключей через Next.js Embed Scraper (`__NEXT_DATA__`); поддержка официального Spotify Web API с OAuth Client Credentials и постраничной загрузкой при указании `SPOTIFY_CLIENT_ID` / `SPOTIFY_CLIENT_SECRET`; извлечение обложек 640×640 и ISRC кодов. 7 тестов в `tests/test_spotify_client.py`. |
| **R3: Клиент Apple Music (`AppleMusicClient`)** | `app/services/apple_music_client.py`, `tests/test_apple_music_client.py` | **DONE** | Асинхронный клиент Apple Music: парсинг треков внутри альбомов (`?i=...`), песен, альбомов и плейлистов; извлечение метаданных без авторизации через `serialized-server-data` и Schema.org `ld+json`; автоматический апскейл обложек до студийного разрешения 1400×1400 (`{w}x{h}bb.jpg` -> `1400x1400bb.jpg`); разбор длительности ISO 8601. 4 теста в `tests/test_apple_music_client.py`. |
| **R4: Интеграция в конвейер загрузки (`SourceResolver` & DI)** | `app/services/source_resolver.py`, `app/dependencies.py`, `app/schemas/download.py`, `app/config.py`, `.env.example`, `tests/test_source_resolver.py` | **DONE** | Автоопределение источников `yandex`, `spotify`, `applemusic` в `detect_source`; расширение `ALLOWED_DOWNLOAD_DOMAINS` и схемы `CreateDownloadRequest`; параллельное сопоставление треков через `AudioMatcher` с семафором `MATCHER_CONCURRENCY`; поддержка прямого скачивания MP3 с Яндекса; изоляция сбойных треков. 3 интеграционных теста в `tests/test_source_resolver.py`. |
| **R5: Пользовательский интерфейс и подсказки** | `app/templates/index.html` | **DONE** | Обновлен плейсхолдер и текст формы ввода URL (поддержка ссылок Яндекс Музыки, Spotify, Apple Music, Deezer, YouTube Music, SoundCloud); добавлены новые опции в селектор источника; обновлены информационные карточки поддерживаемых сервисов. |
| **R6: Верификация, линтинг и тесты** | `tests/` | **DONE** | Все 441 тест успешно пройдены (`pytest tests/ -v`, 100% pass), линтер `ruff check` чист (0 замечаний). Полное соблюдение инвариантов Zero-Database, PathSandbox, ID3v2.4 и внешних текстов песен. |

---

## Блок 13: Продвинутое редактирование тегов: Очистка названий, DND/Перенумерация мультидисков (TPOS) и Галерея обложек (2026-09-10T12:55:00Z)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Очистка названий и извлечение фитов (Clean Titles & Feat Extractor)** | `app/services/tag_service.py`, `app/static/js/tag_editor.js`, `app/templates/index.html`, `tests/test_tag_service.py` | **DONE** | Реализована пакетная и индивидуальная очистка названий треков: удаление мусорных суффиксов в скобках (`[Official Video]`, `(1080p)`, `[Prod. by ...]`, `(Lyric Video)`, `[Remastered 2021]`, `[4K Visualizer]`), удаление ведущих номеров дорожек (`01. `, `01 - `), нормализация префикса артиста (`Artist - Title`). Автоматическое извлечение соисполнителей (`feat.`, `ft.`, `featuring` в скобках и без) в поле исполнителей с разделителем `\\` на фронте и `\x00` в ID3v2.4 без дублирования существующих авторов. 3 теста в `tests/test_tag_service.py`. |
| **R2: Drag-and-Drop, умная перенумерация и поддержка мультидисков (TPOS/TRCK)** | `app/schemas/tag.py`, `app/services/tag_service.py`, `app/routers/tags.py`, `app/static/js/tag_editor.js`, `app/templates/index.html`, `tests/test_tag_service.py`, `tests/test_tags_router.py` | **DONE** | Добавлены поля `disc_number`, `disc_total`, `track_total` в схемы `TrackTags` и `AlbumTags`. Поддержка чтения и записи фреймов `TPOS` (номер диска / общее число) и `TRCK` (номер дорожки / общее число) в Mutagen ID3v2.4. Сортировка треков альбома с учетом номера диска (`(disc_number, track_number)`). Полноценный HTML5 Drag-and-Drop для ручной перестановки строк в таблице треков Tag Editor. Кнопка «Перенумеровать» с автоматическим сбросом нумерации (1..N в рамках каждого диска или глобально). Сохранение `disc_number` и `disc_total` при пакетном сохранении тегов. 4 теста в `tests/test_tag_service.py` и `tests/test_tags_router.py`. |
| **R3: Мульти-источниковая галерея обложек (Multi-Source Cover Gallery)** | `app/schemas/tag.py`, `app/services/tag_service.py`, `app/routers/tags.py`, `app/static/js/tag_editor.js`, `app/templates/index.html`, `tests/test_tag_service.py`, `tests/test_tags_router.py` | **DONE** | Реализован поиск обложек ультравысокого разрешения через публичные API без ключей: Apple Music / iTunes (апскейл до 1400×1400), Deezer (`cover_xl` 1400×1400), Яндекс Музыка (1000×1000) и Spotify (640×640). Эндпоинт `GET /tags/covers/search` с параллельным опросом через `asyncio.gather` и защитой от таймаутов отдельных источников. Модальное окно «Галерея обложек» в UI с фильтрами по источникам (Все, Apple Music, Deezer, Яндекс Музыка, Spotify), сеткой карточек с предпросмотром разрешения и года, мгновенным выбором. Серверное скачивание обложки по `cover_url` в `PUT /tags/batch` с автодетекцией MIME и вшиванием в треки и `cover.jpg`. 4 теста в `tests/test_tag_service.py` и `tests/test_tags_router.py`. |
| **R4: Верификация, линтинг и интеграционные тесты UI** | `tests/test_frontend.py`, `tests/` | **DONE** | Добавлен комплексный тест `test_clean_titles_dnd_renumber_and_cover_gallery_in_tag_editor` в `tests/test_frontend.py`. Все 452 теста успешно пройдены (`pytest`, 100% pass), линтер `ruff check` чист (0 замечаний). Полное соблюдение архитектурных инвариантов: Zero-Database, обязательный PathSandbox, strict ID3v2.4 с null-byte separation, no-build Alpine.js + Tailwind. |

---

## Блок 14: Очередь загрузок и пакетный режим (Batch Download & Queue Manager) (2026-09-10T13:00:00Z)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Схемы и бэкенд пакетной загрузки и очередей** | `app/schemas/download.py`, `app/services/download_service.py`, `app/routers/downloads.py`, `tests/test_batch_downloads.py` | **DONE** | Разработаны Pydantic-схемы `CreateBatchDownloadRequest`, `BatchDownloadResponse`, `QueueSummaryResponse`. В `DownloadService` реализованы методы `create_batch_jobs`, `get_all_jobs`, `get_queue_summary`, `clear_completed_jobs`. Добавлены эндпоинты `POST /downloads/batch`, `GET /downloads`, `GET /downloads/queue/summary`, `POST /downloads/queue/clear`. Изолированное выполнение через семафор параллелизма. 5 тестов в `tests/test_batch_downloads.py`. |
| **R2: Пользовательский интерфейс: Пакетный ввод и плавающий менеджер очередей** | `app/templates/index.html`, `app/static/js/downloader.js`, `tests/test_frontend.py` | **DONE** | В форму загрузчика добавлен переключатель режима («Одна ссылка» / «Список ссылок (пакетно)») с многострочным полем `<textarea>` и бейджем количества обнаруженных ссылок. Создан глобальный стор очереди `Alpine.store('app').queue` с автополлингом каждые 2 сек. Разработан плавающий виджет менеджера очередей в правом нижнем углу экрана (`fixed bottom-4 right-4 z-40`), доступный на всех вкладках: свернутая плашка-кнопка с индикацией активных задач и прогресса, и разворачиваемая панель (Drawer) с управлением задачами, переходом в Tag Editor, отменой и очисткой. 2 UI-теста в `tests/test_frontend.py`. |

---

## Блок 15: Глобальный поиск и аудит медиатеки (Global Search & Library Audit) (2026-09-10T13:08:00Z)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Мгновенный глобальный поиск (`Ctrl+K`) и In-Memory LibraryIndex** | `app/schemas/library_audit.py`, `app/services/library_audit_service.py`, `app/routers/files.py`, `app/templates/index.html`, `tests/test_global_search.py`, `tests/test_frontend.py` | **DONE** | Разработан сервис `LibraryAuditService` с in-memory индексацией и инкрементальным кэшированием по `st_mtime` (строгий инвариант Zero-Database). Эндпоинт `GET /files/search?query=...` с фильтрацией по артистам, альбомам и трекам. В шапку сайта встроена поисковая строка с горячей клавишей `Ctrl+K`, debounce 250мс, выпадающим списком результатов по категориям и быстрым переходом в директорию трека/альбома. 5 тестов в `tests/test_global_search.py` и 1 UI-тест в `tests/test_frontend.py`. |
| **R2: Дашборд аудита, пакетный автопоиск текстов и удаление дубликатов** | `app/schemas/library_audit.py`, `app/services/library_audit_service.py`, `app/routers/files.py`, `app/static/js/library_audit.js`, `app/templates/index.html`, `tests/test_library_audit.py`, `tests/test_frontend.py` | **DONE** | Создана новая вкладка «Аудит медиатеки» (`activeTab === 'audit'`) и Alpine-компонент `libraryAuditComponent()`. 1) Дашборд из 4 метрических карточек (всего треков/релизов, без текстов, без обложек, дубликаты). 2) Подраздел треков без текстов с кнопкой «Найти недостающие тексты для всех» (фоновый асинхронный опрос LRCLIB/Genius с ограничением частоты запросов и прогресс-баром). 3) Подраздел релизов без обложек с переходом в папку. 4) Поиск дубликатов песен (композитный нормализованный ключ + проверка длительности/размера) с безопасным удалением копий через модальное окно и `PathSandbox`. 6 тестов в `tests/test_library_audit.py` и 1 UI-тест в `tests/test_frontend.py`. |
| **R3: Верификация, линтинг и тесты** | `tests/` | **DONE** | Все 472 теста успешно пройдены (`pytest tests/ -v`, 100% pass), линтер `ruff check app tests` чист (0 замечаний). Строгое соблюдение Zero-Database, PathSandbox, ID3v2.4 и no-build архитектуры. |

---

## Блок 16: Группировка релизов в Tag Editor, обложки треков/синглов и мультирелизный Organizer (2026-09-10T13:50:00Z)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Исправление строки ввода и свитчера загрузчика (Downloader URL Input & Switcher Fix)** | `app/static/js/downloader.js`, `app/templates/index.html`, `tests/test_frontend.py` | **DONE** | Устранена блокировка отправки формы браузерной HTML5-валидацией (`novalidate` на `<form>`, замена `type="url"` на `type="text"` с `inputmode="url"`). Реализовано автодобавление схемы `https://` для ссылок без протокола (`music.yandex.ru/...`, `youtube.com/...`) в одиночном и пакетном режимах. Добавлены встроенные в строку кнопки «Вставить» (чтение из буфера обмена) и «Очистить» (`[x]`), а также автоматическая очистка поля ввода `this.url = ''` после успешной постановки задачи в очередь. Обеспечено стабильное переключение режимов через `x-show` без размонтирования DOM-элементов. Исправлен счетчик очереди в плавающем виджете (`undefined акт.` -> `active_jobs`). |
| **R2: Группировка релизов в Tag Editor (Accordion Grouped View + Flat View)** | `app/static/js/tag_editor.js`, `app/templates/index.html`, `tests/test_frontend.py` | **DONE** | В `tagEditorComponent` реализован реактивный геттер `groupedReleases`, формирующий виртуальные группы альбомов и синглов с сохранением прямых ссылок на объекты `this.tracks`. Добавлен переключатель режимов отображения таблицы («По релизам» / «Сплошной список») и аккордеон с возможностью сворачивания групп (`toggleGroupCollapse`, `isGroupCollapsed`). В шапку каждого релиза выведены миниатюра обложки, счетчик треков, кнопка подбора обложки релиза (`openGroupCoverGallery`) и распространения главной обложки (`applyAlbumCoverToGroup`). |
| **R3: Персональные обложки треков (40×40 thumbnails, Multi-source Gallery & Batch Singles Cover Search)** | `app/static/js/tag_editor.js`, `app/templates/index.html`, `tests/test_frontend.py` | **DONE** | В таблицу треков Tag Editor добавлена колонка с 40×40 миниатюрами обложек (`openTrackCoverGallery`), позволяющая выбирать индивидуальный арт для каждого трека через мультисервисную галерею (Apple Music, Deezer, Яндекс Музыка, Spotify). Разработана функция `fetchSinglesCovers()` для пакетного автоматического поиска официальных обложек синглов через Deezer Search API с ограничением частоты запросов, реактивным прогресс-баром и toast-уведомлением. |
| **R4: Бэкенд мультирелизной организации и режим синглов (OrganizerService, Tag Router & Schemas)** | `app/schemas/tag.py`, `app/services/organizer_service.py`, `app/routers/tags.py`, `tests/test_organizer_service.py`, `tests/test_tags_router.py` | **DONE** | В `TrackTags` добавлены поля `cover_url` и `cover_base64`. В `SaveAlbumRequest` добавлены флаги `singles_mode` и `group_by_album`. В `OrganizerService` реализован метод `compute_single_dir(track, default_artist)` и мультирелизное распределение файлов: треки разных альбомов раскладываются по своим папкам `{Artist}/{Album} - {Year}/`, а синглы — по `{Artist}/{Title} - Single ({Year})/`. Запись персональных `cover.jpg` / `cover.png` для каждой папки релиза из `track_covers_data`. В роутере `PUT /tags/batch` поддержана декодировка индивидуальных обложек и вшивание APIC в каждый MP3. 4 новых теста в `tests/test_organizer_service.py` и `tests/test_tags_router.py`. |
| **R5: Верификация, линтинг и тесты** | `tests/` | **DONE** | Все 478 тестов успешно пройдены (`pytest tests/ -v`, 100% pass), линтер `ruff check app tests` чист (All checks passed!). Полное соблюдение архитектурных инвариантов: Zero-Database, PathSandbox, ID3v2.4 и no-build архитектура. |

---

## Блок 17: Release Cards в Tag Editor — перенос меню метаданных внутрь каждого релиза (2026-09-10T14:30:00Z)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Удаление глобального блока и внедрение Release Cards** | `app/templates/index.html`, `app/static/js/tag_editor.js`, `tests/test_frontend.py` | **DONE** | Полностью удален верхний глобальный блок уровня альбома (бывшие строки 1260–1372). Вместо этого в режиме `viewMode === 'grouped'` каждый альбом и группа синглов отображаются в виде полноценной карточки релиза (`x-for="group in releases" :key="group.id"`): шапка с аккордеоном (32×32 обложка, название, бейджи, счетчик треков, кнопка галереи); раскрывающееся тело с большим меню релиза (обложка 128×128, загрузка файла, галерея, инпуты Album Artist, Album Title, Year, Discs, Genre, кнопки `Найти в Deezer`, `Перенумеровать 1..N`, `Найти тексты`, `Применить к трекам релиза`) и вложенной таблицей треков релиза. |
| **R2: Персистентный массив `releases` и аккордеон в тулбаре** | `app/static/js/tag_editor.js`, `app/templates/index.html` | **DONE** | Для устранения потери фокуса при вводе в инпуты `x-model` динамический геттер заменен на постоянный реактивный массив `this.releases`, инициализируемый методом `buildReleasesFromTracks()`. Добавлены методы `expandAllGroups()`, `collapseAllGroups()`, `handleGroupCoverUpload(event, group)`, `applyGroupToTracks(group)`, `renumberGroupTracks(group)`, `fetchLyricsForGroup(group, forceAll)`. В тулбар добавлены кнопки быстрого сворачивания и разворачивания всех аккордеонов. |
| **R3: Верификация, линтинг и тесты** | `tests/test_frontend.py` | **DONE** | Все 478 тестов успешно пройдены (`pytest`, 100% pass), линтер `ruff check app tests` чист (All checks passed!). Полное соблюдение архитектурных инвариантов. |

---

## Блок 18: Загрузка музыки с торрентов (BitTorrent / Magnet / .torrent через aria2c) (2026-09-10T15:05:00Z)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Конфигурация, безопасность и схемы** | `app/config.py`, `app/core/exceptions.py`, `app/schemas/download.py`, `app/services/source_resolver.py`, `.env.example` | **DONE** | Добавлены настройки BitTorrent в `Settings` (`TORRENT_ENABLED`, `ARIA2C_PATH`, `TORRENT_PEER_PORT=6881`, `TORRENT_SEED_TIME=0`, `TORRENT_MAX_PEERS=55`, `TORRENT_TIMEOUT=300`, `TORRENT_MAX_SIZE_GB=50`). Добавлено доменное исключение `TorrentError(DownloadError)` (HTTP 502). В `CreateDownloadRequest` и `CreateBatchDownloadRequest` расширен литерал `source` значением `"torrent"`. В `SourceResolver.detect_source` добавлено распознавание схемы `magnet:?` без блокировки доменами. Обновлен `.env.example`. |
| **R2: Сервис торрентов, CUE-сплиттер и извлечение тегов (TorrentService)** | `app/services/torrent_service.py` | **DONE** | Разработан `TorrentService`: запуск процесса `aria2c` с неблокирующим потоковым разбором stdout (`ARIA2C_PROGRESS_REGEX`: скорость, пиры, процент, ETA). Защита от переполнения диска при превышении `TORRENT_MAX_SIZE_GB`. Класс `CueSheetParser`: парсинг CUE-листов в UTF-8 и Windows-1251, расчет таймкодов треков и автоматическая нарезка монолитных аудиорипов через `ffmpeg -c copy`. Рекурсивное обнаружение аудиофайлов по белому списку расширений (`.flac`, `.mp3`, `.m4a`, `.wav`, `.ape`, `.wv`, `.ogg`, `.opus`, `.aac`). Извлечение метаданных (EasyID3/Vorbis) и графических обложек (`folder.jpg`, `cover.jpg`, APIC/FLAC pictures). |
| **R3: Интеграция в DownloadService и API роутер** | `app/services/download_service.py`, `app/dependencies.py`, `app/routers/downloads.py` | **DONE** | Внедрен `_execute_torrent_pipeline` в `DownloadService`: скачивание раздачи через `aria2c` (0..70%), анализ содержимого и CUE-сплиттинг (70..75%), конвертация в эталонный MP3 320 kbps CBR через `ffmpeg` (75..95%), запись ID3v2.4 тегов через `mutagen` (95..100%), статус `READY`. `TorrentService` подключен в DI `get_download_service`. В роутере добавлен эндпоинт `POST /downloads/torrent` для multipart/form-data загрузки файлов `.torrent` с валидацией расширения. |
| **R4: Пользовательский интерфейс (Frontend no-build)** | `app/templates/index.html`, `app/static/js/downloader.js` | **DONE** | В карточку Загрузчика добавлена кнопка и drag-and-drop область «Загрузить .torrent» (`uploadTorrentFile`, `handleTorrentDrop`, `handleTorrentDragOver`). В выпадающий список источников добавлен вариант «Торрент / Magnet». В `downloader.js` устранено искажение Magnet-ссылок в одиночном и пакетном режимах (защита от принудительного добавления `https://`). Живое отображение скорости и пиров торрента в строке шага задачи. |
| **R5: Верификация, линтинг и тесты** | `tests/test_torrent_service.py`, `tests/test_downloads_router.py`, `tests/test_download_service.py`, `tests/test_source_resolver.py` | **DONE** | Разработано 13 новых юнит-тестов в `test_torrent_service.py` (парсинг regex, размер в байтах, CUE UTF-8 / Windows-1251, ffmpeg сплит, фильтрация файлов, mutagen теги, mock aria2c, таймауты, лимит размера). 3 новых теста в `test_downloads_router.py` (magnet link, .torrent upload, 400 на невалидный файл). 2 новых теста в `test_download_service.py` (torrent pipeline success, disabled error). 1 новый ассерт в `test_source_resolver.py`. Все 496 тестов пройдены (`pytest`, 100% pass), `ruff check app tests` чист (All checks passed!). |

---

## Блок 19: Исправление пакетной загрузки и стабилизация торрент-загрузчика (2026-09-14T12:00:00Z)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Исправление пакетной загрузки (Alpine object-spread & parsing)** | `app/templates/index.html`, `app/static/js/downloader.js`, `tests/test_batch_downloads.py` | **DONE** | Устранена первопричина ошибки «вставьте хотя бы одну ссылку»: замена `x-data="{ inputMode: 'single', ...downloaderComponent() }"` на `x-data="downloaderComponent()"`, устранившая преждевременное вычисление геттера `parsedBatchUrls` в пустой массив при инициализации страницы. В `downloader.js` метод `getParsedBatchUrls()` расширен поддержкой переносов строк, запятых и пробелов. Добавлена функция быстрой вставки из буфера в текстовую область `pasteBatchUrls()` и кнопка очистки. В `pasteUrl()` и `submitDownload()` внедрено автоматическое обнаружение нескольких ссылок в поле одиночного ввода с бесшовным переключением в пакетный режим. |
| **R2: Стабилизация BitTorrent пайплайна (aria2c, DHT, regex, CUE)** | `app/services/source_resolver.py`, `app/services/torrent_service.py`, `tests/test_torrent_service.py`, `tests/test_source_resolver.py` | **DONE** | В `SourceResolver.detect_source` добавлена поддержка Magnet-ссылок без знака вопроса (`magnet:xt=`), ссылок в верхнем регистре (`MAGNET:`) и ссылок на файлы `.torrent` (включая query-параметры). В `TorrentService` обновлен `ARIA2C_PROGRESS_REGEX` для поддержки фазы метаданных (`0B/0B`), количества сидов (`SD:`) и скорости отдачи (`UL:`). В запуск `aria2c` добавлены DHT bootstrap узлы (`dht.transmissionbt.com:6881`) и надежные публичные трекеры, отключен `--check-integrity=false` для предотвращения ложных ошибок контрольной суммы на пустых директориях. Устранен потенциальный дедлок pipe `stderr` с помощью конкурентного чтения строк ошибки. В CUE-обработке добавлен `get_referenced_files` и проверка на уже разделенные треки (защита от повторного сплиттинга немонолитных раздач). |
| **R3: Верификация, линтинг и тесты** | `tests/test_torrent_service.py`, `tests/test_batch_downloads.py`, `tests/test_source_resolver.py`, `tests/test_frontend.py` | **DONE** | Добавлены тесты парсинга строки метаданных `0B/0B`, сидов и отдачи `SD/UL`, извлечения файлов из CUE, пропуска нарезки уже разделенных альбомов, пакетной загрузки Magnet и торрентов. Все 501 тест успешно пройдены (`pytest`, 100% pass), `ruff check app tests` чист (All checks passed!). |

---

## Блок 20: Интеграция с MusicBrainz API и Cover Art Archive (2026-09-14T12:15:00Z)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Асинхронный клиент MusicBrainzClient** | `app/services/musicbrainz_client.py`, `app/dependencies.py` | **DONE** | Создан клиент `MusicBrainzClient`: строгий rate limiting ($\ge 1.1$ с между вызовами через `asyncio.Lock`), соответствие требованиям идентификации MusicBrainz (заголовок `User-Agent: MusicManager/1.0.0 (...)`), обработка HTTP 429/503 с `Retry-After` бэкоффом. Поиск релизов по названию/артисту (`/ws/2/release/`), детальный разбор релиза (`/ws/2/release/{mbid}?inc=...`), форматирование артистов (`format_artist_credit` с сохранением `joinphrase`), получение треклиста с порядковыми номерами и дисками, интеграция с Cover Art Archive (`https://coverartarchive.org/release/{mbid}/front-500`). Зарегистрирован синглтон `MusicBrainzClientDep` в `dependencies.py`. |
| **R2: Схемы данных и эндпоинты роутера** | `app/schemas/tag.py`, `app/routers/tags.py` | **DONE** | В `app/schemas/tag.py` добавлены модели `MusicBrainzReleaseSearchResult`, `MusicBrainzReleaseTrackItem`, `MusicBrainzReleaseDetailsResponse`, в `CoverSearchResultItem` добавлен источник `musicbrainz`. В `app/routers/tags.py` добавлены защищенные эндпоинты `GET /tags/musicbrainz/albums` и `GET /tags/musicbrainz/albums/{release_id}`. |
| **R3: Мультисервисный поиск обложек в TagService** | `app/services/tag_service.py` | **DONE** | В `TagService.search_covers` добавлен асинхронный метод `_search_musicbrainz_covers`, обращающийся к Cover Art Archive и включающий обложки релизов MusicBrainz в общую выдачу. |
| **R4: Frontend интерфейс (Deezer / MusicBrainz)** | `app/templates/index.html`, `app/static/js/tag_editor.js` | **DONE** | В модальном окне автопоиска альбома добавлен селектор источника метаданных: **Deezer** / **MusicBrainz**. В галерею обложек добавлен фильтр и стилизованный бейдж **MusicBrainz (500×500)**. Логика автозаполнения в `tag_editor.js` унифицирована для обоих провайдеров. |
| **R5: Верификация, линтинг и тесты** | `tests/test_musicbrainz_client.py`, `tests/test_tags_router.py` | **DONE** | Разработано 10 новых юнит-тестов в `test_musicbrainz_client.py` (поиск релизов, разбор метаданных треков, format_artist_credit, обработка 404/429/Retry-After, сетевые ошибки, поиск обложек). Добавлены интеграционные тесты для роутера в `test_tags_router.py`. Все 512 тестов успешно пройдены (`pytest`, 100% pass), `ruff check app tests` чист (All checks passed!). |

---

## Блок 21: Исправление отображения и реактивности вкладки «Аудит медиатеки» (2026-09-14T13:00:00Z)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Исправление DOM-иерархии шаблона (Unclosed Section Bugfix)** | `app/templates/index.html`, `tests/test_frontend.py` | **DONE** | Устранена критическая ошибка верстки: закрыт пропущенный тег `</section>` для секции файлового менеджера перед секцией аудита. Ранее блок аудита оказывался дочерним элементом секции файлов и при `$store.app.activeTab = 'audit'` скрывался вместе с родительской секцией (`display: none`). Добавлен тест структуры DOM в `test_frontend.py` для предотвращения регрессий. |
| **R2: Реактивное обновление данных аудита и согласование схем** | `app/static/js/library_audit.js`, `app/templates/index.html`, `app/schemas/library_audit.py`, `app/services/library_audit_service.py` | **DONE** | В `library_audit.js` и кнопку навигации добавлен слушатель и диспатч события `refresh-audit`, а также `$watch` за `$store.app.activeTab === 'audit'`, чтобы при переходе на вкладку данные автоматически подгружались. В `MissingCoverItem` добавлено поле `track_count` для корректного отображения числа треков релиза в таблице. В `DuplicateTrackItem` и шаблоне дубликатов поддержано отображение размера через `size_bytes` / `size`. |
---

## Блок 22: Комплексное исправление ошибок и оптимизация Tag Editor (2026-09-14T14:10:00Z)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Исправление критического бага выбора обложки отдельного трека** | `app/static/js/tag_editor.js`, `app/templates/index.html` | **DONE** | Устранено рассогласование порядка аргументов `openTrackCoverGallery(track, originalIndex)`. В JS сигнатура скорректирована под вызовы из HTML (`track` первым аргументом, `originalIndex` вторым). Теперь при выборе обложки в галерее арт корректно назначается выбранному треку без падений и `undefined`. |
| **R2: Защита индивидуальных правок треков при сохранении (Save Bugfix)** | `app/static/js/tag_editor.js` | **DONE** | В `saveAlbum()` устранена безусловная перезапись полей треков общими данными релиза. Теперь общие метаданные группы (альбом, артист, год, жанр, диски) подставляются в трек только если соответствующее поле трека не заполнено, что сохраняет индивидуальные правки пользователя. |
| **R3: Реактивность группировки релизов и DND авто-перенумерация** | `app/static/js/tag_editor.js`, `app/templates/index.html` | **DONE** | 1) Добавлен вызов `buildReleasesFromTracks()` после завершения `recognizeAllTracks()` (Shazam). 2) Добавлен `$watch('singlesMode')` и `@change` обработчик на чекбокс режима синглов в подвале. 3) После перетаскивания треков (`onDrop`) теперь автоматически перенумеровываются треки затронутых релизов. 4) Глобальный метод `renumberTracks()` в групповом режиме теперь итерирует треки отдельно по каждому релизу, предотвращая сквозное смешение номеров. |
| **R4: Устранение дублирования элементов и очистка мертвого кода** | `app/static/js/tag_editor.js`, `app/templates/index.html` | **DONE** | 1) Удален неиспользуемый мертвый код `applyAlbumToAll()` и `handleCoverUpload()`. 2) Из шапки аккордеона карточки релиза убраны дублирующие кнопки `[Обложка]` и `[Арт трекам]`. 3) Под миниатюрой обложки убран дублирующий текстовый линк `[Загрузить обложку]`, функционал загрузки сохранен на hover-оверлее самой обложки. |
| **R5: Улучшение структуры таблиц и новые функции управления треками** | `app/static/js/tag_editor.js`, `app/templates/index.html` | **DONE** | 1) В групповом режиме из вложенной таблицы треков убраны избыточные колонки «Альбом» и «Год» (уже присутствующие в карточке релиза). 2) Добавлена кнопка исключения трека `[✕]` (`removeTrack(index)`) для групповой и плоской таблиц. 3) В карточку релиза добавлена кнопка `[Очистить названия]` (`cleanGroupTitles(group)`) для очистки названий и извлечения фитов в рамках конкретного релиза. 4) Кнопка «Найти в Deezer» переименована в «Автопоиск релиза» (с учетом MusicBrainz), глобальная кнопка поиска текстов переименована в «Найти все тексты». 5) Версия `tag_editor.js` увеличена до `v=20260914_01`. |
| **R6: Верификация, линтинг и тесты** | `tests/test_frontend.py` | **DONE** | Тесты обновлены с учетом актуализированных наименований кнопок. Все 512 тестов успешно пройдены (`pytest`, 100% pass), линтер `ruff check app tests` чист (All checks passed!). |

---

## Блок 23: Интерактивный селектор дискографии исполнителя (Вариант А) (2026-09-14T14:22:00Z)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Схемы данных дискографии** | `app/schemas/download.py` | **DONE** | Добавлены Pydantic-модели `DiscographyReleaseItem` (id, title, type: album/single/ep/compilation, year, track_count, cover_url, url) и `ArtistDiscographyResponse` (artist_id, artist_name, artist_avatar, source, total_releases, releases). |
| **R2: Клиенты источников (Deezer, Spotify, Yandex, Apple, YouTube)** | `app/services/deezer_client.py`, `app/services/spotify_client.py`, `app/services/yandex_client.py`, `app/services/apple_music_client.py`, `app/services/source_resolver.py` | **DONE** | Во всех клиентах расширен `parse_url` для распознавания URL профилей исполнителей (`/artist/`, `/channel/`, `/@handle`). Реализованы методы извлечения дискографий: `get_artist_discography` / `get_artist_albums`. В `SourceResolver` добавлен единый фасад `resolve_artist_discography(url)` с маппингом релизов и обложек. |
| **R3: API-эндпоинт дискографии** | `app/routers/downloads.py` | **DONE** | Добавлен защищенный эндпоинт `GET /downloads/discography?url=...`, возвращающий `ArtistDiscographyResponse` для выбранного исполнителя. |
| **R4: Frontend-компонент и модальное окно селектора** | `app/static/js/downloader.js`, `app/templates/index.html` | **DONE** | В `downloader.js` добавлена автодетекция ссылок на артистов `checkIsArtistUrl`, состояние `showDiscographyModal`, методы фильтрации по категориям (Все, Альбомы, Синглы & EP, Сборники), поиск по названию, групповой выбор и отправка выбранных URL в пакетную очередь `submitDiscographyBatch()`. В `index.html` добавлена кнопка «Дискография» в строке ввода и модальное окно с адаптивной сеткой карточек релизов. Версия скрипта обновлена до `downloader.js?v=20260914_01`. |
| **R5: Верификация, линтинг и тесты** | `tests/test_deezer_client.py`, `tests/test_spotify_client.py`, `tests/test_yandex_client.py`, `tests/test_downloads_router.py`, `tests/test_frontend.py` | **DONE** | Разработаны юнит-тесты на парсинг ссылок артистов и получение дискографии для всех клиентов, интеграционный тест эндпоинта роутера и тест элементов интерфейса модального окна. Все 520 тестов успешно пройдены (`pytest`, 100% pass), линтер `ruff check app tests` чист (All checks passed!). |

---

## Блок 24: Парсинг дискографии исполнителей Spotify и отказоустойчивый Deezer Fallback (2026-09-14T14:35:00Z)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Скрейпинг открытой SSR-страницы артиста Spotify** | `app/services/spotify_client.py` | **DONE** | Реализован метод `_scrape_artist_web(artist_id)` для извлечения SSR-тегов `<script id="initialState" type="text/plain">` с base64-данными профиля артиста при отсутствии API-ключей. Поддержано извлечение категорий `albums`, `singles`, `compilations`, `popularReleasesAlbums` с дедупликацией по `spotify:album:{id}`, извлечением обложек высокого разрешения и года релиза. |
| **R2: Обновление `get_artist_discography`** | `app/services/spotify_client.py` | **DONE** | Приоритет отдается официальному Web API (при наличии токена), затем скрейпингу публичной SSR-страницы (`_scrape_artist_web`), и только при их недоступности — embed-странице (`_scrape_embed`) для извлечения имени и аватара исполнителя. |
| **R3: Отказоустойчивый Deezer Fallback в SourceResolver** | `app/services/source_resolver.py` | **DONE** | В `resolve_artist_discography` для Spotify добавлен автоматический fallback: если со Spotify получено 0 релизов (например, при антибот-капче или изменении разметки), но известно имя исполнителя, выполняется поиск по каталогу Deezer (`/search/artist`), выбирается лучшее совпадение и загружаются все альбомы и синглы артиста. |
| **R4: Верификация, линтинг и тесты** | `tests/test_spotify_client.py`, `tests/test_source_resolver.py` | **DONE** | Добавлены юнит-тесты `test_spotify_get_artist_discography_web_scrape`, `test_spotify_get_artist_discography_embed_fallback`, `test_resolve_artist_discography_spotify_success`, `test_resolve_artist_discography_spotify_deezer_fallback`. Все 524 теста успешно пройдены (`pytest`), ruff чист (All checks passed!). Проверено на реальной ссылке Eminem (`7dGJo4pcD2V6oG8kP0tJRR`) — 21 релиз. |

---

## Блок 25: Параллельная загрузка/транскодирование треков релиза и ускорение ffmpeg (2026-09-14T14:42:00Z)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Конфигурация параллелизма треков** | `app/config.py`, `.env.example` | **DONE** | Добавлен параметр `TRACK_DOWNLOAD_CONCURRENCY: int = Field(default=3, ge=1, le=10)` в `Settings`. В `.env.example` задокументирована переменная с пояснением (число параллельно загружаемых и конвертируемых треков внутри альбома/плейлиста). |
| **R2: Оптимизация кодировщика ffmpeg / libmp3lame** | `app/services/download_service.py` | **DONE** | В команду транскодирования `_transcode_to_mp3` добавлены аргументы `-threads 0` (автовыбор потоков процессора) и `-compression_level 2` (быстрый режим LAME для постоянного битрейта 320 kbps CBR без потери акустического качества). |
| **R3: Конкурентный пайплайн треков (Track-level Concurrency)** | `app/services/download_service.py` | **DONE** | В `_process_job` пайплайн переработан с последовательного цикла на параллельное выполнение через `asyncio.Semaphore(concurrency)` и `asyncio.gather`. 1) Предварительно вычисляются детерминированные имена целевых файлов (`track_target_paths`), исключающие коллизии и race conditions при нумерации треков. 2) Добавлена плавная агрегация общего прогресса джобы на основе индивидуальных процентов треков (`track_progresses`). 3) Потокобезопасное сохранение первой обложки релиза через `asyncio.Lock`. 4) Изолированная обработка ошибок и очистка временных файлов на уровне отдельного трека. |
| **R4: Верификация, линтинг и тесты** | `tests/test_download_service.py` | **DONE** | Разработан тест `test_download_pipeline_concurrent_tracks_processing`, подтверждающий корректность конкурентной обработки треков альбома, формирование структуры файлов и вычисление прогресса. Все 525 тестов пройдены (`pytest`, 100% pass), линтер `ruff check app tests` чист (All checks passed!). |

---

## Блок 26: Пакетное открытие и редактирование всех релизов из очереди (Bulk Edit Queue in Tag Editor) (2026-09-14T15:50:00Z)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Схемы и роутер тегов (cover_path & track covers)** | `app/schemas/tag.py`, `app/routers/tags.py` | **DONE** | В схему `TrackTags` добавлено поле `cover_path: str | None`. В `PUT /tags/batch` поддержано чтение и валидация бинарных данных обложки из `track.cover_path` через `tag_service.resolve_path()` для наполнения `track_covers_data`. Это исключает передачу громоздких base64 строк при открытии скачанных релизов с локального диска. |
| **R2: Зачистка нескольких задач в OrganizerService** | `app/services/organizer_service.py` | **DONE** | В шаг 6 `organize_album()` добавлено извлечение и очистка всех каталогов задач `job_id` в песочнице `_tmp_sandbox` из путей всех перемещенных треков (`track.path`) и `source_dir`, что обеспечивает полную очистку временных буферов всех сохраненных релизов при пакетной организации. |
| **R3: Frontend очереди и пакетного открытия (index.html)** | `app/templates/index.html` | **DONE** | В глобальный стор `Alpine.store('app').queue` добавлен геттер `readyCount` и метод `openAllReadyJobsInTagEditor()`, выполняющий параллельный опрос `/downloads/{jobId}/tracks` для всех завершенных задач и объединяющий треки с их `cover_path` и массивом `jobIds`. Добавлена кнопка «В теги всё (N)» в шапке шторки очереди, баннер пакетного редактирования «Готово к тегированию: N рел.» внутри списка задач, вспомогательная кнопка в карточке активной загрузки и бейдж на плавающей кнопке-пилюле. |
| **R4: Редактор тегов и сохранение нескольких задач (tag_editor.js)** | `app/static/js/tag_editor.js`, `app/static/js/downloader.js` | **DONE** | В `tag_editor.js` реализована поддержка `this.jobIds`: детерминированное разделение на карточки релизов по композитному ключу `${albumArtistKey}:::${albumName.toLowerCase()}`, сохранение обложки каждого релиза через `cover_path` / `cover_preview_url`, динамический текст кнопки «Сохранить всё в медиатеку (N рел.)», отправка запросов `DELETE /downloads/${jid}` после успешного сохранения и мгновенная синхронизация очереди. |
| **R5: Верификация, линтинг и тесты** | `tests/test_organizer_service.py`, `tests/test_tags_router.py`, `tests/test_frontend.py` | **DONE** | Добавлены тесты `test_organize_multiple_tmp_jobs_cleanup`, `test_batch_save_with_track_cover_path` и `test_queue_bulk_edit_ui`. Все 528 тестов успешно пройдены (`pytest`, 100% pass), линтер `ruff check app tests` чист (All checks passed!). |

---

## Блок 27: Полное извлечение и предзаполнение метаданных Deezer в Tag Editor и запись ID3v2.4 (2026-09-14T16:00:00Z)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Извлечение и обогащение метаданных в SourceResolver** | `app/services/source_resolver.py`, `app/schemas/download.py` | **DONE** | 1) Для одиночных треков Deezer: автоматический запрос `get_album(album_id)` при наличии альбома. Исправлена критическая ошибка, из-за которой `album_artist` становился `"Unknown Artist"` (теперь берется точное имя `artist.name` альбома). Извлечены жанры `genres`, год `release_date`, общее число дорожек `nb_tracks` (`track_total`) и номер диска `disk_number` (`disc_number`). 2) Для альбомов и плейлистов: корректное извлечение `artist.name`, жанров, общего числа треков и расчет `disc_total` как максимума по `disk_number`. 3) Поля `track_total`, `disc_number`, `disc_total` добавлены в модель `ResolvedTrack` и возвращаются в `album_meta`. |
| **R2: Запись тегов TRCK и TPOS в ID3v2.4 и API задач** | `app/services/download_service.py`, `app/schemas/download.py` | **DONE** | 1) В `_write_draft_tags` добавлен импорт `TPOS` из mutagen. Тег `TRCK` записывается в формате `f"{track_number}/{track_total}"`, а фрейм `TPOS` — в формате `f"{disc_number}/{disc_total}"`. 2) В схему `DownloadTracksResponse` и метод `get_job_tracks()` добавлены поля `disc_total` и `track_total`. |
| **R3: Предзаполнение полей в интерфейсе Tag Editor** | `app/templates/index.html`, `app/static/js/downloader.js`, `app/static/js/tag_editor.js` | **DONE** | 1) В `downloader.js` и `index.html` при открытии задачи (`openTagEditorForJob` / `openJobInTagEditor` / `openAllReadyJobsInTagEditor`) поля `disc_total`, `track_total`, `disc_number`, `genre`, `year`, `album_artist` передаются в объект `album` и каждый трек. 2) В `tag_editor.js`: метод `loadData()` сохраняет `disc_total` и `track_total` в объекте `album`, настраивает fallback для треков, а `buildReleasesFromTracks()` устанавливает `group.discTotal`, `group.genre`, `group.year` и дозаполняет их из треков группы. Все поля релиза Deezer мгновенно видны при открытии редактора. |
| **R4: Верификация, линтинг и тесты** | `tests/test_source_resolver.py`, `tests/test_download_service.py`, `tests/test_frontend.py` | **DONE** | Добавлены тесты `test_resolve_deezer_track_full_metadata`, `test_resolve_deezer_album_multi_disc_metadata`, `test_write_draft_tags_with_totals_and_disc`, `test_tag_editor_and_downloader_disc_and_track_total_fields`. Все 532 теста успешно пройдены (`pytest`, 100% pass), линтер `ruff check app tests` чист (All checks passed!). |

---

## Блок 28: Аудит неполных релизов и дозагрузка недостающих треков (Incomplete Albums Audit & One-Click Backfill) (2026-09-14T17:00:00Z)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Схемы данных аудита неполных релизов** | `app/schemas/library_audit.py` | **DONE** | Добавлены Pydantic-модели `IncompleteTrackInfo` (track_number, disc_number, title, duration, is_present, local_path, deezer_id, deezer_url), `IncompleteAlbumItem` (path, album, artist, year, has_cover, cover_url, present_count, expected_count, missing_track_numbers, missing_tracks, all_tracks, is_catalog_matched, deezer_album_id), `IncompleteAlbumsResponse` и `DownloadMissingTracksRequest`. В схему `AuditSummaryResponse` добавлено поле `incomplete_albums_count`. |
| **R2: Офлайн-детекция и онлайн-сверка в LibraryAuditService** | `app/services/library_audit_service.py`, `app/dependencies.py` | **DONE** | Внедрен `DeezerClient` в `LibraryAuditService`. Реализован быстрый офлайн-метод `get_incomplete_albums()` для детекции неполных альбомов по ID3v2.4 тегам `TRCK`/`TPOS` и числовым разрывам нумерации дорожек по каждому диску. В `get_summary()` интегрирован расчет `incomplete_albums_count`. Реализован метод `match_album_catalog()` для сверки локальных треков с официальным треклистом Deezer API по позициям дорожек и нечетким названиям с получением прямых URL треков. |
| **R3: Маршруты API аудита и дозагрузки треков** | `app/routers/files.py` | **DONE** | Реализованы защищенные эндпоинты `GET /files/audit/incomplete-albums` (список неполных релизов коллекции), `GET /files/audit/incomplete-albums/catalog-match` (сопоставление треклиста альбома с Deezer) и `POST /files/audit/incomplete-albums/download-missing` (пакетное или поштучное создание задач в `DownloadService` для недостающих треков релиза). |
| **R4: Frontend интерфейс (5-я карточка, вкладка и аккордеон треклиста)** | `app/static/js/library_audit.js`, `app/templates/index.html` | **DONE** | В `library_audit.js` добавлены методы `loadIncompleteAlbums()`, `toggleAlbumExpand()`, `matchCatalog()` и `downloadMissing()`. В `index.html` добавлена 5-я карточка метрик на дашборде аудита, кнопка подраздела «Неполные релизы» в таб-баре, карточки неполных релизов с прогресс-барами полноты, бейджами пропущенных номеров дорожек, аккордеоном сверки треклиста и кнопками мгновенной догрузки. |
| **R5: Верификация, линтинг и тесты** | `tests/test_library_audit.py`, `tests/test_frontend.py` | **DONE** | Добавлены модульные тесты `test_incomplete_albums_detection`, интеграционные тесты эндпоинтов каталожной сверки и догрузки `test_match_album_catalog_and_download_endpoint`, а также UI-тест `test_incomplete_albums_audit_ui`. Все 535 тестов успешно пройдены (`pytest`, 100% pass), линтер `ruff check app tests` чист (All checks passed!). |

---

## Блок 13: Стабилизация загрузчика YouTube (yt-dlp EJS + Python 3.11)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **EJS Challenge Solver & Deno** | `deploy.sh`, `requirements.txt`, `app/services/download_service.py` | **DONE** | Устранена ошибка YouTube anti-bot (`The page needs to be reloaded`). Добавлен пакет `yt-dlp-ejs>=0.8.0`, в venv внедрен легковесный автономный JS-рантайм Deno (`venv/bin/deno`), в `_execute_yt_dlp` добавлен явный экспорт `PATH` с каталогом `venv/bin`. |
| **Python 3.11 Migration** | `/opt/music-manager/.python/`, `/opt/music-manager/venv/` | **DONE** | Полностью устранены предупреждения `Deprecated Feature: Support for Python version 3.10 has been deprecated`. Окружение на сервере переведено на Python 3.11.16 строго в пределах каталога `/opt/music-manager`. Сервис `music-manager.service` успешно перезапущен и протестирован на боевых треках. |

---

## Блок 14: Интеллектуальный парсинг множественных исполнителей и фитов (2026-09-14T19:35:00Z)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Распознавание составных авторов и фитов в DeezerClient** | `app/services/deezer_client.py`, `tests/test_deezer_client.py` | **DONE** | Реализован статический метод `split_artist_string` для корректного разделения списков артистов по запятым (`,`), фитам (`feat.`, `ft.`, `featuring`, `with`), союзам (`&`, `and`, `и`), `x`, `vs` с защитой составных псевдонимов (*Tyler, The Creator*, *Earth, Wind & Fire*, *Crosby, Stills, Nash & Young*). Метод `extract_feats_from_title` извлекает приглашенных исполнителей из полей `title` и `title_version`. Метод `format_artists` агрегирует всех уникальных артистов через `\\`. |
| **R2: Сквозное форматирование в маршрутах и сервисах** | `app/routers/tags.py`, `app/services/source_resolver.py`, `app/services/tag_service.py`, `tests/test_tags_router.py`, `tests/test_tag_service.py` | **DONE** | В эндпоинтах `GET /tags/deezer/albums` и `GET /tags/deezer/albums/{id}` артисты альбома и каждого трека форматируются через `format_artists`. В `source_resolver.py` сохраняются все соавторы для поиска аудиопотока и записи в ID3v2.4. В `TagService.clean_track_title()` существующие составные имена разбиваются на список отдельных исполнителей. |
| **R3: Поддержка разделения авторов в Tag Editor UI** | `app/static/js/tag_editor.js` | **DONE** | Метод `parseArtistsString` и обновленный `cleanTrackTitle` в интерфейсе редактора тегов для распознавания и разделения запятых и фитов на `\\`. |

## Блок 15: Доработка и исправление механизма дозагрузки недостающих треков (2026-09-14T19:43:00Z)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Определение консенсусного количества треков** | `app/services/library_audit_service.py`, `tests/test_library_audit.py` | **DONE** | Исправлено ложное определение неполных альбомов из-за единичных сбойных тегов `track_total` (например, 35 вместо 13): внедрен расчет консенсуса по большинству треков через `Counter` и корректное распознавание одиночных синглов. |
| **R2: Санитизация поиска альбомов в Deezer** | `app/services/library_audit_service.py` | **DONE** | Реализована многоуровневая санитизация поисковых запросов в Deezer API при каталожной сверке (удаление суффиксов `- Single`, `- EP`, `(feat. ...)`, `[Expanded]`, `(Deluxe)` и поиск по очищенному имени альбома), устраняющая сбои сопоставления синглов и делюкс-релизов. |
| **R3: Передача `deezer_album_id` и прямых ссылок** | `app/schemas/library_audit.py`, `app/routers/files.py`, `app/static/js/library_audit.js` | **DONE** | В схему `DownloadMissingTracksRequest` и эндпоинт `POST /files/audit/incomplete-albums/download-missing` добавлено поле `deezer_album_id`. Во фронтенде `library_audit.js` метод `downloadMissing` передает идентификатор альбома и список уже обнаруженных прямых URL треков. |
| **R4: UI-состояния кнопки и подсказки** | `app/templates/index.html`, `app/static/js/library_audit.js` | **DONE** | В интерфейсе отображается бейдж «Укомплектован» для полных альбомов вместо неактивной кнопки. При нулевом числе созданных задач выдается информативное сообщение пользователю. |

---

## Блок 16: Устранение рассинхронизации данных и кэширования UI (2026-09-14T19:50:00Z)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Заголовки анти-кэширования HTTP** | `app/main.py` | **DONE** | Внедрен глобальный middleware в FastAPI, проставляющий заголовки `Cache-Control: no-store, no-cache, must-revalidate, max-age=0`, `Pragma: no-cache` и `Expires: 0` для всех динамических API-эндпоинтов, предотвращающий застревание ответов в дисковом кэше браузеров. |
| **R2: Принудительный сброс бэкенд-индекса и инвалидация** | `app/routers/files.py`, `app/routers/tags.py`, `app/routers/lyrics.py`, `app/services/library_audit_service.py` | **DONE** | Добавлен эндпоинт `POST /files/audit/refresh` и параметр `force=True` в `GET /files/audit/summary`. Вызов `audit_service.invalidate()` внедрен во все точки модификации медиатеки: `PUT /tags/batch`, `DELETE /files`, `POST /lyrics`, `DELETE /lyrics`. |
| **R3: Реактивные события фронтенда и сброс кэша скриптов** | `app/static/js/file_manager.js`, `app/static/js/library_audit.js`, `app/static/js/lyrics_modal.js`, `app/static/js/tag_editor.js`, `app/templates/index.html` | **DONE** | Добавлены сквозные пользовательские события `refresh-audit` и `refresh-file-manager`, реактивные слушатели переключения вкладок `$watch('$store.app.activeTab')` и слушатели `window.addEventListener('focus')`. Во все `fetch`-запросы списков файлов и аудита добавлены динамические кэш-бастеры `_t=${Date.now()}`. Версии скриптов в `index.html` инкрементированы до `?v=20260914_03`. |

---

## Блок 17: Автоматическая каталожная сверка и кэширование неполных релизов (2026-09-14T20:08:00Z)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Кэширование сверок и отсечение ложно-неполных релизов** | `app/services/library_audit_service.py`, `tests/test_library_audit.py` | **DONE** | Внедрен in-memory кэш `_catalog_cache` в `LibraryAuditService`. В методе `get_incomplete_albums()` добавлена автоматическая сверка кандидатов с официальным каталогом Deezer (`match_album_catalog`). Релизы, у которых все треки официального альбома присутствуют на диске (например, 25 из 25 в `The Slim Shady LP` при старых тегах `track_total=30`), навсегда помечаются укомплектованными и исключаются из списка неполных релизов. |
| **R2: Игнорирование одиночных синглов в неполных альбомах** | `app/services/library_audit_service.py`, `tests/test_library_audit.py` | **DONE** | Папки синглов с 1 треком (где имя каталога или тег альбома содержит `- Single`, `[Single]`, `сингл`) больше не расцениваются как неполные альбомы, даже если трек имел порядковый номер сборника (например, 13/20). |
| **R3: Предзаполнение Deezer URL для мгновенной дозагрузки** | `app/services/library_audit_service.py`, `app/static/js/library_audit.js` | **DONE** | При обнаружении действительно неполных релизов (например, `The Death of Slim Shady` без трека 2 «Habits», `Curtain Call 2` с 4 из 25 треков) они сразу поставляются на фронтенд со статусом `is_catalog_matched: true` и готовыми `deezer_url`. Кнопка «Скачать недостающие» мгновенно ставит недостающие треки в очередь загрузок без необходимости ручной сверки. |
| **R4: Тесты и деплой на сервер** | `tests/test_library_audit.py` | **DONE** | Добавлены модульные тесты `test_incomplete_albums_auto_match_deezer_filters_false_positives` и `test_incomplete_albums_single_folder_ignored`. Все 542 теста успешно пройдены (`pytest tests/`, 100% pass), линтер `ruff` чист. Изменения закоммичены, запушены в репозиторий и развернуты на боевом сервере `nl_vpn` с перезапуском `music-manager.service`. |

---

## Блок 18: Полное удаление интеграции с MusicBrainz (2026-09-14T20:17:00Z)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Очистка бэкенда и эндпоинтов** | `app/services/musicbrainz_client.py`, `app/dependencies.py`, `app/routers/tags.py`, `app/schemas/tag.py`, `app/services/tag_service.py` | **DONE** | Полностью удален сервис `MusicBrainzClient` и файл `app/services/musicbrainz_client.py`. Удалена зависимость `MusicBrainzClientDep` и эндпоинты `/tags/musicbrainz/albums` и `/tags/musicbrainz/albums/{release_id}`. Из схемы `CoverSearchResultItem` и сервиса `TagService.search_covers` исключен источник `musicbrainz`. |
| **R2: Очистка фронтенда** | `app/templates/index.html`, `app/static/js/tag_editor.js` | **DONE** | Удален переключатель источников метаданных (`metadataSource`) в редакторе тегов, поиск релизов переведен на прямой опрос Deezer (`searchDeezerAlbums`), в галерее обложек оставлены 4 проверенных источника (Apple Music, Deezer, Яндекс Музыка, Spotify). |
| **R3: Актуализация тестов и верификация** | `tests/test_musicbrainz_client.py`, `tests/test_tags_router.py`, `tests/test_frontend.py` | **DONE** | Удален устаревший тестовый файл `tests/test_musicbrainz_client.py` и тест `test_musicbrainz_album_endpoints`. Все 531 тест успешно пройдены (`pytest`, 100% pass), линтер `ruff check app tests` без единого замечания. |

---

## Блок 19: Потрековое определение исполнителей при сопоставлении с Deezer (2026-09-14T20:21:00Z)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Обогащение треков через `/track/{id}` в DeezerClient** | `app/services/deezer_client.py`, `tests/test_deezer_client.py` | **DONE** | В методы `DeezerClient.get_album` и `get_playlist` добавлен флаг `fetch_track_details=True` с параллельным получением полного списка `contributors` для каждого трека через `asyncio.gather(Semaphore(10))` менее чем за 0.5с. В `format_artists` добавлена приоритетная фильтрация исполнительских ролей (`Main`, `Featured`, `Primary`, `Performer`). |
| **R2: Потрековое определение в Tag Editor и аудиторе релизов** | `app/routers/tags.py`, `app/schemas/library_audit.py`, `app/services/library_audit_service.py`, `app/services/source_resolver.py` | **DONE** | В эндпоинте `/tags/deezer/albums/{id}` каждый трек альбома получает индивидуально распознанный список соавторов через `\\`. В `LibraryAuditService.match_album_catalog` и `IncompleteTrackInfo` добавлено поле `artist` для каждого трека релиза. В `SourceResolver._resolve_deezer` треки альбомов и плейлистов скачиваются с точными исполнителями. |
| **R3: Интеллектуальное сопоставление в UI (Frontend)** | `app/static/js/tag_editor.js`, `app/templates/index.html` | **DONE** | В `tag_editor.js` метод `applyDeezerAlbum` дополнен интеллектуальным сопоставлением треков (по номеру дорожки `track_number`, затем по нечеткому совпадению названия `title`, с fallback на индекс) и точной записью индивидуальных исполнителей `it.track.artist = dt.artist`. В таблице неполных релизов на дашборде аудита выводится имя трекового артиста. |
| **R4: Тесты и верификация** | `tests/test_deezer_client.py`, `tests/test_library_audit.py` | **DONE** | Добавлены тесты `test_get_album_with_track_details` и `test_format_artists_with_contributor_roles`. Все 533 теста пройдены на 100% (`pytest`), ruff linter — 0 ошибок. |

---

## Блок 20: Нативный Go CLI-сканер медиатеки (`music-scanner`) и потоковая NDJSON-индексация (2026-09-14T22:10:00Z)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Автономный Go-модуль сканера (`music-scanner`)** | `tools/music-scanner/go.mod`, `tools/music-scanner/main.go`, `tools/music-scanner/id3/models.go`, `tools/music-scanner/id3/parser.go`, `tools/music-scanner/scanner/walker.go`, `tools/music-scanner/scanner/worker.go` | **DONE** | Реализован автономный Go-модуль без внешних библиотек (pure Go). Высокоскоростной обход файлов через `filepath.WalkDir` (Producer), пул из $N$ горутин (`NumCPU*2` воркеров), легковесный ID3v2.3/v2.4 парсер с чтением только заголовка и тегов без считывания всего аудиопотока, вычисление длительности из TLEN / MPEG Xing / CBR фреймов, проверка наличия `.lrc`/`.txt`, потоковый вывод в stdout в формате NDJSON. Скомпилирован статический ELF-бинарник `bin/music-scanner` (2.2 МБ). |
| **R2: Адаптер сервиса в Python (`NativeScannerService`)** | `app/services/native_scanner.py`, `app/config.py`, `requirements.txt`, `.env.example` | **DONE** | Создан сервис `NativeScannerService` для безопасного запуска бинарника `music-scanner --root {safe_root}` через `asyncio.create_subprocess_exec`. Построчный асинхронный парсинг NDJSON-потока с ускорением через `orjson` (с fallback на `json`). Проверка прав на исполнение (`is_available`), санитизация путей через `PathSandbox.root`. Опция `MUSIC_SCANNER_BIN_PATH` в `Settings`. Библиотека `orjson` добавлена в `requirements.txt`. |
| **R3: Интеграция в `LibraryAuditService` и Dependency Injection** | `app/services/library_audit_service.py`, `app/dependencies.py` | **DONE** | Внедрен метод `_ensure_index_native()`: создание объектов `TrackTags` напрямую из структур Go, инкрементальное сопоставление по `mtime` и `lyrics_status`, удаление удаленных файлов. Автоматический безопасный fallback на стандартный Python `os.walk` + `mutagen` (`_ensure_index_python()`) при отсутствии бинарника или любых сбоях процесса. Добавлен DI-провайдер `get_native_scanner` и внедрение в `get_library_audit_service`. |
| **R4: Модульные тесты и верификация** | `tests/test_native_scanner.py` | **DONE** | 10 комплексных тестов в `tests/test_native_scanner.py`: проверка доступности бинарника, парсинг NDJSON-потока, устойчивость к поврежденным строкам JSON, обработка кодов ошибок, индексация через Go-сканер, откат на Python при отсутствии бинарника и при ошибках выполнения, сквозной тест реального бинарника `bin/music-scanner`. Все 543 теста успешно пройдены (`pytest`, 100% pass), линтер `ruff check` чист (0 замечаний). |

---

## Блок 21: Предотвращение транслитерации/латинизации названий и исправление редактирования треков (2026-09-14T22:55:00Z)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Запрет транслитерации и латинизации метаданных** | `app/services/download_service.py`, `app/services/source_resolver.py`, `app/services/deezer_client.py`, `app/services/apple_music_client.py`, `app/services/spotify_client.py`, `app/services/recognition_service.py` | **DONE** | 1. `download_service.py`: в команду yt-dlp добавлен заголовок `--add-header "Accept-Language:ru-RU,ru;q=0.9,en-US;q=0.8,en;q=0.7"`.<br>2. `source_resolver.py`: в конфигурацию yt-dlp для поиска треков, альбомов и дискографий добавлены `http_headers: {"Accept-Language": "ru-RU,..."}` и `extractor_args: {"youtube": {"lang": ["ru"]}}`.<br>3. `deezer_client.py`, `apple_music_client.py`, `spotify_client.py`: заменен/добавлен заголовок `Accept-Language: ru-RU,ru;q=0.9,en-US;q=0.8,en;q=0.7` вместо жестко зашитого `en-US`.<br>4. `recognition_service.py`: клиент Shazam инициализируется с `language="ru-RU", endpoint_country="RU"`, что исключает принудительную романизацию распознанных кириллических треков. |
| **R2: Исправление логики сохранения и распространения тегов релиза** | `app/static/js/tag_editor.js` | **DONE** | Устранена блокировка редактирования в методе `saveAlbum`: убрано ошибочное условие `!trk.album` / `!group.isSingles`, которое блокировало сохранение изменений полей карточки релиза (название альбома, исполнитель альбома, год, жанр, диски) для треков, уже имевших теги, и препятствовало объединению синглов в альбом. Изменения карточки релиза гарантированно распространяются на треки группы. В плоском режиме (`viewMode === 'flat'`) сохранены индивидуальные правки таблицы. Добавлена строгая клиентская валидация чисел перед отправкой и информативный вывод ошибок `err.detail` в тосте. |
| **R3: Валидаторы Pydantic и нормализация путей** | `app/schemas/tag.py`, `app/services/tag_service.py`, `app/services/organizer_service.py` | **DONE** | 1. В `AlbumTags` и `TrackTags` добавлены `mode="before"` валидаторы для `year`, `track_number`, `disc_number`, `track_total`, `disc_total`. Значения `0`, некорректные строки и выходящие за диапазон числа безопасно приводятся к `None` или `1` вместо падения с HTTP 422 `RequestValidationError`.<br>2. В `TagService.resolve_path` и `OrganizerService._resolve_source_path` добавлена нормализация путей с префиксом `music_library/` (или именем корневой папки медиатеки), исключающая ложные ошибки HTTP 404 `NotFoundError`. |
| **R4: Тесты и верификация** | `tests/test_recognition_service.py`, `tests/test_tags_router.py` | **DONE** | Добавлены тесты `test_shazam_russian_defaults`, `test_batch_save_with_zero_or_invalid_numbers`, `test_get_tags_and_batch_save_with_music_library_prefix`. Все 546 тестов пройдены успешно (100% pass), линтер `ruff check` чист (0 замечаний). |

---

## Блок 22: Унификация жанров (разделитель `\\\\`) и канонизация `Hip-Hop` (2026-09-15T00:35:00Z)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Функции нормализации и валидаторы Pydantic** | `app/schemas/tag.py`, `app/schemas/download.py` | **DONE** | 1. `normalize_genre_item(item)`: строго приводит все варианты написания (`Hip Hop`, `hip hop`, `hip-hop`, `hiphop`, `хип хоп`, `хип-хоп`, `Russian Hip Hop`) к каноническому виду `Hip-Hop` через дефис, не затрагивая сторонние жанры (`Trip Hop`, `R&B`, `Post-Punk`).<br>2. `format_genres(raw)`: разбивает жанры по всем разделителям (`/`, `,`, `;`, `\|`, `\x00`, `\\`), нормализует элементы через `normalize_genre_item`, дедуплицирует без учета регистра с сохранением исходного порядка и склеивает через `\\`.<br>3. Pydantic `@field_validator("genre", mode="before")` внедрен во все схемы: `AlbumTags`, `TrackTags`, `RecognizedTrackResponse`, `DeezerAlbumDetailsResponse`, `ResolvedTrack`, `DownloadMetadata`. Любые входные данные автоматически нормализуются. |
| **R2: Обновление аудио-пайплайна и сервисов** | `app/services/tag_service.py`, `app/services/download_service.py`, `app/services/source_resolver.py`, `app/routers/tags.py` | **DONE** | 1. `tag_service.py`: метод `parse_genres(raw)` преобразует жанры в список для mutagen ID3v2.4 TCON с null-разделением; `read_tags` использует `format_genres`.<br>2. `download_service.py`: `_embed_metadata` гарантированно форматирует жанры перед добавлением TCON.<br>3. `source_resolver.py` и `routers/tags.py`: списки жанров Deezer (`"Rap/Hip Hop"`) собираются через `format_genres`, исключая сохранение неразбитых слешей. |
| **R3: Поддержка в нативном Go CLI-сканере (`music-scanner`)** | `tools/music-scanner/id3/models.go`, `tools/music-scanner/id3/parser.go`, `app/services/library_audit_service.py` | **DONE** | 1. В Go-модель `TrackMeta` добавлено поле `Genre string` (`json:"genre"`).<br>2. В `parser.go` добавлен парсинг ID3v2.2 фрейма `TCO` и ID3v2.3/2.4 `TCON`.<br>3. `LibraryAuditService._ensure_index_native` передает `genre=item.get("genre")` в `TrackTags`, где поле автоматически нормализуется Pydantic.<br>4. Бинарник `bin/music-scanner` скомпилирован заново (`CGO_ENABLED=0`, `-ldflags="-s -w"`). |
| **R4: Редактор тегов на фронтенде (UI)** | `app/static/js/tag_editor.js`, `app/templates/index.html` | **DONE** | 1. В компонент `tagEditorComponent` добавлен метод `formatGenres(raw)`.<br>2. Интеграция в `buildReleasesFromTracks`, `openTagEditor`, `applyGroupToTracks`, `saveAlbum`, `recognizeTrack`, `recognizeAllTracks` и `applyDeezerAlbum`.<br>3. Поле ввода жанров в шаблоне карточки релиза (`index.html`) оснащено слушателями `@blur` и `@change` (`group.genre = formatGenres(group.genre)`), что сразу разделяет введенные жанры через `\\` и нормализует `Hip-Hop` прямо при редактировании в браузере. |
| **R5: Тесты и верификация** | `tests/test_tag_service.py`, `tests/test_tags_router.py` | **DONE** | Добавлены тесты `test_genre_formatting_and_hip_hop_normalization` и `test_genre_schema_validation_and_id3_roundtrip`. Обновлена проверка нормализации Deezer в `test_tags_router.py`. Все 548 тестов пройдены успешно (100% pass), линтер `ruff check` чист (0 замечаний). |
 
---
 
## Блок 23: Сохранение треков строго по альбомам (`Альбом/Треки.mp3`) без каталогов исполнителей (2026-09-15T01:30:00Z)
 
| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Обновление шаблона путей в `OrganizerService`** | `app/services/organizer_service.py` | **DONE** | 1. `compute_album_dir`: теперь возвращает строго очищенное название альбома (`clean_album`) без префикса исполнителя (`clean_artist/`) и суффикса года (` - {year}`).<br>2. `compute_target_paths`: генерирует целевые пути по новому шаблону `MUSIC_DIR/{Album}/{TrackNumber:02d}. {Title}.mp3`.<br>3. `compute_single_dir`: формирует каталог сингла по названию трека (`clean_folder`) без префикса исполнителя.<br>4. `organize_album`: ключ группировки треков альбома переведен на `__album__{effective_album.lower()}`, гарантируя объединение всех треков релиза в одной папке альбома даже при разных исполнителях треков. |
| **R2: Обновление тестов и верификация** | `tests/test_organizer_service.py`, `tests/test_tags_router.py` | **DONE** | 1. `tests/test_organizer_service.py`: обновлены все 13 модульных тестов с проверками целевых путей, перемещения файлов, смены очередности треков (in-place swap), дедупликации и очистки пустых каталогов под новую схему `Album/01. Track.mp3`.<br>2. `tests/test_tags_router.py`: обновлены тесты `test_batch_save_from_tmp_moves_to_music_dir`, `test_batch_save_with_reorganize_true` и `test_batch_save_with_track_cover_path`.<br>3. Все 548 тестов пройдены успешно (100% pass), линтер `ruff check` чист (0 замечаний). |

---

## Блок 24: Пакетная реорганизация существующих треков по альбомам по кнопке во вкладке Аудит (2026-09-15T01:45:00Z)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Схема ответа и бэкенд-сервисы** | `app/schemas/library_audit.py`, `app/services/organizer_service.py`, `app/services/library_audit_service.py` | **DONE** | 1. Схема `ReorganizeLibraryResponse`: поля `success`, `total_tracks`, `moved_tracks`, `total_albums`, `errors`.<br>2. Метод `OrganizerService.cleanup_all_empty_directories()`: рекурсивная зачистка всех пустых директорий снизу вверх с защитой корня `MUSIC_DIR` и удалением служебного мусора ОС.<br>3. Метод `OrganizerService.reorganize_library(tracks)`: группировка по альбомам и синглам, автопоиск обложек в исходных каталогах, безопасное двухфазное перемещение аудиофайлов, синхронный перенос `.lrc`/`.txt` и обложек `cover.*`, очистка старых папок артистов.<br>4. Метод `LibraryAuditService.reorganize_library(organizer)`: использование кэша индекса, выполнение перемещения в потоке `asyncio.to_thread`, сброс кэша (`invalidate()`) и принудительная переиндексация (`ensure_index(force=True)`). |
| **R2: Эндпоинт API** | `app/routers/files.py` | **DONE** | Добавлен защищенный сессией эндпоинт `POST /files/audit/reorganize` с внедрением зависимостей `LibraryAuditServiceDep` и `OrganizerServiceDep`. |
| **R3: Пользовательский интерфейс (Frontend No-Build)** | `app/templates/index.html`, `app/static/js/library_audit.js` | **DONE** | 1. В шапку вкладки «Аудит» добавлена кнопка «Разложить по альбомам» с индикацией процесса.<br>2. Добавлено модальное окно подтверждения `$store.app.activeModal === 'reorganize_confirm'` с подробным предупреждением.<br>3. Реактивное обновление данных аудита (`loadAll(true)`) и файлового менеджера (`refresh-file-manager`) после завершения с тостом статистики. |
| **R4: Тесты и верификация** | `tests/test_organizer_service.py`, `tests/test_library_audit.py` | **DONE** | Добавлены модульные тесты `test_reorganize_library_moves_all_tracks_and_cleans_artist_folders`, `test_reorganize_library_already_organized_is_idempotent` и интеграционный тест эндпоинта `test_audit_reorganize_endpoint`. Все 550 тестов пройдены успешно (100% pass), линтер `ruff check` чист (0 замечаний). |

---

## Блок 25: Оптимизация производительности аудита и устранение зависаний при загрузке (2026-09-15T02:00:00Z)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Устранение массовых запросов к Deezer в `get_incomplete_albums`** | `app/services/library_audit_service.py`, `app/routers/files.py` | **DONE** | 1. Параметр `auto_match` по умолчанию переведен в `False` как в сигнатуре `get_incomplete_albums(auto_match: bool = False)`, так и в роутере `GET /files/audit/incomplete-albums`.<br>2. Устранена блокировка интерфейса: вместо 110-секундных зависаний с ошибками `Quota limit exceeded [4]` от Deezer API при открытии вкладки, аудит выполняется мгновенно (~0.4 сек) на базе локальных ID3-тегов TRCK/TPOS.<br>3. Добавлена защита `quota_exceeded`: при обнаружении лимита запросов внешние вызовы немедленно прерываются.<br>4. Точечная каталожная сверка Deezer по-прежнему доступна по клику на раскрытие конкретного релиза (`toggleAlbumExpand`). |
| **R2: Оптимизация кнопки реорганизации и кэширования статики** | `app/static/js/library_audit.js`, `app/templates/index.html` | **DONE** | 1. Кнопка «Разложить по альбомам» отвязана от `isLoading`: теперь ее можно нажать в любой момент без ожидания завершения фонового обновления других вкладок.<br>2. В модальном окне подтверждения реорганизации добавлено мгновенное скрытие модала при клике (`$store.app.activeModal = null`).<br>3. После реорганизации на бэкенде вызывается быстрый опрос `loadAll(false)` вместо полного сброса индекса.<br>4. Обновлена версия скрипта `library_audit.js?v=20260915_03` для гарантированного сброса кэша браузеров на клиентах. |

---

## Блок 26: Подсистема аудита и каталогизации медиатеки на Go (Daemon + fsnotify) + SQLite (2026-09-15T16:50:00Z)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Архитектура хранилища SQLite и конфигурация** | `app/core/database.py`, `app/config.py`, `.env.example`, `requirements.txt` | **DONE** | 1. Добавлен параметр `DATABASE_PATH` (по умолчанию `data/library.db`) в `Settings`.<br>2. Реализована схема DDL с WAL-режимом (`PRAGMA journal_mode = WAL; busy_timeout = 5000; synchronous = NORMAL; foreign_keys = ON;`).<br>3. Таблицы: `artists`, `albums` (с поддержкой `youtube_id`, `deezer_id`, `spotify_id`, `yandex_id`, `genius_id`), `tracks`, `genres`, `track_genres`, `missing_tracks`, `audit_issues`.<br>4. Асинхронное подключение `aiosqlite` в FastAPI. |
| **R2: Go Auditor Core (`tools/music-scanner`)** | `tools/music-scanner/` | **DONE** | 1. **Валидатор путей:** проверка `{Album}/{TrackNumber:02d}. {Title}.mp3` в `validator/path.go`.<br>2. **Нормализатор жанров:** проверка `\p{Cyrillic}` и словарь нормализации в `validator/genre.go`, ID3v2.4 TCON rewriter в `id3/writer.go`.<br>3. **Хранилище SQLite (Go):** Pure Go `modernc.org/sqlite` с WAL-режимом и батч-транзакциями в `db/sqlite.go`, `db/schema.go`.<br>4. **Сверка каталога:** поиск и сопоставление альбомов Deezer и фиксация `missing_tracks` в `catalog/deezer.go` и `catalog/matcher.go`.<br>5. **Фоновый демон:** рекурсивный `fsnotify` watcher с дебаунсингом (500 мс) в `watcher/fsnotify.go`.<br>6. Сборка бинарника `bin/music-scanner` (`CGO_ENABLED=0`, `-ldflags="-s -w"`). |
| **R3: Интеграция с FastAPI бэкендом** | `app/schemas/library_audit.py`, `app/services/library_audit_service.py`, `app/routers/files.py`, `app/dependencies.py` | **DONE** | 1. Добавлены схемы `InvalidPathItem`, `NonEnGenreItem`, поля `invalid_paths_count`, `non_en_genres_count` в `AuditSummaryResponse`, внешние платформенные ID в `IncompleteAlbumItem` и `IncompleteTrackInfo`.<br>2. Роуты `GET /files/audit/invalid-paths` и `GET /files/audit/non-en-genres`.<br>3. Чтение сводки, некорректных путей, жанров и недостающих треков из SQLite в `LibraryAuditService`. |
| **R4: Пользовательский интерфейс (Frontend No-Build)** | `app/templates/index.html`, `app/static/js/library_audit.js` | **DONE** | 1. Добавлены карточки «Не по шаблону» и «Жанры (не-EN)» в дашборд сводки аудита.<br>2. Добавлены подразделы (subtabs) «Некорректные пути» и «Жанры на проверку» с таблицами, статусами автоисправлений и переходами в файловый менеджер.<br>3. В `library_audit.js` добавлены методы `loadInvalidPaths()` и `loadNonEnGenres()`. |
| **R5: Системная интеграция и тестирование** | `music-auditor.service`, `tests/test_audit_sqlite.py`, `tests/test_frontend.py` | **DONE** | 1. Создан systemd unit `music-auditor.service` для запуска Go-демона в фоне с параметрами `--daemon --auto-fix-genres --check-missing`.<br>2. Написаны модульные и интеграционные тесты для базы данных, сервиса аудита, эндпоинтов API и UI.<br>3. Все 553 теста пройдены успешно (`pytest`), Go unit tests зелёные, ruff линтер чист. |

---

## Блок 22: Кнопка автоперезаписи тегов жанров, устойчивость реорганизации и дозагрузка недостающих треков (2026-09-15)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Кнопка автоматической перезаписи тегов жанров** | `app/schemas/tag.py`, `app/schemas/library_audit.py`, `app/services/library_audit_service.py`, `app/routers/files.py`, `app/templates/index.html`, `app/static/js/library_audit.js` | **DONE** | 1. Добавлена кнопка «Переписать теги (авто)» в шапку секции «Жанры на проверку» с индикацией процесса (`isRetaggingGenres`).<br>2. Реализован эндпоинт `POST /files/audit/retag-genres` и метод `LibraryAuditService.auto_retag_genres()`.<br>3. Перезаписывает не-EN теги в канонические ID3v2.4 (`\x00` мультифреймы) на диске, обновляет кэш и таблицы `tracks`, `genres`, `track_genres`, `audit_issues`. |
| **R2: Устойчивость SQLite к UTF-8 и сверхбыстрая реорганизация** | `app/services/library_audit_service.py`, `app/core/database.py`, `tools/music-scanner/id3/parser.go` | **DONE** | 1. Установлен `db.text_factory = lambda b: b.decode("utf-8", errors="replace")` на все подключения `aiosqlite`, устранивший сбои `OperationalError: Could not decode to UTF-8 column` на поврежденных тегах.<br>2. В Go сканере добавлен `decodeISO88591` и `strings.ToValidUTF8` для безопасной вставки в SQLite.<br>3. Выполнение `POST /files/audit/reorganize` и сканирование 4209 треков на `nl_music` теперь отрабатывает стабильно за <50 мс. |
| **R3: Дозагрузка недостающих треков с fallback поиском** | `app/routers/files.py`, `tests/test_files_router.py` | **DONE** | 1. В `download_missing_album_tracks` добавлен поиск через `DeezerClient.search_tracks()` и сопоставление через YouTube `AudioMatcher.find_best_match()`, если у кандидата нет прямого `deezer_url`.<br>2. Создание задач скачивания в `DownloadService` для найденных ссылок. |
| **R4: Тестирование и деплой** | `tests/`, `nl_vpn` (144.31.51.105), `nl_music` (194.76.168.20) | **DONE** | 1. 556/556 тестов `pytest` успешно пройдены (100% GREEN), Go unit tests пройдены.<br>2. Статический бинарник Go и обновленный бэкенд развернуты на `nl_vpn` и `nl_music`, сервисы перезапущены и проверены в production. |

---

## Блок 23: Устранение ложных не-EN жанров, нормализация схем и прав доступа (2026-09-16)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Схема genres и валидация английских жанров в Go и Python** | `app/services/library_audit_service.py`, `tools/music-scanner/db/sqlite.go`, `tools/music-scanner/scanner/worker.go` | **DONE** | 1. Устранена ошибка несоответствия схемы `OperationalError: table genres has no column named clean_name` в `auto_retag_genres()`.<br>2. В Go сканере и Python-сервисе исправлена логика установки флага `is_valid_en`: жанры без кириллицы (например, `Alternative \\ Rock`, `Dance Music`) гарантированно получают `is_valid_en = 1`.<br>3. При автоперезаписи старые связи `track_genres` удаляются, создаются новые канонические связи, `audit_issues` помечаются `is_resolved = 1, is_auto_fixed = 1`. |
| **R2: Права доступа и конфигурация на сервере nl_music** | `.env` на `nl_music`, права `/var/lib/music` | **DONE** | 1. Исправлен путь `MUSIC_DIR` в `/opt/music-manager/.env` на `nl_music` (установлен канонический `/var/lib/music` вместо `music_library`).<br>2. Исправлены права доступа к файлам и папкам медиатеки на `nl_music`: `chown -R music:media /var/lib/music` и `chmod -R u+rwX,g+rwX /var/lib/music`, что устранило ошибку `[Errno 13] Permission denied`.<br>3. Успешно перезаписаны теги всех 173 русскоязычных треков на `nl_music`. Очищены устаревшие orphaned `audit_issues`. |
| **R3: Оптимизация потокового сканирования и тесты** | `app/services/native_scanner.py`, `tests/conftest.py`, `tests/test_files_router.py` | **DONE** | 1. Добавлен флаг `--check-missing=false` при вызове `scan_stream()` в `NativeScannerService`, что предотвращает блокирующие внешние HTTP-запросы к Deezer во время синхронизации индекса.<br>2. В тестах изолирован `DATABASE_PATH` в `test_settings` во избежание чтения рабочей БД репозитория.<br>3. 556/556 тестов `pytest` (100% GREEN), `non_en_genres_count = 0` на обоих продакшн-серверах (`nl_vpn`, `nl_music`). |

---

## Блок 24: Каноническое схлопывание жанров в Rock и Hip-Hop, устранение дедлока реорганизации и кнопка «Исправить» для некорректных путей (2026-09-16)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Строгое каноническое схлопывание жанров (Rock & Hip-Hop)** | `app/schemas/tag.py`, `tools/music-scanner/validator/genre.go`, `tools/music-scanner/validator/validator_test.go`, `tests/test_tag_service.py` | **DONE** | 1. Реализовано правило: если жанр содержит `rock` или `рок` (включая составные `Alternative Rock`, `Pop-Rock`, `Alternative \\ Rock`, `Рок`, `Русский рок`, за исключением барокко) — он полностью схлопывается в `Rock`.<br>2. Иначе, если жанр содержит `pop`, `поп`, `hip`, `хип`, `hip-hop`, `rap`, `рэп` — он полностью схлопывается в `Hip-Hop`.<br>3. Полностью исключены пробелы вокруг разделителей `\\` (строгий формат `Genre1\\Genre2`).<br>4. Логика синхронно реализована в Python (`canonicalize_genre`) и в Go-демоне (`AnalyzeAndNormalizeGenre`), бинарник `bin/music-scanner` пересобран. |
| **R2: Устранение дедлока в reorganize_library** | `app/services/library_audit_service.py` | **DONE** | 1. Устранен взаимный нереентерабельный дедлок `asyncio.Lock` в `reorganize_library()`, вызванный вложенным вызовом `await self.ensure_index()` под тем же локом.<br>2. Реорганизация медиатеки выполняется стабильно и без зависаний сервиса. |
| **R3: Кнопка «Исправить» для некорректных путей файлов в UI** | `app/schemas/library_audit.py`, `app/services/library_audit_service.py`, `app/routers/files.py`, `app/templates/index.html`, `app/static/js/library_audit.js`, `tests/test_files_router.py` | **DONE** | 1. В интерфейс таблицы «Файлы с нарушением канонической структуры» добавлена кнопка «Исправить» для каждой строки и кнопка «Исправить все пути» в заголовок раздела.<br>2. Добавлены эндпоинты `POST /files/audit/fix-path` и `POST /files/audit/fix-all-paths`.<br>3. Перемещение файлов проходит строго через `PathSandbox`, переносятся ассоциированные тексты (`.lrc`/`.txt`), удаляются опустевшие папки, безопасно перезаписываются целевые дубликаты (например, `Damn Garden/01. Свет (2).mp3` -> `Damn Garden/01. Свет.mp3`) и обновляется SQLite. |
| **R4: Очистка устаревших track_genres и audit_issues в Go сканере** | `tools/music-scanner/db/sqlite.go`, `tools/music-scanner/scanner/worker.go` | **DONE** | 1. При обновлении трека в Go сканере теперь предварительно очищаются старые связи `track_genres` и старые записи `audit_issues` этого трека.<br>2. Устранено накопление фантомных записей и рассинхронизация статусов аудита. |
---

## Блок 26: Сквозной пайплайн дозагрузки неполных релизов, релиз-скоупинг дубликатов и оптимизация путей (2026-09-16)

| Компонент | Пути | Статус | Описание и контрольные точки |
|---|---|---|---|
| **R1: Сквозной пайплайн дозагрузки неполных релизов** | `app/schemas/download.py`, `app/schemas/library_audit.py`, `app/services/download_service.py`, `app/services/library_audit_service.py`, `app/routers/files.py`, `app/static/js/file_manager.js`, `app/static/js/library_audit.js`, `app/templates/index.html` | **DONE** | 1. Добавлен метод `create_backfill_job(target_album_path, album_title, artist_name, tracks, cover_url)` для создания консолидированной задачи дозагрузки недостающих треков в `tmp_downloads/{job_id}/converted/` с канонической нумерацией.<br>2. Реализован эндпоинт `GET /files/audit/incomplete-albums/editor-data`, бесшовно объединяющий треки из библиотеки (`MUSIC_DIR/{album}`) и дозагруженные треки из `tmp_downloads/{job_id}/` в единый список с корректной сортировкой.<br>3. Во фронтенде добавлены: кнопка «Сверить» в таблице файлового менеджера, отправка объединенного запроса на дозагрузку релиза без раздробления на отдельные ссылки, бейдж «[Дозагрузка]» в Drawer очереди и кнопка «Весь релиз / Редактировать релиз», открывающая весь дополненный альбом в Tag Editor для канонического сохранения. |
| **R2: Поиск дубликатов только в рамках одного релиза и удаление** | `app/services/library_audit_service.py`, `app/routers/files.py`, `app/schemas/library_audit.py`, `app/static/js/library_audit.js`, `app/templates/index.html` | **DONE** | 1. Поиск дубликатов ограничен строго пределами одного релиза (`album_dir::title`). Одноименные треки в разных альбомах больше не помечаются дубликатами.<br>2. Реализована автоматическая оценка и выбор лучшей канонической копии файла (штраф за суффиксы ` (2)`, `- копия`, `_dup`, предпочтение каноническому формату `01. Title.mp3` и большему размеру).<br>3. Добавлены эндпоинты `POST /files/audit/duplicates/fix` и `POST /files/audit/duplicates/fix-all`, удаляющие дубликаты и прилегающие `.lrc`/`.txt` через `PathSandbox`.<br>4. В UI добавлены кнопка «Удалить все дубликаты» и погрупповая кнопка «Оставить лучшую копию». |
| **R3: Исправление кнопки и виртуализация отрисовки неканонических путей** | `app/templates/index.html`, `app/services/library_audit_service.py` | **DONE** | 1. Исправлено условие активности кнопки «Исправить все пути»: снята блокировка при незагруженном списке `invalidPaths`, если счетчик `summary.invalid_paths_count > 0`.<br>2. Отрисовка таблицы ограничена первыми 100 элементами (`slice(0, 100)`) с информационным баннером, что предотвратило зависание Alpine.js при 2800+ записях.<br>3. В `fix_path` и `fix_all_paths` оптимизирована работа с кэшем без многократных рекурсивных переиндексаций при пакетном переименовании. |
| **R4: Тестирование и верификация** | `tests/test_library_audit.py`, `tests/test_files_router.py` | **DONE** | 1. Добавлены тесты `test_duplicate_scoping_strictly_within_release`, `test_fix_duplicate_group_and_all_endpoints`, `test_incomplete_album_editor_data_endpoint`.<br>2. 562/562 тестов `pytest` успешно пройдены (100% pass), `ruff` линтер — 0 замечаний. |

































