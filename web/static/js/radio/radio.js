/**
 * Alpine Store & Controller для Моей Волны, ICY-радиовещания и Navidrome.
 */
document.addEventListener('alpine:init', () => {
    Alpine.store('radio', {
        stats: {
            top_genres: [],
            navidrome_available: false,
            subsonic_configured: false,
            wave_size: 100,
            stream_token_set: false
        },
        waveQueue: [],
        genres: [],
        artistSearch: '',
        isLoading: false,
        isGeneratingM3U: false,
        isSyncingNavidrome: false,
        streamToken: '',

        init() {
            this.fetchStats();
            this.fetchQueue();
            this.fetchGenres();
        },

        getStreamUrl(type = 'wave', param = '') {
            const loc = window.location;
            let path = '/api/radio/stream/wave';
            if (type === 'genre' && param) {
                path = `/api/radio/stream/genre/${encodeURIComponent(param)}`;
            } else if (type === 'artist' && param) {
                path = `/api/radio/stream/artist/${encodeURIComponent(param)}`;
            }
            const tokenParam = this.streamToken ? `?token=${encodeURIComponent(this.streamToken)}` : '';
            return `${loc.protocol}//${loc.host}${path}${tokenParam}`;
        },

        async fetchStats() {
            try {
                const res = await fetch('/api/radio/wave/stats');
                if (res.ok) {
                    this.stats = await res.json();
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
            const url = this.getStreamUrl('wave');
            if (Alpine.store('player')) {
                Alpine.store('player').playRadioStream(url, 'Моя Волна');
                Alpine.store('app').showToast('Воспроизведение «Моя Волна» запущено', 'info');
            }
        },

        playGenre(genre) {
            if (!genre) return;
            const url = this.getStreamUrl('genre', genre);
            if (Alpine.store('player')) {
                Alpine.store('player').playRadioStream(url, `Радио: ${genre}`);
                Alpine.store('app').showToast(`Запущено радио жанра "${genre}"`, 'info');
            }
        },

        playArtist(artist) {
            artist = artist || this.artistSearch.trim();
            if (!artist) return;
            const url = this.getStreamUrl('artist', artist);
            if (Alpine.store('player')) {
                Alpine.store('player').playRadioStream(url, `Радио артиста: ${artist}`);
                Alpine.store('app').showToast(`Запущено радио исполнителя "${artist}"`, 'info');
                this.artistSearch = '';
            }
        },

        async generateM3U() {
            this.isGeneratingM3U = true;
            try {
                const res = await fetch('/api/radio/m3u/generate', { method: 'POST' });
                if (res.ok) {
                    const data = await res.json();
                    Alpine.store('app').showToast(`Плейлист ${data.playlist_path} успешно сгенерирован`, 'success');
                } else {
                    const text = await res.text();
                    Alpine.store('app').showToast(`Ошибка генерации M3U: ${text}`, 'error');
                }
            } catch (err) {
                console.error('[Radio] Error generating M3U:', err);
                Alpine.store('app').showToast('Ошибка генерации плейлиста', 'error');
            } finally {
                this.isGeneratingM3U = false;
            }
        },

        async syncNavidrome() {
            this.isSyncingNavidrome = true;
            try {
                const res = await fetch('/api/radio/navidrome/sync-radio', { method: 'POST' });
                if (res.ok) {
                    const data = await res.json();
                    Alpine.store('app').showToast(`Радиостанция "${data.name}" создана в Navidrome!`, 'success');
                } else {
                    const text = await res.text();
                    Alpine.store('app').showToast(`Ошибка экспорта в Navidrome: ${text}`, 'error');
                }
            } catch (err) {
                console.error('[Radio] Error syncing Navidrome:', err);
                Alpine.store('app').showToast('Сбой синхронизации с Navidrome', 'error');
            } finally {
                this.isSyncingNavidrome = false;
            }
        },

        copyStreamUrl(type = 'wave', param = '') {
            const url = this.getStreamUrl(type, param);
            if (navigator.clipboard) {
                navigator.clipboard.writeText(url).then(() => {
                    Alpine.store('app').showToast('Ссылка на аудиопоток скопирована в буфер', 'success');
                });
            }
        }
    });
});
