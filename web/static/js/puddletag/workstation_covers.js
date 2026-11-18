const puddletagWorkstationCovers = {
    // ==================== БЛОК ОБЛОЖКИ (ARTWORK BOX) ====================

    handleCoverDrop(event) {
        event.preventDefault();
        const files = event.dataTransfer?.files;
        if (files && files.length > 0) {
            this.processCoverFile(files[0]);
        }
    },

    handleCoverFileInput(event) {
        const files = event.target?.files;
        if (files && files.length > 0) {
            this.processCoverFile(files[0]);
        }
    },

    processCoverFile(file) {
        if (!file.type.startsWith('image/')) {
            Alpine.store('app').toast('Файл должен быть изображением (JPG, PNG)', 'warning');
            return;
        }

        const reader = new FileReader();
        reader.onload = (e) => {
            const base64 = e.target.result;
            this.tagPanel.cover_url = base64;
            this.tagPanel.has_cover = true;

            // Применяем ко всем выбранным или ко всем трекам
            const targetIndices = this.selectedIndices.length > 0 ? this.selectedIndices : this.tracks.map((_, i) => i);
            for (const idx of targetIndices) {
                const t = this.tracks[idx];
                if (t) {
                    t.cover_preview_url = base64;
                    t.cover_base64 = base64;
                    t.has_cover = true;
                    this.dirtyIndices.add(idx);
                }
            }
            Alpine.store('app').toast('Обложка применена к выбранным трекам', 'success');
        };
        reader.readAsDataURL(file);
    },

    removeCover() {
        this.tagPanel.cover_url = null;
        this.tagPanel.has_cover = false;
        const targetIndices = this.selectedIndices.length > 0 ? this.selectedIndices : this.tracks.map((_, i) => i);
        for (const idx of targetIndices) {
            const t = this.tracks[idx];
            if (t) {
                t.cover_preview_url = null;
                t.cover_base64 = null;
                t.has_cover = false;
                this.dirtyIndices.add(idx);
            }
        }
        Alpine.store('app').toast('Обложка удалена у выбранных треков', 'info');
    },

    openCoverGallery() {
        this.showCoverGalleryModal = true;
        this.coverSourceFilter = 'all';

        const artist = (this.tagPanel.artist || this.tagPanel.album_artist || this.tracks[0]?.artist || '').trim();
        const album = (this.tagPanel.album || this.tracks[0]?.album || '').trim();
        const title = (this.tagPanel.title || this.tracks[0]?.title || '').trim();

        if (artist && album) {
            this.coverSearchQuery = `${artist} ${album}`;
        } else if (artist && title) {
            this.coverSearchQuery = `${artist} ${title}`;
        } else if (album) {
            this.coverSearchQuery = album;
        } else if (artist) {
            this.coverSearchQuery = artist;
        } else {
            this.coverSearchQuery = '';
        }

        if (this.coverSearchQuery) {
            this.searchCovers();
        }
    },

    closeCoverGallery() {
        this.showCoverGalleryModal = false;
    },

    async searchCovers() {
        const q = (this.coverSearchQuery || '').trim();
        if (!q) return;

        this.isSearchingCovers = true;
        this.coverGalleryResults = [];

        try {
            const results = window.apiClient?.tags?.searchCovers
                ? await window.apiClient.tags.searchCovers(q)
                : await window.api.raw(`/tags/covers/search?query=${encodeURIComponent(q)}&limit=24`).then(r => r.json());

            this.coverGalleryResults = Array.isArray(results) ? results : [];
            if (this.coverGalleryResults.length === 0) {
                Alpine.store('app').toast('Обложки не найдены', 'info');
            }
        } catch (err) {
            console.error('[Puddletag] Ошибка поиска обложек:', err);
            Alpine.store('app').toast(`Ошибка поиска обложек: ${err.message || err}`, 'error');
        } finally {
            this.isSearchingCovers = false;
        }
    },

    filteredCoverResults() {
        if (!this.coverGalleryResults || !Array.isArray(this.coverGalleryResults)) {
            return [];
        }
        if (this.coverSourceFilter === 'all') {
            return this.coverGalleryResults;
        }
        return this.coverGalleryResults.filter((item) => item.source === this.coverSourceFilter);
    },

    selectCover(item) {
        if (!item || !item.cover_url) return;

        this.tagPanel.cover_url = item.cover_url;
        this.tagPanel.has_cover = true;

        const targetIndices = this.selectedIndices.length > 0 ? this.selectedIndices : this.tracks.map((_, i) => i);
        for (const idx of targetIndices) {
            const t = this.tracks[idx];
            if (t) {
                t.cover_preview_url = item.cover_url;
                t.cover_url = item.cover_url;
                t.has_cover = true;
                this.dirtyIndices.add(idx);
            }
        }

        // Фоновая конвертация в base64 для надежного сохранения
        this.fetchCoverBase64(item.cover_url);

        this.closeCoverGallery();
        const srcLabel = (item.source || 'ONLINE').toUpperCase();
        const resLabel = (item.width && item.height) ? ` (${item.width}×${item.height})` : '';
        Alpine.store('app').toast(`Выбрана обложка ${srcLabel}${resLabel}`, 'success');
    },

    async fetchCoverBase64(url) {
        if (!url) return;
        try {
            const res = await window.api.raw(url);
            if (res.ok) {
                const blob = await res.blob();
                const reader = new FileReader();
                reader.onload = (e) => {
                    const b64 = e.target.result;
                    this.tagPanel.cover_base64 = b64;
                    const targetIndices = this.selectedIndices.length > 0 ? this.selectedIndices : this.tracks.map((_, i) => i);
                    for (const idx of targetIndices) {
                        const t = this.tracks[idx];
                        if (t) {
                            t.cover_base64 = b64;
                        }
                    }
                };
                reader.readAsDataURL(blob);
            }
        } catch (e) {
            console.debug('[Puddletag] Фоновая конвертация обложки в DataURL (будет скачано сервером):', e);
        }
    },
};
