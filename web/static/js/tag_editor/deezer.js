/**
 * Модуль интеграции с музыкальным каталогом Deezer (Tag Deezer).
 * Отвечает за поиск альбома по названию/артисту, получение треклиста
 * и автоматическое заполнение метаданных и обложки в редакторе тегов.
 */
window.TagDeezer = {
    openDeezerSearchForGroup(group) {
        this.activeDeezerGroup = group;
        this.showDeezerModal = true;
        this.deezerResults = [];
        const albumPart = group.albumTitle || (!group.isSingles ? group.title : '') || '';
        const artistPart = group.albumArtist || group.items[0]?.track?.artist || '';
        this.deezerQuery = (
            albumPart
                ? `${artistPart} ${albumPart}`
                : group.items[0]?.track?.artist
                ? `${group.items[0].track.artist} ${group.items[0].track.title || ''}`
                : ''
        ).trim();
        if (this.deezerQuery) {
            this.searchDeezerAlbums();
        }
    },

    openDeezerSearch(group = null) {
        if (group) {
            return this.openDeezerSearchForGroup(group);
        }
        if (this.releases && this.releases.length > 0) {
            return this.openDeezerSearchForGroup(this.releases[0]);
        }
        this.showDeezerModal = true;
        this.deezerResults = [];
        const albumPart = this.album.album || '';
        const artistPart = this.album.album_artist || '';
        this.deezerQuery = (
            albumPart
                ? `${artistPart} ${albumPart}`
                : this.tracks[0]?.artist
                ? `${this.tracks[0].artist} ${this.tracks[0].title || ''}`
                : ''
        ).trim();
        if (this.deezerQuery) {
            this.searchDeezerAlbums();
        }
    },

    closeDeezerSearch() {
        this.showDeezerModal = false;
        this.activeDeezerGroup = null;
    },

    async searchDeezerAlbums() {
        if (!this.deezerQuery || !this.deezerQuery.trim()) return;
        this.isSearchingDeezer = true;
        this.deezerResults = [];

        const endpoint = `/tags/deezer/albums?query=${encodeURIComponent(this.deezerQuery.trim())}`;

        try {
            const res = await fetch(endpoint);
            if (res.ok) {
                this.deezerResults = await res.json();
                if (this.deezerResults.length === 0) {
                    Alpine.store('app').toast('Релизы в Deezer не найдены', 'info');
                }
            } else {
                const err = await res.json();
                throw new Error(err.message || 'Ошибка поиска в Deezer');
            }
        } catch (err) {
            Alpine.store('app').toast(`Ошибка: ${err.message}`, 'error');
        } finally {
            this.isSearchingDeezer = false;
        }
    },

    async applyDeezerAlbum(albumId) {
        if (!albumId) return;
        this.isLoadingDeezerDetails = true;

        const endpoint = `/tags/deezer/albums/${albumId}`;

        try {
            const res = await fetch(endpoint);
            if (!res.ok) {
                const err = await res.json();
                throw new Error(err.message || 'Не удалось загрузить данные альбома');
            }

            const data = await res.json();
            const targetGroup = this.activeDeezerGroup || (this.releases && this.releases[0]) || null;

            if (targetGroup) {
                targetGroup.albumTitle = data.title || targetGroup.albumTitle;
                targetGroup.title = data.title || targetGroup.title;
                targetGroup.albumArtist = data.artist || targetGroup.albumArtist;
                if (data.year) targetGroup.year = data.year;
                if (data.genre) targetGroup.genre = this.formatGenres ? this.formatGenres(data.genre) : data.genre;

                if (data.cover_url) {
                    targetGroup.coverPreviewUrl = data.cover_url;
                    targetGroup.selectedCoverUrl = data.cover_url;
                    targetGroup.coverPath = null;
                    targetGroup.coverBase64 = null;
                }

                if (data.tracks && data.tracks.length > 0) {
                    targetGroup.items.forEach((it, idx) => {
                        let dt = null;
                        const curTrackNum = parseInt(it.track.track_number, 10);
                        if (curTrackNum && !isNaN(curTrackNum)) {
                            dt = data.tracks.find((t) => parseInt(t.track_number, 10) === curTrackNum);
                        }
                        if (!dt && it.track.title) {
                            const cleanLocal = it.track.title.toLowerCase().replace(/[^\p{L}\p{N}]+/gu, ' ').trim();
                            dt = data.tracks.find((t) => {
                                const cleanRemote = (t.title || '').toLowerCase().replace(/[^\p{L}\p{N}]+/gu, ' ').trim();
                                return cleanLocal && cleanRemote && (cleanLocal === cleanRemote || cleanLocal.includes(cleanRemote) || cleanRemote.includes(cleanLocal));
                            });
                        }
                        if (!dt) {
                            dt = data.tracks[idx];
                        }

                        if (dt) {
                            it.track.track_number = dt.track_number;
                            it.track.title = dt.title;
                            it.track.artist = dt.artist || data.artist || it.track.artist;
                            it.track.album = data.title;
                            it.track.album_artist = data.artist;
                            if (data.year) it.track.year = data.year;
                            if (data.genre) it.track.genre = this.formatGenres ? this.formatGenres(data.genre) : data.genre;
                        }
                        if (data.cover_url) {
                            it.track.cover_url = data.cover_url;
                            it.track.cover_preview_url = data.cover_url;
                            it.track.has_cover = true;
                        }
                    });
                }
            } else {
                this.album.album = data.title || this.album.album;
                this.album.album_artist = data.artist || this.album.album_artist;
                if (data.year) this.album.year = data.year;
                if (data.genre) this.album.genre = this.formatGenres ? this.formatGenres(data.genre) : data.genre;

                if (data.cover_url) {
                    this.coverPreviewUrl = data.cover_url;
                    this.coverPath = null;
                    if (this.fetchCoverBase64) {
                        this.fetchCoverBase64(data.cover_url);
                    }
                }

                if (data.tracks && data.tracks.length > 0) {
                    this.tracks.forEach((track, idx) => {
                        let dt = null;
                        const curTrackNum = parseInt(track.track_number, 10);
                        if (curTrackNum && !isNaN(curTrackNum)) {
                            dt = data.tracks.find((t) => parseInt(t.track_number, 10) === curTrackNum);
                        }
                        if (!dt && track.title) {
                            const cleanLocal = track.title.toLowerCase().replace(/[^\p{L}\p{N}]+/gu, ' ').trim();
                            dt = data.tracks.find((t) => {
                                const cleanRemote = (t.title || '').toLowerCase().replace(/[^\p{L}\p{N}]+/gu, ' ').trim();
                                return cleanLocal && cleanRemote && (cleanLocal === cleanRemote || cleanLocal.includes(cleanRemote) || cleanRemote.includes(cleanLocal));
                            });
                        }
                        if (!dt) {
                            dt = data.tracks[idx];
                        }

                        if (dt) {
                            track.track_number = dt.track_number;
                            track.title = dt.title;
                            track.artist = dt.artist || data.artist || track.artist;
                            track.album = data.title;
                            track.album_artist = data.artist;
                            if (data.year) track.year = data.year;
                            if (data.genre) track.genre = this.formatGenres ? this.formatGenres(data.genre) : data.genre;
                        }
                    });
                }
            }

            this.closeDeezerSearch();
            Alpine.store('app').toast(`Альбом "${data.title}" успешно применен!`, 'success');
        } catch (err) {
            Alpine.store('app').toast(`Ошибка: ${err.message}`, 'error');
        } finally {
            this.isLoadingDeezerDetails = false;
            this.activeDeezerGroup = null;
        }
    }
};
