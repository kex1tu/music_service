const puddletagWorkstationNavigation = {
    /**
     * Загружает файлы из медиатеки с поддержкой рекурсивного сканирования.
     */
    async loadDirectory(path = '') {
        this.isLoading = true;
        this.editingCell = null;
        this.dirtyIndices.clear();
        this.selectedIndices = [];
        this.currentPage = 1;

        try {
            const url = `/files?path=${encodeURIComponent(path || '')}&recursive=false&_t=${Date.now()}`;
            const res = await window.api.raw(url);
            if (!res.ok) {
                const err = await res.json().catch(() => ({}));
                throw new Error(err.error || err.message || 'Ошибка загрузки каталога');
            }

            const data = await res.json();
            this.currentPath = data.current_path || '';
            this.parentPath = data.parent_path;
            this.breadcrumbs = data.breadcrumbs || [];

            // Преобразуем элементы в формат рабочей модели треков
            const rawItems = data.items || [];
            const audioTracks = [];
            const subDirs = [];

            for (const item of rawItems) {
                if (item.type === 'directory') {
                    subDirs.push(item);
                } else if (item.type === 'file' && (item.extension === '.mp3' || !item.extension)) {
                    audioTracks.push({
                        _trackIndex: audioTracks.length,
                        path: item.path,
                        name: item.name,
                        title: item.title || item.name.replace(/\.[^/.]+$/, ''),
                        artist: item.artist || '',
                        album: item.album || '',
                        album_artist: item.album_artist || '',
                        year: item.year || null,
                        genre: item.genre || '',
                        track_number: item.track_number || null,
                        track_total: item.track_total || null,
                        disc_number: item.disc_number || 1,
                        disc_total: item.disc_total || 1,
                        duration: item.duration || 0,
                        bitrate: item.bitrate || 320,
                        lyrics_status: item.lyrics_status || 'none',
                        has_cover: !!item.has_cover,
                        cover_preview_url: item.has_cover ? `/tags/cover?path=${encodeURIComponent(item.path)}` : null
                    });
                }
            }

            this.directories = subDirs;
            this.tracks = audioTracks;
            this.originalTracks = JSON.parse(JSON.stringify(audioTracks));
            this.syncTagPanel();
        } catch (err) {
            Alpine.store('app').toast(`Ошибка: ${err.message}`, 'error');
        } finally {
            this.isLoading = false;
        }
    },

    navigateUp() {
        if (this.parentPath !== null && this.parentPath !== undefined) {
            this.loadDirectory(this.parentPath);
        }
    },

    navigateTo(path) {
        this.loadDirectory(path);
    },

    setActiveReleaseTab(tab) {
        this.activeReleaseTab = tab;
        this.currentPage = 1;
        this.selectedIndices = [];
        this._selectedSet = new Set();
        if (tab === 'all') {
            this.syncTagPanel();
        } else {
            const rel = (this.releases || []).find(r => r.jobId === tab);
            if (rel) {
                this.tagPanel.album_artist = rel.album_artist || '';
                this.tagPanel.album = rel.album || '';
                this.tagPanel.year = rel.year || '';
                this.tagPanel.genre = rel.genre || '';
                this.tagPanel.cover_url = rel.coverUrl || null;
                this.tagPanel.has_cover = !!(rel.coverUrl || rel.coverPath);
            } else {
                this.syncTagPanel();
            }
        }
    },

    /**
     * Загружает треки свежескачанного релиза из очереди загрузчика.
     */
    loadJobData(payload) {
        if (!payload) return;
        this.isDownloaderMode = true;
        this.saveStatus = 'idle';
        this.jobIds = payload.jobIds || payload.job_ids || (payload.jobId ? [payload.jobId] : (payload.job_id ? [payload.job_id] : []));
        this.jobId = payload.jobId || payload.job_id || (this.jobIds && this.jobIds[0]) || null;
        this.releases = payload.releases || [];
        this.activeReleaseTab = 'all';
        this.currentPath = payload.sourceDir || payload.source_dir || '';
        this.dirtyIndices.clear();
        this.selectedIndices = [];
        this.editingCell = null;

        const coverUrl = payload.coverPreviewUrl || payload.cover_preview_url || payload.coverUrl || payload.cover_url || null;
        const coverPath = payload.coverPath || payload.cover_path || null;
        const hasCover = !!(payload.hasCover || payload.has_cover || coverPath || coverUrl);

        const validTracks = (payload.tracks || []).map((t, idx) => {
            const trackPath = t.path || t.converted_path || '';
            const trackName = (trackPath ? trackPath.split('/').pop() : '') || t.name || t.title || 'Track';
            const trackCover = t.cover_preview_url || t.cover_url || coverUrl;
            return {
                _trackIndex: idx,
                job_id: t.job_id || t.jobId || this.jobId || (this.jobIds && this.jobIds[0]) || '',
                path: trackPath,
                name: trackName,
                title: t.title || '',
                artist: t.artist || payload.album?.album_artist || payload.album?.artist || '',
                album: t.album || payload.album?.album || '',
                album_artist: t.album_artist || payload.album?.album_artist || '',
                year: t.year || payload.album?.year || null,
                genre: t.genre || payload.album?.genre || '',
                track_number: t.track_number !== undefined && t.track_number !== null ? t.track_number : null,
                track_total: t.track_total || payload.tracks.length,
                disc_number: t.disc_number || 1,
                disc_total: t.disc_total || 1,
                duration: t.duration || 0,
                bitrate: t.bitrate || 320,
                lyrics_status: t.lyrics_status || 'none',
                has_cover: !!(t.has_cover || trackCover || hasCover),
                cover_preview_url: trackCover,
                cover_url: t.cover_url || null,
                cover_base64: t.cover_base64 || payload.coverBase64 || payload.cover_base64 || null
            };
        });

        this.tracks = validTracks;
        this.originalTracks = JSON.parse(JSON.stringify(validTracks));
        this.syncTagPanel();
    },
};
