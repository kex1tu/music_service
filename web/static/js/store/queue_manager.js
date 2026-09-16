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

        async openJobInTagEditor(jobId) {
            try {
                const res = await fetch(`/downloads/${jobId}/tracks`);
                if (!res.ok) throw new Error('Не удалось получить треки релиза');
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
                    Alpine.store('app').toast('Нет готовых треков для редактирования', 'warning');
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

                Alpine.store('app').openTagEditor({
                    album,
                    tracks,
                    sourceDir: `tmp_downloads/${jobId}/converted`,
                    coverPath: coverPath,
                    hasCover: hasCover,
                    jobIds: [jobId],
                });
                this.isOpen = false;
            } catch (err) {
                Alpine.store('app').toast(err.message, 'error');
            }
        },

        async openBackfillJobInTagEditor(job) {
            try {
                const albumPath = job.target_album_path;
                if (!albumPath) {
                    return this.openJobInTagEditor(job.id);
                }
                const res = await fetch(`/files/audit/incomplete-albums/editor-data?path=${encodeURIComponent(albumPath)}&job_id=${encodeURIComponent(job.id)}`);
                if (!res.ok) throw new Error('Не удалось загрузить объединенные данные релиза');
                const data = await res.json();
                const album = {
                    album_artist: data.album?.album_artist || job.artist_name || '',
                    album: data.album?.album || job.album_title || '',
                    year: data.album?.year || null,
                    genre: data.album?.genre || '',
                    disc_total: data.album?.disc_total || null,
                    track_total: data.album?.track_total || null,
                    cover_url: null,
                    cover_path: data.cover_path || null,
                };
                const coverPreview = data.cover_path ? `/tags/cover?path=${encodeURIComponent(data.cover_path)}&t=${Date.now()}` : null;
                const tracks = (data.tracks || []).map((t) => ({
                    ...t,
                    cover_preview_url: t.cover_url || coverPreview,
                    job_id: job.id,
                }));
                Alpine.store('app').openTagEditor({
                    album,
                    tracks,
                    sourceDir: albumPath,
                    coverPath: data.cover_path,
                    hasCover: data.has_cover,
                    jobIds: [job.id],
                });
                this.isOpen = false;
                Alpine.store('app').toast(`Релиз "${album.album}" открыт в редакторе (всего треков: ${tracks.length})`, 'info');
            } catch (err) {
                Alpine.store('app').toast(err.message, 'error');
            }
        },

        async openAllReadyJobsInTagEditor() {
            const readyJobs = (this.jobs || []).filter((j) => j.status === 'READY');
            if (readyJobs.length === 0) {
                Alpine.store('app').toast('В очереди нет завершенных релизов для редактирования', 'warning');
                return;
            }

            Alpine.store('app').toast(`Загрузка данных для ${readyJobs.length} релиз(ов)...`, 'info');

            try {
                const responses = await Promise.all(
                    readyJobs.map((j) =>
                        fetch(`/downloads/${j.id}/tracks`).then((r) => (r.ok ? r.json() : null))
                    )
                );

                let allTracks = [];
                let allJobIds = [];
                let firstAlbum = null;
                let firstCoverPath = null;
                let hasAnyCover = false;

                responses.forEach((data, index) => {
                    if (!data) return;
                    const jobId = readyJobs[index].id;
                    allJobIds.push(jobId);

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

                    if (!firstAlbum && (album.album || album.album_artist)) {
                        firstAlbum = album;
                        firstCoverPath = album.cover_path;
                    }

                    const validTracks = (data.tracks || []).filter(
                        (t) => t.status !== 'error' && (t.converted_path || t.status === 'ok' || t.status === 'ready')
                    );

                    const defaultCoverPath = data.cover_path || `tmp_downloads/${jobId}/cover.jpg`;
                    const defaultCoverPreview = data.cover_url || `/tags/cover?path=${encodeURIComponent(defaultCoverPath)}&t=${Date.now()}`;
                    const jobHasCover = !!data.cover_url || !!data.cover_path;
                    if (jobHasCover) hasAnyCover = true;

                    validTracks.forEach((t) => {
                        const trkCoverUrl = t.cover_url || data.cover_url || null;
                        const trkCoverPath = defaultCoverPath;
                        const trkHasCover = !!trkCoverUrl || jobHasCover;

                        allTracks.push({
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
                            has_cover: trkHasCover,
                            cover_url: trkCoverUrl,
                            cover_path: trkCoverPath,
                            cover_preview_url: trkCoverUrl || defaultCoverPreview,
                            job_id: jobId,
                        });
                    });
                });

                if (allTracks.length === 0) {
                    Alpine.store('app').toast('Нет готовых треков для редактирования', 'warning');
                    return;
                }

                Alpine.store('app').openTagEditor({
                    album: firstAlbum || {
                        album_artist: '',
                        album: '',
                        year: null,
                        genre: '',
                    },
                    tracks: allTracks,
                    sourceDir: 'tmp_downloads',
                    coverPath: firstCoverPath || (allTracks[0]?.cover_path || null),
                    hasCover: hasAnyCover,
                    jobIds: allJobIds,
                });
                this.isOpen = false;
                Alpine.store('app').toast(`Открыто релизов: ${allJobIds.length}, треков: ${allTracks.length}`, 'success');
            } catch (err) {
                Alpine.store('app').toast(`Ошибка загрузки очереди: ${err.message}`, 'error');
            }
        },
    };
};
