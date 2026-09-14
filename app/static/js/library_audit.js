/**
 * Компонент аудита медиатеки (Library Audit UI).
 * Отвечает за отображение дашборда метрик целостности, пакетный автопоиск текстов,
 * просмотр релизов без обложек и безопасное удаление дубликатов.
 */

function libraryAuditComponent() {
    return {
        activeSubTab: 'lyrics', // 'lyrics' | 'covers' | 'duplicates' | 'incomplete'
        isLoading: false,
        summary: {
            total_tracks: 0,
            total_albums: 0,
            missing_lyrics_count: 0,
            missing_covers_count: 0,
            duplicates_count: 0,
            incomplete_albums_count: 0,
        },
        missingLyrics: [],
        missingCovers: [],
        duplicatesData: { groups: [], total_duplicate_files: 0 },
        incompleteAlbums: [],
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

        init() {
            this.loadAll();
            this.checkLyricsProgress();

            // Слушаем событие обновления при переключении на вкладку или клике
            window.addEventListener('refresh-audit', () => {
                this.loadAll();
                this.checkLyricsProgress();
            });

            // Автоматически обновляем данные при активации вкладки Аудит
            if (this.$watch) {
                this.$watch('$store.app.activeTab', (tab) => {
                    if (tab === 'audit') {
                        this.loadAll();
                        this.checkLyricsProgress();
                    }
                });
            }
        },

        async loadAll() {
            this.isLoading = true;
            try {
                await Promise.all([
                    this.loadSummary(),
                    this.loadMissingLyrics(),
                    this.loadMissingCovers(),
                    this.loadDuplicates(),
                    this.loadIncompleteAlbums(),
                ]);
            } finally {
                this.isLoading = false;
            }
        },

        async loadSummary() {
            try {
                const res = await fetch('/files/audit/summary');
                if (res.ok) {
                    this.summary = await res.json();
                }
            } catch (e) {
                console.error('Ошибка загрузки сводки аудита:', e);
            }
        },

        async loadMissingLyrics() {
            try {
                const res = await fetch('/files/audit/missing-lyrics');
                if (res.ok) {
                    this.missingLyrics = await res.json();
                }
            } catch (e) {
                console.error('Ошибка загрузки треков без слов:', e);
            }
        },

        async loadMissingCovers() {
            try {
                const res = await fetch('/files/audit/missing-covers');
                if (res.ok) {
                    this.missingCovers = await res.json();
                }
            } catch (e) {
                console.error('Ошибка загрузки релизов без обложек:', e);
            }
        },

        async loadDuplicates() {
            try {
                const res = await fetch('/files/audit/duplicates');
                if (res.ok) {
                    this.duplicatesData = await res.json();
                }
            } catch (e) {
                console.error('Ошибка загрузки дубликатов:', e);
            }
        },

        async startBatchLyricsFetch() {
            if (this.lyricsProgress.is_running) return;
            try {
                const res = await fetch('/files/audit/missing-lyrics/fetch-all', { method: 'POST' });
                if (res.ok) {
                    this.lyricsProgress = await res.json();
                    Alpine.store('app').toast('Запущен пакетный поиск текстов песен', 'info');
                    this.startLyricsPolling();
                }
            } catch (e) {
                Alpine.store('app').toast('Ошибка запуска автопоиска текстов', 'error');
            }
        },

        async checkLyricsProgress() {
            try {
                const res = await fetch('/files/audit/missing-lyrics/progress');
                if (res.ok) {
                    const data = await res.json();
                    this.lyricsProgress = data;
                    if (data.is_running && !this.lyricsPollInterval) {
                        this.startLyricsPolling();
                    }
                }
            } catch (e) {
                console.error(e);
            }
        },

        startLyricsPolling() {
            if (this.lyricsPollInterval) clearInterval(this.lyricsPollInterval);
            this.lyricsPollInterval = setInterval(async () => {
                try {
                    const res = await fetch('/files/audit/missing-lyrics/progress');
                    if (res.ok) {
                        const data = await res.json();
                        this.lyricsProgress = data;
                        if (!data.is_running) {
                            clearInterval(this.lyricsPollInterval);
                            this.lyricsPollInterval = null;
                            Alpine.store('app').toast(
                                `Поиск текстов завершен: найдено ${data.found}, не найдено ${data.failed}`,
                                'success'
                            );
                            await this.loadSummary();
                            await this.loadMissingLyrics();
                        }
                    }
                } catch (e) {
                    console.error(e);
                }
            }, 1000);
        },

        openLyricsForTrack(track) {
            Alpine.store('app').openLyricsModal(track);
        },

        openDirectory(path) {
            Alpine.store('app').activeTab = 'files';
            window.dispatchEvent(new CustomEvent('refresh-file-manager', { detail: path }));
        },

        promptDeleteDuplicate(track) {
            Alpine.store('app').deleteConfirmData = {
                path: track.path,
                name: track.filename,
                type: 'file',
                onDeleted: async () => {
                    await this.loadSummary();
                    await this.loadDuplicates();
                },
            };
            Alpine.store('app').activeModal = 'delete_confirm';
        },

        async loadIncompleteAlbums() {
            this.incompleteLoading = true;
            try {
                const res = await fetch('/files/audit/incomplete-albums');
                if (res.ok) {
                    const data = await res.json();
                    this.incompleteAlbums = data.albums || [];
                }
            } catch (e) {
                console.error('Ошибка загрузки неполных релизов:', e);
            } finally {
                this.incompleteLoading = false;
            }
        },

        async toggleAlbumExpand(album) {
            const path = album.path;
            this.expandedAlbums[path] = !this.expandedAlbums[path];
            if (this.expandedAlbums[path] && !album.is_catalog_matched && !this.catalogMatchLoading[path]) {
                await this.matchCatalog(album);
            }
        },

        async matchCatalog(album) {
            const path = album.path;
            this.catalogMatchLoading[path] = true;
            try {
                const url = `/files/audit/incomplete-albums/catalog-match?path=${encodeURIComponent(path)}`;
                const res = await fetch(url);
                if (res.ok) {
                    const matched = await res.json();
                    const idx = this.incompleteAlbums.findIndex(a => a.path === path);
                    if (idx !== -1) {
                        this.incompleteAlbums[idx] = matched;
                    }
                } else {
                    Alpine.store('app').toast('Не удалось найти треклист альбома в Deezer', 'warning');
                }
            } catch (e) {
                console.error('Ошибка сверки релиза с Deezer:', e);
                Alpine.store('app').toast('Ошибка при сверке с Deezer', 'error');
            } finally {
                this.catalogMatchLoading[path] = false;
            }
        },

        async downloadMissing(album, trackUrl = null) {
            let directUrl = null;
            let trackNumber = null;
            if (typeof trackUrl === 'string') {
                directUrl = trackUrl;
            } else if (trackUrl && typeof trackUrl === 'object') {
                directUrl = trackUrl.deezer_url || null;
                trackNumber = trackUrl.track_number || null;
            }

            const trackKey = directUrl || (trackNumber ? `track_${trackNumber}` : null);
            const key = trackKey ? `${album.path}:::${trackKey}` : album.path;
            this.downloadingTracks[key] = true;
            try {
                const payload = {
                    path: album.path,
                    track_urls: directUrl ? [directUrl] : null,
                    track_numbers: trackNumber ? [trackNumber] : null,
                };
                const res = await fetch('/files/audit/incomplete-albums/download-missing', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(payload),
                });
                if (res.ok) {
                    const jobs = await res.json();
                    if (jobs && jobs.length > 0) {
                        Alpine.store('app').toast(`Поставлено в очередь загрузок: ${jobs.length}`, 'success');
                        if (Alpine.store('app').queue && Alpine.store('app').queue.fetchQueue) {
                            await Alpine.store('app').queue.fetchQueue();
                        }
                    } else {
                        Alpine.store('app').toast('Не найдено ссылок для скачивания недостающих треков', 'warning');
                    }
                } else {
                    let errText = 'Ошибка отправки запроса на скачивание';
                    try {
                        const errData = await res.json();
                        if (errData && errData.detail) errText = errData.detail;
                    } catch (_) {}
                    Alpine.store('app').toast(errText, 'error');
                }
            } catch (e) {
                console.error('Ошибка дозагрузки треков:', e);
                Alpine.store('app').toast('Сбой при запуске загрузки', 'error');
            } finally {
                this.downloadingTracks[key] = false;
            }
        },
    };
}

