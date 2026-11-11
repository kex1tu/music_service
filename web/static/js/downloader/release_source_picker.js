/**
 * Release Source Picker Controller (Alpine.js Store).
 * Интерактивный модальный контроллер ручного выбора цельного релиза (альбома/сета)
 * с детекцией цензуры RU vs Foreign и таблицей сопоставления «бок о бок».
 */
document.addEventListener('alpine:init', () => {
    Alpine.store('releaseSourcePicker', {
        isOpen: false,
        isLoading: false,
        isParsingUrl: false,
        isApplying: false,
        activeTab: 'candidates', // 'candidates' | 'direct'

        jobId: null,
        targetPath: null,
        artist: '',
        album: '',
        isCyrillic: false,

        targetTracks: [],
        candidates: [],
        selectedCandidateIndex: -1,
        versionPolicy: "release",
        searchSerial: 0,
        soundCloudLimit: 30,
        hasMoreSoundCloud: false,
        albumOffset: 0,
        playlistOffset: 0,
        albumToken: '',
        playlistToken: '',
        manualOverrides: {}, // targetIdx -> candTrack object
        activeRemapTargetIdx: null,

        directUrl: '',
        directError: '',
        errorMessage: '',

        openForJob(job) {
            if (!job) return;
            this.reset();
            this.jobId = job.id;
            this.artist = job.artist_name || '';
            this.album = job.album_title || '';
            this.targetTracks = job.tracks ? JSON.parse(JSON.stringify(job.tracks)) : [];
            if (this.targetTracks.length && this.targetTracks.every(t => t.version_policy === this.targetTracks[0].version_policy)) this.versionPolicy = this.targetTracks[0].version_policy || 'release';
            this.isOpen = true;
            this.loadCandidates();
        },

        openForMissingAlbum(album) {
            if (!album) return;
            this.reset();
            this.jobId = null;
            this.targetPath = album.path || null;
            this.artist = album.artist || '';
            this.album = album.album || '';
            this.isOpen = true;
            this.loadCandidates();
        },

        openForLibraryDirectory(path, artist, album) {
            this.reset();
            this.jobId = null;
            this.targetPath = path || null;
            this.artist = artist || '';
            this.album = album || '';
            this.isOpen = true;
            this.loadCandidates();
        },

        close() {
            this.isOpen = false;
            this.reset();
        },

        reset() {
            this.soundCloudLimit = 30;
            this.hasMoreSoundCloud = false;
            this.albumOffset = 0;
            this.playlistOffset = 0;
            this.albumToken = '';
            this.playlistToken = '';
            this.searchSerial++;
            this.versionPolicy = 'release';
            this.isLoading = false;
            this.isParsingUrl = false;
            this.isApplying = false;
            this.activeTab = 'candidates';
            this.jobId = null;
            this.targetPath = null;
            this.artist = '';
            this.album = '';
            this.isCyrillic = false;
            this.targetTracks = [];
            this.candidates = [];
            this.selectedCandidateIndex = -1;
            this.manualOverrides = {};
            this.activeRemapTargetIdx = null;
            this.directUrl = '';
            this.directError = '';
            this.errorMessage = '';
        },

        candidateGroups() {
            return [
                { type: 'album', label: 'Альбомы', items: [] },
                { type: 'playlist', label: 'Плейлисты', items: [] },
            ].map(group => ({
                ...group,
                items: this.candidates.map((candidate, index) => ({ candidate, index }))
                    .filter(entry => (entry.candidate.result_type || (entry.candidate.source === 'soundcloud_set' ? 'playlist' : 'album')) === group.type),
            })).filter(group => group.items.length > 0);
        },

        loadMoreCandidates() {
            if (this.isLoading || !this.hasMoreSoundCloud) return;
            this.loadCandidates(true);
        },

        async loadCandidates(expand = false) {
            const serial = ++this.searchSerial;
            const selectedURL = expand ? this.currentCandidate()?.url : null;
            if (!expand) {
                this.soundCloudLimit = 30;
                this.albumOffset = 0;
                this.playlistOffset = 0;
                this.albumToken = '';
                this.playlistToken = '';
            }
            this.isLoading = true;
            this.errorMessage = '';
            if (!expand) {
                this.candidates = [];
                this.selectedCandidateIndex = -1;
                this.manualOverrides = {};
            }

            try {
                const params = new URLSearchParams();
                if (this.artist) params.append('artist', this.artist);
                if (this.album) params.append('album', this.album);
                params.append("version_policy", this.versionPolicy);
                params.append('soundcloud_limit', '30');
                if (expand) {
                    if (this.albumOffset > 0) params.append('album_offset', String(this.albumOffset));
                    if (this.playlistOffset > 0) params.append('playlist_offset', String(this.playlistOffset));
                    if (this.albumToken) params.append('album_token', this.albumToken);
                    if (this.playlistToken) params.append('playlist_token', this.playlistToken);
                }
                if (this.jobId) params.append('job_id', this.jobId);
                if (this.targetPath) params.append('path', this.targetPath);

                const res = await window.api.raw(`/api/downloads/release-candidates?${params.toString()}`);
                if (!res.ok) {
                    const errData = await res.json().catch(() => ({}));
                    throw new Error(errData.error || `HTTP ${res.status}: Ошибка поиска релизов`);
                }

                const data = await res.json();
                if (serial !== this.searchSerial) return;
                this.artist = data.artist || this.artist;
                this.album = data.album || this.album;
                this.isCyrillic = Boolean(data.is_cyrillic);
                if (data.target_tracks && data.target_tracks.length > 0) {
                    this.targetTracks = data.target_tracks;
                }

                const newCandidates = data.candidates || [];
                if (expand) {
                    const existingUrls = new Set(this.candidates.map(candidate => candidate.url));
                    for (const cand of newCandidates) {
                        if (!existingUrls.has(cand.url)) {
                            this.candidates.push(cand);
                            existingUrls.add(cand.url);
                        }
                    }
                    if (selectedURL) {
                        this.selectedCandidateIndex = this.candidates.findIndex(candidate => candidate.url === selectedURL);
                    }
                } else {
                    this.candidates = newCandidates;
                }

                this.hasMoreSoundCloud = Boolean(data.has_more_soundcloud);
                this.albumOffset = data.album_offset || 0;
                this.playlistOffset = data.playlist_offset || 0;
                this.albumToken = data.album_token || '';
                this.playlistToken = data.playlist_token || '';

                if (this.candidates.length === 0) {
                    this.errorMessage = 'Кандидатов цельного релиза не найдено. Вы можете вставить прямую ссылку на сет или альбом.';
                }
            } catch (err) {
                if (serial !== this.searchSerial) return;
                console.error('[ReleaseSourcePicker] loadCandidates error:', err);
                this.errorMessage = err.message || 'Сбой загрузки кандидатов релиза';
            } finally {
                if (serial === this.searchSerial) this.isLoading = false;
            }
        },

        selectCandidate(idx) {
            if (idx >= 0 && idx < this.candidates.length) {
                this.selectedCandidateIndex = idx;
                this.manualOverrides = {};
                this.activeRemapTargetIdx = null;
            }
        },

        currentCandidate() {
            if (this.candidates && this.candidates.length > this.selectedCandidateIndex) {
                return this.candidates[this.selectedCandidateIndex];
            }
            return null;
        },

        getMappedCandidateTrack(targetIdx) {
            if (this.manualOverrides[targetIdx] !== undefined) {
                return this.manualOverrides[targetIdx];
            }
            const cand = this.currentCandidate();
            if (!cand || !cand.track_mapping || !cand.tracks) {
                return null;
            }
            const candURL = cand.track_mapping[targetIdx];
            if (!candURL) {
                return null;
            }
            // Ищем трек кандидата по DirectURL или VideoID
            return cand.tracks.find(t => (t.direct_url && t.direct_url === candURL) || (t.video_id && candURL.includes(t.video_id))) || null;
        },

        getTrackDelta(targetDur, candDur) {
            if (!targetDur || !candDur) {
                return { delta: 0, text: '—', tier: 'unknown' };
            }
            const diff = candDur - targetDur;
            const sign = diff > 0 ? `+${diff}с` : diff < 0 ? `${diff}с` : '0с';
            const abs = Math.abs(diff);
            if (abs <= 2) {
                return { delta: diff, text: sign, tier: 'exact' }; // Зеленый
            } else if (abs <= 12) {
                return { delta: diff, text: sign, tier: 'close' }; // Желтый
            }
            return { delta: diff, text: sign, tier: 'far' }; // Красный
        },

        toggleRemapSelector(targetIdx) {
            if (this.activeRemapTargetIdx === targetIdx) {
                this.activeRemapTargetIdx = null;
            } else {
                this.activeRemapTargetIdx = targetIdx;
            }
        },

        remapTrack(targetIdx, candTrack) {
            this.manualOverrides[targetIdx] = candTrack;
            this.activeRemapTargetIdx = null;
        },

        clearTrackRemap(targetIdx) {
            // Метка трека как намеренно исключенного из релиза (будет найден поштучно)
            this.manualOverrides[targetIdx] = null;
            this.activeRemapTargetIdx = null;
        },

        resetTrackRemap(targetIdx) {
            delete this.manualOverrides[targetIdx];
            this.activeRemapTargetIdx = null;
        },

        getMatchedCount() {
            let count = 0;
            for (let i = 0; i < this.targetTracks.length; i++) {
                if (this.getMappedCandidateTrack(i)) {
                    count++;
                }
            }
            return count;
        },

        async parseDirectUrl() {
            const url = this.directUrl.trim();
            if (!url) {
                this.directError = 'Вставьте ссылку на сет SoundCloud или альбом YouTube Music';
                return;
            }

            this.isParsingUrl = true;
            this.directError = '';

            try {
                const res = await window.api.raw('/api/downloads/release-candidates/parse-url', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({
                        url: url,
                        artist: this.artist,
                        album: this.album,
                        target_tracks: this.targetTracks,
                    }),
                });

                if (!res.ok) {
                    const errData = await res.json().catch(() => ({}));
                    throw new Error(errData.error || `HTTP ${res.status}: Ошибка парсинга ссылки`);
                }

                const cand = await res.json();
                this.candidates.unshift(cand);
                this.selectedCandidateIndex = -1;
                this.manualOverrides = {};
                this.activeTab = 'candidates';
                this.directUrl = '';
            } catch (err) {
                console.error('[ReleaseSourcePicker] parseDirectUrl error:', err);
                this.directError = err.message || 'Не удалось разобрать ссылку';
            } finally {
                this.isParsingUrl = false;
            }
        },

        async applyReleaseSource() {
            const cand = this.currentCandidate();
            if (!cand && Object.keys(this.manualOverrides).length === 0) {
                alert('Выберите источник релиза перед применением');
                return;
            }

            this.isApplying = true;
            try {
                const trackMapping = {};
                for (let i = 0; i < this.targetTracks.length; i++) {
                    const mapped = this.getMappedCandidateTrack(i);
                    if (mapped) {
                        trackMapping[i] = mapped.direct_url || (mapped.video_id ? `https://music.youtube.com/watch?v=${mapped.video_id}` : '');
                    }
                }

                const payload = {
                    version_policy: this.versionPolicy,
                    target_tracks: this.targetTracks,
                    job_id: this.jobId || '',
                    target_path: this.targetPath || '',
                    source_url: cand ? (cand.url || '') : '',
                    track_mapping: trackMapping,
                };

                const res = await window.api.raw('/api/downloads/releases/switch-source', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(payload),
                });

                if (!res.ok) {
                    const errData = await res.json().catch(() => ({}));
                    throw new Error(errData.error || `HTTP ${res.status}: Сбой применения источника релиза`);
                }

                const result = await res.json();
                if (window.Toasts && typeof window.Toasts.success === 'function') {
                    window.Toasts.success(`Источник релиза применен! Обновлено треков: ${result.updated_count}/${result.total_tracks}`);
                }

                const qm = Alpine.store('queueManager');
                if (qm && typeof qm.fetchSummary === 'function') {
                    qm.fetchSummary();
                }

                this.close();
            } catch (err) {
                console.error('[ReleaseSourcePicker] applyReleaseSource error:', err);
                alert('Ошибка: ' + (err.message || 'Не удалось применить источник релиза'));
            } finally {
                this.isApplying = false;
            }
        },

        openSoundCloudSearch() {
            const initialQuery = `${this.artist || ''} ${this.album || ''}`.trim();
            const picker = Alpine.store('soundCloudPicker');
            if (picker) {
                picker.openForRelease({
                    initialQuery: initialQuery,
                    artist: this.artist,
                    album: this.album,
                    targetTracks: this.targetTracks,
                    onSelect: (cand) => {
                        this.applyCustomCandidate(cand);
                    },
                });
            }
        },

        applyCustomCandidate(cand) {
            if (!cand) return;
            const existingIdx = this.candidates.findIndex(c => c.url === cand.url);
            if (existingIdx >= 0) {
                this.candidates[existingIdx] = cand;
                this.selectedCandidateIndex = existingIdx;
            } else {
                this.candidates.unshift(cand);
                this.selectedCandidateIndex = 0;
            }
            this.manualOverrides = {};
            this.activeRemapTargetIdx = null;
            this.activeTab = 'candidates';
        },
    });
});
