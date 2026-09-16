/**
 * Модуль сверки неполных альбомов с каталогом Deezer и докачки треков (Audit Catalog Matcher).
 */
window.AuditCatalogMatcher = {
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
        const path = album.path;
        this.expandedAlbums[path] = !this.expandedAlbums[path];
        if (this.expandedAlbums[path] && !album.is_catalog_matched && !this.catalogMatchLoading[path]) {
            await this.matchCatalog(album);
        }
    },

    async matchCatalog(album) {
        const path = album.path;
        this.catalogMatchLoading[path] = true;
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
            this.catalogMatchLoading[path] = false;
        }
        return null;
    },

    async downloadMissing(album, trackUrl = null) {
        const path = album.path;
        let currentAlbum = album;

        if (!currentAlbum.is_catalog_matched || !currentAlbum.missing_tracks || currentAlbum.missing_tracks.length === 0) {
            const matched = await this.matchCatalog(currentAlbum);
            if (matched) {
                currentAlbum = matched;
            } else {
                const idx = this.incompleteAlbums.findIndex(a => a.path === path);
                if (idx !== -1) currentAlbum = this.incompleteAlbums[idx];
            }
        }

        if (!trackUrl && currentAlbum.is_catalog_matched && currentAlbum.expected_count > 0 && currentAlbum.expected_count <= currentAlbum.present_count && currentAlbum.missing_tracks && currentAlbum.missing_tracks.length === 0) {
            Alpine.store('app').toast('Все треки релиза уже присутствуют в медиатеке', 'info');
            return;
        }

        let directUrl = null;
        let trackNumber = null;
        if (typeof trackUrl === 'string') {
            directUrl = trackUrl;
        } else if (trackUrl && typeof trackUrl === 'object') {
            directUrl = trackUrl.deezer_url || null;
            trackNumber = trackUrl.track_number || null;
        }

        let trackUrls = null;
        if (directUrl) {
            trackUrls = [directUrl];
        }

        const trackKey = directUrl || (trackNumber ? `track_${trackNumber}` : null);
        const key = trackKey ? `${currentAlbum.path}:::${trackKey}` : currentAlbum.path;
        this.downloadingTracks[key] = true;
        try {
            const payload = {
                path: currentAlbum.path,
                deezer_album_id: currentAlbum.deezer_album_id || null,
                track_urls: trackUrls,
                track_numbers: trackNumber ? [trackNumber] : null,
            };
            const res = await fetch('/files/audit/incomplete-albums/download-missing', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(payload),
            });
            if (res.ok) {
                const jobs = await res.json();
                if (jobs && jobs.length > 0) {
                    Alpine.store('app').toast(`Поставлено в очередь загрузок: ${jobs.length}`, 'success');
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
                    if (currentAlbum.is_catalog_matched && (currentAlbum.expected_count <= currentAlbum.present_count || (currentAlbum.missing_tracks && currentAlbum.missing_tracks.length === 0))) {
                        Alpine.store('app').toast('Все треки релиза уже присутствуют в медиатеке', 'info');
                    } else {
                        Alpine.store('app').toast('Не найдено ссылок для скачивания недостающих треков в каталоге', 'warning');
                    }
                }
            } else {
                let errText = 'Ошибка отправки запроса на скачивание';
                try {
                    const errData = await res.json();
                    if (errData && errData.detail) errText = errData.detail;
                } catch (_) {}
                Alpine.store('app').toast(errText, 'error');
            }
        } catch (e) {
            console.error('Ошибка дозагрузки треков:', e);
            Alpine.store('app').toast('Сбой при запуске загрузки', 'error');
        } finally {
            this.downloadingTracks[key] = false;
        }
    },

    getBackfillJobForAlbum(albumPath) {
        const norm = (albumPath || '').replace(/\\/g, '/').replace(/^\/+|\/+$/g, '');
        return (Alpine.store('app').queue?.jobs || []).find((j) => {
            if (!j.is_backfill || !j.target_album_path) return false;
            const jNorm = j.target_album_path.replace(/\\/g, '/').replace(/^\/+|\/+$/g, '');
            return jNorm === norm && j.status === 'READY';
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
                    Alpine.store('app').openTagEditor(data);
                }
            } catch (e) {
                Alpine.store('app').toast('Не удалось открыть релиз в редакторе тегов', 'error');
            }
        }
    }
};
