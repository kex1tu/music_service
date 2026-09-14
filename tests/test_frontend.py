"""Тесты пользовательского интерфейса (шаблонов и статических скриптов).

Проверяет современную SVG-иконографию (Lucide / Heroicons), отсутствие устаревших
текстовых плейсхолдеров, корректность логики прогресса и интервала поллинга.
"""

from fastapi.testclient import TestClient

from app.config import PROJECT_ROOT


def test_index_page_loads_and_contains_modern_svgs(client: TestClient):
    """Проверка рендеринга главной страницы и современной векторной иконографии."""
    resp = client.get("/")
    assert resp.status_code == 200
    html = resp.text

    # 1. Отсутствие устаревших буквенных бейджей D, YT, SC
    assert ">D<" not in html, "Legacy Deezer badge >D< found in HTML"
    assert ">YT<" not in html, "Legacy YouTube badge >YT< found in HTML"
    assert ">SC<" not in html, "Legacy SoundCloud badge >SC< found in HTML"

    # 2. Наличие современных векторных SVG-иконок в инфо-карточках (Google Material Symbols)
    assert "M7 17V7q0-.425.288-.712T8 6" in html, "Deezer equalizer/waves SVG icon missing"
    assert "M8 17.175V6.825q0-.425.3-.713" in html, "YouTube play SVG icon missing"
    assert "M6.5 20q-2.275 0-3.887-1.575" in html, "SoundCloud cloud SVG missing"

    # 3. Файловый менеджер: контурная 24x24 Material Symbols иконка папки и музыкальная нота для треков
    assert "M4 20q-.825 0-1.412-.587T2 18V6" in html, "Modern Material Symbols folder icon missing"
    assert "M7.175 19.825Q6 18.65 6 17t1.175-2.825T10 13" in html, "Music note SVG icon for audio tracks missing"

    # 4. Шапка и навигация: иконка пользователя и выхода
    assert "M9.175 10.825Q8 9.65 8" in html, "User avatar SVG icon missing"
    assert "M5 21q-.825 0-1.412-.587T3 19V5" in html, "Logout SVG icon missing"

    # 5. Хлебные крошки: иконка Home
    assert "M6 19h3v-5q0-.425.288-.712T10 13h4" in html, "Home breadcrumb SVG icon missing"

    # 6. Действия над файлами: иконки редактирования и удаления
    assert "M4 21q-.425 0-.712-.288T3 20v-2.425" in html, "Edit folder action icon missing"
    assert "M7 21q-.825 0-1.412-.587T5 19V6" in html, "Trash delete icon missing"

    # 7. Модальное окно тегов: иконки применения ко всем и сохранения
    assert "m12 16.175l3.9-3.875" in html, "Apply to all arrow SVG missing"
    assert "M5 21q-.825 0-1.412-.587T3 19V5q0-.825.588-1.412T5 3h11.175" in html, "Save floppy SVG missing"

    # 8. Уведомления (Toasts): SVG-иконки статусов success, error, info
    assert "m10.6 13.8l-2.15-2.15" in html, "Toast check-circle success SVG missing"
    assert "M12 22q-2.075 0-3.9-.788t-3.175-2.137T2.788 15.9T2 12" in html, "Toast circle icon missing"


def test_index_html_downloader_progress_and_button_invariants(client: TestClient):
    """Проверка исправлений логики прогресса и блокировки кнопки скачивания в index.html."""
    resp = client.get("/")
    assert resp.status_code == 200
    html = resp.text

    # Кнопка submit должна быть заблокирована во время QUEUED и RESOLVING
    assert "job.status !== \x27READY\x27 && job.status !== \x27ERROR\x27" in html, (
        "Submit button disabled condition must guard against all active job states"
    )

    # Прогресс-бар должен быть ограничен диапазоном 0-100%
    assert "Math.min(100, Math.max(0, Math.round(job.progress || 0)))" in html, (
        "Progress bar width style must clamp progress between 0 and 100"
    )

    # Индикатор мульти-трековых задач
    assert "job.total_tracks > 1" in html, "Multi-track indicator condition missing"
    assert "${job.completed_tracks}/${job.total_tracks}" in html, "Multi-track counter display missing"


def test_downloader_js_polling_interval():
    """Проверка интервала поллинга 1000мс в downloader.js."""
    downloader_js_path = PROJECT_ROOT / "app" / "static" / "js" / "downloader.js"
    assert downloader_js_path.is_file(), f"File not found: {downloader_js_path}"
    content = downloader_js_path.read_text(encoding="utf-8")

    assert "}, 1000);" in content, "Polling interval must be 1000ms in downloader.js"
    assert "}, 1500);" not in content, "Outdated 1500ms polling interval found in downloader.js"


def test_lyrics_features_in_tag_editor_and_lyrics_modal(client: TestClient):
    """Проверка наличия кнопки поиска текстов для альбома и расширенных функций вставки текста."""
    resp = client.get("/")
    assert resp.status_code == 200
    html = resp.text

    # 1. Проверка кнопки пакетного поиска текстов для альбома в редакторе тегов
    assert "@click=\"fetchLyricsForAlbum()\"" in html, "fetchLyricsForAlbum button missing in index.html"
    assert "Найти тексты для альбома" in html or "lyricsProgress" in html

    # 2. Проверка кнопки вставки текста из буфера и переключателя формата в модальном окне текстов
    assert "@click=\"pasteFromClipboard()\"" in html, "pasteFromClipboard button missing in index.html"
    assert "Вставить текст" in html
    assert "@click=\"setKind('lrc')\"" in html, "LRC format switcher button missing in index.html"
    assert "@click=\"setKind('txt')\"" in html, "TXT format switcher button missing in index.html"
    assert "@input=\"handleContentInput()\"" in html, "handleContentInput handler missing on textarea"
    assert "@paste=\"handleContentPaste($event)\"" in html, "handleContentPaste handler missing on textarea"

    # 3. Проверка методов в tag_editor.js
    tag_editor_js = (PROJECT_ROOT / "app" / "static" / "js" / "tag_editor.js").read_text(encoding="utf-8")
    assert "fetchLyricsForAlbum" in tag_editor_js
    assert "isFetchingLyrics" in tag_editor_js

    # 4. Проверка методов в lyrics_modal.js
    lyrics_modal_js = (PROJECT_ROOT / "app" / "static" / "js" / "lyrics_modal.js").read_text(encoding="utf-8")
    assert "pasteFromClipboard" in lyrics_modal_js
    assert "autoDetectKind" in lyrics_modal_js
    assert "handleContentInput" in lyrics_modal_js
    assert "clearContent" in lyrics_modal_js


def test_shazam_and_deezer_features_in_tag_editor(client: TestClient):
    """Проверка наличия элементов управления Shazam и поиска в Deezer в Tag Editor."""
    resp = client.get("/")
    assert resp.status_code == 200
    html = resp.text

    # 1. Проверка кнопки и модального окна поиска в Deezer
    assert "openDeezerSearch" in html, "openDeezerSearch missing in index.html"
    assert "Найти в Deezer" in html
    assert "showDeezerModal" in html
    assert '@click="searchDeezerAlbums()"' in html
    assert '@click="applyDeezerAlbum(item.id)"' in html

    # 2. Проверка кнопок Shazam (пакетной и индивидуальной)
    assert "@click=\"recognizeAllTracks()\"" in html, "recognizeAllTracks button missing in index.html"
    assert "Распознать всё (Shazam)" in html or "shazamProgress" in html
    assert "@click=\"recognizeTrack(track)\"" in html, "recognizeTrack button missing in table"

    # 3. Проверка реализации методов в tag_editor.js
    tag_editor_js = (PROJECT_ROOT / "app" / "static" / "js" / "tag_editor.js").read_text(encoding="utf-8")
    assert "recognizeTrack" in tag_editor_js
    assert "recognizeAllTracks" in tag_editor_js
    assert "openDeezerSearch" in tag_editor_js
    assert "searchDeezerAlbums" in tag_editor_js
    assert "applyDeezerAlbum" in tag_editor_js
    assert "fetchCoverBase64" in tag_editor_js


def test_track_album_and_year_fields_in_tag_editor(client: TestClient):
    """Проверка наличия полей 'Альбом' и 'Год' для каждого трека в таблице Tag Editor."""
    resp = client.get("/")
    assert resp.status_code == 200
    html = resp.text

    assert 'x-model="track.album"' in html
    assert 'x-model="track.year"' in html
    assert "Альбом" in html
    assert "Год" in html


def test_clean_titles_dnd_renumber_and_cover_gallery_in_tag_editor(client: TestClient):
    """Проверка наличия элементов управления для очистки названий, DND/перенумерации и галереи обложек."""
    resp = client.get("/")
    assert resp.status_code == 200
    html = resp.text

    # 1. Кнопки очистки названий и перенумерации треков
    assert '@click="cleanTitles()"' in html, "cleanTitles button missing in index.html"
    assert "Очистить названия" in html
    assert '@click="renumberTracks()"' in html, "renumberTracks button missing in index.html"
    assert "Перенумеровать" in html

    # 2. Карточки релизов вместо старого верхнего глобального меню
    assert "ВЕРХНИЙ БЛОК: Метаданные уровня альбома" not in html, "Old global album block must be removed"
    assert 'x-for="group in releases"' in html, "Release cards repeater missing in index.html"
    assert 'x-model="group.discTotal"' in html, "group.discTotal input missing in release cards"
    assert 'openGroupCoverGallery' in html or 'openCoverGallery' in html
    assert 'handleGroupCoverUpload' in html, "handleGroupCoverUpload handler missing in release cards"
    assert 'expandAllGroups()' in html, "expandAllGroups button missing in toolbar"
    assert 'collapseAllGroups()' in html, "collapseAllGroups button missing in toolbar"

    # 3. Элементы таблицы треков: Drag-and-Drop, номер диска и индивидуальная очистка
    assert '@dragstart="onDragStart($event, index)"' in html, "DND dragstart missing on track row"
    assert '@drop="onDrop($event, index)"' in html, "DND drop missing on track row"
    assert 'x-model="track.disc_number"' in html, "track.disc_number input missing in table"
    assert '@click="cleanTrackTitle(track)"' in html, "cleanTrackTitle button missing in title column"

    # 4. Модальное окно галереи обложек и фильтры источников
    assert "showCoverGalleryModal" in html, "Cover gallery modal binding missing"
    assert '@click="searchCovers()"' in html, "searchCovers button missing in modal"
    assert "coverSourceFilter === 'all'" in html, "Source filter 'all' missing in modal"
    assert "coverSourceFilter === 'apple'" in html, "Source filter 'apple' missing in modal"
    assert "coverSourceFilter === 'deezer'" in html, "Source filter 'deezer' missing in modal"
    assert "coverSourceFilter === 'yandex'" in html, "Source filter 'yandex' missing in modal"
    assert "coverSourceFilter === 'spotify'" in html, "Source filter 'spotify' missing in modal"
    assert '@click="selectCover(item)"' in html, "selectCover handler missing in modal"

    # 5. Проверка реализации логики в tag_editor.js
    tag_editor_js = (PROJECT_ROOT / "app" / "static" / "js" / "tag_editor.js").read_text(encoding="utf-8")
    assert "cleanTrackTitle" in tag_editor_js
    assert "cleanTitles" in tag_editor_js
    assert "renumberTracks" in tag_editor_js
    assert "onDragStart" in tag_editor_js
    assert "onDragOver" in tag_editor_js
    assert "onDrop" in tag_editor_js
    assert "openCoverGallery" in tag_editor_js
    assert "closeCoverGallery" in tag_editor_js
    assert "searchCovers" in tag_editor_js
    assert "selectCover" in tag_editor_js
    assert "filteredCoverResults" in tag_editor_js


def test_batch_downloader_ui(client: TestClient):
    """Проверка наличия переключателя пакетного режима и поля ввода списка URL."""
    resp = client.get("/")
    assert resp.status_code == 200
    html = resp.text

    assert "inputMode = 'single'" in html
    assert "inputMode = 'batch'" in html
    assert "parsedBatchUrls.length" in html
    assert "batchUrlsText" in html
    assert "Список ссылок (пакетно)" in html


def test_global_search_header_ui(client: TestClient):
    """Проверка наличия поисковой строки в шапке с горячей клавишей Ctrl+K и секциями результатов."""
    resp = client.get("/")
    assert resp.status_code == 200
    html = resp.text

    assert "performSearch()" in html
    assert "Ctrl+K" in html
    assert "/files/search?query=" in html
    assert "results.artists" in html
    assert "results.albums" in html
    assert "results.tracks" in html
    assert "navigateToDir" in html


def test_library_audit_tab_and_components_ui(client: TestClient):
    """Проверка наличия вкладки Аудит медиатеки, сводки метрик, подвкладок и действий."""
    resp = client.get("/")
    assert resp.status_code == 200
    html = resp.text

    # Навигация и инициализация компонента
    assert "$store.app.activeTab === 'audit'" in html
    assert "$dispatch('refresh-audit')" in html
    assert "libraryAuditComponent()" in html
    assert "library_audit.js" in html

    # Проверка DOM-структуры: секция аудита не должна быть вложена в секцию файлов
    files_sec_idx = html.find("x-show=\"$store.app.activeTab === 'files'\"")
    audit_sec_idx = html.find("x-show=\"$store.app.activeTab === 'audit'\"")
    assert files_sec_idx != -1 and audit_sec_idx != -1
    assert files_sec_idx < audit_sec_idx
    # Между секцией files и секцией audit обязательно должен быть закрывающий тег </section>
    between_sections = html[files_sec_idx:audit_sec_idx]
    assert "</section>" in between_sections

    # Сводка метрик
    assert "summary.total_tracks" in html
    assert "summary.missing_lyrics_count" in html
    assert "summary.missing_covers_count" in html
    assert "summary.duplicates_count" in html

    # Подвкладки и действия
    assert "activeSubTab === 'lyrics'" in html
    assert "activeSubTab === 'covers'" in html
    assert "activeSubTab === 'duplicates'" in html
    assert "startBatchLyricsFetch()" in html
    assert "promptDeleteDuplicate(trk)" in html

    # Проверка исходного файла library_audit.js
    audit_js = (PROJECT_ROOT / "app" / "static" / "js" / "library_audit.js").read_text(encoding="utf-8")
    assert "libraryAuditComponent" in audit_js
    assert "refresh-audit" in audit_js
    assert "$watch" in audit_js
    assert "loadSummary" in audit_js
    assert "loadMissingLyrics" in audit_js
    assert "loadMissingCovers" in audit_js
    assert "loadDuplicates" in audit_js
    assert "startBatchLyricsFetch" in audit_js
    assert "promptDeleteDuplicate" in audit_js


def test_floating_queue_widget_ui(client: TestClient):
    """Проверка наличия плавающего менеджера очередей и интеграции с хранилищем."""
    resp = client.get("/")
    assert resp.status_code == 200
    html = resp.text

    assert "$store.app.queue.isOpen" in html
    assert "$store.app.queue.activeCount" in html
    assert "$store.app.queue.clearCompleted()" in html
    assert "$store.app.queue.openJobInTagEditor" in html
    assert "$store.app.queue.cancelJob" in html
    assert "Очередь загрузок" in html


def test_tag_editor_grouped_releases_and_per_track_covers_ui(client: TestClient):
    """Проверка элементов управления группировкой по релизам, обложек треков и режима синглов."""
    resp = client.get("/")
    assert resp.status_code == 200
    html = resp.text

    # 1. Переключатель вида (По релизам vs Сплошной список)
    assert "viewMode = 'grouped'" in html
    assert "viewMode = 'flat'" in html
    assert "По релизам" in html
    assert "Сплошной список" in html

    # 2. Кнопка автопоиска обложек синглов
    assert "@click=\"fetchSinglesCovers()\"" in html
    assert "⚡ Обложки синглов" in html

    # 3. Галерея обложек для трека и группы
    assert "openTrackCoverGallery" in html
    assert "openGroupCoverGallery" in html
    assert "applyAlbumCoverToGroup" in html
    assert "toggleGroupCollapse" in html

    # 4. Чекбоксы организации в подвале
    assert 'x-model="groupByAlbum"' in html
    assert 'x-model="singlesMode"' in html
    assert "Раскладывать по альбомам" in html
    assert "Режим синглов (Artist/Track)" in html

    # 5. Проверка методов и свойств в tag_editor.js
    tag_editor_js = (PROJECT_ROOT / "app" / "static" / "js" / "tag_editor.js").read_text(encoding="utf-8")
    assert "groupedReleases" in tag_editor_js
    assert "openTrackCoverGallery" in tag_editor_js
    assert "openGroupCoverGallery" in tag_editor_js
    assert "applyAlbumCoverToGroup" in tag_editor_js
    assert "fetchSinglesCovers" in tag_editor_js
    assert "isSearchingSinglesCovers" in tag_editor_js
    assert "singlesMode" in tag_editor_js
    assert "groupByAlbum" in tag_editor_js





