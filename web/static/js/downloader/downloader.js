/**
 * Главный контроллер компонента загрузки аудио (Downloader UI).
 * Поддерживает одиночный и пакетный ввод ссылок, опрос прогресса задачи (polling),
 * переход в Tag Editor при статусе READY, а также подключение модулей торрентов и дискографий.
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
        discographyFilter: 'all',
        discographySearch: '',

        // Подмешиваем функционал специализированных модулей
        ...(window.DownloaderTorrent || {}),
        ...(window.DownloaderDiscography || {}),

        setMode(mode) {
            this.inputMode = mode;
        },

        async pasteUrl() {
            try {
                const text = window.AppClipboard ? await window.AppClipboard.readText() : null;
                if (text) {
                    if (text.includes('\n') || text.split(/https?:\/\/|magnet:/i).length > 2) {
                        this.inputMode = 'batch';
                        this.batchUrlsText = text;
                        Alpine.store('app').toast('Обнаружено несколько ссылок: включен пакетный режим', 'info');
                    } else {
                        this.url = text;
                    }
                }
            } catch (err) {
                Alpine.store('app').toast('Не удалось получить текст из буфера обмена', 'error');
            }
        },

        async pasteBatchUrls() {
            try {
                const text = window.AppClipboard ? await window.AppClipboard.readText() : null;
                if (text) {
                    if (this.batchUrlsText && this.batchUrlsText.trim()) {
                        this.batchUrlsText += '\n' + text;
                    } else {
                        this.batchUrlsText = text;
                    }
                    Alpine.store('app').toast('Ссылки вставлены из буфера обмена', 'info');
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

            if (cleanUrl.includes('\n') || cleanUrl.split(/https?:\/\/|magnet:/i).length > 2) {
                this.batchUrlsText = cleanUrl;
                this.url = '';
                this.inputMode = 'batch';
                return this.submitBatchDownloads();
            }

            if (!/^https?:\/\//i.test(cleanUrl) && !/^magnet:/i.test(cleanUrl) && cleanUrl.includes('.')) {
                cleanUrl = 'https://' + cleanUrl;
            }

            if (this.checkIsArtistUrl && this.checkIsArtistUrl(cleanUrl)) {
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
                    disc_total: data.disc_total || null,
                    track_total: data.track_total || null,
                    cover_url: data.cover_url || null,
                    cover_path: data.cover_path || `tmp_downloads/${jobId}/cover.jpg`,
                };

                const validTracks = (data.tracks || []).filter(
                    (t) => t.status !== 'error' && (t.converted_path || t.status === 'ok' || t.status === 'ready')
                );

                if (validTracks.length === 0) {
                    Alpine.store('app').toast('Нет успешно загруженных треков для редактирования тегов', 'warning');
                    return;
                }

                const coverPath = data.cover_path || `tmp_downloads/${jobId}/cover.jpg`;
                const coverPreview = data.cover_url || `/tags/cover?path=${encodeURIComponent(coverPath)}&t=${Date.now()}`;
                const hasCover = !!data.cover_url || !!data.cover_path;

                const tracks = validTracks.map((t) => ({
                    path: t.converted_path || `tmp_downloads/${jobId}/converted/${t.track_number}.mp3`,
                    track_number: t.track_number !== undefined && t.track_number !== null ? t.track_number : 1,
                    track_total: t.track_total || data.track_total || validTracks.length,
                    disc_number: t.disc_number || 1,
                    disc_total: t.disc_total || data.disc_total || 1,
                    title: t.title,
                    artist: t.artist,
                    album: t.album || album.album,
                    album_artist: t.album_artist || album.album_artist,
                    year: t.year || album.year,
                    genre: t.genre || album.genre,
                    duration: t.duration,
                    lyrics_status: 'none',
                    has_cover: hasCover,
                    cover_url: t.cover_url || data.cover_url || null,
                    cover_path: coverPath,
                    cover_preview_url: t.cover_url || coverPreview,
                    job_id: jobId,
                }));

                const sourceDir = `tmp_downloads/${jobId}/converted`;

                Alpine.store('app').openTagEditor({
                    album,
                    tracks,
                    sourceDir,
                    coverPath,
                    hasCover,
                    jobIds: [jobId],
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
        }
    };
}
