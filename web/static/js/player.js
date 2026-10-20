/**
 * Реактивный плеер воспроизведения треков (Alpine Global Store).
 * Поддерживает:
 * 1. HTML5 Range-стриминг аудиопотока (RFC 7233).
 * 2. Web Audio API AnalyserNode & Canvas спектроанализатор частот (Teenage Engineering Hi-Fi).
 * 3. Движок синхронизированного караоке-текста .lrc (автоскролл, подсветка строки, перемотка по клику).
 * 4. Управление интерактивной очередью (перемещение, удаление, рандомизация, зацикливание).
 * 5. Аудиофильский паспорт трека (ID3v2.4 UTF-8, битрейт 320 kbps, 44.1 kHz, SHA-256 хэш).
 */
document.addEventListener('alpine:init', () => {
    Alpine.store('player', {
        // Базовое состояние воспроизведения
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

        // Режимы воспроизведения
        loopMode: 'none', // 'none' | 'all' | 'one'
        isShuffle: false,

        // Студийный монитор (Center Deck)
        centerDeckMode: 'spectrum', // 'spectrum' | 'lyrics' | 'specs'

        // Web Audio API & Спектроанализатор
        audioCtx: null,
        analyserNode: null,
        audioSourceNode: null,
        canvasEl: null,
        canvasCtx: null,
        visualizerRunning: false,
        animationFrameId: null,
        peakValues: new Float32Array(32),
        peakVelocities: new Float32Array(32),
        vuMeterL: 0,
        vuMeterR: 0,
        radioPollInterval: null,

        // Движок караоке LRC
        lyricsLines: [],
        rawLyrics: '',
        currentLyricIndex: -1,
        lyricsKind: 'none', // 'lrc' | 'txt' | 'none'
        lyricsLoading: false,
        lyricsError: null,
        autoScrollLyrics: true,

        // Аудиофильский паспорт трека
        techSpecs: {
            bitrate: 320,
            sampleRate: 44100,
            fileSize: null,
            trackHash: '',
            codec: 'MP3 CBR',
            trackNumber: null,
            year: null,
            genre: ''
        },

        init() {
            // Восстановление уровня громкости
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

            // Глобальные горячие клавиши
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
                    this.seek(Math.min(this.duration || Infinity, this.currentTime + 5));
                } else if (e.code === 'ArrowLeft' && this.currentTrack) {
                    e.preventDefault();
                    this.seek(Math.max(0, this.currentTime - 5));
                } else if (e.code === 'KeyM' && this.currentTrack) {
                    e.preventDefault();
                    this.toggleMute();
                } else if (e.code === 'KeyL' && this.currentTrack) {
                    e.preventDefault();
                    this.toggleLoop();
                }
            });
        },

        bindAudio(el) {
            this.audioElement = el;
            if (this.audioElement) {
                this.audioElement.volume = this.isMuted ? 0 : this.volume;
            }
        },

        bindCanvas(canvas) {
            this.canvasEl = canvas;
            if (this.canvasEl) {
                this.canvasCtx = this.canvasEl.getContext('2d');
                this.resizeCanvas();
                window.addEventListener('resize', () => this.resizeCanvas());
                if (this.isPlaying) {
                    this.startVisualizer();
                }
            }
        },

        resizeCanvas() {
            if (!this.canvasEl) return;
            const rect = this.canvasEl.getBoundingClientRect();
            if (rect.width > 0 && rect.height > 0) {
                const dpr = window.devicePixelRatio || 1;
                this.canvasEl.width = Math.floor(rect.width * dpr);
                this.canvasEl.height = Math.floor(rect.height * dpr);
            }
        },

        ensureAudioContext() {
            if (!this.audioCtx) {
                const AudioContextClass = window.AudioContext || window.webkitAudioContext;
                if (AudioContextClass) {
                    this.audioCtx = new AudioContextClass();
                }
            }
            if (this.audioCtx && this.audioCtx.state === 'suspended') {
                this.audioCtx.resume().catch(() => {});
            }
            if (this.audioCtx && !this.audioSourceNode && this.audioElement) {
                try {
                    this.audioSourceNode = this.audioCtx.createMediaElementSource(this.audioElement);
                    this.analyserNode = this.audioCtx.createAnalyser();
                    this.analyserNode.fftSize = 64; // 32 частотных бина
                    this.analyserNode.smoothingTimeConstant = 0.82;
                    this.audioSourceNode.connect(this.analyserNode);
                    this.analyserNode.connect(this.audioCtx.destination);
                } catch (err) {
                    console.warn('Web Audio API connect warning:', err);
                }
            }
        },

        playTrack(track, playlist = []) {
            if (!track) return;
            this.stopRadioMetadataPolling();

            // Если кликнули на уже играющий трек — переключаем паузу/воспроизведение
            if (this.currentTrack && (this.currentTrack.path === track.path || (this.currentTrack.id && this.currentTrack.id === track.id))) {
                this.togglePlay();
                return;
            }

            this.currentTrack = track;
            this.isVisible = true;
            this.currentTime = 0;
            this.duration = track.duration || 0;

            const trackPath = track.path || track.file_path || '';
            const trackId = track.id || trackPath;

            if (playlist && playlist.length > 0) {
                this.playlist = playlist.filter(item => (item.type === 'file' || item.file_path) && (item.extension === '.mp3' || (item.file_path && item.file_path.endsWith('.mp3')) || item.stream_url));
                this.currentIndex = this.playlist.findIndex(item => (item.path || item.file_path) === trackPath || (item.id && item.id === track.id));
            }

            const streamUrl = track.stream_url || `/api/tracks/${trackId}/stream`;

            this.ensureAudioContext();

            if (this.audioElement) {
                this.isLoading = true;
                this.audioElement.src = streamUrl;
                this.audioElement.load();
                this.audioElement.play()
                    .then(() => {
                        this.isPlaying = true;
                        this.isLoading = false;
                        this.startVisualizer();
                    })
                    .catch((err) => {
                        console.warn('Автовоспроизведение прервано:', err);
                        this.isPlaying = false;
                        this.isLoading = false;
                    });
            }

            // Загружаем тексты песен и метаданные трека
            this.loadLyrics({ ...track, path: trackPath });
            this.fetchTrackSpecs({ ...track, path: trackPath, id: trackId });
        },

        playPreviewStream(streamUrl, title, artist) {
            if (!streamUrl) return;
            if (this.currentTrack && this.currentTrack.preview_url === streamUrl && this.isPlaying) {
                this.togglePlay();
                return;
            }
            this.currentTrack = {
                title: title || 'Предпрослушивание',
                artist: artist || 'Фрагмент (15 сек)',
                duration: 15,
                preview_url: streamUrl,
            };
            this.isVisible = true;
            this.currentTime = 0;
            this.duration = 15;
            this.ensureAudioContext();
            if (this.audioElement) {
                this.isLoading = true;
                this.audioElement.src = streamUrl;
                this.audioElement.load();
                this.audioElement.play()
                    .then(() => {
                        this.isPlaying = true;
                        this.isLoading = false;
                        this.startVisualizer();
                    })
                    .catch((err) => {
                        console.warn('Ошибка превью воспроизведения:', err);
                        this.isPlaying = false;
                        this.isLoading = false;
                    });
            }
        },

        playCensorshipSpot(streamUrl, spot, trackTitle, trackArtist) {
            if (!streamUrl || !spot) return;

            // Смещение на 2 секунды назад для контекста
            const startTime = Math.max(0, (spot.time_sec || 0) - 2.0);
            const stopDuration = 5.0; // Играем ровно 5 секунд

            this.currentTrack = {
                title: trackTitle || 'Отслушивание точки',
                artist: `${trackArtist || ''} • «${spot.word}» (${spot.time_str || ''})`,
                duration: stopDuration,
                stream_url: streamUrl,
            };
            this.isVisible = true;

            if (this._spotTimer) {
                clearTimeout(this._spotTimer);
                this._spotTimer = null;
            }

            this.ensureAudioContext();

            if (this.audioElement) {
                this.isLoading = true;
                const audio = this.audioElement;
                const applySeek = () => {
                    try {
                        audio.currentTime = startTime;
                    } catch (e) {
                        // Ignore seek exceptions
                    }
                };

                const isSameSrc = audio.src && audio.src.includes(streamUrl);
                if (!isSameSrc) {
                    audio.src = streamUrl;
                    audio.load();
                    if (audio.readyState >= 1) {
                        applySeek();
                    } else {
                        audio.addEventListener('loadedmetadata', applySeek, { once: true });
                    }
                } else {
                    applySeek();
                }

                audio.play()
                    .then(() => {
                        applySeek();
                        this.isPlaying = true;
                        this.isLoading = false;
                        this.startVisualizer();
                        this._spotTimer = setTimeout(() => {
                            if (this.audioElement) {
                                this.audioElement.pause();
                            }
                            this.isPlaying = false;
                        }, stopDuration * 1000);
                    })
                    .catch((err) => {
                        console.warn('Spot play error:', err);
                        this.isPlaying = false;
                        this.isLoading = false;
                    });
            }

            const toastMsg = `🎧 Точка: «${spot.word}» (${spot.time_str || ''})${spot.line_text ? '\n' + spot.line_text : ''}`;
            if (window.showToast) {
                window.showToast(toastMsg, 'info');
            } else if (window.Alpine && window.Alpine.store('app') && window.Alpine.store('app').toast) {
                window.Alpine.store('app').toast(toastMsg, 'info');
            }
        },

        playRadioStream(streamUrl, stationName) {
            if (!streamUrl) return;
            if (this.currentTrack && this.currentTrack.stream_url === streamUrl && this.isPlaying) {
                this.togglePlay();
                return;
            }
            this.currentTrack = {
                title: stationName || 'Моя Волна',
                artist: 'Бесконечный ICY-поток (320 kbps)',
                duration: 0,
                is_radio: true,
                stream_url: streamUrl,
            };
            this.isVisible = true;
            this.currentTime = 0;
            this.duration = 0;
            this.ensureAudioContext();
            if (this.audioElement) {
                this.isLoading = true;
                this.audioElement.src = streamUrl;
                this.audioElement.load();
                this.audioElement.play()
                    .then(() => {
                        this.isPlaying = true;
                        this.isLoading = false;
                        this.startVisualizer();
                        this.startRadioMetadataPolling();
                    })
                    .catch((err) => {
                        console.warn('Ошибка воспроизведения радиопотока:', err);
                        this.isPlaying = false;
                        this.isLoading = false;
                        this.stopRadioMetadataPolling();
                    });
            }
        },

        startRadioMetadataPolling() {
            this.stopRadioMetadataPolling();
            const poll = async () => {
                if (!this.isPlaying || !this.currentTrack || !this.currentTrack.is_radio) {
                    return;
                }
                try {
                    const res = await fetch('/api/radio/stream/now-playing');
                    if (res.ok) {
                        const data = await res.json();
                        if (data && data.current_track && this.currentTrack && this.currentTrack.is_radio) {
                            const cur = data.current_track;
                            if (this.currentTrack.title !== cur.title || this.currentTrack.artist !== cur.artist) {
                                this.currentTrack.title = cur.title;
                                this.currentTrack.artist = cur.artist;
                                this.currentTrack.album = cur.album || '';
                                if (cur.duration) {
                                    this.currentTrack.duration = cur.duration;
                                }
                                if (cur.id) {
                                    this.coverUrl = `/api/tracks/${cur.id}/cover`;
                                }
                            }
                        }
                    }
                } catch (e) {}
            };
            poll();
            this.radioPollInterval = setInterval(poll, 4000);
        },

        stopRadioMetadataPolling() {
            if (this.radioPollInterval) {
                clearInterval(this.radioPollInterval);
                this.radioPollInterval = null;
            }
        },

        togglePlay() {
            if (!this.audioElement || !this.currentTrack) return;

            this.ensureAudioContext();

            if (this.isPlaying) {
                this.audioElement.pause();
                this.isPlaying = false;
                this.stopVisualizer();
                this.stopRadioMetadataPolling();
            } else {
                this.audioElement.play()
                    .then(() => {
                        this.isPlaying = true;
                        this.startVisualizer();
                        if (this.currentTrack && this.currentTrack.is_radio) {
                            this.startRadioMetadataPolling();
                        }
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
            this.updateCurrentLyricIndex(targetTime);
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

        toggleLoop() {
            if (this.loopMode === 'none') {
                this.loopMode = 'all';
            } else if (this.loopMode === 'all') {
                this.loopMode = 'one';
            } else {
                this.loopMode = 'none';
            }
        },

        toggleShuffle() {
            this.isShuffle = !this.isShuffle;
            if (this.isShuffle) {
                this.shuffleQueue();
            }
        },

        // ==================== ОЧЕРЕДЬ (TAPE RACK) ====================

        removeFromQueue(index) {
            if (index < 0 || index >= this.playlist.length) return;
            if (index === this.currentIndex) {
                if (this.playlist.length > 1) {
                    const nextIdx = (this.currentIndex + 1) % this.playlist.length;
                    const nextTrack = this.playlist[nextIdx];
                    this.playlist.splice(index, 1);
                    this.currentIndex = this.playlist.findIndex(t => t.path === nextTrack.path);
                    this.playTrack(nextTrack, this.playlist);
                } else {
                    this.close();
                }
            } else {
                this.playlist.splice(index, 1);
                if (this.currentTrack) {
                    this.currentIndex = this.playlist.findIndex(t => t.path === this.currentTrack.path);
                }
            }
        },

        moveInQueue(fromIdx, toIdx) {
            if (fromIdx < 0 || fromIdx >= this.playlist.length || toIdx < 0 || toIdx >= this.playlist.length) return;
            const item = this.playlist.splice(fromIdx, 1)[0];
            this.playlist.splice(toIdx, 0, item);
            if (this.currentTrack) {
                this.currentIndex = this.playlist.findIndex(t => t.path === this.currentTrack.path);
            }
        },

        clearQueue() {
            if (this.currentTrack) {
                this.playlist = [this.currentTrack];
                this.currentIndex = 0;
            } else {
                this.playlist = [];
                this.currentIndex = -1;
            }
        },

        shuffleQueue() {
            if (this.playlist.length <= 2) return;
            const current = this.currentTrack;
            const others = this.playlist.filter((_, i) => i !== this.currentIndex);
            for (let i = others.length - 1; i > 0; i--) {
                const j = Math.floor(Math.random() * (i + 1));
                [others[i], others[j]] = [others[j], others[i]];
            }
            this.playlist = current ? [current, ...others] : others;
            this.currentIndex = current ? 0 : -1;
        },

        get queueTotalDuration() {
            if (!this.playlist || this.playlist.length === 0) return 0;
            return this.playlist.reduce((acc, t) => acc + (t.duration || 0), 0);
        },

        // ==================== WEB AUDIO API СПЕКТРОАНАЛИЗАТОР ====================

        startVisualizer() {
            if (this.visualizerRunning) return;
            this.visualizerRunning = true;
            this.renderVisualizer();
        },

        stopVisualizer() {
            this.visualizerRunning = false;
            if (this.animationFrameId) {
                cancelAnimationFrame(this.animationFrameId);
                this.animationFrameId = null;
            }
            const barL = document.getElementById('vu-meter-bar-l');
            const barR = document.getElementById('vu-meter-bar-r');
            const valL = document.getElementById('vu-meter-val-l');
            const valR = document.getElementById('vu-meter-val-r');
            if (barL) barL.style.width = '0%';
            if (barR) barR.style.width = '0%';
            if (valL) valL.textContent = '0%';
            if (valR) valR.textContent = '0%';
        },

        renderVisualizer() {
            if (!this.visualizerRunning) return;

            // Если вкладка Плеер не активна или режим не 'spectrum' — усыпляем rAF-луп
            const app = Alpine.store('app');
            const isPlayerTab = app && app.activeTab === 'player';

            if (!isPlayerTab || this.centerDeckMode !== 'spectrum') {
                this.visualizerRunning = false;
                this.animationFrameId = null;
                return;
            }

            if (!this.canvasEl || !this.canvasCtx) {
                this.animationFrameId = requestAnimationFrame(() => this.renderVisualizer());
                return;
            }

            const ctx = this.canvasCtx;
            const width = this.canvasEl.width;
            const height = this.canvasEl.height;

            if (width === 0 || height === 0) {
                this.resizeCanvas();
                this.animationFrameId = requestAnimationFrame(() => this.renderVisualizer());
                return;
            }

            // Очистка CRT экрана (матовый угольно-черный)
            ctx.fillStyle = '#080a0d';
            ctx.fillRect(0, 0, width, height);

            // Отрисовка тонкой измерительной сетки (децибельные уровни)
            ctx.strokeStyle = 'rgba(255, 255, 255, 0.04)';
            ctx.lineWidth = 1;
            const gridLines = 5;
            for (let i = 1; i <= gridLines; i++) {
                const y = Math.floor((height / (gridLines + 1)) * i);
                ctx.beginPath();
                ctx.moveTo(0, y);
                ctx.lineTo(width, y);
                ctx.stroke();
            }

            const binCount = 32;
            const freqData = new Uint8Array(binCount);

            if (this.analyserNode && this.isPlaying) {
                this.analyserNode.getByteFrequencyData(freqData);
            }

            const paddingX = 16 * (window.devicePixelRatio || 1);
            const usableWidth = width - paddingX * 2;
            const gap = 3 * (window.devicePixelRatio || 1);
            const barWidth = Math.max(2, (usableWidth - gap * (binCount - 1)) / binCount);

            const gravity = 0.5 * (window.devicePixelRatio || 1);
            let sumL = 0;
            let sumR = 0;

            for (let i = 0; i < binCount; i++) {
                const val = freqData[i] || 0;
                const normalized = val / 255;
                const barHeight = Math.max(2, normalized * (height - 30));

                const x = paddingX + i * (barWidth + gap);
                const y = height - 16 - barHeight;

                // Пик-холд с физикой гравитации
                if (barHeight >= this.peakValues[i]) {
                    this.peakValues[i] = barHeight;
                    this.peakVelocities[i] = 0;
                } else {
                    this.peakVelocities[i] += gravity * 0.15;
                    this.peakValues[i] = Math.max(2, this.peakValues[i] - this.peakVelocities[i]);
                }

                // Градиент полосы: Студийный электрический оранжевый -> Янтарный пик
                const grad = ctx.createLinearGradient(0, height - 16, 0, y);
                grad.addColorStop(0, '#c2410c');    // Deep warm orange
                grad.addColorStop(0.7, '#ff5500');  // SoundCloud electric orange
                grad.addColorStop(0.9, '#f59e0b');  // Amber warning
                grad.addColorStop(1, '#ef4444');    // Peak red

                ctx.fillStyle = grad;
                ctx.fillRect(x, y, barWidth, barHeight);

                // Отрисовка маркера Peak-Hold
                const peakY = height - 16 - this.peakValues[i];
                ctx.fillStyle = '#fbbf24'; // Warm amber peak dot
                ctx.fillRect(x, peakY, barWidth, 2 * (window.devicePixelRatio || 1));

                // Расчет средних для стерео VU-метра
                if (i < 16) {
                    sumL += val;
                } else {
                    sumR += val;
                }
            }

            // Обновление аналоговых VU пиков напрямую в DOM без реактивного шторма Alpine
            const pctL = Math.min(100, Math.round((sumL / (16 * 255)) * 100));
            const pctR = Math.min(100, Math.round((sumR / (16 * 255)) * 100));
            const barL = document.getElementById('vu-meter-bar-l');
            const barR = document.getElementById('vu-meter-bar-r');
            const valL = document.getElementById('vu-meter-val-l');
            const valR = document.getElementById('vu-meter-val-r');
            if (barL) barL.style.width = pctL + '%';
            if (barR) barR.style.width = pctR + '%';
            if (valL) valL.textContent = pctL + '%';
            if (valR) valR.textContent = pctR + '%';

            // Частотные подписи внизу
            ctx.fillStyle = '#52525b'; // Zinc 600
            ctx.font = `${Math.floor(9 * (window.devicePixelRatio || 1))}px monospace`;
            ctx.textAlign = 'center';
            const labels = ['32', '64', '125', '250', '500', '1k', '2k', '4k', '8k', '16k'];
            const step = Math.floor(binCount / labels.length);
            for (let j = 0; j < labels.length; j++) {
                const targetIdx = Math.min(binCount - 1, j * step);
                const lblX = paddingX + targetIdx * (barWidth + gap) + barWidth / 2;
                ctx.fillText(labels[j], lblX, height - 4);
            }

            this.animationFrameId = requestAnimationFrame(() => this.renderVisualizer());
        },

        // ==================== КАРАОКЕ LRC ====================

        async loadLyrics(track) {
            const trackPath = track ? (track.path || track.file_path) : null;
            if (!track || !trackPath) {
                this.lyricsLines = [];
                this.lyricsKind = 'none';
                return;
            }
            this.lyricsLoading = true;
            this.lyricsError = null;
            this.lyricsLines = [];
            this.currentLyricIndex = -1;

            try {
                const res = await fetch(`/lyrics?path=${encodeURIComponent(trackPath)}`);
                if (!res.ok) throw new Error('Lyrics fetch failed');
                const data = await res.json();
                this.lyricsKind = data.kind || 'none';
                const text = data.content || data.lyrics || '';
                this.rawLyrics = text;

                if (data.kind === 'lrc' || data.synced) {
                    this.lyricsLines = this.parseLRC(text);
                } else if (text.trim()) {
                    // Plain text: разбиваем по строкам без таймкодов
                    this.lyricsLines = text.split(/\r?\n/).map(line => ({
                        time: null,
                        text: line.trim()
                    })).filter(item => item.text.length > 0);
                }
            } catch (e) {
                console.warn('Lyrics load error:', e);
                this.lyricsError = 'Не удалось загрузить текст песни';
            } finally {
                this.lyricsLoading = false;
            }
        },

        parseLRC(text) {
            if (!text) return [];
            const lines = [];
            const rawLines = text.split(/\r?\n/);
            const timeRegex = /\[(\d{2}):(\d{2}(?:\.\d{2,3})?)\]/g;

            for (const line of rawLines) {
                const trimmed = line.trim();
                if (!trimmed) continue;
                if (/^\[[a-zA-Z]+:/.test(trimmed)) continue; // Метаданные тегов [ar:], [ti:]

                const matches = [...trimmed.matchAll(timeRegex)];
                if (matches.length > 0) {
                    const lyricText = trimmed.replace(timeRegex, '').trim();
                    for (const match of matches) {
                        const min = parseInt(match[1], 10);
                        const sec = parseFloat(match[2]);
                        const totalSec = min * 60 + sec;
                        lines.push({
                            time: totalSec,
                            text: lyricText || '♪'
                        });
                    }
                }
            }
            lines.sort((a, b) => a.time - b.time);
            return lines;
        },

        updateCurrentLyricIndex(curTime) {
            if (!this.lyricsLines || this.lyricsLines.length === 0 || this.lyricsKind !== 'lrc') return;
            let idx = -1;
            for (let i = 0; i < this.lyricsLines.length; i++) {
                if (curTime >= this.lyricsLines[i].time) {
                    idx = i;
                } else {
                    break;
                }
            }

            if (idx !== this.currentLyricIndex) {
                this.currentLyricIndex = idx;
                if (this.autoScrollLyrics && idx >= 0) {
                    const el = document.getElementById(`lyric-line-${idx}`);
                    if (el) {
                        el.scrollIntoView({ behavior: 'smooth', block: 'center' });
                    }
                }
            }
        },

        // ==================== АУДИОФИЛЬСКИЙ ПАСПОРТ ====================

        async fetchTrackSpecs(track) {
            if (!track) return;
            try {
                const id = track.id || track.path;
                const res = await fetch(`/api/tracks/${encodeURIComponent(id)}/metadata`);
                if (res.ok) {
                    const data = await res.json();
                    this.techSpecs = {
                        bitrate: data.bitrate_kbps || 320,
                        sampleRate: data.sample_rate || 44100,
                        fileSize: data.file_size || track.file_size || null,
                        trackHash: data.id ? data.id.substring(0, 16) : (track.id ? track.id.substring(0, 16) : '—'),
                        codec: (data.codec || 'mp3').toUpperCase() + ' CBR',
                        trackNumber: data.track_number || null,
                        year: data.year || null,
                        genre: data.genre || '—'
                    };
                }
            } catch (e) {
                console.warn('Metadata fetch error:', e);
            }
        },

        get bufferProgressPercent() {
            if (!this.audioElement || !this.duration || this.duration === 0) return 0;
            try {
                const buffered = this.audioElement.buffered;
                if (buffered.length > 0) {
                    const loaded = buffered.end(buffered.length - 1);
                    return Math.min(100, Math.round((loaded / this.duration) * 100));
                }
            } catch (e) {
                // Ignore buffer errors
            }
            return 0;
        },

        // ==================== ОБРАБОТЧИКИ СОБЫТИЙ AUDIO ====================

        onTimeUpdate() {
            if (this.audioElement) {
                this.currentTime = this.audioElement.currentTime;
                if (!this.duration && this.audioElement.duration && !isNaN(this.audioElement.duration)) {
                    this.duration = this.audioElement.duration;
                }
                this.updateCurrentLyricIndex(this.currentTime);
            }
        },

        onDurationChange() {
            if (this.audioElement && this.audioElement.duration && !isNaN(this.audioElement.duration)) {
                this.duration = this.audioElement.duration;
            }
        },

        onEnded() {
            if (this.loopMode === 'one') {
                this.seek(0);
                this.audioElement.play();
                return;
            }
            if (this.playlist && this.playlist.length > 0) {
                if (this.currentIndex < this.playlist.length - 1) {
                    this.next();
                } else if (this.loopMode === 'all') {
                    this.next();
                } else {
                    this.isPlaying = false;
                    this.stopVisualizer();
                }
            } else {
                this.isPlaying = false;
                this.stopVisualizer();
            }
        },

        onError(e) {
            this.isLoading = false;
            this.isPlaying = false;
            this.stopVisualizer();
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
            this.stopVisualizer();
            this.currentTrack = null;
            this.isVisible = false;
        },

        // ==================== НАВИГАЦИЯ И ВКЛАДКИ ====================

        openPlayerTab() {
            if (Alpine.store('app')) {
                Alpine.store('app').activeTab = 'player';
            }
            this.onTabOpened();
        },

        onTabOpened() {
            this.ensureAudioContext();
            if (this.currentTrack && (!this.lyricsLines || this.lyricsLines.length === 0)) {
                this.loadLyrics(this.currentTrack);
                this.fetchTrackSpecs(this.currentTrack);
            }
            setTimeout(() => {
                this.resizeCanvas();
                if (this.isPlaying && this.centerDeckMode === 'spectrum') {
                    this.startVisualizer();
                }
            }, 50);
        },

        setCenterDeckMode(mode) {
            this.centerDeckMode = mode;
            if (mode === 'spectrum') {
                setTimeout(() => {
                    this.resizeCanvas();
                    if (this.isPlaying) {
                        this.startVisualizer();
                    }
                }, 30);
            } else {
                this.stopVisualizer();
            }
        },

        // ==================== УТИЛИТЫ ФОРМАТИРОВАНИЯ ====================

        formatDuration(sec) {
            if (window.AppFormatters && window.AppFormatters.formatDuration) {
                return window.AppFormatters.formatDuration(sec);
            }
            if (!sec || isNaN(sec)) return '0:00';
            const m = Math.floor(sec / 60);
            const s = Math.floor(sec % 60);
            return `${m}:${s < 10 ? '0' : ''}${s}`;
        },

        formatPreciseTime(sec) {
            if (!sec || isNaN(sec)) return '00:00.00';
            const m = Math.floor(sec / 60);
            const s = Math.floor(sec % 60);
            const ms = Math.floor((sec % 1) * 100);
            const mm = m < 10 ? '0' + m : m;
            const ss = s < 10 ? '0' + s : s;
            const mss = ms < 10 ? '0' + ms : ms;
            return `${mm}:${ss}.${mss}`;
        },

        formatBytes(bytes) {
            if (!bytes || isNaN(bytes)) return '—';
            const mb = bytes / (1024 * 1024);
            return `${mb.toFixed(1)} MB`;
        }
    });
});
