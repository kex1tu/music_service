/**
 * Главный оркестратор двухуровневого редактора тегов ID3v2.4 (Tag Editor).
 * Объединяет специализированные модули:
 * - TagLoader (загрузка и сброс состояния)
 * - TagSaver (валидация и сохранение альбома/синглов в медиатеку)
 * - TagCleaner (очистка названий, извлечение фитов, парсинг артистов)
 * - TagReleases (группировка релизов, синглы, сворачивание)
 * - TagCovers (галерея обложек, загрузка, автоподбор)
 * - TagDeezer (поиск альбомов в Deezer и автозаполнение)
 * - TagRecognition (распознавание треков через Shazam)
 * - TagDragDrop (перетаскивание, нумерация, мульти-диски)
 * - TagLyrics (пакетный поиск текстов песен)
 */

function tagEditorComponent() {
    return {
        // Базовые метаданные альбома
        album: {
            album_artist: '',
            album: '',
            year: null,
            genre: '',
            disc_total: null,
            track_total: null,
        },
        tracks: [],
        sourceDir: '',
        coverPath: null,
        coverPreviewUrl: null,
        coverBase64: null,
        isSaving: false,
        isFetchingLyrics: false,
        lyricsProgress: { current: 0, total: 0 },

        // Shazam Audio Recognition
        recognizingTrackPath: null,
        isRecognizingAll: false,
        shazamProgress: { current: 0, total: 0 },

        // Drag-and-Drop
        draggedIndex: null,
        dragOverIndex: null,

        // Cover Gallery
        showCoverGalleryModal: false,
        coverSearchQuery: '',
        isSearchingCovers: false,
        coverGalleryResults: [],
        coverSourceFilter: 'all',
        selectedCoverUrl: null,

        // Release Grouping & Singles Mode
        viewMode: 'grouped', // 'grouped' | 'flat'
        releases: [],
        collapsedGroups: {},
        coverGalleryTarget: { type: 'album', id: null, originalIndex: null },
        isSearchingSinglesCovers: false,
        singlesCoverProgress: { current: 0, total: 0 },
        singlesMode: false,
        groupByAlbum: true,

        // Album Metadata Search (Deezer)
        showDeezerModal: false,
        deezerQuery: '',
        isSearchingDeezer: false,
        deezerResults: [],
        activeDeezerGroup: null,
        isLoadingDeezerDetails: false,
        jobIds: [],

        get groupedReleases() {
            return this.releases;
        },

        // Подмешиваем функционал специализированных модулей
        ...(window.TagCleaner || {}),
        ...(window.TagReleases || {}),
        ...(window.TagCovers || {}),
        ...(window.TagDeezer || {}),
        ...(window.TagRecognition || {}),
        ...(window.TagDragDrop || {}),
        ...(window.TagLyrics || {}),

        init() {
            this.$watch('$store.app.tagEditorData', (newData) => {
                if (newData) {
                    this.loadData(newData);
                }
            });

            if (Alpine.store('app').tagEditorData) {
                this.loadData(Alpine.store('app').tagEditorData);
            }

            window.addEventListener('lyrics-updated', (e) => {
                const { path, kind } = e.detail;
                if (!this.tracks) return;
                const track = this.tracks.find((t) => t.path === path);
                if (track) {
                    track.lyrics_status = kind;
                }
            });

            this.$watch('singlesMode', () => {
                if (this.buildReleasesFromTracks) {
                    this.buildReleasesFromTracks();
                }
            });
        },

        loadData(data) {
            if (window.TagLoader) {
                window.TagLoader.loadData(this, data);
            }
        },

        async saveAlbum() {
            if (window.TagSaver) {
                return window.TagSaver.saveAlbum(this);
            }
        },

        removeTrack(index) {
            this.tracks.splice(index, 1);
            if (this.tracks.length === 0) {
                Alpine.store('app').closeTagEditor();
                return;
            }
            if (this.buildReleasesFromTracks) {
                this.buildReleasesFromTracks();
            }
        },

        close() {
            Alpine.store('app').closeTagEditor();
        }
    };
}
