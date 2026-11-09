/**
 * client.js — Единый типизированный API-клиент Music Manager.
 * 
 * Инкапсулирует все HTTP-взаимодействия с 6 вертикальными слайсами бэкенда
 * (Downloader, TagEditor, FileManager, Audit, Radio, Radar) и подсистемами Auth/Lyrics.
 * Работает в no-build среде браузера (Vanilla JS / Alpine.js CDN).
 */
(function(global) {
    'use strict';

    /**
     * Базовый метод для отправки HTTP-запросов с централизованной обработкой ошибок.
     * @param {string} url - URL эндпоинта
     * @param {RequestInit} [options] - Параметры fetch
     * @returns {Promise<any>} Десериализованный JSON или текст ответа
     */
    async function raw(url, options = {}) {
        const target = new URL(url, window.location.href);
        const res = await fetch(url, { ...options, ...(target.origin === window.location.origin ? { credentials: 'same-origin' } : {}) });
        if (target.origin === window.location.origin && res.status === 401) {
            window.dispatchEvent(new CustomEvent('api:unauthorized'));
        }
        return res;
    }
    async function request(url, options = {}) {
        const headers = { ...(options.headers || {}) };

        // Если передано тело-объект (не FormData и не строка), кодируем в JSON
        let body = options.body;
        if (body && typeof body === 'object' && !(body instanceof FormData) && !(body instanceof Blob)) {
            headers['Content-Type'] = 'application/json';
            body = JSON.stringify(body);
        }

        const res = await raw(url, {
            ...options,
            headers,
            body,
        });

        if (!res.ok) {
            let errorMsg = `HTTP ${res.status}`;
            let errorDetails = null;
            try {
                const data = await res.json();
                errorDetails = data;
                if (data && (data.error || data.message)) {
                    errorMsg = data.error || data.message;
                }
            } catch (_) {
                // Если не JSON, пробуем прочесть текст
                try {
                    const text = await res.text();
                    if (text && text.trim().length > 0) {
                        errorMsg = text.trim();
                    }
                } catch (_) {}
            }

            const error = new Error(errorMsg);
            error.status = res.status;
            error.response = res;
            error.data = errorDetails;
            throw error;
        }

        // Если код 204 No Content
        if (res.status === 204) {
            return null;
        }

        const contentType = res.headers.get('content-type') || '';
        if (contentType.includes('application/json')) {
            return res.json();
        }
        return res.text();
    }

    /**
     * Помощник сериализации параметров строки запроса.
     */
    function toQueryString(params = {}) {
        const q = new URLSearchParams();
        for (const [key, val] of Object.entries(params)) {
            if (val !== undefined && val !== null && val !== '') {
                q.append(key, val);
            }
        }
        const str = q.toString();
        return str ? '?' + str : '';
    }

    const api = {
        request,
        raw,

        // ==================== AUTH ====================
        auth: {
            me: () => request('/api/auth/me'),
            login: (login, password) => request('/login', { method: 'POST', body: { login, password } }),
            logout: () => request('/logout', { method: 'POST' }),
            musicbrainzStatus: () => request('/api/auth/musicbrainz/status'),
            musicbrainzDisconnect: () => request('/api/auth/musicbrainz/disconnect', { method: 'POST' }),
        },

        // ==================== DOWNLOADER ====================
        downloader: {
            list: () => request('/downloads'),
            get: (id) => request(`/downloads/${encodeURIComponent(id)}`),
            create: (url, source = 'auto') => request('/downloads', { method: 'POST', body: { url, source } }),
            batch: (urls, source = 'auto') => request('/downloads/batch', { method: 'POST', body: { urls, source } }),
            delete: (id) => request(`/downloads/${encodeURIComponent(id)}`, { method: 'DELETE' }),
            retry: (id) => request(`/downloads/${encodeURIComponent(id)}/retry`, { method: 'POST' }),
            tracks: (id) => request(`/downloads/${encodeURIComponent(id)}/tracks`),
            preview: (url) => request(`/downloads/preview?url=${encodeURIComponent(url)}`),
            candidates: (params) => request(`/downloads/candidates${toQueryString(params)}`),
            switchVersion: (trackId, sourceUrl) => request(`/downloads/tracks/${encodeURIComponent(trackId)}/switch-version`, { method: 'POST', body: { source_url: sourceUrl } }),
            bindSource: (trackId, sourceUrl) => request(`/downloads/tracks/${encodeURIComponent(trackId)}/bind-source`, { method: 'POST', body: { source_url: sourceUrl } }),
            bindBackfillSource: (albumPath, trackNumber, sourceUrl) => request('/downloads/backfill/bind-source', { method: 'POST', body: { album_path: albumPath, track_number: trackNumber, source_url: sourceUrl } }),
            queueSummary: () => request('/downloads/queue/summary'),
            clearCompleted: () => request('/downloads/clear-completed', { method: 'POST' }),
            clearQueue: () => request('/downloads/queue/clear', { method: 'POST' }),
            pause: () => request('/downloads/pause', { method: 'POST' }),
            resume: () => request('/downloads/resume', { method: 'POST' }),
            retryFailed: () => request('/downloads/retry-failed', { method: 'POST' }),
            discography: (url) => request(`/downloads/discography?url=${encodeURIComponent(url)}`),
            uploadTorrent: (formData) => request('/downloads/torrent', { method: 'POST', body: formData }),
        },

        // ==================== TAG EDITOR ====================
        tags: {
            get: (path) => request(`/tags?path=${encodeURIComponent(path)}`),
            save: (payload) => request('/tags', { method: 'POST', body: payload }),
            batchSave: (payload) => request('/tags/batch', { method: 'POST', body: payload }),
            recognizeAudio: (path) => request('/tags/recognize-audio', { method: 'POST', body: { path } }),
            getCover: (path) => request(`/tags/cover?path=${encodeURIComponent(path)}`),
            searchCovers: (query) => request(`/tags/covers/search?query=${encodeURIComponent(query)}`),
            renamePattern: (payload) => request('/tags/rename-pattern', { method: 'POST', body: payload }),
            deezerAlbums: (query) => request(`/tags/deezer/albums?query=${encodeURIComponent(query)}`),
            deezerAlbumDetails: (id) => request(`/tags/deezer/albums/${encodeURIComponent(id)}`),
            deezerTracks: (query) => request(`/tags/deezer/tracks?query=${encodeURIComponent(query)}`),
            deezerTrackDetails: (id) => request(`/tags/deezer/tracks/${encodeURIComponent(id)}`),
        },

        // ==================== CATALOG SEARCH ====================
        catalog: {
            search: (query, type = 'all', source = 'all') => request(`/api/catalog/search${toQueryString({ query, type, source })}`),
            deezerAlbum: (id) => request(`/api/catalog/deezer/album/${encodeURIComponent(id)}`),
            musicbrainzRelease: (id) => request(`/api/catalog/musicbrainz/release/${encodeURIComponent(id)}`),
            shazamTrack: (id) => request(`/api/catalog/shazam/track/${encodeURIComponent(id)}`),
        },

        // ==================== FILE MANAGER & STREAMING ====================
        files: {
            list: (dir = '') => request(`/files${toQueryString({ dir })}`),
            delete: (path) => request(`/files?path=${encodeURIComponent(path)}`, { method: 'DELETE' }),
            deleteBatch: (paths) => request('/files', { method: 'DELETE', body: { paths } }),
            rename: (oldPath, newPath) => request('/files/rename', { method: 'POST', body: { old_path: oldPath, new_path: newPath } }),
            reorganize: (payload) => request('/files/reorganize', { method: 'POST', body: payload }),
            redownload: (payload) => request('/files/redownload', { method: 'POST', body: payload }),
            search: (query) => request(`/files/search?query=${encodeURIComponent(query)}`),
            streamTrackUrl: (id) => `/api/tracks/${encodeURIComponent(id)}/stream`,
            trackMetadata: (id) => request(`/api/tracks/${encodeURIComponent(id)}/metadata`),
            trackCoverUrl: (id) => `/api/tracks/${encodeURIComponent(id)}/cover`,
            libraryTracks: () => request('/api/library/tracks'),
        },

        // ==================== AUDIT & INTEGRITY ====================
        audit: {
            summary: () => request('/files/audit/summary'),
            refresh: () => request('/files/audit/refresh', { method: 'POST' }),
            search: (query) => request(`/files/audit/search?query=${encodeURIComponent(query)}`),
            invalidPaths: () => request('/files/audit/invalid-paths'),
            fixPath: (oldPath, newPath) => request('/files/audit/fix-path', { method: 'POST', body: { old_path: oldPath, new_path: newPath } }),
            fixAllPaths: () => request('/files/audit/fix-all-paths', { method: 'POST' }),
            reorganize: () => request('/files/audit/reorganize', { method: 'POST' }),
            normalizeNavidrome: () => request('/files/audit/normalize-navidrome', { method: 'POST' }),
            duplicates: () => request('/files/audit/duplicates'),
            fixDuplicates: (keepPath, deletePath) => request('/files/audit/duplicates/fix', { method: 'POST', body: { keep: keepPath, delete: deletePath } }),
            fixAllDuplicates: () => request('/files/audit/duplicates/fix-all', { method: 'POST' }),
            resolveDuplicate: (keep, del) => request('/api/audit/duplicates/resolve', { method: 'POST', body: { keep, delete: del } }),
            missingLyrics: () => request('/files/audit/missing-lyrics'),
            batchLyricsFetch: () => request('/files/audit/missing-lyrics/fetch-all', { method: 'POST' }),
            batchLyricsProgress: () => request('/files/audit/missing-lyrics/progress'),
            missingCovers: () => request('/files/audit/missing-covers'),
            backfillCover: (payload) => request('/files/audit/missing-covers/backfill', { method: 'POST', body: payload }),
            batchCoversBackfill: () => request('/files/audit/missing-covers/backfill-all', { method: 'POST' }),
            batchCoversProgress: () => request('/files/audit/missing-covers/progress'),
            cancelCoversBatch: () => request('/files/audit/missing-covers/cancel', { method: 'POST' }),
            nonEnGenres: () => request('/files/audit/non-en-genres'),
            retagGenres: () => request('/files/audit/retag-genres', { method: 'POST' }),
            incompleteAlbums: () => request('/files/audit/incomplete-albums'),
            catalogMatch: (albumPath) => request(`/files/audit/incomplete-albums/catalog-match?album_path=${encodeURIComponent(albumPath)}`),
            backfillEditorData: (albumPath, jobId) => request(`/files/audit/incomplete-albums/editor-data?path=${encodeURIComponent(albumPath)}&job_id=${encodeURIComponent(jobId || '')}`),
            downloadMissing: (payload) => request('/files/audit/incomplete-albums/download-missing', { method: 'POST', body: payload }),
            tagIssues: (params) => request(`/files/audit/tag-issues${toQueryString(params)}`),
            fixTagIssue: (payload) => request('/files/audit/tag-issues/fix', { method: 'POST', body: payload }),
            fixSafeTagIssues: () => request('/files/audit/tag-issues/fix-safe', { method: 'POST' }),
            fixSelectedTagIssues: (items) => request('/files/audit/tag-issues/fix-selected', { method: 'POST', body: { items } }),
            fixAllTagIssues: () => request('/files/audit/tag-issues/fix-all', { method: 'POST' }),
            catalogMatchTrack: (params) => request(`/files/audit/tag-issues/catalog-match${toQueryString(params)}`),
            catalogMatchAlbum: (params) => request(`/files/audit/tag-issues/catalog-match-album${toQueryString(params)}`),
            applyCatalog: (payload) => request('/files/audit/tag-issues/apply-catalog', { method: 'POST', body: payload }),
            applyCatalogAlbum: (payload) => request('/files/audit/tag-issues/apply-catalog-album', { method: 'POST', body: payload }),
        },

        // ==================== RADIO & MY WAVE ====================
        radio: {
            nowPlaying: () => request('/api/radio/now-playing'),
            waveQueue: (mode) => request(`/api/radio/wave/queue${toQueryString({ mode })}`),
            waveStats: () => request('/api/radio/wave/stats'),
            genres: () => request('/api/radio/genres'),
            generateM3U: () => request('/api/radio/m3u/generate', { method: 'POST' }),
            syncNavidrome: () => request('/api/radio/navidrome/sync-radio', { method: 'POST' }),
            streamUrl: (mode = 'wave') => `/api/radio/stream/${encodeURIComponent(mode)}`,
        },

        // ==================== RELEASE RADAR ====================
        radar: {
            feed: (params = {}) => request(`/api/radar/feed${toQueryString(params)}`),
            unreadCount: () => request('/api/radar/unread-count'),
            artists: (params = {}) => request(`/api/radar/artists${toQueryString(params)}`),
            addArtist: (name) => request('/api/radar/artists', { method: 'POST', body: { name } }),
            followArtist: (name) => request(`/api/radar/artists/${encodeURIComponent(name)}/follow`, { method: 'POST' }),
            setAutoDownload: (name, autoDownload = true) => request(`/api/radar/artists/${encodeURIComponent(name)}/auto-download`, { method: 'POST', body: { auto_download: autoDownload } }),
            customLinks: (name, links) => request(`/api/radar/artists/${encodeURIComponent(name)}/custom-links`, { method: 'POST', body: { links } }),
            discography: (name) => request(`/api/radar/artists/${encodeURIComponent(name)}/discography`),
            scan: () => request('/api/radar/scan', { method: 'POST' }),
            scanProgress: () => request('/api/radar/progress'),
            cancelScan: () => request('/api/radar/cancel', { method: 'POST' }),
            setReleaseStatus: (id, status) => request(`/api/radar/releases/${encodeURIComponent(id)}/status`, { method: 'POST', body: { status } }),
        },

        // ==================== LYRICS ====================
        lyrics: {
            search: (artist, title) => request(`/lyrics/search${toQueryString({ artist, title })}`),
            save: (path, lyrics) => request('/lyrics', { method: 'POST', body: { path, lyrics } }),
            get: (path) => request(`/lyrics?path=${encodeURIComponent(path)}`),
        }
    };

    // Регистрируем глобально в window и/или экспортируем для сред CommonJS
    global.api = api;
    if (typeof module !== 'undefined' && module.exports) {
        module.exports = { api, request };
    }
})(typeof window !== 'undefined' ? window : this);
