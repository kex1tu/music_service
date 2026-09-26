/**
 * Реактивный плеер воспроизведения треков (Alpine Global Store).
 * Управляет HTML5 аудио-потоком с поддержкой HTTP Range (перемотка),
 * очередью текущей директории, обложками, горячими клавишами и громкостью.
 */
document.addEventListener('alpine:init', () => {
    Alpine.store('player', {
        currentTrack: null,
        playlist: [],
        currentIndex: -1,
        isPlaying: false,
        isLoading: false,
        currentTime: 0,
        duration: 0,
        volume: 0.8,
        isMuted: false,
        isVisible: false,
        audioElement: null,

        init() {
            // Восстанавливаем громкость из LocalStorage
            try {
                const savedVol = localStorage.getItem('mm_player_volume');
                if (savedVol !== null) {
                    const parsed = parseFloat(savedVol);
                    if (!isNaN(parsed) && parsed >= 0 && parsed <= 1) {
                        this.volume = parsed;
                    }
                }
            } catch (e) {
                // Ignore storage errors
            }

            // Глобальные горячие клавиши (Space: Play/Pause, Стрелки: перемотка, M: Mute)
            window.addEventListener('keydown', (e) => {
                const target = e.target;
                const isInput = target && (
                    target.tagName === 'INPUT' ||
                    target.tagName === 'TEXTAREA' ||
                    target.tagName === 'SELECT' ||
                    target.isContentEditable
                );
                if (isInput) return;

                if (e.code === 'Space' && this.currentTrack) {
                    e.preventDefault();
                    this.togglePlay();
                } else if (e.code === 'ArrowRight' && this.currentTrack) {
                    e.preventDefault();
                    this.seek(Math.min(this.duration, this.currentTime + 5));
                } else if (e.code === 'ArrowLeft' && this.currentTrack) {
                    e.preventDefault();
                    this.seek(Math.max(0, this.currentTime - 5));
                } else if (e.code === 'KeyM' && this.currentTrack) {
                    e.preventDefault();
                    this.toggleMute();
                }
            });
        },

        bindAudio(el) {
            this.audioElement = el;
            if (this.audioElement) {
                this.audioElement.volume = this.isMuted ? 0 : this.volume;
            }
        },

        playTrack(track, playlist = []) {
            if (!track) return;

            // Если кликнули на уже играющий трек — переключаем паузу/воспроизведение
            if (this.currentTrack && (this.currentTrack.path === track.path || this.currentTrack.id === track.id)) {
                this.togglePlay();
                return;
            }

            this.currentTrack = track;
            this.isVisible = true;
            this.currentTime = 0;
            this.duration = track.duration || 0;

            if (playlist && playlist.length > 0) {
                this.playlist = playlist.filter(item => item.type === 'file' && (item.extension === '.mp3' || item.stream_url));
                this.currentIndex = this.playlist.findIndex(item => item.path === track.path);
            }

            const streamUrl = track.stream_url || `/api/tracks/${track.id || track.path}/stream`;

            if (this.audioElement) {
                this.isLoading = true;
                this.audioElement.src = streamUrl;
                this.audioElement.load();
                this.audioElement.play()
                    .then(() => {
                        this.isPlaying = true;
                        this.isLoading = false;
                    })
                    .catch((err) => {
                        console.warn('Автовоспроизведение прервано:', err);
                        this.isPlaying = false;
                        this.isLoading = false;
                    });
            }
        },

        togglePlay() {
            if (!this.audioElement || !this.currentTrack) return;

            if (this.isPlaying) {
                this.audioElement.pause();
                this.isPlaying = false;
            } else {
                this.audioElement.play()
                    .then(() => {
                        this.isPlaying = true;
                    })
                    .catch((err) => {
                        console.error('Ошибка воспроизведения:', err);
                    });
            }
        },

        seek(time) {
            if (!this.audioElement || isNaN(time)) return;
            const targetTime = Math.max(0, Math.min(this.duration || Infinity, time));
            this.audioElement.currentTime = targetTime;
            this.currentTime = targetTime;
        },

        next() {
            if (!this.playlist || this.playlist.length === 0 || this.currentIndex === -1) return;
            const nextIdx = (this.currentIndex + 1) % this.playlist.length;
            this.playTrack(this.playlist[nextIdx], this.playlist);
        },

        prev() {
            if (!this.playlist || this.playlist.length === 0 || this.currentIndex === -1) return;
            if (this.currentTime > 3) {
                this.seek(0);
                return;
            }
            const prevIdx = (this.currentIndex - 1 + this.playlist.length) % this.playlist.length;
            this.playTrack(this.playlist[prevIdx], this.playlist);
        },

        setVolume(val) {
            this.volume = Math.max(0, Math.min(1, val));
            if (this.isMuted && this.volume > 0) {
                this.isMuted = false;
            }
            if (this.audioElement) {
                this.audioElement.volume = this.isMuted ? 0 : this.volume;
            }
            try {
                localStorage.setItem('mm_player_volume', String(this.volume));
            } catch (e) {
                // Ignore storage errors
            }
        },

        toggleMute() {
            this.isMuted = !this.isMuted;
            if (this.audioElement) {
                this.audioElement.volume = this.isMuted ? 0 : this.volume;
            }
        },

        onTimeUpdate() {
            if (this.audioElement) {
                this.currentTime = this.audioElement.currentTime;
                if (!this.duration && this.audioElement.duration && !isNaN(this.audioElement.duration)) {
                    this.duration = this.audioElement.duration;
                }
            }
        },

        onDurationChange() {
            if (this.audioElement && this.audioElement.duration && !isNaN(this.audioElement.duration)) {
                this.duration = this.audioElement.duration;
            }
        },

        onEnded() {
            this.isPlaying = false;
            if (this.playlist && this.playlist.length > 0 && this.currentIndex < this.playlist.length - 1) {
                this.next();
            }
        },

        onError(e) {
            this.isLoading = false;
            this.isPlaying = false;
            console.error('Ошибка воспроизведения аудио:', e);
            if (Alpine.store('app') && Alpine.store('app').toast) {
                Alpine.store('app').toast('Ошибка стриминга аудиофайла', 'error');
            }
        },

        close() {
            if (this.audioElement) {
                this.audioElement.pause();
                this.audioElement.src = '';
            }
            this.isPlaying = false;
            this.currentTrack = null;
            this.isVisible = false;
        },

        formatDuration(sec) {
            if (window.AppFormatters && window.AppFormatters.formatDuration) {
                return window.AppFormatters.formatDuration(sec);
            }
            if (!sec || isNaN(sec)) return '0:00';
            const m = Math.floor(sec / 60);
            const s = Math.floor(sec % 60);
            return `${m}:${s < 10 ? '0' : ''}${s}`;
        }
    });
});
