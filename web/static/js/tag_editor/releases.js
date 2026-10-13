/**
 * Модуль группировки треков по релизам и альбомам (Tag Releases).
 * Отвечает за структурирование треков в релизы/синглы, сворачивание групп
 * и пакетное применение метаданных группы ко всем её трекам.
 */
window.TagReleases = {
    /**
     * Формирует список релизов (releases) из плоского массива треков.
     */
    buildReleasesFromTracks() {
        const groupsMap = {};
        const order = [];

        (this.tracks || []).forEach((track, originalIndex) => {
            const albumName = (track.album || '').trim();
            const isSingle = !albumName || albumName.toLowerCase() === 'single' || albumName.toLowerCase() === 'singles' || this.singlesMode;
            const singleAlbumTitle = (track.album && track.album.toLowerCase() !== 'singles' && track.album.toLowerCase() !== 'single') ? track.album : (track.title || 'Singles');
            const artistName = (track.album_artist || track.artist || this.album.album_artist || 'Unknown Artist').trim();
            const albumArtistKey = artistName.toLowerCase();
            const isSingleRelease = this.tracks.length === 1 && !this.singlesMode;
            const key = isSingle ? (isSingleRelease ? `${albumArtistKey}:::${singleAlbumTitle.toLowerCase()}` : `__singles__:::${albumArtistKey}`) : `${albumArtistKey}:::${albumName.toLowerCase()}`;

            if (!groupsMap[key]) {
                const isGroupSingles = isSingle && !isSingleRelease;
                groupsMap[key] = {
                    id: key,
                    isSingles: isGroupSingles,
                    title: isGroupSingles ? (artistName ? `Синглы — ${artistName}` : 'Синглы и отдельные треки') : (singleAlbumTitle || track.album || 'Неизвестный альбом'),
                    albumTitle: isSingle ? singleAlbumTitle : (track.album || ''),
                    albumArtist: artistName,
                    year: track.year || this.album.year || null,
                    discTotal: track.disc_total || this.album.disc_total || 1,
                    genre: this.formatGenres ? this.formatGenres(track.genre || this.album.genre || '') : (track.genre || ''),
                    coverPreviewUrl: null,
                    coverPath: null,
                    coverBase64: null,
                    selectedCoverUrl: null,
                    items: [],
                };
                order.push(key);
            } else {
                if (!groupsMap[key].year && track.year) {
                    groupsMap[key].year = track.year;
                }
                if (!groupsMap[key].genre && track.genre) {
                    groupsMap[key].genre = this.formatGenres ? this.formatGenres(track.genre) : track.genre;
                }
                if (track.disc_total && track.disc_total > groupsMap[key].discTotal) {
                    groupsMap[key].discTotal = track.disc_total;
                }
            }
            groupsMap[key].items.push({ track, originalIndex });
        });

        const result = order.map((key) => groupsMap[key]);
        result.forEach((group) => {
            if (!group.isSingles) {
                const firstWithCover = group.items.find(
                    (it) => it.track.cover_preview_url || it.track.cover_url || it.track.cover_base64 || it.track.cover_path
                );
                if (firstWithCover) {
                    group.coverPreviewUrl =
                        firstWithCover.track.cover_preview_url ||
                        firstWithCover.track.cover_url ||
                        firstWithCover.track.cover_base64;
                    group.coverPath = firstWithCover.track.cover_path || null;
                    group.coverBase64 = firstWithCover.track.cover_base64 || null;
                    group.selectedCoverUrl = firstWithCover.track.cover_url || null;
                } else if (this.coverPreviewUrl) {
                    group.coverPreviewUrl = this.coverPreviewUrl;
                    group.coverPath = this.coverPath;
                    group.coverBase64 = this.coverBase64;
                    group.selectedCoverUrl = this.selectedCoverUrl;
                }
            } else if (this.coverPreviewUrl) {
                group.coverPreviewUrl = this.coverPreviewUrl;
                group.coverPath = this.coverPath;
            }
        });

        result.sort((a, b) => {
            if (a.isSingles) return 1;
            if (b.isSingles) return -1;
            return a.title.localeCompare(b.title);
        });

        this.releases = result;
    },

    toggleGroupCollapse(groupId) {
        this.collapsedGroups[groupId] = !this.collapsedGroups[groupId];
    },

    isGroupCollapsed(groupId) {
        return !!this.collapsedGroups[groupId];
    },

    expandAllGroups() {
        this.collapsedGroups = {};
    },

    collapseAllGroups() {
        const nextCollapsed = {};
        (this.releases || []).forEach((g) => {
            nextCollapsed[g.id] = true;
        });
        this.collapsedGroups = nextCollapsed;
    },

    /**
     * Применяет общие метаданные группы (альбом, артист, год, жанр, обложка)
     * ко всем трекам, входящим в этот релиз.
     */
    applyGroupToTracks(group) {
        if (!group || !group.items || group.items.length === 0) return;

        group.items.forEach((itemObj) => {
            const track = itemObj.track;
            if (group.isSingles) {
                if (!track.album || track.album.toLowerCase() === 'single' || track.album.toLowerCase() === 'singles') {
                    track.album = track.title || 'Single';
                }
            } else if (group.albumTitle) {
                track.album = group.albumTitle;
            }
            if (group.albumArtist) track.album_artist = group.albumArtist;
            if (group.year !== null && group.year !== undefined && group.year !== '') {
                track.year = parseInt(group.year, 10);
            }
            if (group.genre) track.genre = this.formatGenres ? this.formatGenres(group.genre) : group.genre;
            if (group.discTotal !== null && group.discTotal !== undefined && group.discTotal !== '') {
                track.disc_total = parseInt(group.discTotal, 10);
            }
            const cov = group.selectedCoverUrl || group.coverPreviewUrl;
            if (cov) {
                track.cover_url = group.selectedCoverUrl || (cov.startsWith('http') ? cov : null);
                track.cover_base64 = group.coverBase64 || (cov.startsWith('data:') ? cov : null);
                track.cover_preview_url = cov;
                track.has_cover = true;
            }
        });

        Alpine.store('app').toast(
            `Параметры релиза "${group.albumTitle || group.title}" применены к трекам (${group.items.length})`,
            'info'
        );
    }
};
