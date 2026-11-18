/**
 * Puddletag Workstation Controller (Alpine.js)
 * 
 * Главный оркестратор рабочей станции Puddletag / Mp3tag:
 * - Управление таблицей-сеткой (Spreadsheet Data Grid)
 * - Инлайн-редактирование ячеек (In-Cell Editing: Enter/Tab/Esc)
 * - Левая панель тегов (Tag Panel) с поддержкой мультиселекта (<Разные значения>)
 * - Блок обложек (Artwork Box): загрузка, драг-н-дроп, удаление, извлечение
 * - Конвертеры шаблонов (Filename ↔ Tag) с окном предпросмотра Diff
 * - Набор автоматизаций (Actions): Регистр, Автонумератор, Очистка, Поиск/Замена
 * - Стейджинг изменений (Dirty State) и сохранение (Ctrl+S)
 */

function puddletagWorkstationComponent(isTagEditor = false) {
    return {
        // Данные треков
        tracks: [],
        originalTracks: [],
        dirtyIndices: new Set(),
        selectedIndices: [],
        lastClickedIndex: null,

        // Состояние навигации и каталога
        currentPath: '',
        parentPath: null,
        breadcrumbs: [],
        directories: [],
        dirFilter: '',
        isLoading: false,
        recursiveMode: false,
        isDownloaderMode: false,
        jobId: null,
        jobIds: [],
        releases: [],
        activeReleaseTab: 'all',

        // Пагинация таблицы
        currentPage: 1,
        pageSize: 100,

        // Поиск и сортировка в таблице
        searchFilter: '',
        sortColumn: 'track_number',
        sortAsc: true,

        // Инлайн-редактирование ячеек
        editingCell: null, // { index, field, value }

        // Мобильный инспектор тегов и меню трека
        showMobileTagInspector: false,
        mobileTrackMenuIndex: null,

        // Плотность сетки и ширина колонок (ПК & Mobile)
        gridDensity: (function() {
            try {
                return localStorage.getItem('mm_grid_density') || 'normal';
            } catch (e) {
                return 'normal';
            }
        })(),
        colWidths: (function() {
            try {
                return JSON.parse(localStorage.getItem('mm_col_widths') || '{}');
            } catch (e) {
                return {};
            }
        })(),
        resizingCol: null,

        // Состояние левой панели тегов (Tag Panel)
        tagPanel: {
            title: '',
            artist: '',
            album: '',
            album_artist: '',
            year: '',
            genre: '',
            track_number: '',
            track_total: '',
            disc_number: '',
            disc_total: '',
            cover_url: null,
            cover_base64: null,
            has_cover: false
        },
        tagPanelDiff: {}, // { [field]: boolean } (true если значения в выборке различаются)

        // Модальное окно конвертера шаблонов (Filename ↔ Tag)
        showPatternModal: false,
        patternMode: 'filename_to_tag', // 'filename_to_tag' | 'tag_to_filename'
        patternInput: '%artist% - %title%',
        patternDiffResults: [],
        patternPresets: (window.PuddletagPatterns && window.PuddletagPatterns.presets) || [
            { label: '%track% - %title%', pattern: '%track% - %title%' },
            { label: '%artist% - %title%', pattern: '%artist% - %title%' },
            { label: '%track% - %artist% - %title%', pattern: '%track% - %artist% - %title%' },
            { label: '%artist% - %album% - %track% - %title%', pattern: '%artist% - %album% - %track% - %title%' }
        ],

        // Модальные окна быстрых действий (Actions)
        showCaseModal: false,
        caseStyle: 'title',
        caseFields: { title: true, artist: true, album: false },

        showAutoNumberModal: false,
        autoNumberStart: 1,
        autoNumberLeadingZeros: true,
        autoNumberResetPerDisc: false,
        autoNumberSetTotal: true,

        showSearchReplaceModal: false,
        searchQueryText: '',
        replaceWithText: '',
        searchFields: { title: true, artist: true, album: true },
        searchIsRegex: false,
        searchCaseSensitive: false,

        // Модальное окно галереи обложек (Apple Music, Deezer, Yandex, MusicBrainz, Spotify)
        showCoverGalleryModal: false,
        coverSearchQuery: '',
        isSearchingCovers: false,
        coverGalleryResults: [],
        coverSourceFilter: 'all',

        // Модальное окно поиска релизов в базах тегов (Deezer, MusicBrainz, Shazam)
        showSourceModal: false,
        sourceSearchQuery: '',
        activeSourceTab: 'deezer', // 'deezer' | 'musicbrainz' | 'shazam'
        isSearchingSources: false,
        isLoadingReleaseDetails: false,
        sourceResults: {
            deezer: { count: 0, items: [] },
            musicbrainz: { count: 0, items: [] },
            shazam: { count: 0, items: [] }
        },
        selectedReleaseSummary: null,
        selectedReleaseDetails: null,
        matchedPairs: [],
        targetTrackOptions: [],
        sourceOptions: {
            titleArtist: true,
            trackDisc: true,
            albumYear: true,
            genres: true,
            cover: true
        },
        mbOAuth: {
            configured: false,
            connected: false,
            username: '',
            expires_at: null,
            isLoading: false
        },

        get currentTabReleases() {
            if (!this.sourceResults) return [];
            const tabData = this.sourceResults[this.activeSourceTab];
            return (tabData && tabData.items) ? tabData.items : [];
        },

        // Сохранение
        isSaving: false,
        saveStatus: 'idle', // 'idle' | 'saving' | 'saved' | 'error'

        // Геттер отфильтрованных и отсортированных треков
        get displayTracks() {
            let list = this.tracks;

            // 0. Фильтр по активному релизу в режиме загрузчика
            if (this.isDownloaderMode && this.activeReleaseTab && this.activeReleaseTab !== 'all') {
                list = list.filter(t => t.job_id === this.activeReleaseTab);
            }

            // 1. Поисковый фильтр
            if (this.searchFilter && this.searchFilter.trim()) {
                const q = this.searchFilter.toLowerCase().trim();
                list = list.filter(t => {
                    return (t.title && t.title.toLowerCase().includes(q)) ||
                           (t.artist && t.artist.toLowerCase().includes(q)) ||
                           (t.album && t.album.toLowerCase().includes(q)) ||
                           (t.genre && t.genre.toLowerCase().includes(q)) ||
                           (t.name && t.name.toLowerCase().includes(q));
                });
            }

            // 2. Сортировка
            if (this.sortColumn) {
                const col = this.sortColumn;
                const asc = this.sortAsc ? 1 : -1;
                list = list.slice().sort((a, b) => {
                    let valA = a[col];
                    let valB = b[col];

                    if (valA === undefined || valA === null) valA = '';
                    if (valB === undefined || valB === null) valB = '';

                    // Числовая сортировка для номеров треков, дисков, года, длительности
                    if (col === 'track_number' || col === 'disc_number' || col === 'year' || col === 'duration') {
                        const numA = Number(valA) || 0;
                        const numB = Number(valB) || 0;
                        return (numA - numB) * asc;
                    }

                    return String(valA).localeCompare(String(valB), undefined, { numeric: true, sensitivity: 'base' }) * asc;
                });
            }

            return list;
        },

        get totalFiltered() {
            return this.displayTracks.length;
        },

        get totalPages() {
            const size = Number(this.pageSize);
            if (size <= 0 || isNaN(size)) return 1;
            return Math.max(1, Math.ceil(this.totalFiltered / size));
        },

        get paginatedTracks() {
            const list = this.displayTracks;
            const size = Number(this.pageSize);
            if (size <= 0 || isNaN(size)) return list;
            const start = (this.currentPage - 1) * size;
            return list.slice(start, start + size);
        },

        get paginationInfo() {
            const total = this.totalFiltered;
            if (total === 0) return '0 из 0';
            const size = Number(this.pageSize);
            if (size <= 0 || isNaN(size)) return `1–${total} из ${total}`;
            const start = (this.currentPage - 1) * size + 1;
            const end = Math.min(this.currentPage * size, total);
            return `${start}–${end} из ${total}`;
        },

        setPage(p) {
            const max = this.totalPages;
            this.currentPage = Math.max(1, Math.min(max, p));
        },

        prevPage() {
            if (this.currentPage > 1) this.currentPage--;
        },

        nextPage() {
            if (this.currentPage < this.totalPages) this.currentPage++;
        },

        get visibleDirectories() {
            const dirs = this.directories || [];
            if (!this.dirFilter || !this.dirFilter.trim()) {
                return dirs.slice(0, 60);
            }
            const q = this.dirFilter.toLowerCase().trim();
            return dirs.filter(d => (d.name && d.name.toLowerCase().includes(q)) || (d.path && d.path.toLowerCase().includes(q))).slice(0, 60);
        },

        get isAllSelected() {
            const list = this.displayTracks;
            return list.length > 0 && this.selectedIndices.length >= list.length && list.every(t => this.isTrackSelected(t._trackIndex));
        },

        get selectedTracksCount() {
            return this.selectedIndices.length;
        },

        get selectedTracksDuration() {
            let totalSec = 0;
            for (const idx of this.selectedIndices) {
                if (this.tracks[idx] && this.tracks[idx].duration) {
                    totalSec += Number(this.tracks[idx].duration) || 0;
                }
            }
            return this.formatDuration(totalSec);
        },

        get hasDirtyChanges() {
            return this.dirtyIndices.size > 0;
        },

        init() {
            // Предупреждение о потере данных при закрытии/перезагрузке страницы
            window.addEventListener('beforeunload', (e) => {
                if (this.hasDirtyChanges && (!isTagEditor || Alpine.store('app').tagEditorData)) {
                    e.preventDefault();
                    e.returnValue = '';
                    return '';
                }
            });

            // Перехват глобальной комбинации клавиш Ctrl+S / Cmd+S и Delete
            window.addEventListener('keydown', (e) => {
                if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 's') {
                    const app = Alpine.store('app');
                    const isActive = isTagEditor
                        ? app.activeModal === 'tag_editor'
                        : app.activeTab === 'files' && !app.activeModal;
                    if (isActive) {
                        e.preventDefault();
                        this.saveAllChanges();
                    }
                } else if (!isTagEditor && (e.key === 'Delete' || e.key === 'Del')) {
                    // Удаление выбранных файлов клавишей Delete при активной вкладке файлов
                    const tag = document.activeElement ? document.activeElement.tagName.toLowerCase() : '';
                    if (tag !== 'input' && tag !== 'textarea' && tag !== 'select') {
                        const app = Alpine.store('app');
                        if (app && app.activeTab === 'files' && !app.activeModal) {
                            if (this.selectedIndices && this.selectedIndices.length > 0) {
                                e.preventDefault();
                                this.promptDeleteSelected();
                            }
                        }
                    }
                }
            });

            if (isTagEditor) {
                this.$watch('$store.app.tagEditorData', (data) => {
                    if (data) this.loadJobData(data);
                });
                const data = Alpine.store('app').tagEditorData;
                if (data) this.loadJobData(data);
            } else {
                window.addEventListener('puddletag-load-path', (e) => {
                    const path = typeof e.detail === 'string' ? e.detail : (e.detail?.path || '');
                    this.loadDirectory(path);
                });
                window.addEventListener('refresh-file-manager', (e) => {
                    const path = typeof e.detail === 'string' ? e.detail : e.detail?.path;
                    this.loadDirectory(path || this.currentPath || '');
                });
                this.$watch('$store.app.activeTab', (tab) => {
                    if (tab === 'files' && this.tracks.length === 0 && !this.isLoading) {
                        this.loadDirectory(this.currentPath || '');
                    }
                });
            }
            this.$watch('searchFilter', () => { this.currentPage = 1; });
        },

        ...puddletagWorkstationNavigation,
        ...puddletagWorkstationGrid,
        ...puddletagWorkstationCovers,
        ...puddletagWorkstationActions,
        ...puddletagWorkstationSave,
        ...puddletagWorkstationCatalog,
        ...puddletagWorkstationFiles,
    };
}

// Алиасы для обратной совместимости с существующими компонентами и роутер-тестами
function fileManagerComponent() {
    return puddletagWorkstationComponent();
}

function tagEditorComponent() {
    return puddletagWorkstationComponent(true);
}
