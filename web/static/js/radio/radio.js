/**
 * Alpine Store & Controller для Моей Волны, ICY-радиовещания и Navidrome.
 */
document.addEventListener('alpine:init', () => {
    Alpine.store('radio', {
        stats: {
            top_genres: [],
            navidrome_available: false,
            subsonic_configured: false,
            wave_size: 60,
            stream_token_set: false,
            stream_token: ''
        },
        waveQueue: [],
        genres: [],
        artistSearch: '',
        nowPlaying: null,
        recentHistory: [],
        queueTab: 'queue', // 'queue' | 'history'
        isLoading: false,
        isGeneratingM3U: false,
        isSyncingNavidrome: false,
        streamToken: '',
        pollTimer: null,

        init() {
            this.fetchStats();
            this.fetchQueue();
            this.fetchGenres();
            this.fetchNowPlaying();
            if (!this.pollTimer) {
                this.pollTimer = setInterval(() => this.fetchNowPlaying(), 4000);
            }
        },

        async fetchNowPlaying() {
            try {
                const res = await fetch('/api/radio/stream/now-playing');
                if (res.ok) {
                    const data = await res.json();
                    this.nowPlaying = data.current_track || null;
                    this.recentHistory = data.history || [];
                }
            } catch (err) {
                console.debug('[Radio] Error polling now playing:', err);
            }
        },

        getStreamUrl(type = 'wave', param = '', absolute = false) {
            let path = '/api/radio/stream/wave';
            if (type === 'genre' && param) {
                path = `/api/radio/stream/genre/${encodeURIComponent(param)}`;
            } else if (type === 'artist' && param) {
                path = `/api/radio/stream/artist/${encodeURIComponent(param)}`;
            }
            const token = this.streamToken || (this.stats && this.stats.stream_token) || '';
            const tokenParam = token ? `?token=${encodeURIComponent(token)}` : '';

            if (!absolute) {
                return `${path}${tokenParam}`;
            }

            const loc = window.location;
            return `${loc.protocol}//${loc.host}${path}${tokenParam}`;
        },

        async fetchStats() {
            try {
                const res = await fetch('/api/radio/wave/stats');
                if (res.ok) {
                    this.stats = await res.json();
                    if (this.stats && this.stats.stream_token) {
                        this.streamToken = this.stats.stream_token;
                    }
                }
            } catch (err) {
                console.error('[Radio] Error fetching stats:', err);
            }
        },

        async fetchQueue() {
            this.isLoading = true;
            try {
                const res = await fetch('/api/radio/wave/queue?limit=25');
                if (res.ok) {
                    const data = await res.json();
                    this.waveQueue = data.tracks || [];
                }
            } catch (err) {
                console.error('[Radio] Error fetching wave queue:', err);
            } finally {
                this.isLoading = false;
            }
        },

        async fetchGenres() {
            try {
                const res = await fetch('/api/radio/genres');
                if (res.ok) {
                    const data = await res.json();
                    this.genres = data.genres || [];
                }
            } catch (err) {
                console.error('[Radio] Error fetching genres:', err);
            }
        },

        playWave() {
            const url = this.getStreamUrl('wave', '', false);
            if (Alpine.store('player')) {
                Alpine.store('player').playRadioStream(url, 'Моя Волна');
                if (Alpine.store('app') && Alpine.store('app').toast) {
                    Alpine.store('app').toast('Воспроизведение «Моя Волна» запущено', 'info');
                }
            }
        },

        playGenre(genre) {
            if (!genre) return;
            const url = this.getStreamUrl('genre', genre, false);
            if (Alpine.store('player')) {
                Alpine.store('player').playRadioStream(url, `Радио: ${genre}`);
                if (Alpine.store('app') && Alpine.store('app').toast) {
                    Alpine.store('app').toast(`Запущено радио жанра "${genre}"`, 'info');
                }
            }
        },

        playArtist(artist) {
            artist = artist || this.artistSearch.trim();
            if (!artist) return;
            const url = this.getStreamUrl('artist', artist, false);
            if (Alpine.store('player')) {
                Alpine.store('player').playRadioStream(url, `Радио артиста: ${artist}`);
                if (Alpine.store('app') && Alpine.store('app').toast) {
                    Alpine.store('app').toast(`Запущено радио исполнителя "${artist}"`, 'info');
                }
                this.artistSearch = '';
            }
        },

        async generateM3U() {
            this.isGeneratingM3U = true;
            try {
                const res = await fetch('/api/radio/m3u/generate', { method: 'POST' });
                if (res.ok) {
                    const data = await res.json();
                    Alpine.store('app').toast(`Плейлист ${data.playlist_path} успешно сгенерирован`, 'success');
                } else {
                    const text = await res.text();
                    Alpine.store('app').toast(`Ошибка генерации M3U: ${text}`, 'error');
                }
            } catch (err) {
                console.error('[Radio] Error generating M3U:', err);
                Alpine.store('app').toast('Ошибка генерации плейлиста', 'error');
            } finally {
                this.isGeneratingM3U = false;
            }
        },

        async syncNavidrome(name = 'Моя Волна', streamUrl = '') {
            this.isSyncingNavidrome = true;
            try {
                if (!streamUrl) {
                    streamUrl = this.getStreamUrl('wave', '', true);
                }
                const res = await fetch('/api/radio/navidrome/sync-radio', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ name: name, stream_url: streamUrl })
                });
                if (res.ok) {
                    const data = await res.json();
                    Alpine.store('app').toast(`Радиостанция "${data.name}" успешно экспортирована в Navidrome!`, 'success');
                } else {
                    const text = await res.text();
                    Alpine.store('app').toast(`Ошибка экспорта в Navidrome: ${text}`, 'error');
                }
            } catch (err) {
                console.error('[Radio] Error syncing Navidrome:', err);
                Alpine.store('app').toast('Сбой синхронизации с Navidrome', 'error');
            } finally {
                this.isSyncingNavidrome = false;
            }
        },

        exportArtistToNavidrome(artist) {
            artist = (artist || this.artistSearch || '').trim();
            if (!artist) {
                if (Alpine.store('app') && Alpine.store('app').toast) {
                    Alpine.store('app').toast('Введите имя исполнителя для экспорта в Navidrome', 'warning');
                }
                return;
            }
            const streamUrl = this.getStreamUrl('artist', artist, true);
            this.syncNavidrome(`Радио: ${artist}`, streamUrl);
        },

        exportGenreToNavidrome(genre) {
            genre = (genre || '').trim();
            if (!genre) return;
            const streamUrl = this.getStreamUrl('genre', genre, true);
            this.syncNavidrome(`Радио: ${genre}`, streamUrl);
        },

        copyStreamUrl(type = 'wave', param = '') {
            const url = this.getStreamUrl(type, param, true);
            if (navigator.clipboard) {
                navigator.clipboard.writeText(url).then(() => {
                    Alpine.store('app').toast('Прямая ссылка на ICY-аудиопоток скопирована в буфер', 'success');
                });
            }
        }
    });
});
