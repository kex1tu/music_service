/**
 * Модуль опроса прогресса задачи загрузки (Polling) и передачи в TagEditor.
 */
window.DownloaderPolling = {
    async poll(downloader, jobId) {
        try {
            const res = await fetch(`/downloads/${jobId}`);
            if (!res.ok) {
                if (res.status === 404) {
                    if (downloader.pollInterval) {
                        clearInterval(downloader.pollInterval);
                        downloader.pollInterval = null;
                    }
                    downloader.job = null;
                }
                return;
            }

            const currentJob = await res.json();
            downloader.job = currentJob;

            if (currentJob.status === 'READY') {
                if (downloader.pollInterval) {
                    clearInterval(downloader.pollInterval);
                    downloader.pollInterval = null;
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
                if (downloader.pollInterval) {
                    clearInterval(downloader.pollInterval);
                    downloader.pollInterval = null;
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

    startPolling(downloader, jobId) {
        this.poll(downloader, jobId);
        downloader.pollInterval = setInterval(() => {
            this.poll(downloader, jobId);
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

            if (validTracks.length === 1) {
                const single = validTracks[0];
                if (!album.album_artist && single.artist) album.album_artist = single.artist;
                if ((!album.album || album.album.toLowerCase() === 'single' || album.album.toLowerCase() === 'singles') && single.title) {
                    album.album = single.title;
                }
                if (!album.year && single.year) album.year = single.year;
                if (!album.genre && single.genre) album.genre = single.genre;
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
                track_id: t.track_id,
                version_status: t.version_status,
                candidates: t.candidates,
                audio_source_url: t.audio_source_url,
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
    }
};
