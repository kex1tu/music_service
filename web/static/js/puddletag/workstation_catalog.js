const puddletagWorkstationCatalog = {
    // ==================== МУЛЬТИ-ИСТОЧНИКОВЫЙ ПОИСК РЕЛИЗОВ (DEEZER, MUSICBRAINZ, SHAZAM) ====================

    openSourceModal() {
        if (this.tracks.length === 0) {
            Alpine.store('app').toast('Таблица пуста. Загрузите или выберите файлы', 'warning');
            return;
        }

        // Определяем кандидатов на поисковый запрос из выделенных или первых треков
        let candidateArtist = '';
        let candidateAlbum = '';
        let candidateTitle = '';

        const targetTrack = (this.selectedIndices.length > 0)
            ? this.tracks[this.selectedIndices[0]]
            : this.tracks[0];

        if (targetTrack) {
            candidateArtist = (targetTrack.artist || targetTrack.album_artist || '').trim();
            candidateAlbum = (targetTrack.album || '').trim();
            candidateTitle = (targetTrack.title || '').trim();
        }

        if (candidateArtist && candidateAlbum) {
            this.sourceSearchQuery = `${candidateArtist} - ${candidateAlbum}`;
        } else if (candidateArtist && candidateTitle) {
            this.sourceSearchQuery = `${candidateArtist} - ${candidateTitle}`;
        } else if (candidateAlbum) {
            this.sourceSearchQuery = candidateAlbum;
        } else if (candidateTitle) {
            this.sourceSearchQuery = candidateTitle;
        } else {
            this.sourceSearchQuery = '';
        }

        this.selectedReleaseSummary = null;
        this.selectedReleaseDetails = null;
        this.matchedPairs = [];
        this.targetTrackOptions = [];
        this.showSourceModal = true;
        this.checkMusicBrainzOAuth();

        // Если сформирован поисковый запрос, сразу запускаем поиск
        if (this.sourceSearchQuery) {
            this.searchSources();
        }
    },

    async searchSources() {
        if (!window.PuddletagSources) return;
        const q = this.sourceSearchQuery.trim();
        if (!q) {
            Alpine.store('app').toast('Введите поисковый запрос', 'warning');
            return;
        }

        this.isSearchingSources = true;
        this.selectedReleaseSummary = null;
        this.selectedReleaseDetails = null;
        this.matchedPairs = [];
        this.targetTrackOptions = [];

        try {
            // Извлекаем относительный путь первого трека, если доступен
            let audioPath = '';
            const firstTr = (this.selectedIndices.length > 0) ? this.tracks[this.selectedIndices[0]] : this.tracks[0];
            if (firstTr) {
                audioPath = firstTr.rel_path || firstTr.path || firstTr.filename || '';
            }

            const results = await window.PuddletagSources.searchAllSources(q, audioPath);
            this.sourceResults = results;

            // Автоматическое переключение вкладки на первую с результатами
            const currentCount = (this.sourceResults[this.activeSourceTab] && this.sourceResults[this.activeSourceTab].count) || 0;
            if (currentCount === 0) {
                if (this.sourceResults.deezer && this.sourceResults.deezer.count > 0) {
                    this.activeSourceTab = 'deezer';
                } else if (this.sourceResults.musicbrainz && this.sourceResults.musicbrainz.count > 0) {
                    this.activeSourceTab = 'musicbrainz';
                } else if (this.sourceResults.shazam && this.sourceResults.shazam.count > 0) {
                    this.activeSourceTab = 'shazam';
                }
            }

            // Авто-выбор первой найденной карточки релиза
            const items = this.currentTabReleases;
            if (items && items.length > 0) {
                this.selectRelease(items[0]);
            }
        } catch (err) {
            Alpine.store('app').toast(err.message || 'Ошибка поиска в базах тегов', 'error');
        } finally {
            this.isSearchingSources = false;
        }
    },

    async recognizeSelectedAudio() {
        if (!window.PuddletagSources) return;
        const firstTr = (this.selectedIndices.length > 0) ? this.tracks[this.selectedIndices[0]] : this.tracks[0];
        if (!firstTr) {
            Alpine.store('app').toast('Нет аудиофайлов для распознавания', 'warning');
            return;
        }

        const audioPath = firstTr.rel_path || firstTr.path || firstTr.filename || '';
        if (!audioPath) {
            Alpine.store('app').toast('Путь к аудиофайлу недоступен', 'warning');
            return;
        }

        this.isSearchingSources = true;
        this.activeSourceTab = 'shazam';
        this.selectedReleaseSummary = null;
        this.selectedReleaseDetails = null;
        this.matchedPairs = [];

        try {
            const results = await window.PuddletagSources.searchAllSources('', audioPath);
            this.sourceResults = results;

            const items = this.currentTabReleases;
            if (items && items.length > 0) {
                this.selectRelease(items[0]);
                Alpine.store('app').toast('Трек успешно распознан Shazam!', 'success');
            } else {
                Alpine.store('app').toast('Shazam не смог распознать данный аудиофрагмент', 'info');
            }
        } catch (err) {
            Alpine.store('app').toast(err.message || 'Ошибка распознавания Shazam', 'error');
        } finally {
            this.isSearchingSources = false;
        }
    },

    async selectRelease(rel) {
        if (!rel || !window.PuddletagSources) return;
        this.selectedReleaseSummary = rel;
        this.isLoadingReleaseDetails = true;
        this.selectedReleaseDetails = null;
        this.matchedPairs = [];

        try {
            let audioPath = '';
            const firstTr = (this.selectedIndices.length > 0) ? this.tracks[this.selectedIndices[0]] : this.tracks[0];
            if (firstTr) {
                audioPath = firstTr.rel_path || firstTr.path || firstTr.filename || '';
            }

            const details = await window.PuddletagSources.fetchReleaseDetails(rel.source, rel.id, audioPath);
            this.selectedReleaseDetails = details;

            // Формируем список целевых файлов из выделенных или всех строк в таблице
            const targetIndices = (this.selectedIndices.length > 0)
                ? this.selectedIndices
                : this.tracks.map((_, i) => i);

            const targetTracks = targetIndices.map(i => this.tracks[i]);

            this.targetTrackOptions = targetIndices.map(origIdx => {
                const tr = this.tracks[origIdx];
                const fname = tr.filename || (tr.path ? tr.path.split('/').pop() : '') || `Трек #${origIdx + 1}`;
                const label = tr.title
                    ? (tr.artist ? `${tr.artist} — ${tr.title}` : tr.title)
                    : fname;
                return {
                    originalIndex: origIdx,
                    label: label,
                    filename: fname
                };
            });

            this.matchedPairs = window.PuddletagSources.matchTracks(
                details.tracks,
                targetTracks,
                targetIndices
            );
        } catch (err) {
            Alpine.store('app').toast(err.message || 'Ошибка загрузки треклиста релиза', 'error');
        } finally {
            this.isLoadingReleaseDetails = false;
        }
    },

    onMatchedFileChange(pairIndex, newOrigIndexStr) {
        const pair = this.matchedPairs[pairIndex];
        if (!pair) return;

        if (newOrigIndexStr === '' || newOrigIndexStr === null || newOrigIndexStr === undefined) {
            pair.localTrack = null;
            pair.originalIndex = null;
            pair.isMatched = false;
            pair.isChecked = false;
            return;
        }

        const newOrigIdx = parseInt(newOrigIndexStr, 10);
        if (isNaN(newOrigIdx)) return;

        // Строго уникальное сопоставление (1-к-1):
        // Если выбранный локальный трек был привязан к другой строке, освобождаем её
        for (const otherPair of this.matchedPairs) {
            if (otherPair.index !== pairIndex && otherPair.originalIndex === newOrigIdx) {
                otherPair.localTrack = null;
                otherPair.originalIndex = null;
                otherPair.isMatched = false;
                otherPair.isChecked = false;
            }
        }

        const localTrk = this.tracks[newOrigIdx];
        pair.localTrack = localTrk || null;
        pair.originalIndex = localTrk ? newOrigIdx : null;
        pair.isMatched = !!localTrk;
        pair.isChecked = !!localTrk;
    },

    toggleAllMatched(checked) {
        for (const pair of this.matchedPairs) {
            if (pair.isMatched) {
                pair.isChecked = !!checked;
            }
        }
    },

    isAllMatchedChecked() {
        const matched = this.matchedPairs.filter(p => p.isMatched);
        if (matched.length === 0) return false;
        return matched.every(p => p.isChecked);
    },

    reMatchTracks() {
        if (!this.selectedReleaseDetails || !window.PuddletagSources) return;
        const targetIndices = (this.selectedIndices.length > 0)
            ? this.selectedIndices
            : this.tracks.map((_, i) => i);
        const targetTracks = targetIndices.map(i => this.tracks[i]);

        this.matchedPairs = window.PuddletagSources.matchTracks(
            this.selectedReleaseDetails.tracks,
            targetTracks,
            targetIndices
        );
        Alpine.store('app').toast('Авто-сопоставление треков выполнено заново', 'info');
    },

    applySourceTags() {
        if (!this.selectedReleaseDetails || !window.PuddletagSources) return;

        const modifiedIndices = window.PuddletagSources.applyTagsToTracks(
            this.tracks,
            this.matchedPairs,
            this.selectedReleaseDetails,
            this.sourceOptions
        );

        if (modifiedIndices.size === 0) {
            Alpine.store('app').toast('Нет сопоставленных треков для обновления', 'warning');
            return;
        }

        for (const idx of modifiedIndices) {
            this.dirtyIndices.add(idx);
        }

        this.syncTagPanel();
        this.showSourceModal = false;

        const srcName = (this.selectedReleaseDetails.source || 'каталога').toUpperCase();
        Alpine.store('app').toast(`Теги из базы ${srcName} успешно применены (${modifiedIndices.size} треков)! Нажмите Сохранить (Ctrl+S) для записи на диск.`, 'success');
    },

    async checkMusicBrainzOAuth() {
        if (!window.PuddletagSources) return;
        this.mbOAuth.isLoading = true;
        try {
            const st = await window.PuddletagSources.getMusicBrainzOAuthStatus();
            this.mbOAuth.configured = !!st.configured;
            this.mbOAuth.connected = !!st.connected;
            this.mbOAuth.username = st.username || '';
            this.mbOAuth.expires_at = st.expires_at || null;
        } finally {
            this.mbOAuth.isLoading = false;
        }
    },

    openMusicBrainzOAuth(event) {
        if (event) event.preventDefault();
        const width = 600;
        const height = 700;
        const left = Math.max(0, (window.innerWidth - width) / 2 + window.screenX);
        const top = Math.max(0, (window.innerHeight - height) / 2 + window.screenY);
        const popup = window.open(
            '/api/auth/musicbrainz/login',
            'MusicBrainzOAuth',
            `width=${width},height=${height},left=${left},top=${top},resizable=yes,scrollbars=yes,status=yes`
        );

        const onMessage = (e) => {
            if (e.data && e.data.type === 'mb_oauth_success') {
                window.removeEventListener('message', onMessage);
                this.mbOAuth.connected = true;
                this.mbOAuth.username = e.data.username || '';
                if (Alpine.store('app') && Alpine.store('app').toast) {
                    Alpine.store('app').toast('MusicBrainz успешно подключен! Лимиты повышены.', 'success');
                }
                this.checkMusicBrainzOAuth();
            }
        };
        window.addEventListener('message', onMessage);

        if (popup) {
            const timer = setInterval(() => {
                if (popup.closed) {
                    clearInterval(timer);
                    window.removeEventListener('message', onMessage);
                    this.checkMusicBrainzOAuth();
                }
            }, 1000);
        }
    },

    async disconnectMusicBrainzOAuth() {
        if (!confirm('Отключить аккаунт MusicBrainz? Будет возвращен стандартный rate limit (1 запрос/сек).')) {
            return;
        }
        if (!window.PuddletagSources) return;
        const ok = await window.PuddletagSources.disconnectMusicBrainzOAuth();
        if (ok) {
            this.mbOAuth.connected = false;
            this.mbOAuth.username = '';
            if (Alpine.store('app') && Alpine.store('app').toast) {
                Alpine.store('app').toast('MusicBrainz отключен', 'info');
            }
        } else {
            if (Alpine.store('app') && Alpine.store('app').toast) {
                Alpine.store('app').toast('Не удалось отключить MusicBrainz', 'error');
            }
        }
    },
};
