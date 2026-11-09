/**
 * Track Source Picker Controller (Alpine.js Store).
 * Интерактивный модальный контроллер ручного выбора и привязки источника трека.
 */
document.addEventListener('alpine:init', () => {
    Alpine.store('sourcePicker', {
        isOpen: false,
        activeTab: 'search', // 'direct' | 'search'

        // Контекст открытого трека
        context: {
            jobId: null,
            trackId: null,
            trackNumber: 1,
            title: '',
            artist: '',
            album: '',
            expectedDuration: 0,
            albumPath: null,
            mode: 'queue', // 'queue' | 'missing'
        },

        // Вкладка 1: Прямая ссылка
        directUrl: '',

        // Вкладка 2: Мульти-поиск кандидатов
        searchQuery: '',
        versionPolicy: "release",
        searchSerial: 0,
        explain(e) {
            const names = { title_mismatch: 'Другое название', artist_mismatch: 'Другой исполнитель', version_mismatch: 'Другая версия записи', duration_mismatch: 'Существенное отличие длительности', short_title_mismatch: 'Короткое название не совпало', remix_identity_unknown: 'Не подтверждён тот же ремикс', version_detail_mismatch: 'Другая редакция версии' };
            return (e?.conflicts || []).map(v => names[v] || v).join(' · ');
        },
        isLoadingCandidates: false,
        candidates: [],
        candidatesError: null,
        previewingUrl: null,

        // Состояние отправки / инлайн-прогресс
        isSubmitting: false,
        submitStep: '',
        submitError: null,
        submitSuccess: false,

        openForQueueTrack(job, track) {
            this.reset();
            this.versionPolicy = track.version_policy || 'release';
            this.context = {
                jobId: job ? job.id : null,
                trackId: track.track_id || track.id || '',
                isrc: track.isrc || '',
                trackNumber: track.track_number || 1,
                title: track.title || '',
                artist: track.artist || (job ? job.artist_name : '') || '',
                album: track.album || (job ? job.album_title : '') || '',
                expectedDuration: track.duration || 0,
                albumPath: job ? job.target_album_path : null,
                mode: 'queue',
            };
            this.directUrl = '';
            this.searchQuery = `${this.context.artist} - ${this.context.title}`.trim();
            if (this.searchQuery === '-') this.searchQuery = '';
            this.isOpen = true;
            this.activeTab = 'search';
            this.searchCandidates();
        },

        openForMissingTrack(album, track) {
            this.reset();
            const art = track.artist || album.artist || '';
            const tit = track.title || `Трек #${track.track_number}`;
            this.context = {
                jobId: null,
                trackId: track.track_number ? `missing_${track.track_number}` : '',
                isrc: track.isrc || '',
                trackNumber: track.track_number || 1,
                title: tit,
                artist: art,
                album: album.album || '',
                expectedDuration: track.duration || 0,
                albumPath: album.path || null,
                mode: 'missing',
            };
            this.directUrl = '';
            this.searchQuery = `${art} - ${tit}`.trim();
            if (this.searchQuery === '-') this.searchQuery = '';
            this.isOpen = true;
            this.activeTab = 'search';
            this.searchCandidates();
        },

        close() {
            this.stopPreview();
            this.isOpen = false;
            this.reset();
        },

        reset() {
            this.searchSerial++;
            this.versionPolicy = 'release';
            this.directUrl = '';
            this.searchQuery = '';
            this.isLoadingCandidates = false;
            this.candidates = [];
            this.candidatesError = null;
            this.previewingUrl = null;
            this.isSubmitting = false;
            this.submitStep = '';
            this.submitError = null;
            this.submitSuccess = false;
        },

        async searchCandidates() {
            const serial=++this.searchSerial;
            const q = this.searchQuery.trim();
            if (!q && !this.context.title) return;

            this.isLoadingCandidates = true;
            this.candidatesError = null;
            this.candidates = [];
            this.stopPreview();

            try {
                const params = new URLSearchParams({
                    artist: this.context.artist || '',
                    title: this.context.title || '',
                    duration: String(this.context.expectedDuration || 0),
                    version_policy: this.versionPolicy,
                    isrc: this.context.isrc || '',
                    q: q,
                });

                const res = await window.api.raw(`/api/downloads/candidates?${params.toString()}`);
                if (!res.ok) {
                    const errData = await res.json().catch(() => ({}));
                    throw new Error(errData.error || `HTTP ${res.status}: Ошибка поиска кандидатов`);
                }

                const data = await res.json();
                if (serial !== this.searchSerial) return;
                this.candidates = data.candidates || [];
                if (this.candidates.length === 0) {
                    this.candidatesError = 'По запросу кандидатов не найдено. Попробуйте уточнить поисковый запрос.';
                }
            } catch (err) {
                console.error('[SourcePicker] Ошибка поиска:', err);
                this.candidatesError = err.message || 'Сбой выполнения мульти-поиска';
            } finally {
                if (serial === this.searchSerial) this.isLoadingCandidates = false;
            }
        },

        togglePreview(candidate) {
            if (!candidate || !candidate.url) return;

            const player = Alpine.store('player');
            if (this.previewingUrl === candidate.url) {
                if (player) player.togglePlay();
                this.previewingUrl = null;
                return;
            }

            this.previewingUrl = candidate.url;
            const streamUrl = `/api/downloads/preview?url=${encodeURIComponent(candidate.url)}`;
            if (player) {
                player.playPreviewStream(streamUrl, candidate.title, candidate.uploader || candidate.source);
            }
        },

        stopPreview() {
            if (this.previewingUrl) {
                const player = Alpine.store('player');
                if (player && player.isPlaying) {
                    player.togglePlay();
                }
                this.previewingUrl = null;
            }
        },

        async bindDirectUrl() {
            const url = this.directUrl.trim();
            if (!url) {
                this.submitError = 'Вставьте ссылку на трек';
                return;
            }
            await this.executeBind(url);
        },

        async selectCandidate(candidate) {
            if (!candidate || !candidate.url) return;
            await this.executeBind(candidate.url);
        },

        async executeBind(targetUrl) {
            this.isSubmitting = true;
            this.submitStep = 'Подключение к источнику...';
            this.submitError = null;
            this.submitSuccess = false;
            this.stopPreview();

            try {
                if (this.context.mode === 'missing') {
                    // Режим дозагрузки отсутствующего трека в альбом
                    this.submitStep = 'Запуск дозагрузки трека...';
                    const payload = {
                        path: this.context.albumPath,
                        album_title: this.context.album,
                        artist_name: this.context.artist,
                        track_title: this.context.title,
                        track_number: this.context.trackNumber,
                        target_url: targetUrl,
                        version_policy: this.versionPolicy,
                        duration: this.context.expectedDuration,
                    };

                    const res = await window.api.raw('/api/downloads/backfill/bind-source', {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify(payload),
                    });

                    if (!res.ok) {
                        const errData = await res.json().catch(() => ({}));
                        throw new Error(errData.error || `HTTP ${res.status}: Сбой привязки источника`);
                    }

                    this.submitSuccess = true;
                    this.submitStep = 'Трек успешно привязан и отправлен в очередь!';
                } else {
                    // Режим трека из очереди
                    this.submitStep = 'Скачивание и накат ID3v2.4 тегов...';
                    const res = await window.api.raw(`/api/downloads/tracks/${encodeURIComponent(this.context.trackId)}/bind-source`, {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({ target_url: targetUrl, version_policy: this.versionPolicy }),
                    });

                    if (!res.ok) {
                        const errData = await res.json().catch(() => ({}));
                        throw new Error(errData.error || `HTTP ${res.status}: Сбой привязки источника`);
                    }

                    this.submitSuccess = true;
                    this.submitStep = 'Трек успешно скачан и обновлен!';
                }

                // Обновляем очередь и аудит
                if (Alpine.store('app')?.queue) {
                    Alpine.store('app').queue.fetchQueue();
                }

                // Плавное закрытие через 700мс
                setTimeout(() => {
                    this.close();
                    if (Alpine.store('app')) {
                        Alpine.store('app').toast('Источник успешно привязан!', 'success');
                    }
                }, 750);

            } catch (err) {
                console.error('[SourcePicker] Ошибка bind:', err);
                this.submitError = err.message || 'Не удалось привязать источник трека';
            } finally {
                if (!this.submitSuccess) {
                    this.isSubmitting = false;
                }
            }
        },

        formatDelta(delta) {
            if (delta === 0) return '0с';
            return delta > 0 ? `+${delta}с` : `${delta}с`;
        },

        getSourceBadge(source) {
            switch (source) {
                case 'ytmusic':
                    return {
                        label: 'YT Music',
                        iconClass: 'text-rose-400',
                        bgClass: 'bg-rose-500/15 text-rose-300 border-rose-500/30',
                    };
                case 'soundcloud':
                    return {
                        label: 'SoundCloud',
                        iconClass: 'text-amber-400',
                        bgClass: 'bg-amber-500/15 text-amber-300 border-amber-500/30',
                    };
                case 'bandcamp':
                    return {
                        label: 'Bandcamp',
                        iconClass: 'text-teal-400',
                        bgClass: 'bg-teal-500/15 text-teal-300 border-teal-500/30',
                    };
                default:
                    return {
                        label: 'YouTube',
                        iconClass: 'text-red-400',
                        bgClass: 'bg-red-500/15 text-red-300 border-red-500/30',
                    };
            }
        },
    });
});
