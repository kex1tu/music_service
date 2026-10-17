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

function puddletagWorkstationComponent() {
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
        recursiveMode: true,
        isDownloaderMode: false,
        jobId: null,

        // Пагинация таблицы
        currentPage: 1,
        pageSize: 100,

        // Поиск и сортировка в таблице
        searchFilter: '',
        sortColumn: 'track_number',
        sortAsc: true,

        // Инлайн-редактирование ячеек
        editingCell: null, // { index, field, value }

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

        // Геттер отфильтрованных и отсортированных треков
        get displayTracks() {
            let list = this.tracks;

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
            // Перехват глобальной комбинации клавиш Ctrl+S / Cmd+S и Delete
            window.addEventListener('keydown', (e) => {
                if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 's') {
                    // Если активна вкладка файлов или открыто модальное окно Puddletag
                    if (Alpine.store('app').activeTab === 'files' || Alpine.store('app').activeModal === 'tag_editor') {
                        e.preventDefault();
                        this.saveAllChanges();
                    }
                } else if (e.key === 'Delete' || e.key === 'Del') {
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

            // Слушаем событие обновления данных из File Manager или Downloader
            window.addEventListener('puddletag-load-path', (e) => {
                const path = typeof e.detail === 'string' ? e.detail : (e.detail?.path || '');
                this.loadDirectory(path);
            });

            window.addEventListener('refresh-file-manager', (e) => {
                let targetPath = '';
                if (typeof e?.detail === 'string') {
                    targetPath = e.detail;
                } else if (e?.detail && typeof e.detail.path === 'string') {
                    targetPath = e.detail.path;
                } else if (typeof this.currentPath === 'string') {
                    targetPath = this.currentPath;
                }
                this.loadDirectory(targetPath);
            });

            window.addEventListener('puddletag-load-job', (e) => {
                if (e.detail) {
                    this.loadJobData(e.detail);
                }
            });

            // Реактивная связь с хранилищем при открытии модального окна тегов
            if (this.$watch) {
                this.$watch('$store.app.tagEditorData', (newData) => {
                    if (newData) {
                        this.loadJobData(newData);
                    }
                });
                this.$watch('$store.app.activeTab', (tab) => {
                    if (tab === 'files' && this.tracks.length === 0 && !this.isLoading) {
                        this.loadDirectory(this.currentPath || '');
                    }
                });
                this.$watch('searchFilter', () => {
                    this.currentPage = 1;
                });
            }

            if (window.Alpine && Alpine.store('app')?.tagEditorData) {
                this.loadJobData(Alpine.store('app').tagEditorData);
            }
        },

        /**
         * Загружает файлы из медиатеки с поддержкой рекурсивного сканирования.
         */
        async loadDirectory(path = '') {
            this.isLoading = true;
            this.editingCell = null;
            this.dirtyIndices.clear();
            this.selectedIndices = [];
            this.currentPage = 1;

            try {
                const url = `/files?path=${encodeURIComponent(path || '')}&recursive=${this.recursiveMode ? 'true' : 'false'}&_t=${Date.now()}`;
                const res = await fetch(url);
                if (!res.ok) {
                    const err = await res.json().catch(() => ({}));
                    throw new Error(err.error || err.message || 'Ошибка загрузки каталога');
                }

                const data = await res.json();
                this.currentPath = data.current_path || '';
                this.parentPath = data.parent_path;
                this.breadcrumbs = data.breadcrumbs || [];

                // Преобразуем элементы в формат рабочей модели треков
                const rawItems = data.items || [];
                const audioTracks = [];
                const subDirs = [];

                for (const item of rawItems) {
                    if (item.type === 'directory') {
                        subDirs.push(item);
                    } else if (item.type === 'file' && (item.extension === '.mp3' || !item.extension)) {
                        audioTracks.push({
                            _trackIndex: audioTracks.length,
                            path: item.path,
                            name: item.name,
                            title: item.title || item.name.replace(/\.[^/.]+$/, ''),
                            artist: item.artist || '',
                            album: item.album || '',
                            album_artist: item.album_artist || '',
                            year: item.year || null,
                            genre: item.genre || '',
                            track_number: item.track_number || null,
                            track_total: item.track_total || null,
                            disc_number: item.disc_number || 1,
                            disc_total: item.disc_total || 1,
                            duration: item.duration || 0,
                            bitrate: item.bitrate || 320,
                            lyrics_status: item.lyrics_status || 'none',
                            has_cover: !!item.has_cover,
                            cover_preview_url: item.has_cover ? `/tags/cover?path=${encodeURIComponent(item.path)}` : null
                        });
                    }
                }

                this.directories = subDirs;
                this.tracks = audioTracks;
                this.originalTracks = JSON.parse(JSON.stringify(audioTracks));
                this.syncTagPanel();
            } catch (err) {
                Alpine.store('app').toast(`Ошибка: ${err.message}`, 'error');
            } finally {
                this.isLoading = false;
            }
        },

        navigateUp() {
            if (this.parentPath !== null && this.parentPath !== undefined) {
                this.loadDirectory(this.parentPath);
            }
        },

        navigateTo(path) {
            this.loadDirectory(path);
        },

        /**
         * Загружает треки свежескачанного релиза из очереди загрузчика.
         */
        loadJobData(payload) {
            if (!payload) return;
            this.isDownloaderMode = true;
            this.jobId = payload.jobId || (payload.jobIds && payload.jobIds[0]) || null;
            this.currentPath = payload.sourceDir || '';
            this.dirtyIndices.clear();
            this.selectedIndices = [];
            this.editingCell = null;

            const coverUrl = payload.coverPreviewUrl || payload.cover_preview_url || payload.coverUrl || payload.cover_url || null;
            const hasCover = !!(payload.hasCover || payload.has_cover || payload.coverPath || payload.cover_path || coverUrl);

            const validTracks = (payload.tracks || []).map((t, idx) => {
                const trackPath = t.path || t.converted_path || '';
                const trackName = (trackPath ? trackPath.split('/').pop() : '') || t.name || t.title || 'Track';
                const trackCover = t.cover_preview_url || t.cover_url || coverUrl;
                return {
                    _trackIndex: idx,
                    path: trackPath,
                    name: trackName,
                    title: t.title || '',
                    artist: t.artist || payload.album?.album_artist || payload.album?.artist || '',
                    album: t.album || payload.album?.album || '',
                    album_artist: t.album_artist || payload.album?.album_artist || '',
                    year: t.year || payload.album?.year || null,
                    genre: t.genre || payload.album?.genre || '',
                    track_number: t.track_number !== undefined && t.track_number !== null ? t.track_number : null,
                    track_total: t.track_total || payload.tracks.length,
                    disc_number: t.disc_number || 1,
                    disc_total: t.disc_total || 1,
                    duration: t.duration || 0,
                    bitrate: t.bitrate || 320,
                    lyrics_status: t.lyrics_status || 'none',
                    has_cover: !!(t.has_cover || trackCover || hasCover),
                    cover_preview_url: trackCover,
                    cover_base64: t.cover_base64 || payload.coverBase64 || payload.cover_base64 || null
                };
            });

            this.tracks = validTracks;
            this.originalTracks = JSON.parse(JSON.stringify(validTracks));
            this.syncTagPanel();
        },

        // ==================== ВЫДЕЛЕНИЕ И НАВИГАЦИЯ ====================

        toggleSelectAll() {
            if (this.isAllSelected) {
                this.selectedIndices = [];
                this._selectedSet = new Set();
            } else {
                this.selectedIndices = this.displayTracks.map(t => t._trackIndex);
                this._selectedSet = new Set(this.selectedIndices);
            }
            this.syncTagPanel();
        },

        selectRow(index, event) {
            if (event.shiftKey && this.lastClickedIndex !== null) {
                // Выделение диапазона строк (Shift+Click)
                const start = Math.min(this.lastClickedIndex, index);
                const end = Math.max(this.lastClickedIndex, index);
                const range = [];
                for (let i = start; i <= end; i++) {
                    range.push(i);
                }
                // Объединяем с текущим выбором без дубликатов
                const set = new Set([...this.selectedIndices, ...range]);
                this.selectedIndices = Array.from(set);
                this._selectedSet = set;
            } else if (event.ctrlKey || event.metaKey) {
                // Одиночное переключение (Ctrl+Click)
                const pos = this.selectedIndices.indexOf(index);
                if (pos > -1) {
                    this.selectedIndices.splice(pos, 1);
                } else {
                    this.selectedIndices.push(index);
                }
                this._selectedSet = new Set(this.selectedIndices);
                this.lastClickedIndex = index;
            } else {
                // Обычный клик — выбор одной строки
                this.selectedIndices = [index];
                this._selectedSet = new Set([index]);
                this.lastClickedIndex = index;
            }

            this.syncTagPanel();
        },

        isTrackSelected(index) {
            if (!this._selectedSet || this._selectedSet.size !== this.selectedIndices.length) {
                this._selectedSet = new Set(this.selectedIndices);
            }
            return this._selectedSet.has(index);
        },

        isTrackDirty(index) {
            return this.dirtyIndices.has(index);
        },

        isCellDirty(index, field) {
            if (!this.dirtyIndices.has(index)) return false;
            const original = this.originalTracks[index];
            const current = this.tracks[index];
            if (!original || !current) return false;
            return String(original[field] ?? '') !== String(current[field] ?? '');
        },

        // ==================== ИНЛАЙН-РЕДАКТИРОВАНИЕ ЯЧЕЕК ====================

        startEdit(index, field, event) {
            if (event) event.stopPropagation();
            this.editingCell = {
                index,
                field,
                value: this.tracks[index][field] !== null && this.tracks[index][field] !== undefined 
                       ? String(this.tracks[index][field]) 
                       : ''
            };
            // Фокусируем инпут на следующем тике
            this.$nextTick(() => {
                const el = this.$refs.cellEditorInput;
                if (el) {
                    el.focus();
                    el.select();
                }
            });
        },

        saveEdit() {
            if (!this.editingCell) return;
            const { index, field, value } = this.editingCell;
            const track = this.tracks[index];
            if (!track) return;

            let finalVal = value.trim();
            // Числовые поля
            if (field === 'track_number' || field === 'disc_number' || field === 'year' || field === 'track_total' || field === 'disc_total') {
                finalVal = finalVal === '' ? null : Number(finalVal);
            }

            const oldVal = track[field];
            if (oldVal !== finalVal) {
                track[field] = finalVal;
                this.dirtyIndices.add(index);
                this.syncTagPanel();
            }

            this.editingCell = null;
        },

        cancelEdit() {
            this.editingCell = null;
        },

        onCellKeyDown(event) {
            if (event.key === 'Enter') {
                event.preventDefault();
                const curIndex = this.editingCell ? this.editingCell.index : null;
                const curField = this.editingCell ? this.editingCell.field : null;
                this.saveEdit();

                // Переход на строку ниже в той же колонке
                if (curIndex !== null && curIndex + 1 < this.tracks.length) {
                    this.startEdit(curIndex + 1, curField);
                }
            } else if (event.key === 'Tab') {
                event.preventDefault();
                const fieldsOrder = ['track_number', 'disc_number', 'title', 'artist', 'album', 'album_artist', 'year', 'genre'];
                const curIndex = this.editingCell ? this.editingCell.index : null;
                const curField = this.editingCell ? this.editingCell.field : null;
                this.saveEdit();

                if (curIndex !== null && curField !== null) {
                    const fieldIdx = fieldsOrder.indexOf(curField);
                    if (!event.shiftKey) {
                        // Вперед
                        if (fieldIdx + 1 < fieldsOrder.length) {
                            this.startEdit(curIndex, fieldsOrder[fieldIdx + 1]);
                        } else if (curIndex + 1 < this.tracks.length) {
                            this.startEdit(curIndex + 1, fieldsOrder[0]);
                        }
                    } else {
                        // Назад
                        if (fieldIdx > 0) {
                            this.startEdit(curIndex, fieldsOrder[fieldIdx - 1]);
                        } else if (curIndex > 0) {
                            this.startEdit(curIndex - 1, fieldsOrder[fieldsOrder.length - 1]);
                        }
                    }
                }
            } else if (event.key === 'Escape') {
                event.preventDefault();
                this.cancelEdit();
            }
        },

        // ==================== СИНХРОНИЗАЦИЯ ТЕГ-ПАНЕЛИ ====================

        syncTagPanel() {
            if (this.selectedIndices.length === 0) {
                // Если ничего не выбрано, очищаем поля
                this.tagPanel = {
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
                    has_cover: false
                };
                this.tagPanelDiff = {};
                return;
            }

            const fields = ['title', 'artist', 'album', 'album_artist', 'year', 'genre', 'track_number', 'track_total', 'disc_number', 'disc_total'];
            const firstTrack = this.tracks[this.selectedIndices[0]] || {};
            const diffs = {};

            for (const f of fields) {
                const firstVal = firstTrack[f] ?? '';
                let isDiff = false;
                for (let i = 1; i < this.selectedIndices.length; i++) {
                    const t = this.tracks[this.selectedIndices[i]];
                    if (!t) continue;
                    if (String(t[f] ?? '') !== String(firstVal)) {
                        isDiff = true;
                        break;
                    }
                }
                diffs[f] = isDiff;
                this.tagPanel[f] = isDiff ? '' : (firstVal ?? '');
            }

            this.tagPanelDiff = diffs;
            this.tagPanel.cover_url = firstTrack.cover_preview_url || null;
            this.tagPanel.has_cover = !!firstTrack.has_cover;
        },

        onTagPanelInput(field, value) {
            if (this.selectedIndices.length === 0) return;

            let finalVal = value.trim();
            if (field === 'track_number' || field === 'disc_number' || field === 'year' || field === 'track_total' || field === 'disc_total') {
                finalVal = finalVal === '' ? null : Number(finalVal);
            }

            for (const idx of this.selectedIndices) {
                const track = this.tracks[idx];
                if (track) {
                    track[field] = finalVal;
                    this.dirtyIndices.add(idx);
                }
            }

            this.tagPanelDiff[field] = false;
        },

        // ==================== БЛОК ОБЛОЖКИ (ARTWORK BOX) ====================

        handleCoverDrop(event) {
            event.preventDefault();
            const files = event.dataTransfer?.files;
            if (files && files.length > 0) {
                this.processCoverFile(files[0]);
            }
        },

        handleCoverFileInput(event) {
            const files = event.target?.files;
            if (files && files.length > 0) {
                this.processCoverFile(files[0]);
            }
        },

        processCoverFile(file) {
            if (!file.type.startsWith('image/')) {
                Alpine.store('app').toast('Файл должен быть изображением (JPG, PNG)', 'warning');
                return;
            }

            const reader = new FileReader();
            reader.onload = (e) => {
                const base64 = e.target.result;
                this.tagPanel.cover_url = base64;
                this.tagPanel.has_cover = true;

                // Применяем ко всем выбранным или ко всем трекам
                const targetIndices = this.selectedIndices.length > 0 ? this.selectedIndices : this.tracks.map((_, i) => i);
                for (const idx of targetIndices) {
                    const t = this.tracks[idx];
                    if (t) {
                        t.cover_preview_url = base64;
                        t.cover_base64 = base64;
                        t.has_cover = true;
                        this.dirtyIndices.add(idx);
                    }
                }
                Alpine.store('app').toast('Обложка применена к выбранным трекам', 'success');
            };
            reader.readAsDataURL(file);
        },

        removeCover() {
            this.tagPanel.cover_url = null;
            this.tagPanel.has_cover = false;
            const targetIndices = this.selectedIndices.length > 0 ? this.selectedIndices : this.tracks.map((_, i) => i);
            for (const idx of targetIndices) {
                const t = this.tracks[idx];
                if (t) {
                    t.cover_preview_url = null;
                    t.cover_base64 = null;
                    t.has_cover = false;
                    this.dirtyIndices.add(idx);
                }
            }
            Alpine.store('app').toast('Обложка удалена у выбранных треков', 'info');
        },

        // ==================== КОНВЕРТЕР ШАБЛОНОВ (PATTERNS) ====================

        openPatternModal(mode) {
            this.patternMode = mode;
            if (mode === 'filename_to_tag') {
                this.patternInput = '%artist% - %title%';
            } else {
                this.patternInput = '%track% - %title%';
            }
            this.updatePatternDiff();
            this.showPatternModal = true;
        },

        updatePatternDiff() {
            if (!window.PuddletagPatterns) return;
            const targetTracks = this.selectedIndices.length > 0 
                ? this.selectedIndices.map(i => this.tracks[i]) 
                : this.tracks;

            this.patternDiffResults = window.PuddletagPatterns.calculateDiff(
                targetTracks, 
                this.patternInput, 
                this.patternMode
            );
        },

        async applyPatternDiff() {
            if (!this.patternDiffResults || this.patternDiffResults.length === 0) return;

            if (this.patternMode === 'filename_to_tag') {
                // Применяем распарсенные теги в стейдж
                let count = 0;
                for (const diff of this.patternDiffResults) {
                    if (diff.has_change && diff.changes) {
                        // Ищем реальный индекс трека по пути
                        const realIdx = this.tracks.findIndex(t => t.path === diff.track_path);
                        if (realIdx > -1) {
                            const track = this.tracks[realIdx];
                            for (const [field, valObj] of Object.entries(diff.changes)) {
                                track[field] = valObj.new;
                            }
                            this.dirtyIndices.add(realIdx);
                            count++;
                        }
                    }
                }
                this.syncTagPanel();
                this.showPatternModal = false;
                Alpine.store('app').toast(`Теги извлечены из имен файлов (${count} треков)`, 'success');
            } else {
                // Переименование файлов (Tag to Filename)
                const itemsToRename = [];
                for (const diff of this.patternDiffResults) {
                    if (diff.has_change && !diff.has_conflict) {
                        itemsToRename.push({
                            current_path: diff.track_path,
                            new_name: diff.new_filename
                        });
                    }
                }

                if (itemsToRename.length === 0) {
                    Alpine.store('app').toast('Нет файлов для переименования', 'info');
                    return;
                }

                this.isSaving = true;
                try {
                    const res = await fetch('/files/rename-pattern', {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({ items: itemsToRename, dry_run: false })
                    });
                    if (!res.ok) {
                        const err = await res.json().catch(() => ({}));
                        throw new Error(err.error || 'Ошибка переименования файлов');
                    }

                    const results = await res.json();
                    let successCount = 0;
                    for (const r of results) {
                        if (r.status === 'ok') {
                            successCount++;
                            const tIdx = this.tracks.findIndex(t => t.path === r.current_path);
                            if (tIdx > -1) {
                                this.tracks[tIdx].path = r.new_path;
                                this.tracks[tIdx].name = r.new_path.split('/').pop() || r.new_path;
                            }
                        }
                    }

                    this.showPatternModal = false;
                    Alpine.store('app').toast(`Переименовано ${successCount} файлов`, 'success');
                    // Перезагружаем текущий каталог для актуализации ФС
                    await this.loadDirectory(this.currentPath);
                } catch (err) {
                    Alpine.store('app').toast(`Ошибка: ${err.message}`, 'error');
                } finally {
                    this.isSaving = false;
                }
            }
        },

        // ==================== БЫСТРЫЕ ДЕЙСТВИЯ (ACTIONS) ====================

        applyCaseConversion() {
            if (!window.PuddletagActions) return;
            const targetFields = Object.keys(this.caseFields).filter(f => this.caseFields[f]);
            const targetIndices = this.selectedIndices.length > 0 ? this.selectedIndices : this.tracks.map((_, i) => i);

            const count = window.PuddletagActions.applyCase(this.tracks, targetIndices, targetFields, this.caseStyle);
            for (const idx of targetIndices) {
                this.dirtyIndices.add(idx);
            }

            this.syncTagPanel();
            this.showCaseModal = false;
            Alpine.store('app').toast(`Регистр обновлен у ${count} треков`, 'success');
        },

        applyAutoNumbering() {
            if (!window.PuddletagActions) return;
            const targetIndices = this.selectedIndices.length > 0 ? this.selectedIndices : this.tracks.map((_, i) => i);

            window.PuddletagActions.autoNumber(this.tracks, targetIndices, {
                startNum: Number(this.autoNumberStart) || 1,
                leadingZeros: this.autoNumberLeadingZeros,
                resetPerDisc: this.autoNumberResetPerDisc,
                setTotal: this.autoNumberSetTotal
            });

            for (const idx of targetIndices) {
                this.dirtyIndices.add(idx);
            }

            this.syncTagPanel();
            this.showAutoNumberModal = false;
            Alpine.store('app').toast('Номера треков успешно расставлены', 'success');
        },

        cleanTitlesAction() {
            if (!window.PuddletagActions) return;
            const targetIndices = this.selectedIndices.length > 0 ? this.selectedIndices : this.tracks.map((_, i) => i);
            const count = window.PuddletagActions.cleanTitles(this.tracks, targetIndices);

            for (const idx of targetIndices) {
                this.dirtyIndices.add(idx);
            }

            this.syncTagPanel();
            Alpine.store('app').toast(`Очищены названия у ${count} треков`, 'success');
        },

        applySearchAndReplace() {
            if (!window.PuddletagActions) return;
            const targetFields = Object.keys(this.searchFields).filter(f => this.searchFields[f]);
            const targetIndices = this.selectedIndices.length > 0 ? this.selectedIndices : this.tracks.map((_, i) => i);

            const count = window.PuddletagActions.searchAndReplace(
                this.tracks,
                targetIndices,
                this.searchQueryText,
                this.replaceWithText,
                targetFields,
                this.searchIsRegex,
                this.searchCaseSensitive
            );

            for (const idx of targetIndices) {
                this.dirtyIndices.add(idx);
            }

            this.syncTagPanel();
            this.showSearchReplaceModal = false;
            Alpine.store('app').toast(`Выполнена замена в ${count} треках`, 'success');
        },

        // ==================== СОХРАНЕНИЕ И ОТКАТ (PERSISTENCE) ====================

        saveChanges() {
            return this.saveAllChanges();
        },

        async saveAllChanges() {
            if (!this.isDownloaderMode && this.dirtyIndices.size === 0) {
                Alpine.store('app').toast('Нет несохраненных изменений', 'info');
                return;
            }
            if (!this.tracks || this.tracks.length === 0) {
                Alpine.store('app').toast('Нет треков для сохранения', 'warning');
                return;
            }

            this.isSaving = true;
            try {
                // В зависимости от режима отправляем сохранение
                if (this.isDownloaderMode) {
                    // Режим загрузчика: переносим в библиотеку ВСЕ треки релиза через BatchSaveTags с флагом reorganize
                    const tracksToSave = this.tracks.map((t, idx) => ({
                        ...t,
                        title: t.title || '',
                        artist: t.artist || this.tagPanel.artist || this.tagPanel.album_artist || '',
                        album: t.album || this.tagPanel.album || '',
                        album_artist: t.album_artist || this.tagPanel.album_artist || '',
                        year: t.year || Number(this.tagPanel.year) || null,
                        genre: t.genre || this.tagPanel.genre || '',
                        track_number: t.track_number !== undefined && t.track_number !== null ? t.track_number : (idx + 1),
                        track_total: t.track_total || this.tracks.length,
                    }));

                    const res = await fetch('/tags/batch', {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({
                            album: {
                                album_artist: this.tagPanel.album_artist || tracksToSave[0]?.album_artist || '',
                                album: this.tagPanel.album || tracksToSave[0]?.album || '',
                                year: Number(this.tagPanel.year) || tracksToSave[0]?.year || null,
                                genre: this.tagPanel.genre || tracksToSave[0]?.genre || ''
                            },
                            tracks: tracksToSave,
                            source_dir: this.currentPath,
                            cover_base64: this.tagPanel.cover_base64 || tracksToSave[0]?.cover_base64 || '',
                            reorganize: true
                        })
                    });

                    if (!res.ok) {
                        const err = await res.json().catch(() => ({}));
                        throw new Error(err.error || 'Ошибка перемещения в медиатеку');
                    }

                    Alpine.store('app').toast('Релиз успешно принят в медиатеку!', 'success');
                    Alpine.store('app').closeTagEditor();

                    // Очищаем завершенные задачи из очереди загрузок
                    const jobIdsToClean = (this.jobIds && this.jobIds.length > 0) ? this.jobIds : (this.jobId ? [this.jobId] : []);
                    if (jobIdsToClean.length > 0) {
                        try {
                            await Promise.allSettled(jobIdsToClean.map(jid => fetch(`/downloads/${jid}`, { method: 'DELETE' })));
                            if (Alpine.store('app').queue) {
                                await Alpine.store('app').queue.fetchQueue();
                            }
                        } catch (cleanupErr) {
                            console.debug('[Puddletag] Сбой удаления задач из очереди:', cleanupErr);
                        }
                    }
                    window.dispatchEvent(new CustomEvent('refresh-file-manager', { detail: '' }));
                } else {
                    const dirtyTracksList = Array.from(this.dirtyIndices).map(idx => this.tracks[idx]);
                    // Режим медиатеки: атомарно перезаписываем ID3v2.4 теги
                    const res = await fetch('/tags/batch', {
                        method: 'PUT',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({
                            album: {
                                album_artist: this.tagPanel.album_artist || '',
                                album: this.tagPanel.album || '',
                                year: Number(this.tagPanel.year) || null,
                                genre: this.tagPanel.genre || ''
                            },
                            tracks: dirtyTracksList,
                            source_dir: this.currentPath,
                            cover_base64: this.tagPanel.cover_base64 || '',
                            reorganize: false
                        })
                    });

                    if (!res.ok) {
                        const err = await res.json().catch(() => ({}));
                        throw new Error(err.error || 'Ошибка записи ID3-тегов');
                    }

                    Alpine.store('app').toast(`Успешно сохранено ${dirtyTracksList.length} файлов! (ID3v2.4)`, 'success');
                    this.dirtyIndices.clear();
                    this.originalTracks = JSON.parse(JSON.stringify(this.tracks));
                    window.dispatchEvent(new CustomEvent('refresh-file-manager', { detail: '' }));
                }
            } catch (err) {
                Alpine.store('app').toast(`Ошибка: ${err.message}`, 'error');
            } finally {
                this.isSaving = false;
            }
        },

        revertAllChanges() {
            if (this.dirtyIndices.size === 0) return;
            this.tracks = JSON.parse(JSON.stringify(this.originalTracks));
            this.dirtyIndices.clear();
            this.syncTagPanel();
            Alpine.store('app').toast('Все изменения отменены', 'info');
        },

        // ==================== МУЛЬТИ-ИСТОЧНИКОВЫЙ ПОИСК РЕЛИЗОВ (DEEZER, MUSICBRAINZ, SHAZAM) ====================

        openSourceModal() {
            if (this.tracks.length === 0) {
                Alpine.store('app').toast('Таблица пуста. Загрузите или выберите файлы', 'warning');
                return;
            }

            // Определяем кандидатов на поисковый запрос из выделенных или первых треков
            let candidateArtist = '';
            let candidateAlbum = '';
            let candidateTitle = '';

            const targetTrack = (this.selectedIndices.length > 0)
                ? this.tracks[this.selectedIndices[0]]
                : this.tracks[0];

            if (targetTrack) {
                candidateArtist = (targetTrack.artist || targetTrack.album_artist || '').trim();
                candidateAlbum = (targetTrack.album || '').trim();
                candidateTitle = (targetTrack.title || '').trim();
            }

            if (candidateArtist && candidateAlbum) {
                this.sourceSearchQuery = `${candidateArtist} - ${candidateAlbum}`;
            } else if (candidateArtist && candidateTitle) {
                this.sourceSearchQuery = `${candidateArtist} - ${candidateTitle}`;
            } else if (candidateAlbum) {
                this.sourceSearchQuery = candidateAlbum;
            } else if (candidateTitle) {
                this.sourceSearchQuery = candidateTitle;
            } else {
                this.sourceSearchQuery = '';
            }

            this.selectedReleaseSummary = null;
            this.selectedReleaseDetails = null;
            this.matchedPairs = [];
            this.targetTrackOptions = [];
            this.showSourceModal = true;
            this.checkMusicBrainzOAuth();

            // Если сформирован поисковый запрос, сразу запускаем поиск
            if (this.sourceSearchQuery) {
                this.searchSources();
            }
        },

        async searchSources() {
            if (!window.PuddletagSources) return;
            const q = this.sourceSearchQuery.trim();
            if (!q) {
                Alpine.store('app').toast('Введите поисковый запрос', 'warning');
                return;
            }

            this.isSearchingSources = true;
            this.selectedReleaseSummary = null;
            this.selectedReleaseDetails = null;
            this.matchedPairs = [];
            this.targetTrackOptions = [];

            try {
                // Извлекаем относительный путь первого трека, если доступен
                let audioPath = '';
                const firstTr = (this.selectedIndices.length > 0) ? this.tracks[this.selectedIndices[0]] : this.tracks[0];
                if (firstTr) {
                    audioPath = firstTr.rel_path || firstTr.path || firstTr.filename || '';
                }

                const results = await window.PuddletagSources.searchAllSources(q, audioPath);
                this.sourceResults = results;

                // Автоматическое переключение вкладки на первую с результатами
                const currentCount = (this.sourceResults[this.activeSourceTab] && this.sourceResults[this.activeSourceTab].count) || 0;
                if (currentCount === 0) {
                    if (this.sourceResults.deezer && this.sourceResults.deezer.count > 0) {
                        this.activeSourceTab = 'deezer';
                    } else if (this.sourceResults.musicbrainz && this.sourceResults.musicbrainz.count > 0) {
                        this.activeSourceTab = 'musicbrainz';
                    } else if (this.sourceResults.shazam && this.sourceResults.shazam.count > 0) {
                        this.activeSourceTab = 'shazam';
                    }
                }

                // Авто-выбор первой найденной карточки релиза
                const items = this.currentTabReleases;
                if (items && items.length > 0) {
                    this.selectRelease(items[0]);
                }
            } catch (err) {
                Alpine.store('app').toast(err.message || 'Ошибка поиска в базах тегов', 'error');
            } finally {
                this.isSearchingSources = false;
            }
        },

        async recognizeSelectedAudio() {
            if (!window.PuddletagSources) return;
            const firstTr = (this.selectedIndices.length > 0) ? this.tracks[this.selectedIndices[0]] : this.tracks[0];
            if (!firstTr) {
                Alpine.store('app').toast('Нет аудиофайлов для распознавания', 'warning');
                return;
            }

            const audioPath = firstTr.rel_path || firstTr.path || firstTr.filename || '';
            if (!audioPath) {
                Alpine.store('app').toast('Путь к аудиофайлу недоступен', 'warning');
                return;
            }

            this.isSearchingSources = true;
            this.activeSourceTab = 'shazam';
            this.selectedReleaseSummary = null;
            this.selectedReleaseDetails = null;
            this.matchedPairs = [];

            try {
                const results = await window.PuddletagSources.searchAllSources('', audioPath);
                this.sourceResults = results;

                const items = this.currentTabReleases;
                if (items && items.length > 0) {
                    this.selectRelease(items[0]);
                    Alpine.store('app').toast('Трек успешно распознан Shazam!', 'success');
                } else {
                    Alpine.store('app').toast('Shazam не смог распознать данный аудиофрагмент', 'info');
                }
            } catch (err) {
                Alpine.store('app').toast(err.message || 'Ошибка распознавания Shazam', 'error');
            } finally {
                this.isSearchingSources = false;
            }
        },

        async selectRelease(rel) {
            if (!rel || !window.PuddletagSources) return;
            this.selectedReleaseSummary = rel;
            this.isLoadingReleaseDetails = true;
            this.selectedReleaseDetails = null;
            this.matchedPairs = [];

            try {
                let audioPath = '';
                const firstTr = (this.selectedIndices.length > 0) ? this.tracks[this.selectedIndices[0]] : this.tracks[0];
                if (firstTr) {
                    audioPath = firstTr.rel_path || firstTr.path || firstTr.filename || '';
                }

                const details = await window.PuddletagSources.fetchReleaseDetails(rel.source, rel.id, audioPath);
                this.selectedReleaseDetails = details;

                // Формируем список целевых файлов из выделенных или всех строк в таблице
                const targetIndices = (this.selectedIndices.length > 0)
                    ? this.selectedIndices
                    : this.tracks.map((_, i) => i);

                const targetTracks = targetIndices.map(i => this.tracks[i]);

                this.targetTrackOptions = targetIndices.map(origIdx => {
                    const tr = this.tracks[origIdx];
                    const fname = tr.filename || (tr.path ? tr.path.split('/').pop() : '') || `Трек #${origIdx + 1}`;
                    const label = tr.title
                        ? (tr.artist ? `${tr.artist} — ${tr.title}` : tr.title)
                        : fname;
                    return {
                        originalIndex: origIdx,
                        label: label,
                        filename: fname
                    };
                });

                this.matchedPairs = window.PuddletagSources.matchTracks(
                    details.tracks,
                    targetTracks,
                    targetIndices
                );
            } catch (err) {
                Alpine.store('app').toast(err.message || 'Ошибка загрузки треклиста релиза', 'error');
            } finally {
                this.isLoadingReleaseDetails = false;
            }
        },

        onMatchedFileChange(pairIndex, newOrigIndexStr) {
            const pair = this.matchedPairs[pairIndex];
            if (!pair) return;

            if (newOrigIndexStr === '' || newOrigIndexStr === null || newOrigIndexStr === undefined) {
                pair.localTrack = null;
                pair.originalIndex = null;
                pair.isMatched = false;
                pair.isChecked = false;
                return;
            }

            const newOrigIdx = parseInt(newOrigIndexStr, 10);
            if (isNaN(newOrigIdx)) return;

            // Строго уникальное сопоставление (1-к-1):
            // Если выбранный локальный трек был привязан к другой строке, освобождаем её
            for (const otherPair of this.matchedPairs) {
                if (otherPair.index !== pairIndex && otherPair.originalIndex === newOrigIdx) {
                    otherPair.localTrack = null;
                    otherPair.originalIndex = null;
                    otherPair.isMatched = false;
                    otherPair.isChecked = false;
                }
            }

            const localTrk = this.tracks[newOrigIdx];
            pair.localTrack = localTrk || null;
            pair.originalIndex = localTrk ? newOrigIdx : null;
            pair.isMatched = !!localTrk;
            pair.isChecked = !!localTrk;
        },

        toggleAllMatched(checked) {
            for (const pair of this.matchedPairs) {
                if (pair.isMatched) {
                    pair.isChecked = !!checked;
                }
            }
        },

        isAllMatchedChecked() {
            const matched = this.matchedPairs.filter(p => p.isMatched);
            if (matched.length === 0) return false;
            return matched.every(p => p.isChecked);
        },

        reMatchTracks() {
            if (!this.selectedReleaseDetails || !window.PuddletagSources) return;
            const targetIndices = (this.selectedIndices.length > 0)
                ? this.selectedIndices
                : this.tracks.map((_, i) => i);
            const targetTracks = targetIndices.map(i => this.tracks[i]);

            this.matchedPairs = window.PuddletagSources.matchTracks(
                this.selectedReleaseDetails.tracks,
                targetTracks,
                targetIndices
            );
            Alpine.store('app').toast('Авто-сопоставление треков выполнено заново', 'info');
        },

        applySourceTags() {
            if (!this.selectedReleaseDetails || !window.PuddletagSources) return;

            const modifiedIndices = window.PuddletagSources.applyTagsToTracks(
                this.tracks,
                this.matchedPairs,
                this.selectedReleaseDetails,
                this.sourceOptions
            );

            if (modifiedIndices.size === 0) {
                Alpine.store('app').toast('Нет сопоставленных треков для обновления', 'warning');
                return;
            }

            for (const idx of modifiedIndices) {
                this.dirtyIndices.add(idx);
            }

            this.syncTagPanel();
            this.showSourceModal = false;

            const srcName = (this.selectedReleaseDetails.source || 'каталога').toUpperCase();
            Alpine.store('app').toast(`Теги из базы ${srcName} успешно применены (${modifiedIndices.size} треков)! Нажмите Сохранить (Ctrl+S) для записи на диск.`, 'success');
        },

        async checkMusicBrainzOAuth() {
            if (!window.PuddletagSources) return;
            this.mbOAuth.isLoading = true;
            try {
                const st = await window.PuddletagSources.getMusicBrainzOAuthStatus();
                this.mbOAuth.configured = !!st.configured;
                this.mbOAuth.connected = !!st.connected;
                this.mbOAuth.username = st.username || '';
                this.mbOAuth.expires_at = st.expires_at || null;
            } finally {
                this.mbOAuth.isLoading = false;
            }
        },

        openMusicBrainzOAuth(event) {
            if (event) event.preventDefault();
            const width = 600;
            const height = 700;
            const left = Math.max(0, (window.innerWidth - width) / 2 + window.screenX);
            const top = Math.max(0, (window.innerHeight - height) / 2 + window.screenY);
            const popup = window.open(
                '/api/auth/musicbrainz/login',
                'MusicBrainzOAuth',
                `width=${width},height=${height},left=${left},top=${top},resizable=yes,scrollbars=yes,status=yes`
            );

            const onMessage = (e) => {
                if (e.data && e.data.type === 'mb_oauth_success') {
                    window.removeEventListener('message', onMessage);
                    this.mbOAuth.connected = true;
                    this.mbOAuth.username = e.data.username || '';
                    if (Alpine.store('app') && Alpine.store('app').toast) {
                        Alpine.store('app').toast('MusicBrainz успешно подключен! Лимиты повышены.', 'success');
                    }
                    this.checkMusicBrainzOAuth();
                }
            };
            window.addEventListener('message', onMessage);

            if (popup) {
                const timer = setInterval(() => {
                    if (popup.closed) {
                        clearInterval(timer);
                        window.removeEventListener('message', onMessage);
                        this.checkMusicBrainzOAuth();
                    }
                }, 1000);
            }
        },

        async disconnectMusicBrainzOAuth() {
            if (!confirm('Отключить аккаунт MusicBrainz? Будет возвращен стандартный rate limit (1 запрос/сек).')) {
                return;
            }
            if (!window.PuddletagSources) return;
            const ok = await window.PuddletagSources.disconnectMusicBrainzOAuth();
            if (ok) {
                this.mbOAuth.connected = false;
                this.mbOAuth.username = '';
                if (Alpine.store('app') && Alpine.store('app').toast) {
                    Alpine.store('app').toast('MusicBrainz отключен', 'info');
                }
            } else {
                if (Alpine.store('app') && Alpine.store('app').toast) {
                    Alpine.store('app').toast('Не удалось отключить MusicBrainz', 'error');
                }
            }
        },

        // ==================== УДАЛЕНИЕ ФАЙЛОВ И ПАПОК ====================

        promptDeleteTrack(track) {
            if (!track || !track.path) return;
            const displayName = (track.artist && track.title) ? `${track.artist} - ${track.title}` : (track.name || track.path);
            const app = Alpine.store('app');
            if (app) {
                app.deleteConfirmData = {
                    name: displayName,
                    path: track.path,
                    type: 'file',
                    onDeleted: async () => {
                        await this.loadDirectory(this.currentPath);
                    }
                };
                app.activeModal = 'delete_confirm';
            }
        },

        promptDeleteDirectory(dir) {
            if (!dir || !dir.path) return;
            const app = Alpine.store('app');
            if (app) {
                app.deleteConfirmData = {
                    name: dir.name || dir.path,
                    path: dir.path,
                    type: 'directory',
                    onDeleted: async () => {
                        await this.loadDirectory(this.currentPath);
                    }
                };
                app.activeModal = 'delete_confirm';
            }
        },

        promptDeleteCurrentFolder() {
            if (!this.currentPath) return;
            const app = Alpine.store('app');
            if (app) {
                const folderName = this.currentPath.split('/').pop() || this.currentPath;
                app.deleteConfirmData = {
                    name: folderName,
                    path: this.currentPath,
                    type: 'directory',
                    onDeleted: async () => {
                        const target = this.parentPath || '';
                        await this.loadDirectory(target);
                    }
                };
                app.activeModal = 'delete_confirm';
            }
        },

        promptDeleteSelected() {
            if (!this.selectedIndices || this.selectedIndices.length === 0) {
                const app = Alpine.store('app');
                if (app && app.toast) {
                    app.toast('Нет выбранных треков для удаления', 'warning');
                }
                return;
            }
            const paths = this.selectedIndices.map(i => this.tracks[i] && this.tracks[i].path).filter(Boolean);
            if (paths.length === 0) return;

            const app = Alpine.store('app');
            if (app) {
                app.deleteConfirmData = {
                    name: `${paths.length} выбранных треков`,
                    paths: paths,
                    type: 'batch',
                    onDeleted: async () => {
                        this.selectedIndices = [];
                        this._selectedSet = new Set();
                        await this.loadDirectory(this.currentPath);
                    }
                };
                app.activeModal = 'delete_confirm';
            }
        },

        // ==================== ПОВТОРНОЕ СКАЧИВАНИЕ (REDOWNLOAD) ====================

        promptRedownloadTrack(track) {
            if (!track || !track.path) return;
            const displayName = (track.artist && track.title) ? `${track.artist} - ${track.title}` : (track.name || track.path);
            const app = Alpine.store('app');
            if (app) {
                app.redownloadConfirmData = {
                    name: displayName,
                    path: track.path,
                    type: 'track',
                    delete_existing: true,
                    onSuccess: async () => {
                        await this.loadDirectory(this.currentPath);
                    }
                };
                app.activeModal = 'redownload_confirm';
            }
        },

        promptRedownloadDirectory(dir) {
            if (!dir || !dir.path) return;
            const app = Alpine.store('app');
            if (app) {
                app.redownloadConfirmData = {
                    name: dir.name || dir.path,
                    path: dir.path,
                    type: 'album',
                    delete_existing: true,
                    onSuccess: async () => {
                        await this.loadDirectory(this.currentPath);
                    }
                };
                app.activeModal = 'redownload_confirm';
            }
        },

        promptRedownloadCurrentFolder() {
            if (!this.currentPath) return;
            const app = Alpine.store('app');
            if (app) {
                const folderName = this.currentPath.split('/').pop() || this.currentPath;
                app.redownloadConfirmData = {
                    name: folderName,
                    path: this.currentPath,
                    type: 'album',
                    delete_existing: true,
                    onSuccess: async () => {
                        const target = this.parentPath || '';
                        await this.loadDirectory(target);
                    }
                };
                app.activeModal = 'redownload_confirm';
            }
        },

        // ==================== ВСПОМОГАТЕЛЬНЫЕ ФОРМАТТЕРЫ ====================

        formatDuration(seconds) {
            if (window.AppFormatters && window.AppFormatters.formatDuration) {
                return window.AppFormatters.formatDuration(seconds);
            }
            if (!seconds) return '0:00';
            const m = Math.floor(seconds / 60);
            const s = Math.floor(seconds % 60);
            return `${m}:${s < 10 ? '0' : ''}${s}`;
        }
    };
}

// Алиасы для обратной совместимости с существующими компонентами и роутер-тестами
function fileManagerComponent() {
    return puddletagWorkstationComponent();
}

function tagEditorComponent() {
    return puddletagWorkstationComponent();
}
