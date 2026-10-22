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
        ...(window.DownloaderVersions || {}),

        init() {
            window.addEventListener('job-created', (e) => {
                if (e.detail && e.detail.job) {
                    this.job = e.detail.job;
                    if (this.url === e.detail.url) {
                        this.url = '';
                    }
                    this.startPolling(e.detail.job.id);
                }
            });
        },

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
            if (window.DownloaderBatch) {
                return window.DownloaderBatch.pasteBatchUrls(this);
            }
        },

        getParsedBatchUrls() {
            if (window.DownloaderBatch) {
                return window.DownloaderBatch.getParsedBatchUrls(this);
            }
            return [];
        },

        get parsedBatchUrls() {
            return this.getParsedBatchUrls();
        },

        async submitBatchDownloads() {
            if (window.DownloaderBatch) {
                return window.DownloaderBatch.submitBatchDownloads(this);
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

            // Pre-flight check:
            // Для альбомов и плейлистов (>1 трека) открываем предпросмотр треклиста автоматически.
            // Для синглов проверяем наличие дубликата в 320 kbps.
            if (!cleanUrl.startsWith('magnet:') && this.source !== 'torrent') {
                try {
                    const preResp = await fetch('/downloads/preflight', {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({
                            url: cleanUrl,
                            source: this.source,
                        }),
                    });

                    if (preResp.ok) {
                        const preData = await preResp.json();
                        const tracks = preData.tracks || [];
                        const preStore = Alpine.store('preflight');

                        if (tracks.length > 1 && preStore) {
                            // Альбом или плейлист: открываем модалку предпросмотра
                            this.isSubmitting = false;
                            preStore.setPreflightData(cleanUrl, this.source, preData);
                            return;
                        } else if (tracks.length === 1 && tracks[0].status === 'EXISTS_HIGH_QUALITY' && preStore) {
                            // Сингл уже есть в медиатеке в 320k: предупреждаем и открываем окно подтверждения
                            this.isSubmitting = false;
                            Alpine.store('app').toast('Этот трек уже есть в медиатеке в 320 kbps. Открыт предпросмотр для подтверждения.', 'warning');
                            preStore.setPreflightData(cleanUrl, this.source, preData);
                            return;
                        }
                    }
                } catch (e) {
                    console.warn('[Downloader] Preflight check error, falling back to direct download:', e);
                }
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

        poll(jobId) {
            if (window.DownloaderPolling) {
                return window.DownloaderPolling.poll(this, jobId);
            }
        },

        startPolling(jobId) {
            if (window.DownloaderPolling) {
                return window.DownloaderPolling.startPolling(this, jobId);
            }
        },

        async openTagEditorForJob(jobId) {
            if (window.DownloaderPolling) {
                return window.DownloaderPolling.openTagEditorForJob(jobId);
            }
        },

        async acceptJobDirectly(jobId) {
            if (window.DownloaderPolling) {
                return window.DownloaderPolling.acceptJobDirectly(jobId);
            }
        },

        async retryJob(jobId) {
            const targetId = jobId || (this.job ? this.job.id : null);
            if (!targetId) return;

            this.isSubmitting = true;
            try {
                const response = await fetch(`/downloads/${targetId}/retry`, {
                    method: 'POST',
                });
                if (!response.ok) {
                    const err = await response.json();
                    throw new Error(err.error || 'Ошибка перезапуска задачи');
                }
                const jobData = await response.json();
                this.job = jobData;
                Alpine.store('app').currentJobId = jobData.id;
                Alpine.store('app').toast('Задача перезапущена', 'info');
                this.startPolling(jobData.id);
            } catch (err) {
                Alpine.store('app').toast(err.message, 'error');
            } finally {
                this.isSubmitting = false;
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
