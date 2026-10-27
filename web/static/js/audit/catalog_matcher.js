/**
 * Модуль сверки неполных альбомов с каталогом Deezer и докачки треков (Audit Catalog Matcher).
 */
window.AuditCatalogMatcher = {
    getAlbumTracks(album) {
        if (!album) return [];
        if (Array.isArray(album.all_tracks) && album.all_tracks.length > 0) {
            return album.all_tracks;
        }
        if (Array.isArray(album.missing_tracks) && album.missing_tracks.length > 0) {
            return album.missing_tracks;
        }
        return [];
    },

    isDownloading(album, track = null) {
        if (!album || !album.path) return false;
        const path = album.path;
        if (!track) {
            return !!this.downloadingTracks[path];
        }
        let directUrl = null;
        let trackNumber = null;
        if (typeof track === 'string') {
            directUrl = track;
        } else if (track && typeof track === 'object') {
            directUrl = track.deezer_url || null;
            trackNumber = track.track_number || null;
        }
        const trackKey = directUrl || (trackNumber ? `track_${trackNumber}` : null);
        const key = trackKey ? `${path}:::${trackKey}` : path;
        return !!this.downloadingTracks[key] || !!this.downloadingTracks[path];
    },

    async loadIncompleteAlbums() {
        this.incompleteLoading = true;
        try {
            const res = await fetch(`/files/audit/incomplete-albums?_t=${Date.now()}`);
            if (res.ok) {
                const data = await res.json();
                this.incompleteAlbums = data.albums || [];
            }
        } catch (e) {
            console.error('Ошибка загрузки неполных релизов:', e);
        } finally {
            this.incompleteLoading = false;
        }
    },

    async toggleAlbumExpand(album) {
        if (!album || !album.path) return;
        const path = album.path;
        this.expandedAlbums = { ...this.expandedAlbums, [path]: !this.expandedAlbums[path] };
        if (this.expandedAlbums[path] && !album.is_catalog_matched && !this.catalogMatchLoading[path]) {
            await this.matchCatalog(album);
        }
    },

    async matchCatalog(album) {
        if (!album || !album.path) return null;
        const path = album.path;
        this.catalogMatchLoading = { ...this.catalogMatchLoading, [path]: true };
        try {
            const url = `/files/audit/incomplete-albums/catalog-match?path=${encodeURIComponent(path)}&_t=${Date.now()}`;
            const res = await fetch(url);
            if (res.ok) {
                const matched = await res.json();
                const idx = this.incompleteAlbums.findIndex(a => a.path === path);
                if (idx !== -1) {
                    this.incompleteAlbums[idx] = matched;
                }
                return matched;
            } else {
                Alpine.store('app').toast('Не удалось найти треклист альбома в Deezer', 'warning');
            }
        } catch (e) {
            console.error('Ошибка сверки релиза с Deezer:', e);
            Alpine.store('app').toast('Ошибка при сверке с Deezer', 'error');
        } finally {
            this.catalogMatchLoading = { ...this.catalogMatchLoading, [path]: false };
        }
        return null;
    },

    async downloadMissing(album, track = null) {
        if (!album || !album.path) {
            console.warn('[Audit] downloadMissing called with invalid album:', album);
            return;
        }
        const path = album.path;
        console.log('[Audit] downloadMissing triggered for:', { path, album, track });

        let directUrl = null;
        let trackNumber = null;
        if (typeof track === 'string') {
            directUrl = track;
        } else if (track && typeof track === 'object') {
            directUrl = track.deezer_url || null;
            trackNumber = track.track_number || null;
        }

        const trackKey = directUrl || (trackNumber ? `track_${trackNumber}` : null);
        const key = trackKey ? `${path}:::${trackKey}` : path;

        // Включаем спиннер реактивно
        this.downloadingTracks = { ...this.downloadingTracks, [key]: true };

        try {
            let trackUrls = null;
            let trackNumbers = null;

            if (directUrl || trackNumber) {
                if (directUrl) trackUrls = [directUrl];
                if (trackNumber) trackNumbers = [trackNumber];
            } else {
                if (album.missing_tracks && album.missing_tracks.length > 0) {
                    trackNumbers = album.missing_tracks.map(t => t.track_number).filter(n => n > 0);
                    trackUrls = album.missing_tracks.map(t => t.deezer_url).filter(Boolean);
                } else if (album.missing_track_numbers && album.missing_track_numbers.length > 0) {
                    trackNumbers = album.missing_track_numbers;
                }
            }

            const payload = {
                path: album.path,
                deezer_album_id: album.deezer_album_id || null,
                track_urls: trackUrls && trackUrls.length > 0 ? trackUrls : null,
                track_numbers: trackNumbers && trackNumbers.length > 0 ? trackNumbers : null,
            };

            console.log('[Audit] Sending POST /files/audit/incomplete-albums/download-missing:', payload);

            const res = await fetch('/files/audit/incomplete-albums/download-missing', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(payload),
            });

            console.log('[Audit] Response status:', res.status);

            if (res.ok) {
                const jobs = await res.json();
                if (jobs && jobs.length > 0) {
                    Alpine.store('app').toast(`Дозагрузка поставлена в очередь (${jobs.length} задач)`, 'success');
                    if (Alpine.store('app').queue) {
                        if (Alpine.store('app').queue.wakeUpPolling) {
                            Alpine.store('app').queue.wakeUpPolling();
                        }
                        if (Alpine.store('app').queue.fetchQueue) {
                            await Alpine.store('app').queue.fetchQueue();
                        }
                        Alpine.store('app').queue.isOpen = true;
                    }
                } else {
                    Alpine.store('app').toast('Все треки уже скачаны или не найдены в каталоге', 'info');
                }
            } else {
                let errText = 'Ошибка отправки запроса на скачивание';
                try {
                    const errData = await res.json();
                    if (errData && (errData.error || errData.detail)) {
                        errText = errData.error || errData.detail;
                    }
                } catch (_) {}
                Alpine.store('app').toast(errText, 'error');
            }
        } catch (e) {
            console.error('[Audit] Ошибка дозагрузки треков:', e);
            Alpine.store('app').toast('Сбой при запуске загрузки: ' + (e.message || e), 'error');
        } finally {
            this.downloadingTracks = { ...this.downloadingTracks, [key]: false };
        }
    },

    getBackfillJobForAlbum(albumPath) {
        const norm = (albumPath || '').replace(/\\/g, '/').replace(/^\/+|\/+$/g, '');
        return (Alpine.store('app').queue?.jobs || []).find((j) => {
            if (!j.is_backfill || !j.target_album_path) return false;
            const jNorm = j.target_album_path.replace(/\\/g, '/').replace(/^\/+|\/+$/g, '');
            return jNorm === norm && (j.status === 'READY' || j.status === 'PARTIAL_READY');
        });
    },

    async openBackfillJob(albumPath) {
        const job = this.getBackfillJobForAlbum(albumPath);
        if (job) {
            await Alpine.store('app').queue.openBackfillJobInTagEditor(job);
        } else {
            try {
                const res = await fetch(`/files/audit/incomplete-albums/editor-data?path=${encodeURIComponent(albumPath)}`);
                if (res.ok) {
                    const data = await res.json();
                    Alpine.store('app').openTagEditor({
                        album: data.album || {},
                        tracks: data.tracks || [],
                        sourceDir: data.source_dir || data.sourceDir || albumPath,
                        coverPath: data.cover_path || data.coverPath || '',
                        hasCover: !!(data.has_cover || data.hasCover || data.cover_path),
                        jobIds: data.job_ids || data.jobIds || [],
                    });
                }
            } catch (e) {
                Alpine.store('app').toast('Не удалось открыть релиз в редакторе тегов', 'error');
            }
        }
    }
};
