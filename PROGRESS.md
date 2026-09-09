# PROGRESS.md — Матрица статусов реализации Music Downloader & Tag Manager

## Текущий статус проекта
- **Общий прогресс:** 18 / 18 компонентов завершены (100%)
- **Активный этап:** Все блоки (1–9), включая извлечение метаданных и обложек плейлистов SoundCloud, конвертацию WebP, очистку временных файлов и исключение ID3v1, полностью завершены.
- **Результаты тестов:** 407/407 passed (`pytest tests/ -v`), ruff linter — 0 замечаний (All checks passed!).



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







