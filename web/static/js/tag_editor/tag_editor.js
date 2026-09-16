/**
 * Главный оркестратор двухуровневого редактора тегов ID3v2.4 (Tag Editor).
 * Объединяет специализированные модули:
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
            this.viewMode = 'grouped';
            this.collapsedGroups = {};
            this.coverGalleryTarget = { type: 'album', id: null, originalIndex: null };
            this.isSearchingSinglesCovers = false;
            this.singlesCoverProgress = { current: 0, total: 0 };
            this.singlesMode = false;
            this.groupByAlbum = true;
            this.coverBase64 = null;
            this.selectedCoverUrl = null;
            this.draggedIndex = null;
            this.dragOverIndex = null;
            this.showCoverGalleryModal = false;
            this.coverSearchQuery = '';
            this.coverGalleryResults = [];
            this.isSearchingCovers = false;
            this.isFetchingLyrics = false;
            this.lyricsProgress = { current: 0, total: 0 };
            this.recognizingTrackPath = null;
            this.isRecognizingAll = false;
            this.shazamProgress = { current: 0, total: 0 };
            this.showDeezerModal = false;
            this.activeDeezerGroup = null;
            this.deezerQuery = '';
            this.deezerResults = [];
            this.isSearchingDeezer = false;
            this.isLoadingDeezerDetails = false;
            this.jobIds = data.jobIds || (data.jobId ? [data.jobId] : []);

            const formatGenres = this.formatGenres || (window.AppFormatters ? window.AppFormatters.formatGenres : (g) => g);

            this.album = {
                album_artist: data.album?.album_artist || '',
                album: data.album?.album || '',
                year: data.album?.year || null,
                genre: formatGenres(data.album?.genre || ''),
                disc_total: data.album?.disc_total || null,
                track_total: data.album?.track_total || null,
            };

            this.tracks = (data.tracks || []).map((t) => {
                let covPrev = t.cover_preview_url || t.cover_url || null;
                if (!covPrev && t.cover_path) {
                    covPrev = `/tags/cover?path=${encodeURIComponent(t.cover_path)}&t=${Date.now()}`;
                } else if (!covPrev && t.has_cover && t.path) {
                    covPrev = `/tags/cover?path=${encodeURIComponent(t.path)}&t=${Date.now()}`;
                }

                return {
                    ...t,
                    track_number: t.track_number !== undefined && t.track_number !== null ? t.track_number : 1,
                    track_total: t.track_total || (data.album?.track_total || null),
                    disc_number: t.disc_number !== undefined && t.disc_number !== null ? t.disc_number : 1,
                    disc_total: t.disc_total || (data.album?.disc_total || null),
                    album: (t.album !== undefined && t.album !== null && t.album !== '') ? t.album : '',
                    album_artist: (t.album_artist !== undefined && t.album_artist !== null && t.album_artist !== '') ? t.album_artist : (t.artist || ''),
                    year: (t.year !== undefined && t.year !== null && t.year !== '') ? t.year : (this.album.year || null),
                    genre: formatGenres((t.genre !== undefined && t.genre !== null && t.genre !== '') ? t.genre : (this.album.genre || '')),
                    cover_url: t.cover_url || null,
                    cover_path: t.cover_path || null,
                    cover_base64: t.cover_base64 || null,
                    cover_preview_url: covPrev,
                    job_id: t.job_id || null,
                };
            });
            this.sourceDir = data.sourceDir || '';
            this.coverPath = data.coverPath || null;

            if (this.coverPath) {
                this.coverPreviewUrl = `/tags/cover?path=${encodeURIComponent(this.coverPath)}&t=${Date.now()}`;
            } else if (data.hasCover && this.sourceDir) {
                this.coverPreviewUrl = `/tags/cover?path=${encodeURIComponent(this.sourceDir)}&t=${Date.now()}`;
            } else {
                this.coverPreviewUrl = null;
            }

            if (this.buildReleasesFromTracks) {
                this.buildReleasesFromTracks();
            }
        },

        async saveAlbum() {
            if (!this.tracks || this.tracks.length === 0) {
                Alpine.store('app').toast('Нет треков для сохранения', 'error');
                return;
            }

            this.isSaving = true;
            const formatGenres = this.formatGenres || (window.AppFormatters ? window.AppFormatters.formatGenres : (g) => g);

            try {
                if (this.viewMode === 'grouped' && this.releases) {
                    this.releases.forEach((group) => {
                        group.items.forEach((itemObj) => {
                            const trk = itemObj.track;
                            if (group.isSingles) {
                                if (!trk.album || trk.album.toLowerCase() === 'single' || trk.album.toLowerCase() === 'singles') {
                                    trk.album = trk.title || 'Single';
                                }
                            } else if (group.albumTitle !== undefined && group.albumTitle !== null) {
                                trk.album = group.albumTitle;
                            }
                            if (group.albumArtist !== undefined && group.albumArtist !== null) {
                                trk.album_artist = group.albumArtist;
                            }
                            if (group.year !== undefined && group.year !== null && group.year !== '') {
                                const parsedYear = parseInt(group.year, 10);
                                trk.year = !isNaN(parsedYear) && parsedYear > 0 ? parsedYear : null;
                            }
                            if (group.genre !== undefined && group.genre !== null) {
                                trk.genre = formatGenres(group.genre);
                            }
                            if (group.discTotal !== undefined && group.discTotal !== null && group.discTotal !== '') {
                                const parsedDiscTotal = parseInt(group.discTotal, 10);
                                trk.disc_total = !isNaN(parsedDiscTotal) && parsedDiscTotal > 0 ? parsedDiscTotal : null;
                            }
                            const cov = group.selectedCoverUrl || group.coverPreviewUrl;
                            if (cov) {
                                if (group.selectedCoverUrl) trk.cover_url = group.selectedCoverUrl;
                                else if (group.coverBase64) trk.cover_base64 = group.coverBase64;
                                else if (cov.startsWith('http')) trk.cover_url = cov;
                                else if (cov.startsWith('data:')) trk.cover_base64 = cov;
                                trk.has_cover = true;
                            }
                            if (group.coverPath) {
                                trk.cover_path = group.coverPath;
                                trk.has_cover = true;
                            }
                        });
                    });
                }

                const firstRelease = this.releases && this.releases.length > 0 ? this.releases[0] : null;
                const isSingleRelease = this.releases && this.releases.length === 1;

                const payload = {
                    album: {
                        album: isSingleRelease && firstRelease ? (firstRelease.albumTitle || '') : (this.album.album || ''),
                        album_artist: isSingleRelease && firstRelease ? (firstRelease.albumArtist || '') : (this.album.album_artist || ''),
                        year: isSingleRelease && firstRelease && firstRelease.year ? (parseInt(firstRelease.year, 10) || null) : (this.album.year ? parseInt(this.album.year, 10) : null),
                        genre: formatGenres(isSingleRelease && firstRelease ? (firstRelease.genre || '') : (this.album.genre || '')),
                        cover_path: isSingleRelease && firstRelease ? firstRelease.coverPath : this.coverPath,
                        disc_total: isSingleRelease && firstRelease && firstRelease.discTotal ? (parseInt(firstRelease.discTotal, 10) || null) : (this.album.disc_total ? parseInt(this.album.disc_total, 10) : null),
                    },
                    tracks: this.tracks.map((t) => {
                        const parsedTrackNum = parseInt(t.track_number, 10);
                        const parsedTrackTotal = parseInt(t.track_total, 10);
                        const parsedDiscNum = parseInt(t.disc_number, 10);
                        const parsedDiscTotal = parseInt(t.disc_total, 10);
                        const parsedYear = parseInt(t.year, 10);
                        let trkAlbum = (t.album || '').trim();
                        if (!trkAlbum || trkAlbum.toLowerCase() === 'single' || trkAlbum.toLowerCase() === 'singles') {
                            trkAlbum = (t.title || '').trim() || 'Single';
                        }
                        return {
                            path: t.path,
                            track_number: !isNaN(parsedTrackNum) && parsedTrackNum >= 1 ? parsedTrackNum : 1,
                            track_total: !isNaN(parsedTrackTotal) && parsedTrackTotal >= 1 ? parsedTrackTotal : null,
                            disc_number: !isNaN(parsedDiscNum) && parsedDiscNum >= 1 ? parsedDiscNum : 1,
                            disc_total: !isNaN(parsedDiscTotal) && parsedDiscTotal >= 1 ? parsedDiscTotal : null,
                            title: t.title || '',
                            artist: t.artist || '',
                            album: trkAlbum,
                            album_artist: t.album_artist || '',
                            year: !isNaN(parsedYear) && parsedYear >= 1000 && parsedYear <= 2999 ? parsedYear : null,
                            genre: formatGenres(t.genre || ''),
                            lyrics_status: t.lyrics_status || 'none',
                            has_cover: t.has_cover || !!t.cover_preview_url || !!t.cover_url || !!t.cover_base64 || !!t.cover_path,
                            cover_url: t.cover_url || null,
                            cover_base64: t.cover_base64 || null,
                            cover_path: t.cover_path || null,
                        };
                    }),
                    source_dir: this.sourceDir || 'tmp_downloads',
                    cover_path: isSingleRelease && firstRelease ? firstRelease.coverPath : this.coverPath,
                    cover_base64: isSingleRelease && firstRelease ? firstRelease.coverBase64 : (this.coverBase64 || null),
                    cover_url: isSingleRelease && firstRelease ? firstRelease.selectedCoverUrl : (this.selectedCoverUrl || null),
                    apply_album_to_tracks: false,
                    reorganize: true,
                    singles_mode: this.singlesMode,
                    group_by_album: this.groupByAlbum,
                };

                const res = await fetch('/tags/batch', {
                    method: 'PUT',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(payload),
                });

                if (!res.ok) {
                    const err = await res.json().catch(() => ({}));
                    const detailMsg = err.detail
                        ? (Array.isArray(err.detail) ? err.detail.map(d => d.msg || JSON.stringify(d)).join(', ') : String(err.detail))
                        : (err.message || 'Ошибка сохранения альбома');
                    throw new Error(detailMsg);
                }

                const result = await res.json();
                const totalSavedReleases = this.releases.length;
                const toastMsg = totalSavedReleases > 1
                    ? `Успешно сохранено ${totalSavedReleases} релизов (${this.tracks.length} треков) в медиатеку!`
                    : 'Теги успешно сохранены и треки размещены в медиатеке!';
                Alpine.store('app').toast(toastMsg, 'success');

                if (this.jobIds && this.jobIds.length > 0) {
                    try {
                        await Promise.allSettled(
                            this.jobIds.map((jid) => fetch(`/downloads/${jid}`, { method: 'DELETE' }))
                        );
                        if (Alpine.store('app').queue) {
                            await Alpine.store('app').queue.fetchQueue();
                        }
                    } catch (cleanupErr) {
                        console.debug('Сбой очистки сохраненных задач из очереди:', cleanupErr);
                    }
                }

                Alpine.store('app').closeTagEditor();
                Alpine.store('app').activeTab = 'files';
                window.dispatchEvent(new CustomEvent('refresh-file-manager', { detail: result.destination_dir }));
                window.dispatchEvent(new CustomEvent('refresh-audit', { detail: { force: true } }));
            } catch (err) {
                Alpine.store('app').toast(`Ошибка: ${err.message}`, 'error');
            } finally {
                this.isSaving = false;
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
