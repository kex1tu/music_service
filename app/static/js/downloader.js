/**
 * Компонент загрузчика аудио (Downloader UI).
 * Отвечает за отправку URL на скачивание, опрос прогресса задачи (polling)
 * и автоматический переход в Tag Editor при достижении статуса READY.
 */

function downloaderComponent() {
    return {
        inputMode: 'single', // 'single' | 'batch'
        url: '',
        batchUrlsText: '',
        source: 'auto',
        isSubmitting: false,
        job: null,
        pollInterval: null,

        // Состояние селектора дискографии
        showDiscographyModal: false,
        isResolvingDiscography: false,
        discographyData: null,
        selectedReleaseIds: {},
        discographyFilter: 'all', // 'all' | 'album' | 'single' | 'compilation'
        discographySearch: '',

        setMode(mode) {
            this.inputMode = mode;
        },

        async pasteUrl() {
            try {
                if (navigator.clipboard && navigator.clipboard.readText) {
                    const text = await navigator.clipboard.readText();
                    if (text) {
                        const trimmed = text.trim();
                        if (trimmed.includes('\n') || trimmed.split(/https?:\/\/|magnet:/i).length > 2) {
                            this.inputMode = 'batch';
                            this.batchUrlsText = trimmed;
                            Alpine.store('app').toast('Обнаружено несколько ссылок: включен пакетный режим', 'info');
                        } else {
                            this.url = trimmed;
                        }
                    }
                }
            } catch (err) {
                Alpine.store('app').toast('Не удалось получить текст из буфера обмена', 'error');
            }
        },

        async pasteBatchUrls() {
            try {
                if (navigator.clipboard && navigator.clipboard.readText) {
                    const text = await navigator.clipboard.readText();
                    if (text) {
                        const trimmed = text.trim();
                        if (this.batchUrlsText && this.batchUrlsText.trim()) {
                            this.batchUrlsText += '\n' + trimmed;
                        } else {
                            this.batchUrlsText = trimmed;
                        }
                        Alpine.store('app').toast('Ссылки вставлены из буфера обмена', 'info');
                    }
                }
            } catch (err) {
                Alpine.store('app').toast('Не удалось получить текст из буфера обмена', 'error');
            }
        },

        getParsedBatchUrls() {
            const raw = (this.batchUrlsText || '').trim();
            if (!raw) return [];

            const lines = raw.split(/[\r\n]+/);
            const urls = [];
            for (const line of lines) {
                const trimmed = line.trim();
                if (!trimmed) continue;

                // Если в одной строке несколько URL через пробел или запятую
                const tokens = trimmed.split(/[\s,]+/);
                if (
                    tokens.length > 1 &&
                    tokens.every((t) => /^https?:\/\//i.test(t) || /^magnet:/i.test(t) || t.includes('.'))
                ) {
                    urls.push(...tokens);
                } else {
                    urls.push(trimmed);
                }
            }
            return urls.filter((u) => u.length > 0);
        },

        get parsedBatchUrls() {
            return this.getParsedBatchUrls();
        },

        async submitBatchDownloads() {
            const rawUrls = this.getParsedBatchUrls();
            if (!rawUrls.length) {
                Alpine.store('app').toast('Введите хотя бы одну ссылку в список', 'error');
                return;
            }

            const urls = rawUrls.map((u) =>
                !/^https?:\/\//i.test(u) && !/^magnet:/i.test(u) && u.includes('.') ? 'https://' + u : u
            );

            this.isSubmitting = true;
            try {
                const response = await fetch('/downloads/batch', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({
                        urls: urls,
                        source: this.source,
                    }),
                });

                if (!response.ok) {
                    const err = await response.json();
                    throw new Error(err.message || err.error || 'Ошибка пакетной загрузки');
                }

                const batchData = await response.json();
                this.batchUrlsText = '';
                Alpine.store('app').toast(`Поставлено в очередь задач: ${batchData.total_queued}`, 'success');
                if (Alpine.store('app').queue) {
                    Alpine.store('app').queue.isOpen = true;
                    await Alpine.store('app').queue.fetchQueue();
                }
            } catch (err) {
                Alpine.store('app').toast(err.message, 'error');
            } finally {
                this.isSubmitting = false;
            }
        },

        async submitDownload() {
            if (this.inputMode === 'batch') {
                return this.submitBatchDownloads();
            }

            let cleanUrl = this.url.trim();
            if (!cleanUrl) {
                Alpine.store('app').toast('Введите корректный URL для загрузки', 'error');
                return;
            }

            // Автоматическое переключение в пакетный режим, если введено несколько строк или ссылок
            if (cleanUrl.includes('\n') || cleanUrl.split(/https?:\/\/|magnet:/i).length > 2) {
                this.batchUrlsText = cleanUrl;
                this.url = '';
                this.inputMode = 'batch';
                return this.submitBatchDownloads();
            }

            if (!/^https?:\/\//i.test(cleanUrl) && !/^magnet:/i.test(cleanUrl) && cleanUrl.includes('.')) {
                cleanUrl = 'https://' + cleanUrl;
            }

            // Если обнаружена ссылка на профиль артиста — открываем селектор дискографии
            if (this.checkIsArtistUrl(cleanUrl)) {
                return this.openDiscographyModal(cleanUrl);
            }

            this.isSubmitting = true;
            this.job = null;
            if (this.pollInterval) {
                clearInterval(this.pollInterval);
                this.pollInterval = null;
            }

            try {
                const response = await fetch('/downloads', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({
                        url: cleanUrl,
                        source: this.source,
                    }),
                });

                if (!response.ok) {
                    const err = await response.json();
                    throw new Error(err.message || 'Ошибка запуска задачи загрузки');
                }

                const jobData = await response.json();
                this.job = jobData;
                this.url = '';
                Alpine.store('app').currentJobId = jobData.id;
                Alpine.store('app').toast('Задача загрузки добавлена в очередь', 'info');

                // Запускаем опрос прогресса
                this.startPolling(jobData.id);
            } catch (err) {
                Alpine.store('app').toast(err.message, 'error');
            } finally {
                this.isSubmitting = false;
            }
        },

        async poll(jobId) {
            try {
                const res = await fetch(`/downloads/${jobId}`);
                if (!res.ok) {
                    if (res.status === 404) {
                        if (this.pollInterval) {
                            clearInterval(this.pollInterval);
                            this.pollInterval = null;
                        }
                        this.job = null;
                    }
                    return;
                }

                const currentJob = await res.json();
                this.job = currentJob;

                // Если задача готова — останавливаем опрос и загружаем треки для Tag Editor
                if (currentJob.status === 'READY') {
                    if (this.pollInterval) {
                        clearInterval(this.pollInterval);
                        this.pollInterval = null;
                    }
                    if (currentJob.total_tracks > 1 && currentJob.completed_tracks < currentJob.total_tracks) {
                        const skipped = currentJob.total_tracks - currentJob.completed_tracks;
                        Alpine.store('app').toast(
                            `Загрузка завершена: ${currentJob.completed_tracks} из ${currentJob.total_tracks} треков (пропущено: ${skipped})`,
                            'warning'
                        );
                    } else {
                        Alpine.store('app').toast('Загрузка завершена! Открываем редактор тегов...', 'success');
                    }
                    await this.openTagEditorForJob(jobId);
                } else if (currentJob.status === 'ERROR') {
                    if (this.pollInterval) {
                        clearInterval(this.pollInterval);
                        this.pollInterval = null;
                    }
                    Alpine.store('app').toast(
                        `Ошибка загрузки: ${currentJob.error || 'Неизвестная ошибка'}`,
                        'error'
                    );
                }
            } catch (e) {
                console.error('Сбой опроса задачи загрузки:', e);
            }
        },

        startPolling(jobId) {
            this.poll(jobId);
            this.pollInterval = setInterval(() => {
                this.poll(jobId);
            }, 1000);
        },

        async openTagEditorForJob(jobId) {
            try {
                const res = await fetch(`/downloads/${jobId}/tracks`);
                if (!res.ok) {
                    throw new Error('Не удалось получить подготовленные треки задачи');
                }

                const data = await res.json();

                const album = {
                    album_artist: data.album_artist || '',
                    album: data.album || '',
                    year: data.year || null,
                    genre: data.genre || '',
                    cover_url: data.cover_url || null,
                };

                const validTracks = (data.tracks || []).filter(
                    (t) => t.status !== 'error' && (t.converted_path || t.status === 'ok' || t.status === 'ready')
                );

                if (validTracks.length === 0) {
                    Alpine.store('app').toast('Нет успешно загруженных треков для редактирования тегов', 'warning');
                    return;
                }

                const tracks = validTracks.map((t) => ({
                    path: t.converted_path || `tmp_downloads/${jobId}/converted/${t.track_number}.mp3`,
                    track_number: t.track_number,
                    title: t.title,
                    artist: t.artist,
                    album: t.album || album.album,
                    album_artist: t.album_artist || album.album_artist,
                    year: t.year || album.year,
                    genre: t.genre || album.genre,
                    duration: t.duration,
                    lyrics_status: 'none',
                    has_cover: !!data.cover_url,
                }));

                const coverPath = `tmp_downloads/${jobId}/cover.jpg`;
                const sourceDir = `tmp_downloads/${jobId}/converted`;

                Alpine.store('app').openTagEditor({
                    album,
                    tracks,
                    sourceDir,
                    coverPath,
                    hasCover: !!data.cover_url,
                });
            } catch (err) {
                Alpine.store('app').toast(`Ошибка открытия редактора: ${err.message}`, 'error');
            }
        },

        async cancelJob() {
            if (!this.job) return;
            if (this.pollInterval) {
                clearInterval(this.pollInterval);
                this.pollInterval = null;
            }

            try {
                await fetch(`/downloads/${this.job.id}`, { method: 'DELETE' });
                Alpine.store('app').toast('Задача загрузки отменена', 'info');
            } catch (e) {
                console.error(e);
            } finally {
                this.job = null;
            }
        },

        resetForm() {
            if (this.pollInterval) {
                clearInterval(this.pollInterval);
                this.pollInterval = null;
            }
            this.job = null;
            this.url = '';
        },

        async uploadTorrentFile(event) {
            const file = event.target?.files?.[0] || event.dataTransfer?.files?.[0];
            if (!file) return;

            if (!file.name.toLowerCase().endsWith('.torrent')) {
                Alpine.store('app').toast('Допускаются только файлы .torrent', 'error');
                return;
            }

            this.isSubmitting = true;
            this.job = null;
            if (this.pollInterval) {
                clearInterval(this.pollInterval);
                this.pollInterval = null;
            }

            try {
                const formData = new FormData();
                formData.append('file', file);

                const response = await fetch('/downloads/torrent', {
                    method: 'POST',
                    body: formData,
                });

                if (!response.ok) {
                    const err = await response.json();
                    throw new Error(err.message || 'Ошибка загрузки .torrent файла');
                }

                const jobData = await response.json();
                this.job = jobData;
                Alpine.store('app').currentJobId = jobData.id;
                Alpine.store('app').toast(`Торрент "${file.name}" поставлен в очередь`, 'success');

                this.startPolling(jobData.id);
            } catch (err) {
                Alpine.store('app').toast(err.message, 'error');
            } finally {
                this.isSubmitting = false;
                // Сброс input[type=file]
                if (event.target && event.target.value) {
                    event.target.value = '';
                }
            }
        },

        handleTorrentDrop(event) {
            event.preventDefault();
            this.uploadTorrentFile(event);
        },

        handleTorrentDragOver(event) {
            event.preventDefault();
        },

        checkIsArtistUrl(url) {
            if (!url) return false;
            const u = url.trim();
            if (/deezer\.com\/(?:[a-zA-Z]{2}\/)?artist\/\d+/i.test(u)) return true;
            if (/spotify\.com\/(?:[a-zA-Z]{2}\/)?artist\/[a-zA-Z0-9]+/i.test(u)) return true;
            if (/music\.yandex\.[a-zA-Z.]+\/artist\/\d+/i.test(u)) return true;
            if (/music\.apple\.com\/[a-zA-Z]{2}\/artist\/(?:[^/]+\/)?\d+/i.test(u)) return true;
            if (/(?:music\.)?youtube\.com\/(?:channel\/|@|c\/)/i.test(u)) return true;
            return false;
        },

        async openDiscographyModal(targetUrl) {
            const cleanUrl = (targetUrl || this.url || '').trim();
            if (!cleanUrl) {
                Alpine.store('app').toast('Введите ссылку на исполнителя', 'error');
                return;
            }

            this.isResolvingDiscography = true;
            try {
                const resp = await fetch(`/downloads/discography?url=${encodeURIComponent(cleanUrl)}`);
                if (!resp.ok) {
                    const err = await resp.json();
                    throw new Error(err.detail || err.message || 'Не удалось получить дискографию');
                }
                const data = await resp.json();
                this.discographyData = data;
                this.selectedReleaseIds = {};
                if (data.releases) {
                    data.releases.forEach((r) => {
                        this.selectedReleaseIds[r.id] = true;
                    });
                }
                this.discographyFilter = 'all';
                this.discographySearch = '';
                this.showDiscographyModal = true;
                Alpine.store('app').toast(`Найдено релизов: ${data.total_releases}`, 'info');
            } catch (err) {
                Alpine.store('app').toast(err.message, 'error');
            } finally {
                this.isResolvingDiscography = false;
            }
        },

        closeDiscographyModal() {
            this.showDiscographyModal = false;
        },

        filteredReleases() {
            if (!this.discographyData || !this.discographyData.releases) return [];
            let list = this.discographyData.releases;
            if (this.discographyFilter !== 'all') {
                list = list.filter((r) => {
                    const t = (r.type || 'album').toLowerCase();
                    if (this.discographyFilter === 'album') return t === 'album';
                    if (this.discographyFilter === 'single') return t === 'single' || t === 'ep';
                    if (this.discographyFilter === 'compilation') return t === 'compilation' || t === 'compile';
                    return true;
                });
            }
            if (this.discographySearch && this.discographySearch.trim()) {
                const q = this.discographySearch.trim().toLowerCase();
                list = list.filter((r) => {
                    const titleMatch = (r.title || '').toLowerCase().includes(q);
                    const yearMatch = r.year ? String(r.year).includes(q) : false;
                    return titleMatch || yearMatch;
                });
            }
            return list;
        },

        toggleRelease(releaseId) {
            this.selectedReleaseIds[releaseId] = !this.selectedReleaseIds[releaseId];
        },

        selectAllFiltered(select = true) {
            const list = this.filteredReleases();
            list.forEach((r) => {
                this.selectedReleaseIds[r.id] = select;
            });
        },

        getSelectedCount() {
            if (!this.discographyData || !this.discographyData.releases) return 0;
            return this.discographyData.releases.filter((r) => this.selectedReleaseIds[r.id]).length;
        },

        async submitDiscographyBatch() {
            if (!this.discographyData || !this.discographyData.releases) return;
            const selectedUrls = this.discographyData.releases
                .filter((r) => this.selectedReleaseIds[r.id])
                .map((r) => r.url);

            if (selectedUrls.length === 0) {
                Alpine.store('app').toast('Выберите хотя бы один релиз для скачивания', 'error');
                return;
            }

            this.isSubmitting = true;
            try {
                const resp = await fetch('/downloads/batch', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({
                        urls: selectedUrls,
                        source: 'auto',
                    }),
                });
                if (!resp.ok) {
                    const err = await resp.json();
                    throw new Error(err.detail || err.message || 'Ошибка пакетной постановки задач');
                }
                const batchData = await resp.json();
                Alpine.store('app').toast(`Поставлено в очередь релизов: ${batchData.total_queued}`, 'success');
                this.closeDiscographyModal();
                this.url = '';
                if (Alpine.store('app').queue) {
                    Alpine.store('app').queue.isOpen = true;
                    await Alpine.store('app').queue.fetchQueue();
                }
            } catch (err) {
                Alpine.store('app').toast(err.message, 'error');
            } finally {
                this.isSubmitting = false;
            }
        },
    };
}
