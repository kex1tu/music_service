/**
 * Главный контроллер аудита медиатеки (Library Audit UI).
 * Отвечает за дашборд метрик целостности и загрузку списков дефектов:
 * - треки без текстов песен
 * - релизы без обложек
 * - дубликаты аудиофайлов
 * - неполные альбомы
 * - некорректные пути
 * - неанглийские жанры
 *
 * Подключает специализированные модули:
 * - AuditLyrics (пакетный сбор текстов)
 * - AuditCatalogMatcher (сверка альбомов с Deezer и докачка)
 * - AuditFixes (реорганизация, пути, жанры, дубликаты)
 */

function libraryAuditComponent() {
    return {
        activeSubTab: 'lyrics', // 'lyrics' | 'covers' | 'duplicates' | 'incomplete' | 'paths' | 'genres'
        isLoading: false,
        isReorganizing: false,
        isRetaggingGenres: false,
        isFixingPaths: false,
        isFixingDuplicates: false,
        isNormalizingNavidrome: false,
        summary: {
            total_tracks: 0,
            total_albums: 0,
            missing_lyrics_count: 0,
            missing_covers_count: 0,
            duplicates_count: 0,
            incomplete_albums_count: 0,
            invalid_paths_count: 0,
            non_en_genres_count: 0,
        },
        missingLyrics: [],
        missingCovers: [],
        duplicatesData: { groups: [], total_duplicate_files: 0 },
        incompleteAlbums: [],
        invalidPaths: [],
        artistConflicts: [],
        nonEnGenres: [],
        incompleteLoading: false,
        expandedAlbums: {},
        catalogMatchLoading: {},
        downloadingTracks: {},
        lyricsProgress: {
            is_running: false,
            total: 0,
            current: 0,
            found: 0,
            failed: 0,
            current_track: '',
        },
        lyricsPollInterval: null,

        // Подмешиваем функционал специализированных модулей
        ...(window.AuditLyrics || {}),
        ...(window.AuditCovers || {}),
        ...(window.AuditCatalogMatcher || {}),
        ...(window.AuditFixes || {}),
        ...(window.AuditTagAudit || {}),

        init() {
            this.loadAll();
            if (this.checkLyricsProgress) {
                this.checkLyricsProgress();
            }
            if (this.checkCoversProgress) {
                this.checkCoversProgress();
            }

            window.addEventListener('confirm-reorganize-library', () => {
                if (this.reorganizeLibrary) this.reorganizeLibrary();
            });

            window.addEventListener('match-album-audit', (e) => {
                const path = e?.detail?.path;
                if (path) {
                    this.activeSubTab = 'incomplete';
                    this.$nextTick(async () => {
                        const alb = this.incompleteAlbums.find((a) => a.path === path) || {
                            path,
                            album: path.split('/').pop() || path,
                        };
                        this.expandedAlbums[path] = true;
                        if (this.matchCatalog) await this.matchCatalog(alb);
                    });
                }
            });

            window.addEventListener('refresh-audit', (e) => {
                const force = e?.detail?.force || false;
                this.loadAll(force);
                if (this.checkLyricsProgress) this.checkLyricsProgress();
                if (this.checkCoversProgress) this.checkCoversProgress();
            });

            if (this.$watch) {
                this.$watch('$store.app.activeTab', (tab) => {
                    if (tab === 'audit') {
                        this.loadAll();
                        if (this.checkLyricsProgress) this.checkLyricsProgress();
                        if (this.checkCoversProgress) this.checkCoversProgress();
                    }
                });
            }
        },

        async loadAll(force = false) {
            this.isLoading = true;
            try {
                if (force) {
                    try {
                        await window.api.raw('/files/audit/refresh', { method: 'POST' });
                    } catch (e) {
                        console.warn('Сбой принудительного обновления индекса:', e);
                    }
                }
                await Promise.all([
                    this.loadSummary(),
                    this.loadMissingLyrics(),
                    this.loadMissingCovers(),
                    this.loadDuplicates(),
                    this.loadIncompleteAlbums(),
                    this.loadInvalidPaths(),
                    this.loadArtistConflicts(),
                    this.loadNonEnGenres(),
                    this.loadTagIssues ? this.loadTagIssues() : Promise.resolve(),
                ]);
            } finally {
                this.isLoading = false;
            }
        },

        async loadSummary() {
            try {
                const res = await window.api.raw(`/files/audit/summary?_t=${Date.now()}`);
                if (res.ok) {
                    this.summary = await res.json();
                }
            } catch (e) {
                console.error('Ошибка загрузки сводки аудита:', e);
            }
        },

        async loadMissingLyrics() {
            try {
                const res = await window.api.raw(`/files/audit/missing-lyrics?_t=${Date.now()}`);
                if (res.ok) {
                    this.missingLyrics = await res.json();
                }
            } catch (e) {
                console.error('Ошибка загрузки треков без слов:', e);
            }
        },

        async loadMissingCovers() {
            try {
                const res = await window.api.raw(`/files/audit/missing-covers?_t=${Date.now()}`);
                if (res.ok) {
                    this.missingCovers = await res.json();
                }
            } catch (e) {
                console.error('Ошибка загрузки релизов без обложек:', e);
            }
        },

        async loadDuplicates() {
            try {
                const res = await window.api.raw(`/files/audit/duplicates?_t=${Date.now()}`);
                if (res.ok) {
                    this.duplicatesData = await res.json();
                }
            } catch (e) {
                console.error('Ошибка загрузки дубликатов:', e);
            }
        },

        async loadInvalidPaths() {
            try {
                const res = await window.api.raw(`/files/audit/invalid-paths?_t=${Date.now()}`);
                if (res.ok) {
                    this.invalidPaths = await res.json();
                }
            } catch (e) {
                console.error('Ошибка загрузки некорректных путей:', e);
            }
        },

        async loadArtistConflicts() {
            try {
                const response = await window.api.raw('/files/audit/artist-conflicts');
                if (response.ok) this.artistConflicts = await response.json();
            } catch (error) {
                console.error('Не удалось загрузить конфликты исполнителей', error);
            }
        },

        async resolveArtistConflict(conflict, artistID) {
            try {
                const response = await window.api.raw('/files/audit/artist-conflicts/resolve', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ path: conflict.path, album_artist: conflict.album_artist, artist_id: artistID }),
                });
                if (!response.ok) throw new Error('Не удалось применить выбор');
                await this.loadArtistConflicts();
                await this.loadSummary();
            } catch (error) {
                Alpine.store('app').toast(error.message, 'error');
            }
        },

        async loadNonEnGenres() {
            try {
                const res = await window.api.raw(`/files/audit/non-en-genres?_t=${Date.now()}`);
                if (res.ok) {
                    this.nonEnGenres = await res.json();
                }
            } catch (e) {
                console.error('Ошибка загрузки не-EN жанров:', e);
            }
        }
    };
}
