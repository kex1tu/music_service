/**
 * Мост между QueueManager и TagEditor.
 * Загружает метаданные завершенных задач и формирует модель для TagEditor.
 */
window.QueueEditorBridge = {
    async openJobInTagEditor(queueMgr, jobId) {
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
            queueMgr.isOpen = false;
        } catch (err) {
            Alpine.store('app').toast(err.message, 'error');
        }
    },

    async openBackfillJobInTagEditor(queueMgr, job) {
        try {
            const albumPath = job.target_album_path;
            if (!albumPath) {
                return this.openJobInTagEditor(queueMgr, job.id);
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
            queueMgr.isOpen = false;
            Alpine.store('app').toast(`Релиз "${album.album}" открыт в редакторе (всего треков: ${tracks.length})`, 'info');
        } catch (err) {
            Alpine.store('app').toast(err.message, 'error');
        }
    },

    async openAllReadyJobsInTagEditor(queueMgr) {
        const readyJobs = (queueMgr.jobs || []).filter((j) => j.status === 'READY');
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
            queueMgr.isOpen = false;
            Alpine.store('app').toast(`Открыто релизов: ${allJobIds.length}, треков: ${allTracks.length}`, 'success');
        } catch (err) {
            Alpine.store('app').toast(`Ошибка загрузки очереди: ${err.message}`, 'error');
        }
    }
};
