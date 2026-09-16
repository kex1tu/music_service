/**
 * Модуль управления обложками релизов и треков (Tag Covers).
 * Отвечает за загрузку файлов обложек, конвертацию в base64,
 * поиск в онлайн-галерее, пакетный подбор для синглов и применение обложек.
 */
window.TagCovers = {
    handleGroupCoverUpload(event, group) {
        const file = event.target.files[0];
        if (!file) return;

        if (!file.type.startsWith('image/')) {
            Alpine.store('app').toast('Пожалуйста, выберите изображение (JPEG или PNG)', 'error');
            return;
        }

        const reader = new FileReader();
        reader.onload = (e) => {
            const dataUrl = e.target.result;
            group.coverPreviewUrl = dataUrl;
            group.coverBase64 = dataUrl;
            group.selectedCoverUrl = null;
            group.coverPath = null;
            group.items.forEach((it) => {
                it.track.cover_preview_url = dataUrl;
                it.track.cover_base64 = dataUrl;
                it.track.cover_url = null;
                it.track.has_cover = true;
            });
            Alpine.store('app').toast(`Обложка установлена для релиза "${group.albumTitle || group.title}"`, 'success');
        };
        reader.readAsDataURL(file);
    },

    async fetchCoverBase64(url) {
        try {
            const res = await fetch(url);
            if (res.ok) {
                const blob = await res.blob();
                const reader = new FileReader();
                reader.onload = (e) => {
                    this.coverBase64 = e.target.result;
                };
                reader.readAsDataURL(blob);
            }
        } catch (e) {
            console.warn('Не удалось конвертировать обложку в base64:', e);
        }
    },

    openCoverGallery(group = null) {
        if (group) {
            return this.openGroupCoverGallery(group);
        }
        if (this.releases && this.releases.length > 0) {
            return this.openGroupCoverGallery(this.releases[0]);
        }
        this.coverGalleryTarget = { type: 'album', id: null, originalIndex: null };
        this.showCoverGalleryModal = true;
        this.coverSourceFilter = 'all';
        const albumPart = this.album.album || '';
        const artistPart = this.album.album_artist || '';
        this.coverSearchQuery = (
            albumPart
                ? `${artistPart} ${albumPart}`
                : this.tracks[0]?.artist
                ? `${this.tracks[0].artist} ${this.tracks[0].title || ''}`
                : ''
        ).trim();
        if (this.coverSearchQuery) {
            this.searchCovers();
        }
    },

    openGroupCoverGallery(group) {
        this.coverGalleryTarget = { type: 'group', group: group };
        this.showCoverGalleryModal = true;
        this.coverSourceFilter = 'all';
        const artistPart = group.albumArtist || this.album.album_artist || '';
        const titlePart = group.isSingles ? '' : (group.albumTitle || group.title);
        this.coverSearchQuery = `${artistPart} ${titlePart}`.trim() || (this.tracks[0]?.artist || '');
        if (this.coverSearchQuery) {
            this.searchCovers();
        }
    },

    openTrackCoverGallery(track, originalIndex) {
        this.coverGalleryTarget = { type: 'track', originalIndex: originalIndex, track: track };
        this.showCoverGalleryModal = true;
        this.coverSourceFilter = 'all';
        const artistPart = track.artist || track.album_artist || this.album.album_artist || '';
        this.coverSearchQuery = `${artistPart} ${track.title || ''}`.trim();
        if (this.coverSearchQuery) {
            this.searchCovers();
        }
    },

    applyAlbumCoverToGroup(group) {
        const cov = group.selectedCoverUrl || group.coverPreviewUrl || this.selectedCoverUrl || this.coverPreviewUrl;
        if (!cov) {
            this.openGroupCoverGallery(group);
            return;
        }
        group.coverPreviewUrl = cov;
        group.selectedCoverUrl = cov.startsWith('http') ? cov : null;
        group.coverBase64 = cov.startsWith('data:') ? cov : null;
        group.items.forEach((itemObj) => {
            itemObj.track.cover_url = cov.startsWith('http') ? cov : null;
            itemObj.track.cover_base64 = cov.startsWith('data:') ? cov : null;
            itemObj.track.cover_preview_url = cov;
            itemObj.track.has_cover = true;
        });
        Alpine.store('app').toast(`Обложка применена ко всем трекам релиза "${group.albumTitle || group.title}"`, 'success');
    },

    async fetchSinglesCovers() {
        if (!this.tracks || this.tracks.length === 0) return;
        const targets = this.tracks.filter(
            (t) => (!t.album || !t.album.trim() || this.singlesMode) && !t.cover_preview_url && !t.cover_url
        );

        if (targets.length === 0) {
            Alpine.store('app').toast('Все синглы уже имеют обложки', 'info');
            return;
        }

        this.isSearchingSinglesCovers = true;
        this.singlesCoverProgress = { current: 0, total: targets.length };
        let foundCount = 0;

        Alpine.store('app').toast(
            `Запущен автопоиск обложек для ${targets.length} синглов...`,
            'info'
        );

        for (let i = 0; i < targets.length; i++) {
            const track = targets[i];
            this.singlesCoverProgress.current = i + 1;

            const q = `${track.artist || this.album.album_artist || ''} ${track.title || ''}`.trim();
            if (q) {
                try {
                    const res = await fetch(
                        `/tags/covers/search?query=${encodeURIComponent(q)}&limit=1`
                    );
                    if (res.ok) {
                        const results = await res.json();
                        if (results && results.length > 0) {
                            track.cover_url = results[0].cover_url;
                            track.cover_preview_url = results[0].cover_url;
                            track.has_cover = true;
                            foundCount++;
                        }
                    }
                } catch (e) {
                    console.warn('Ошибка поиска обложки для трека', track.title, e);
                }
            }
            await new Promise((r) => setTimeout(r, 150));
        }

        this.isSearchingSinglesCovers = false;
        Alpine.store('app').toast(
            `Автопоиск обложек завершен: найдено ${foundCount} из ${targets.length}`,
            foundCount > 0 ? 'success' : 'warning'
        );
    },

    closeCoverGallery() {
        this.showCoverGalleryModal = false;
    },

    async searchCovers() {
        if (!this.coverSearchQuery || !this.coverSearchQuery.trim()) return;
        this.isSearchingCovers = true;
        this.coverGalleryResults = [];

        try {
            const res = await fetch(
                `/tags/covers/search?query=${encodeURIComponent(this.coverSearchQuery.trim())}&limit=24`
            );
            if (res.ok) {
                this.coverGalleryResults = await res.json();
                if (this.coverGalleryResults.length === 0) {
                    Alpine.store('app').toast('Обложки не найдены', 'info');
                }
            } else {
                const err = await res.json();
                throw new Error(err.message || 'Ошибка поиска обложек');
            }
        } catch (err) {
            Alpine.store('app').toast(`Ошибка: ${err.message}`, 'error');
        } finally {
            this.isSearchingCovers = false;
        }
    },

    filteredCoverResults() {
        if (this.coverSourceFilter === 'all') {
            return this.coverGalleryResults;
        }
        return this.coverGalleryResults.filter((item) => item.source === this.coverSourceFilter);
    },

    selectCover(item) {
        if (!item || !item.cover_url) return;

        if (this.coverGalleryTarget?.type === 'track') {
            const idx = this.coverGalleryTarget.originalIndex;
            const tr = this.tracks[idx] || this.coverGalleryTarget.track;
            if (tr) {
                tr.cover_url = item.cover_url;
                tr.cover_preview_url = item.cover_url;
                tr.has_cover = true;
            }
            Alpine.store('app').toast(
                `Выбрана обложка для трека "${tr?.title || ''}" (${item.source.toUpperCase()})`,
                'success'
            );
        } else if (this.coverGalleryTarget?.type === 'group') {
            const group = this.coverGalleryTarget.group;
            if (group) {
                group.coverPreviewUrl = item.cover_url;
                group.selectedCoverUrl = item.cover_url;
                group.coverBase64 = null;
                group.coverPath = null;
                group.items.forEach((itemObj) => {
                    itemObj.track.cover_url = item.cover_url;
                    itemObj.track.cover_preview_url = item.cover_url;
                    itemObj.track.cover_base64 = null;
                    itemObj.track.has_cover = true;
                });
            }
            Alpine.store('app').toast(
                `Выбрана обложка для релиза "${group?.albumTitle || group?.title || ''}" (${item.source.toUpperCase()})`,
                'success'
            );
        } else {
            this.coverPreviewUrl = item.cover_url;
            this.selectedCoverUrl = item.cover_url;
            this.coverPath = null;
            this.coverBase64 = null;
            this.fetchCoverBase64(item.cover_url);
            Alpine.store('app').toast(
                `Выбрана обложка альбома (${item.source.toUpperCase()} ${item.width}x${item.height})`,
                'success'
            );
        }

        this.closeCoverGallery();
    }
};
