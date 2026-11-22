/**
 * Alpine Store & Controller для Release Radar & Gap Detector.
 */
document.addEventListener('alpine:init', () => {
    Alpine.store('radar', {
        activeSubTab: 'feed', // 'feed' | 'artists'
        feedItems: [],
        feedTotal: 0,
        feedPage: 1,
        feedLimit: 24,
        feedType: 'all',
        feedSearch: '',
        unreadCount: 0,
        isLoading: false,
        isLoadingMore: false,

        artists: [],
        artistsTotal: 0,
        artistsSearch: '',

        // Прогресс сканирования
        progress: {
            is_running: false,
            current_index: 0,
            total_artists: 0,
            current_artist: '',
            new_releases_found: 0,
            last_error: ''
        },
        pollTimer: null,

        // Модальное окно дискографии артиста
        isDiscographyModalOpen: false,
        selectedArtist: null,
        artistTrackedInfo: null,
        discographyReleases: [],
        isDiscographyLoading: false,
        customAlias: '',
        customDeezerId: '',
        customMbId: '',
        customYandexId: '',

        init() {
            this.fetchUnreadCount();
            this.fetchFeed(1);
            this.fetchArtists();
            this.pollProgress();
        },

        async fetchUnreadCount() {
            try {
                const res = await window.api.raw('/api/radar/unread-count');
                if (res.ok) {
                    const data = await res.json();
                    this.unreadCount = data.unread || 0;
                }
            } catch (err) {
                console.error('[Radar] Error fetching unread count:', err);
            }
        },

        async fetchFeed(page = 1) {
            this.isLoading = true;
            this.feedPage = page;
            try {
                const params = new URLSearchParams({
                    page: this.feedPage,
                    limit: this.feedLimit,
                    type: this.feedType,
                    search: this.feedSearch.trim()
                });
                const res = await window.api.raw(`/api/radar/feed?${params.toString()}`);
                if (res.ok) {
                    const data = await res.json();
                    this.feedItems = data.items || [];
                    this.feedTotal = data.total || 0;
                    this.unreadCount = data.unread || 0;
                }
            } catch (err) {
                console.error('[Radar] Error fetching feed:', err);
            } finally {
                this.isLoading = false;
            }
        },

        async loadMoreFeed() {
            if (this.isLoadingMore || this.isLoading || this.feedItems.length >= this.feedTotal) {
                return;
            }
            this.isLoadingMore = true;
            const nextPage = this.feedPage + 1;
            try {
                const params = new URLSearchParams({
                    page: nextPage,
                    limit: this.feedLimit,
                    type: this.feedType,
                    search: this.feedSearch.trim()
                });
                const res = await window.api.raw(`/api/radar/feed?${params.toString()}`);
                if (res.ok) {
                    const data = await res.json();
                    const newItems = data.items || [];
                    this.feedItems.push(...newItems);
                    this.feedPage = nextPage;
                    this.feedTotal = data.total || 0;
                    this.unreadCount = data.unread || 0;
                }
            } catch (err) {
                console.error('[Radar] Error loading more feed items:', err);
            } finally {
                this.isLoadingMore = false;
            }
        },

        changeFeedLimit(newLimit) {
            this.feedLimit = newLimit;
            this.fetchFeed(1);
        },

        goToFeedPage(page) {
            if (page < 1 || (page > Math.ceil(this.feedTotal / this.feedLimit) && this.feedTotal > 0)) {
                return;
            }
            this.fetchFeed(page);
        },

        async fetchArtists() {
            this.isLoading = true;
            try {
                const params = new URLSearchParams();
                const search = this.artistsSearch.trim();
                if (search) params.set('search', search);
                const res = await window.api.raw(`/api/radar/artists?${params.toString()}`);
                if (res.ok) {
                    const data = await res.json();
                    if (this.artistsSearch.trim() !== search) return;
                    this.artists = data.items || [];
                    this.artistsTotal = data.total || 0;
                }
            } catch (err) {
                console.error('[Radar] Error fetching artists:', err);
            } finally {
                this.isLoading = false;
            }
        },

        async triggerScan(artistName = '', artistId = 0) {
            try {
                const body = artistName ? { artist: artistName, artist_id: artistId || undefined } : {};
                const res = await window.api.raw('/api/radar/scan', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(body)
                });
                if (res.ok) {
                    if (artistName) {
                        const data = await res.json();
                        Alpine.store('app').showToast(`Дискография ${artistName} обновлена: найдено ${data.new_releases || 0} новинок`, 'success');
                        this.fetchFeed(1);
                        this.fetchArtists();
                    } else {
                        Alpine.store('app').showToast('Запущено фоновое сканирование всех артистов', 'info');
                        this.progress.is_running = true;
                        this.progress.current_artist = 'запуск...';
                        this.pollProgress();
                    }
                } else {
                    const data = await res.json().catch(() => ({}));
                    Alpine.store('app').showToast(data.error || 'Ошибка сканирования', 'error');
                }
            } catch (err) {
                console.error('[Radar] Error triggering scan:', err);
                Alpine.store('app').showToast('Ошибка запуска сканирования', 'error');
            }
        },

        async pollProgress() {
            if (this.pollTimer) clearInterval(this.pollTimer);
            let wasRunning = this.progress.is_running;
            const check = async () => {
                try {
                    const res = await window.api.raw('/api/radar/progress');
                    if (res.ok) {
                        const data = await res.json();
                        const previouslyRunning = wasRunning || this.progress.is_running;
                        this.progress = data;
                        wasRunning = data.is_running;
                        if (!data.is_running) {
                            clearInterval(this.pollTimer);
                            this.pollTimer = null;
                            if (previouslyRunning) {
                                Alpine.store('app').showToast(`Сканирование завершено: найдено ${data.new_releases_found || 0} новинок`, 'success');
                            }
                            this.fetchUnreadCount();
                            this.fetchFeed(1);
                            this.fetchArtists();
                        }
                    }
                } catch (e) {
                    // ignore network errors
                }
            };
            await check();
            if (this.progress.is_running) {
                this.pollTimer = setInterval(check, 1200);
            }
        },

        async cancelScan() {
            try {
                await window.api.raw('/api/radar/cancel', { method: 'POST' });
                this.progress.is_running = false;
                if (this.pollTimer) clearInterval(this.pollTimer);
                Alpine.store('app').showToast('Сканирование остановлено', 'warning');
            } catch (err) {
                console.error('[Radar] Error cancelling scan:', err);
            }
        },

        async setReleaseStatus(release, status) {
            try {
                const res = await window.api.raw(`/api/radar/releases/${release.id}/status`, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ status })
                });
                if (res.ok) {
                    release.status = status;
                    if (status === 'ignored') {
                        Alpine.store('app').showToast(`Релиз скрыт: ${release.title}`, 'info');
                    }
                    if (status === 'ignored' || status === 'downloaded') {
                        this.feedItems = this.feedItems.filter(item => item.id !== release.id);
                        this.feedTotal = Math.max(0, this.feedTotal - 1);
                    }
                    this.fetchUnreadCount();
                }
            } catch (err) {
                console.error('[Radar] Error setting release status:', err);
            }
        },

        async downloadRelease(release) {
            if (!release.source_url) {
                Alpine.store('app').showToast('URL источника отсутствует', 'warning');
                return;
            }
            try {
                const res = await window.api.raw('/downloads', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ url: release.source_url, source: 'auto' })
                });
                if (res.ok) {
                    await this.setReleaseStatus(release, 'downloaded');
                    Alpine.store('app').showToast(`Загрузка "${release.title}" добавлена в очередь`, 'success');
                    if (Alpine.store('queueManager')) {
                        Alpine.store('queueManager').fetchQueue();
                    }
                } else {
                    const data = await res.json();
                    Alpine.store('app').showToast(data.error || 'Ошибка добавления в очередь', 'error');
                }
            } catch (err) {
                console.error('[Radar] Error creating download job:', err);
            }
        },

        async openDiscography(artist) {
            const chosen = typeof artist === 'object' ? artist : null;
            const targetArtist = chosen ? chosen.name : artist?.split(',')[0].trim();
            this.artistTrackedInfo = null;
            this.selectedArtist = targetArtist;
            this.isDiscographyModalOpen = true;
            this.isDiscographyLoading = true;
            this.discographyReleases = [];

            try {
                const artistQuery = artist?.artist_id ? `?artist_id=${encodeURIComponent(artist.artist_id)}` : '';
                const res = await window.api.raw(`/api/radar/artists/${encodeURIComponent(targetArtist)}/discography${artistQuery}`);
                if (res.ok) {
                    const data = await res.json();
                    this.discographyReleases = data.releases || [];
                    this.artistTrackedInfo = chosen || data.tracked || null;
                    if (this.artistTrackedInfo) {
                        this.customAlias = this.artistTrackedInfo.custom_search_alias || '';
                        this.customDeezerId = this.artistTrackedInfo.deezer_id || '';
                        this.customMbId = this.artistTrackedInfo.musicbrainz_id || '';
                        this.customYandexId = this.artistTrackedInfo.yandex_id || '';
                    }
                }
            } catch (err) {
                console.error('[Radar] Error fetching discography:', err);
            } finally {
                this.isDiscographyLoading = false;
            }
        },

        async saveCustomLinks() {
            if (!this.artistTrackedInfo?.id) return;
            try {
                const res = await window.api.raw(`/api/radar/artists/${encodeURIComponent(this.selectedArtist)}/custom-links`, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({
                        artist_id: this.artistTrackedInfo.artist_id || undefined,
                        alias: this.customAlias.trim(),
                        deezer_id: this.customDeezerId.trim(),
                        musicbrainz_id: this.customMbId.trim(),
                        yandex_id: this.customYandexId.trim()
                    })
                });
                if (res.ok) {
                    Alpine.store('app').showToast('Идентификаторы артиста сохранены', 'success');
                    this.triggerScan(this.selectedArtist, this.artistTrackedInfo.artist_id);
                }
            } catch (err) {
                console.error('[Radar] Error saving custom links:', err);
            }
        }
    });
});
