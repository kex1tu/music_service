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
            return (this.jobs || []).filter((j) => j.status === 'READY').length;
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
                const [sumRes, jobsRes] = await Promise.all([
                    fetch('/downloads/queue/summary'),
                    fetch('/downloads'),
                ]);
                if (sumRes.status === 401 || jobsRes.status === 401) {
                    Alpine.store('app').user = null;
                    return;
                }
                if (sumRes.ok) this.summary = await sumRes.json();
                if (jobsRes.ok) {
                    const data = await jobsRes.json();
                    let newJobs = Array.isArray(data) ? data : (data?.jobs || []);
                    const getStatusPriority = (st) => {
                        switch (st) {
                            case 'QUEUED':
                                return 1; // Ожидают
                            case 'RESOLVING':
                            case 'DOWNLOADING':
                            case 'CONVERTING':
                            case 'TAGGING':
                                return 2; // Скачиваются / обрабатываются
                            case 'READY':
                                return 3; // Скачаны
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
            if (this.activeCount > 0 && this.pollTimeout) {
                this.scheduleNextPoll();
            }
        },

        scheduleNextPoll() {
            if (this.pollTimeout) clearTimeout(this.pollTimeout);
            const delay = this.activeCount > 0 ? 2000 : 10000;
            this.pollTimeout = setTimeout(async () => {
                await this.fetchQueue();
                this.scheduleNextPoll();
            }, delay);
        },

        wakeUpPolling() {
            if (this.pollTimeout) clearTimeout(this.pollTimeout);
            this.fetchQueue().then(() => {
                this.scheduleNextPoll();
            });
        },

        startPolling() {
            this.scheduleNextPoll();
        },

        async clearCompleted() {
            try {
                const res = await fetch('/downloads/queue/clear', { method: 'POST' });
                if (res.ok) {
                    const data = await res.json();
                    Alpine.store('app').toast(`Очищено завершенных задач: ${data.count || 0}`, 'info');
                    await this.fetchQueue();
                }
            } catch (e) {
                Alpine.store('app').toast('Ошибка очистки очереди', 'error');
            }
        },

        async cancelJob(jobId) {
            try {
                const res = await fetch(`/downloads/${jobId}`, { method: 'DELETE' });
                if (res.ok) {
                    Alpine.store('app').toast('Задача отменена', 'info');
                    await this.fetchQueue();
                }
            } catch (e) {
                Alpine.store('app').toast('Ошибка отмены задачи', 'error');
            }
        },

        async retryJob(jobId) {
            try {
                const res = await fetch(`/downloads/${jobId}/retry`, { method: 'POST' });
                if (res.ok) {
                    Alpine.store('app').toast('Задача перезапущена', 'info');
                    this.wakeUpPolling();
                } else {
                    const data = await res.json();
                    Alpine.store('app').toast(data.error || 'Ошибка перезапуска задачи', 'error');
                }
            } catch (e) {
                Alpine.store('app').toast('Ошибка перезапуска задачи', 'error');
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
    };
};
