/**
 * Подсистема управления очередью загрузок (Queue Manager).
 * Обеспечивает фоновый опрос задач, подсчет прогресса и открытие
 * завершенных релизов в двухуровневом редакторе тегов.
 */
window.createQueueManager = function() {
    return {
        isOpen: false,
        summary: {
            active_jobs: 0,
            queued_jobs: 0,
            completed_jobs: 0,
            error_jobs: 0,
            total_tracks: 0,
            completed_tracks: 0,
            overall_progress: 0.0,
        },
        jobs: [],
        pollTimeout: null,

        get activeCount() {
            return (this.summary?.active_jobs || 0) + (this.summary?.queued_jobs || 0);
        },
        get totalCount() {
            return this.summary?.total_tracks || 0;
        },
        get percent() {
            return Math.round(this.summary?.overall_progress || 0);
        },
        get readyCount() {
            return (this.jobs || []).filter((j) => !j.is_redownload && (j.status === 'READY' || j.status === 'PARTIAL_READY')).length;
        },
        get errorCount() {
            return (this.jobs || []).filter((j) => j.status === 'ERROR').length;
        },
        get completedCount() {
            return (this.jobs || []).filter((j) => j.status === 'READY' || j.status === 'PARTIAL_READY' || j.status === 'ERROR' || j.status === 'CANCELLED').length;
        },
        get isPaused() {
            return !!this.summary?.is_paused;
        },

        async init() {
            await this.fetchQueue();
            this.scheduleNextPoll();
        },

        async fetchQueue() {
            if (!Alpine.store('app')?.user) {
                return;
            }
            try {
                let sumData = null;
                let jobsData = null;

                if (window.api && window.api.downloader) {
                    try {
                        const [s, j] = await Promise.all([
                            window.api.downloader.queueSummary(),
                            window.api.downloader.list(),
                        ]);
                        sumData = s;
                        jobsData = j;
                    } catch (netErr) {
                        if (netErr.status === 401) {
                            Alpine.store('app').user = null;
                            return;
                        }
                        throw netErr;
                    }
                } else {
                    const [sumRes, jobsRes] = await Promise.all([
                        window.api.raw('/downloads/queue/summary'),
                        window.api.raw('/downloads'),
                    ]);
                    if (sumRes.status === 401 || jobsRes.status === 401) {
                        Alpine.store('app').user = null;
                        return;
                    }
                    if (sumRes.ok) sumData = await sumRes.json();
                    if (jobsRes.ok) jobsData = await jobsRes.json();
                }

                if (sumData) this.summary = sumData;
                if (jobsData) {
                    const data = jobsData;
                    let newJobs = Array.isArray(data) ? data : (data?.jobs || []);
                    const getStatusPriority = (st) => {
                        switch (st) {
                            case 'QUEUED':
                            case 'WAITING_SOURCE':
                                return 1; // Ожидают
                            case 'RESOLVING':
                            case 'DOWNLOADING':
                            case 'CONVERTING':
                            case 'TAGGING':
                                return 2; // Скачиваются / обрабатываются
                            case 'READY':
                            case 'PARTIAL_READY':
                                return 3; // Скачаны / Готовы частично
                            case 'ERROR':
                                return 4; // Ошибки
                            case 'CANCELLED':
                                return 5; // Отменены
                            default:
                                return 6;
                        }
                    };

                    newJobs.sort((a, b) => {
                        const pA = getStatusPriority(a.status);
                        const pB = getStatusPriority(b.status);
                        if (pA !== pB) {
                            return pA - pB;
                        }
                        const ca = a.created_at || '';
                        const cb = b.created_at || '';
                        if (ca !== cb) {
                            return ca > cb ? -1 : 1;
                        }
                        return (a.id || '').localeCompare(b.id || '');
                    });

                    if (!this.jobs || this.jobs.length === 0) {
                        this.jobs = newJobs;
                    } else {
                        const currentIds = this.jobs.map((j) => j.id).join(',');
                        const newIds = newJobs.map((j) => j.id).join(',');
                        if (currentIds !== newIds) {
                            this.jobs = newJobs;
                        } else {
                            const newMap = new Map(newJobs.map((j) => [j.id, j]));
                            for (let i = 0; i < this.jobs.length; i++) {
                                const fresh = newMap.get(this.jobs[i].id);
                                if (fresh) {
                                    Object.assign(this.jobs[i], fresh);
                                }
                            }
                        }
                    }
                }
            } catch (e) {
                console.error('Ошибка опроса очереди:', e);
            }
        },

        scheduleNextPoll() {
            if (this.pollTimeout) {
                clearTimeout(this.pollTimeout);
                this.pollTimeout = null;
            }
            const delay = this.activeCount > 0 ? 2000 : 10000;
            this.pollTimeout = setTimeout(async () => {
                try {
                    await this.fetchQueue();
                } finally {
                    this.scheduleNextPoll();
                }
            }, delay);
        },

        wakeUpPolling() {
            if (this.pollTimeout) {
                clearTimeout(this.pollTimeout);
                this.pollTimeout = null;
            }
            this.fetchQueue().then(() => {
                this.scheduleNextPoll();
            });
        },

        startPolling() {
            this.scheduleNextPoll();
        },

        async clearCompleted() {
            try {
                const data = (window.api && window.api.downloader)
                    ? await window.api.downloader.clearCompleted()
                    : await window.api.raw('/downloads/clear-completed', { method: 'POST' }).then(r => r.json());
                Alpine.store('app').toast(`Очищено завершенных задач: ${data?.cleared || data?.count || 0}`, 'info');
                await this.fetchQueue();
            } catch (e) {
                Alpine.store('app').toast('Ошибка очистки очереди', 'error');
            }
        },

        async pauseQueue() {
            try {
                if (window.api && window.api.downloader) {
                    await window.api.downloader.pause();
                } else {
                    await window.api.raw('/downloads/pause', { method: 'POST' });
                }
                Alpine.store('app').toast('Очередь загрузок приостановлена', 'info');
                await this.fetchQueue();
            } catch (e) {
                Alpine.store('app').toast('Ошибка приостановки очереди', 'error');
            }
        },

        async resumeQueue() {
            try {
                if (window.api && window.api.downloader) {
                    await window.api.downloader.resume();
                } else {
                    await window.api.raw('/downloads/resume', { method: 'POST' });
                }
                Alpine.store('app').toast('Очередь загрузок возобновлена', 'success');
                this.wakeUpPolling();
            } catch (e) {
                Alpine.store('app').toast('Ошибка возобновления очереди', 'error');
            }
        },

        async retryAllFailed() {
            try {
                const data = (window.api && window.api.downloader)
                    ? await window.api.downloader.retryFailed()
                    : await window.api.raw('/downloads/retry-failed', { method: 'POST' }).then(r => r.json());
                Alpine.store('app').toast(`Перезапущено задач с ошибкой: ${data?.retried_count || data?.count || 0}`, 'info');
                this.wakeUpPolling();
            } catch (e) {
                Alpine.store('app').toast(e.message || 'Ошибка перезапуска задач', 'error');
            }
        },

        async cancelJob(jobId) {
            try {
                if (window.api && window.api.downloader) {
                    await window.api.downloader.delete(jobId);
                } else {
                    await window.api.raw(`/downloads/${jobId}`, { method: 'DELETE' });
                }
                Alpine.store('app').toast('Задача отменена', 'info');
                await this.fetchQueue();
            } catch (e) {
                Alpine.store('app').toast(e.message || 'Ошибка отмены задачи', 'error');
            }
        },

        async retryJob(jobId) {
            try {
                if (window.api && window.api.downloader) {
                    await window.api.downloader.retry(jobId);
                } else {
                    await window.api.raw(`/downloads/${jobId}/retry`, { method: 'POST' });
                }
                Alpine.store('app').toast('Задача перезапущена', 'info');
                this.wakeUpPolling();
            } catch (e) {
                Alpine.store('app').toast(e.message || 'Ошибка перезапуска задачи', 'error');
            }
        },

        openJobInTagEditor(jobId) {
            if (window.QueueEditorBridge) {
                return window.QueueEditorBridge.openJobInTagEditor(this, jobId);
            }
        },

        openBackfillJobInTagEditor(job) {
            if (window.QueueEditorBridge) {
                return window.QueueEditorBridge.openBackfillJobInTagEditor(this, job);
            }
        },

        openAllReadyJobsInTagEditor() {
            if (window.QueueEditorBridge) {
                return window.QueueEditorBridge.openAllReadyJobsInTagEditor(this);
            }
        },

        acceptJobDirectly(jobId) {
            if (window.DownloaderPolling) {
                return window.DownloaderPolling.acceptJobDirectly(jobId);
            }
        },

        async acceptAllReadyJobs() {
            const readyJobs = (this.jobs || []).filter(j => !j.is_redownload && (j.status === 'READY' || j.status === 'PARTIAL_READY'));
            if (readyJobs.length === 0) {
                Alpine.store('app').toast('Нет готовых релизов для принятия', 'warning');
                return;
            }

            Alpine.store('app').toast(`Принятие готовых релизов (${readyJobs.length})...`, 'info');
            let accepted = 0;
            for (const j of readyJobs) {
                try {
                    if (window.DownloaderPolling && window.DownloaderPolling.acceptJobDirectly) {
                        await window.DownloaderPolling.acceptJobDirectly(j.id);
                        accepted++;
                    }
                } catch (err) {
                    console.error(`Ошибка принятия релиза ${j.id}:`, err);
                }
            }
            Alpine.store('app').toast(`Успешно принято релизов: ${accepted}`, 'success');
            await this.fetchQueue();
        },
    };
};
