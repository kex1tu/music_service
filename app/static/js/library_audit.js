/**
 * Компонент аудита медиатеки (Library Audit UI).
 * Отвечает за отображение дашборда метрик целостности, пакетный автопоиск текстов,
 * просмотр релизов без обложек и безопасное удаление дубликатов.
 */

function libraryAuditComponent() {
    return {
        activeSubTab: 'lyrics', // 'lyrics' | 'covers' | 'duplicates' | 'incomplete' | 'paths' | 'genres'
        isLoading: false,
        isReorganizing: false,
        isRetaggingGenres: false,
        isFixingPaths: false,
        summary: {
            total_tracks: 0,
            total_albums: 0,
            missing_lyrics_count: 0,
            missing_covers_count: 0,
            duplicates_count: 0,
            incomplete_albums_count: 0,
            invalid_paths_count: 0,
            non_en_genres_count: 0,
        },
        missingLyrics: [],
        missingCovers: [],
        duplicatesData: { groups: [], total_duplicate_files: 0 },
        incompleteAlbums: [],
        invalidPaths: [],
        nonEnGenres: [],
        incompleteLoading: false,
        isFixingDuplicates: false,
        expandedAlbums: {},
        catalogMatchLoading: {},
        downloadingTracks: {},
        lyricsProgress: {
            is_running: false,
            total: 0,
            current: 0,
            found: 0,
            failed: 0,
            current_track: '',
        },
        lyricsPollInterval: null,

        init() {
            this.loadAll();
            this.checkLyricsProgress();

            // Слушаем событие подтверждения реорганизации
            window.addEventListener('confirm-reorganize-library', () => {
                this.reorganizeLibrary();
            });

            // Слушаем переход к сверке альбома из файлового менеджера
            window.addEventListener('match-album-audit', (e) => {
                const path = e?.detail?.path;
                if (path) {
                    this.activeSubTab = 'incomplete';
                    this.$nextTick(async () => {
                        const alb = this.incompleteAlbums.find((a) => a.path === path) || {
                            path,
                            album: path.split('/').pop() || path,
                        };
                        this.expandedAlbums[path] = true;
                        await this.matchCatalog(alb);
                    });
                }
            });

            // Слушаем событие обновления при переключении на вкладку или клике
            window.addEventListener('refresh-audit', (e) => {
                const force = e?.detail?.force || false;
                this.loadAll(force);
                this.checkLyricsProgress();
            });

            // Автоматически обновляем данные при активации вкладки Аудит
            if (this.$watch) {
                this.$watch('$store.app.activeTab', (tab) => {
                    if (tab === 'audit') {
                        this.loadAll();
                        this.checkLyricsProgress();
                    }
                });
            }
        },

        async loadAll(force = false) {
            this.isLoading = true;
            try {
                if (force) {
                    try {
                        await fetch('/files/audit/refresh', { method: 'POST' });
                    } catch (e) {
                        console.warn('Сбой принудительного обновления индекса:', e);
                    }
                }
                await Promise.all([
                    this.loadSummary(),
                    this.loadMissingLyrics(),
                    this.loadMissingCovers(),
                    this.loadDuplicates(),
                    this.loadIncompleteAlbums(),
                    this.loadInvalidPaths(),
                    this.loadNonEnGenres(),
                ]);
            } finally {
                this.isLoading = false;
            }
        },

        async loadSummary() {
            try {
                const res = await fetch(`/files/audit/summary?_t=${Date.now()}`);
                if (res.ok) {
                    this.summary = await res.json();
                }
            } catch (e) {
                console.error('Ошибка загрузки сводки аудита:', e);
            }
        },

        async loadMissingLyrics() {
            try {
                const res = await fetch(`/files/audit/missing-lyrics?_t=${Date.now()}`);
                if (res.ok) {
                    this.missingLyrics = await res.json();
                }
            } catch (e) {
                console.error('Ошибка загрузки треков без слов:', e);
            }
        },

        async loadMissingCovers() {
            try {
                const res = await fetch(`/files/audit/missing-covers?_t=${Date.now()}`);
                if (res.ok) {
                    this.missingCovers = await res.json();
                }
            } catch (e) {
                console.error('Ошибка загрузки релизов без обложек:', e);
            }
        },

        async loadDuplicates() {
            try {
                const res = await fetch(`/files/audit/duplicates?_t=${Date.now()}`);
                if (res.ok) {
                    this.duplicatesData = await res.json();
                }
            } catch (e) {
                console.error('Ошибка загрузки дубликатов:', e);
            }
        },

        async loadInvalidPaths() {
            try {
                const res = await fetch(`/files/audit/invalid-paths?_t=${Date.now()}`);
                if (res.ok) {
                    this.invalidPaths = await res.json();
                }
            } catch (e) {
                console.error('Ошибка загрузки некорректных путей:', e);
            }
        },

        async loadNonEnGenres() {
            try {
                const res = await fetch(`/files/audit/non-en-genres?_t=${Date.now()}`);
                if (res.ok) {
                    this.nonEnGenres = await res.json();
                }
            } catch (e) {
                console.error('Ошибка загрузки не-EN жанров:', e);
            }
        },

        async startBatchLyricsFetch() {
            if (this.lyricsProgress.is_running) return;
            try {
                const res = await fetch('/files/audit/missing-lyrics/fetch-all', { method: 'POST' });
                if (res.ok) {
                    this.lyricsProgress = await res.json();
                    Alpine.store('app').toast('Запущен пакетный поиск текстов песен', 'info');
                    this.startLyricsPolling();
                }
            } catch (e) {
                Alpine.store('app').toast('Ошибка запуска автопоиска текстов', 'error');
            }
        },

        async checkLyricsProgress() {
            try {
                const res = await fetch('/files/audit/missing-lyrics/progress');
                if (res.ok) {
                    const data = await res.json();
                    this.lyricsProgress = data;
                    if (data.is_running && !this.lyricsPollInterval) {
                        this.startLyricsPolling();
                    }
                }
            } catch (e) {
                console.error(e);
            }
        },

        startLyricsPolling() {
            if (this.lyricsPollInterval) clearInterval(this.lyricsPollInterval);
            this.lyricsPollInterval = setInterval(async () => {
                try {
                    const res = await fetch('/files/audit/missing-lyrics/progress');
                    if (res.ok) {
                        const data = await res.json();
                        this.lyricsProgress = data;
                        if (!data.is_running) {
                            clearInterval(this.lyricsPollInterval);
                            this.lyricsPollInterval = null;
                            Alpine.store('app').toast(
                                `Поиск текстов завершен: найдено ${data.found}, не найдено ${data.failed}`,
                                'success'
                            );
                            await this.loadSummary();
                            await this.loadMissingLyrics();
                        }
                    }
                } catch (e) {
                    console.error(e);
                }
            }, 1000);
        },

        openLyricsForTrack(track) {
            Alpine.store('app').openLyricsModal(track);
        },

        openDirectory(path) {
            Alpine.store('app').activeTab = 'files';
            window.dispatchEvent(new CustomEvent('refresh-file-manager', { detail: path }));
        },

        promptDeleteDuplicate(track) {
            Alpine.store('app').deleteConfirmData = {
                path: track.path,
                name: track.filename,
                type: 'file',
                onDeleted: async () => {
                    await this.loadSummary();
                    await this.loadDuplicates();
                },
            };
            Alpine.store('app').activeModal = 'delete_confirm';
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

            // 1. Если релиз еще не был сверен с Deezer, сначала выполняем сверку
            if (!currentAlbum.is_catalog_matched) {
                const matched = await this.matchCatalog(currentAlbum);
                if (matched) {
                    currentAlbum = matched;
                } else {
                    const idx = this.incompleteAlbums.findIndex(a => a.path === path);
                    if (idx !== -1) currentAlbum = this.incompleteAlbums[idx];
                }
            }

            // 2. Если после сверки все треки релиза уже есть
            if (currentAlbum.is_catalog_matched && (currentAlbum.expected_count <= currentAlbum.present_count || !currentAlbum.missing_tracks || currentAlbum.missing_tracks.length === 0)) {
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

        confirmReorganizeLibrary() {
            Alpine.store('app').activeModal = 'reorganize_confirm';
        },

        async reorganizeLibrary() {
            if (this.isReorganizing) return;
            this.isReorganizing = true;
            Alpine.store('app').activeModal = null;
            Alpine.store('app').toast('Запущена реорганизация медиатеки по альбомам...', 'info');
            try {
                const res = await fetch('/files/audit/reorganize', { method: 'POST' });
                if (res.ok) {
                    const data = await res.json();
                    if (data.errors && data.errors.length > 0) {
                        Alpine.store('app').toast(
                            `Реорганизация завершена с предупреждениями: перемещено ${data.moved_tracks} треков из ${data.total_tracks}`,
                            'warning'
                        );
                    } else {
                        Alpine.store('app').toast(
                            `Реорганизация завершена: перемещено ${data.moved_tracks} из ${data.total_tracks} треков (${data.total_albums} альбомов)`,
                            'success'
                        );
                    }
                    await this.loadAll(false);
                    window.dispatchEvent(new CustomEvent('refresh-file-manager', { detail: '' }));
                } else {
                    let msg = 'Ошибка при реорганизации медиатеки';
                    try {
                        const errData = await res.json();
                        if (errData && errData.detail) msg = errData.detail;
                    } catch (_) {}
                    Alpine.store('app').toast(msg, 'error');
                }
            } catch (e) {
                console.error('Ошибка реорганизации:', e);
                Alpine.store('app').toast('Сбой сетевого запроса при реорганизации', 'error');
            } finally {
                this.isReorganizing = false;
            }
        },

        async autoRetagGenres() {
            if (this.isRetaggingGenres) return;
            this.isRetaggingGenres = true;
            Alpine.store('app').toast('Запущена автоматическая перезапись тегов жанров...', 'info');
            try {
                const res = await fetch('/files/audit/retag-genres', { method: 'POST' });
                if (res.ok) {
                    const data = await res.json();
                    if (data.updated_count > 0) {
                        Alpine.store('app').toast(`Успешно перезаписаны теги у ${data.updated_count} треков`, 'success');
                    } else {
                        Alpine.store('app').toast('Все жанры уже соответствуют каноническому стандарту', 'info');
                    }
                    await this.loadNonEnGenres();
                    await this.loadSummary();
                } else {
                    let msg = 'Ошибка при перезаписи тегов жанров';
                    try {
                        const errData = await res.json();
                        if (errData && errData.detail) msg = errData.detail;
                    } catch (_) {}
                    Alpine.store('app').toast(msg, 'error');
                }
            } catch (e) {
                console.error('Ошибка автоперезаписи жанров:', e);
                Alpine.store('app').toast('Сбой сетевого запроса при перезаписи жанров', 'error');
            } finally {
                this.isRetaggingGenres = false;
            }
        },

        async fixPath(item) {
            if (item._isFixing) return;
            item._isFixing = true;
            try {
                const res = await fetch('/files/audit/fix-path', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ path: item.path, overwrite: true })
                });
                if (res.ok) {
                    const data = await res.json();
                    Alpine.store('app').toast(`Файл перемещен в: ${data.target_path}`, 'success');
                    await this.loadInvalidPaths();
                    await this.loadSummary();
                    window.dispatchEvent(new CustomEvent('refresh-file-manager', { detail: '' }));
                } else {
                    let msg = 'Ошибка исправления пути';
                    try {
                        const err = await res.json();
                        if (err && err.detail) msg = err.detail;
                    } catch (_) {}
                    Alpine.store('app').toast(msg, 'error');
                }
            } catch (e) {
                console.error(e);
                Alpine.store('app').toast('Сбой сетевого запроса при исправлении пути', 'error');
            } finally {
                item._isFixing = false;
            }
        },

        async fixAllPaths() {
            if (this.isFixingPaths) return;
            this.isFixingPaths = true;
            Alpine.store('app').toast('Исправление путей всех некорректных файлов...', 'info');
            try {
                const res = await fetch('/files/audit/fix-all-paths', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ overwrite: true })
                });
                if (res.ok) {
                    const data = await res.json();
                    if (data.errors && data.errors.length > 0) {
                        Alpine.store('app').toast(
                            `Исправлено путей: ${data.fixed_count} из ${data.total_invalid} (с ошибками: ${data.errors.length})`,
                            'warning'
                        );
                    } else {
                        Alpine.store('app').toast(`Успешно исправлено путей: ${data.fixed_count} из ${data.total_invalid}`, 'success');
                    }
                    await this.loadInvalidPaths();
                    await this.loadSummary();
                    window.dispatchEvent(new CustomEvent('refresh-file-manager', { detail: '' }));
                } else {
                    let msg = 'Ошибка при пакетном исправлении путей';
                    try {
                        const err = await res.json();
                        if (err && err.detail) msg = err.detail;
                    } catch (_) {}
                    Alpine.store('app').toast(msg, 'error');
                }
            } catch (e) {
                console.error(e);
                Alpine.store('app').toast('Сбой сетевого запроса при исправлении путей', 'error');
            } finally {
                this.isFixingPaths = false;
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
        },

        async fixDuplicateGroup(grp) {
            if (grp._isFixing) return;
            grp._isFixing = true;
            Alpine.store('app').toast(`Удаление дубликатов для "${grp.artist} — ${grp.title}"...`, 'info');
            try {
                const res = await fetch('/files/audit/duplicates/fix', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ group_key: grp.key, album_path: grp.album_dir || null }),
                });
                if (res.ok) {
                    const data = await res.json();
                    Alpine.store('app').toast(`Успешно удалено дубликатов: ${data.deleted_count}`, 'success');
                    await this.loadDuplicates();
                    await this.loadSummary();
                    window.dispatchEvent(new CustomEvent('refresh-file-manager', { detail: '' }));
                } else {
                    let msg = 'Ошибка при удалении дубликатов';
                    try {
                        const err = await res.json();
                        if (err && err.detail) msg = err.detail;
                    } catch (_) {}
                    Alpine.store('app').toast(msg, 'error');
                }
            } catch (e) {
                console.error(e);
                Alpine.store('app').toast('Сетевая ошибка при удалении дубликатов', 'error');
            } finally {
                grp._isFixing = false;
            }
        },

        async fixAllDuplicates() {
            if (this.isFixingDuplicates) return;
            this.isFixingDuplicates = true;
            Alpine.store('app').toast('Удаление всех дубликатов в медиатеке...', 'info');
            try {
                const res = await fetch('/files/audit/duplicates/fix-all', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                });
                if (res.ok) {
                    const data = await res.json();
                    Alpine.store('app').toast(`Успешно удалено избыточных файлов: ${data.deleted_count}`, 'success');
                    await this.loadDuplicates();
                    await this.loadSummary();
                    window.dispatchEvent(new CustomEvent('refresh-file-manager', { detail: '' }));
                } else {
                    let msg = 'Ошибка при удалении дубликатов';
                    try {
                        const err = await res.json();
                        if (err && err.detail) msg = err.detail;
                    } catch (_) {}
                    Alpine.store('app').toast(msg, 'error');
                }
            } catch (e) {
                console.error(e);
                Alpine.store('app').toast('Сетевая ошибка при удалении всех дубликатов', 'error');
            } finally {
                this.isFixingDuplicates = false;
            }
        },
    };
}


