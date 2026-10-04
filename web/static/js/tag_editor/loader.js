/**
 * Модуль загрузки и сброса состояния редактора тегов.
 */
window.TagLoader = {
    loadData(component, data) {
        component.viewMode = 'grouped';
        component.collapsedGroups = {};
        component.coverGalleryTarget = { type: 'album', id: null, originalIndex: null };
        component.isSearchingSinglesCovers = false;
        component.singlesCoverProgress = { current: 0, total: 0 };
        component.singlesMode = false;
        component.groupByAlbum = true;
        component.coverBase64 = null;
        component.selectedCoverUrl = null;
        component.draggedIndex = null;
        component.dragOverIndex = null;
        component.showCoverGalleryModal = false;
        component.coverSearchQuery = '';
        component.coverGalleryResults = [];
        component.isSearchingCovers = false;
        component.isFetchingLyrics = false;
        component.lyricsProgress = { current: 0, total: 0 };
        component.recognizingTrackPath = null;
        component.isRecognizingAll = false;
        component.shazamProgress = { current: 0, total: 0 };
        component.showDeezerModal = false;
        component.deezerSearchType = 'album';
        component.deezerTargetTrack = null;
        component.activeDeezerGroup = null;
        component.deezerQuery = '';
        component.deezerResults = [];
        component.isSearchingDeezer = false;
        component.isLoadingDeezerDetails = false;
        component.jobIds = data.jobIds || (data.jobId ? [data.jobId] : []);

        const formatGenres = component.formatGenres || (window.AppFormatters ? window.AppFormatters.formatGenres : (g) => g);

        component.album = {
            album_artist: data.album?.album_artist || '',
            album: data.album?.album || '',
            year: data.album?.year || null,
            genre: formatGenres(data.album?.genre || ''),
            disc_total: data.album?.disc_total || null,
            track_total: data.album?.track_total || null,
        };

        component.tracks = (data.tracks || []).map((t) => {
            let covPrev = t.cover_preview_url || t.cover_url || null;
            if (!covPrev && t.cover_path) {
                covPrev = `/tags/cover?path=${encodeURIComponent(t.cover_path)}&t=${Date.now()}`;
            } else if (!covPrev && t.has_cover && t.path) {
                covPrev = `/tags/cover?path=${encodeURIComponent(t.path)}&t=${Date.now()}`;
            }

            return {
                ...t,
                track_number: t.track_number !== undefined && t.track_number !== null ? t.track_number : 1,
                track_total: t.track_total || (data.album?.track_total || null),
                disc_number: t.disc_number !== undefined && t.disc_number !== null ? t.disc_number : 1,
                disc_total: t.disc_total || (data.album?.disc_total || null),
                album: (t.album !== undefined && t.album !== null && t.album !== '') ? t.album : '',
                album_artist: (t.album_artist !== undefined && t.album_artist !== null && t.album_artist !== '') ? t.album_artist : (t.artist || ''),
                year: (t.year !== undefined && t.year !== null && t.year !== '') ? t.year : (component.album.year || null),
                genre: formatGenres((t.genre !== undefined && t.genre !== null && t.genre !== '') ? t.genre : (component.album.genre || '')),
                cover_url: t.cover_url || null,
                cover_path: t.cover_path || null,
                cover_base64: t.cover_base64 || null,
                cover_preview_url: covPrev,
                job_id: t.job_id || null,
            };
        });
        component.sourceDir = data.sourceDir || '';
        component.coverPath = data.coverPath || null;

        if (component.coverPath) {
            component.coverPreviewUrl = `/tags/cover?path=${encodeURIComponent(component.coverPath)}&t=${Date.now()}`;
        } else if (data.hasCover && component.sourceDir) {
            component.coverPreviewUrl = `/tags/cover?path=${encodeURIComponent(component.sourceDir)}&t=${Date.now()}`;
        } else {
            component.coverPreviewUrl = null;
        }

        if (component.buildReleasesFromTracks) {
            component.buildReleasesFromTracks();
        }
    }
};
