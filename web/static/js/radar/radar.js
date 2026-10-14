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

        artists: [],
        artistsTotal: 0,
        artistsSearch: '',
        artistsOnlyFollowed: false,
        newArtistName: '',

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
            this.pollProgress();
        },

        async fetchUnreadCount() {
            try {
                const res = await fetch('/api/radar/unread-count');
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
                const res = await fetch(`/api/radar/feed?${params.toString()}`);
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

        async fetchArtists() {
            this.isLoading = true;
            try {
                const params = new URLSearchParams();
                if (this.artistsOnlyFollowed) {
                    params.set('followed', 'true');
                }
                const res = await fetch(`/api/radar/artists?${params.toString()}`);
                if (res.ok) {
                    const data = await res.json();
                    this.artists = data.items || [];
                    this.artistsTotal = data.total || 0;
                }
            } catch (err) {
                console.error('[Radar] Error fetching artists:', err);
            } finally {
                this.isLoading = false;
            }
        },

        filteredArtists() {
            if (!this.artistsSearch.trim()) return this.artists;
            const q = this.artistsSearch.toLowerCase().trim();
            return this.artists.filter(a => a.name.toLowerCase().includes(q) || (a.custom_search_alias && a.custom_search_alias.toLowerCase().includes(q)));
        },

        async addArtist() {
            const name = this.newArtistName.trim();
            if (!name) return;
            try {
                const res = await fetch('/api/radar/artists', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ name })
                });
                if (res.ok) {
                    this.newArtistName = '';
                    Alpine.store('app').showToast(`Артист "${name}" добавлен в отслеживание`, 'success');
                    this.fetchArtists();
                    this.triggerScan(name);
                }
            } catch (err) {
                console.error('[Radar] Error adding artist:', err);
            }
        },

        async toggleFollow(artist) {
            const targetState = !artist.is_followed;
            try {
                const res = await fetch(`/api/radar/artists/${encodeURIComponent(artist.name)}/follow`, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ followed: targetState })
                });
                if (res.ok) {
                    artist.is_followed = targetState;
                    Alpine.store('app').showToast(targetState ? `Отслеживание ${artist.name} включено` : `Отслеживание ${artist.name} приостановлено`, 'info');
                }
            } catch (err) {
                console.error('[Radar] Error toggling follow:', err);
            }
        },

        async toggleAutoDownload(artist) {
            const targetState = !artist.auto_download;
            try {
                const res = await fetch(`/api/radar/artists/${encodeURIComponent(artist.name)}/auto-download`, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ auto_download: targetState })
                });
                if (res.ok) {
                    artist.auto_download = targetState;
                    Alpine.store('app').showToast(targetState ? `Автозагрузка для ${artist.name} включена` : `Автозагрузка для ${artist.name} выключена`, 'info');
                }
            } catch (err) {
                console.error('[Radar] Error toggling auto-download:', err);
            }
        },

        async triggerScan(artistName = '') {
            try {
                const body = artistName ? { artist: artistName } : {};
                const res = await fetch('/api/radar/scan', {
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
                    const res = await fetch('/api/radar/progress');
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
                await fetch('/api/radar/cancel', { method: 'POST' });
                this.progress.is_running = false;
                if (this.pollTimer) clearInterval(this.pollTimer);
                Alpine.store('app').showToast('Сканирование остановлено', 'warning');
            } catch (err) {
                console.error('[Radar] Error cancelling scan:', err);
            }
        },

        async setReleaseStatus(release, status) {
            try {
                const res = await fetch(`/api/radar/releases/${release.id}/status`, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ status })
                });
                if (res.ok) {
                    release.status = status;
                    if (status === 'ignored') {
                        Alpine.store('app').showToast(`Релиз скрыт: ${release.title}`, 'info');
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
                const res = await fetch('/downloads', {
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

        async openDiscography(artistName) {
            this.selectedArtist = artistName;
            this.isDiscographyModalOpen = true;
            this.isDiscographyLoading = true;
            this.discographyReleases = [];

            try {
                const res = await fetch(`/api/radar/artists/${encodeURIComponent(artistName)}/discography`);
                if (res.ok) {
                    const data = await res.json();
                    this.discographyReleases = data.releases || [];
                    this.artistTrackedInfo = data.tracked || null;
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
            if (!this.selectedArtist) return;
            try {
                const res = await fetch(`/api/radar/artists/${encodeURIComponent(this.selectedArtist)}/custom-links`, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({
                        alias: this.customAlias.trim(),
                        deezer_id: this.customDeezerId.trim(),
                        musicbrainz_id: this.customMbId.trim(),
                        yandex_id: this.customYandexId.trim()
                    })
                });
                if (res.ok) {
                    Alpine.store('app').showToast('Идентификаторы артиста сохранены', 'success');
                    this.triggerScan(this.selectedArtist);
                }
            } catch (err) {
                console.error('[Radar] Error saving custom links:', err);
            }
        }
    });
});
