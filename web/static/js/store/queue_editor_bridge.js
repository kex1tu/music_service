/**
 * Мост между QueueManager и TagEditor.
 * Загружает метаданные завершенных задач и формирует модель для TagEditor.
 */
window.QueueEditorBridge = {
    async openJobInTagEditor(queueMgr, jobId) {
        try {
            const job = (queueMgr.jobs || []).find((j) => j.id === jobId);
            if (job && job.is_backfill && job.target_album_path) {
                return this.openBackfillJobInTagEditor(queueMgr, job);
            }

            const res = await fetch(`/downloads/${jobId}/tracks`);
            if (!res.ok) throw new Error('Не удалось получить треки релиза');
            const data = await res.json();

            if (data.is_backfill && data.target_album_path) {
                return this.openBackfillJobInTagEditor(queueMgr, {
                    id: jobId,
                    is_backfill: true,
                    target_album_path: data.target_album_path,
                    album_title: data.album,
                    artist_name: data.album_artist
                });
            }

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

            const releases = [{
                jobId: jobId,
                album: album.album || 'Unknown Album',
                album_artist: album.album_artist || 'Unknown Artist',
                albumArtist: album.album_artist || 'Unknown Artist',
                year: album.year,
                genre: album.genre,
                coverUrl: data.cover_url || null,
                coverPath: coverPath,
                tracksCount: tracks.length,
            }];

            Alpine.store('app').openTagEditor({
                album,
                tracks,
                sourceDir: `tmp_downloads/${jobId}/converted`,
                coverPath: coverPath,
                hasCover: hasCover,
                jobIds: [jobId],
                releases: releases,
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
                track_total: data.album?.track_total || (data.tracks || []).length,
                cover_url: null,
                cover_path: data.cover_path || null,
            };
            const coverPreview = data.cover_path ? `/tags/cover?path=${encodeURIComponent(data.cover_path)}&t=${Date.now()}` : null;
            const tracks = (data.tracks || []).map((t) => ({
                ...t,
                cover_preview_url: t.cover_url || coverPreview,
                job_id: job.id,
            }));
            const releases = [{
                jobId: job.id,
                album: album.album || 'Unknown Album',
                album_artist: album.album_artist || 'Unknown Artist',
                albumArtist: album.album_artist || 'Unknown Artist',
                year: album.year,
                genre: album.genre,
                coverUrl: null,
                coverPath: data.cover_path || null,
                tracksCount: tracks.length,
            }];
            Alpine.store('app').openTagEditor({
                album,
                tracks,
                sourceDir: data.source_dir || albumPath,
                coverPath: data.cover_path,
                hasCover: !!(data.has_cover || data.cover_path),
                jobIds: [job.id],
                releases: releases,
            });
            queueMgr.isOpen = false;
            Alpine.store('app').toast(`Релиз "${album.album}" открыт в редакторе (всего треков: ${tracks.length})`, 'info');
        } catch (err) {
            Alpine.store('app').toast(err.message, 'error');
        }
    },

    async openAllReadyJobsInTagEditor(queueMgr) {
        const readyJobs = (queueMgr.jobs || []).filter((j) => j.status === 'READY' || j.status === 'PARTIAL_READY');
        if (readyJobs.length === 0) {
            Alpine.store('app').toast('В очереди нет завершенных релизов для редактирования', 'warning');
            return;
        }

        Alpine.store('app').toast(`Загрузка данных для ${readyJobs.length} релиз(ов)...`, 'info');

        try {
            const responses = await Promise.all(
                readyJobs.map(async (j) => {
                    if (j.is_backfill && j.target_album_path) {
                        try {
                            const r = await fetch(`/files/audit/incomplete-albums/editor-data?path=${encodeURIComponent(j.target_album_path)}&job_id=${encodeURIComponent(j.id)}`);
                            if (r.ok) {
                                const bData = await r.json();
                                return { isBackfill: true, job: j, data: bData };
                            }
                        } catch (err) {
                            console.warn('Failed to load backfill editor-data for job', j.id, err);
                        }
                    }
                    const r = await fetch(`/downloads/${j.id}/tracks`);
                    if (r.ok) {
                        const dData = await r.json();
                        if (dData.is_backfill && dData.target_album_path) {
                            try {
                                const bRes = await fetch(`/files/audit/incomplete-albums/editor-data?path=${encodeURIComponent(dData.target_album_path)}&job_id=${encodeURIComponent(j.id)}`);
                                if (bRes.ok) {
                                    const bData = await bRes.json();
                                    return { isBackfill: true, job: j, data: bData };
                                }
                            } catch (err) {
                                console.warn('Failed to load backfill editor-data fallback for job', j.id, err);
                            }
                        }
                        return { isBackfill: false, job: j, data: dData };
                    }
                    return null;
                })
            );

            let allTracks = [];
            let allJobIds = [];
            let firstAlbum = null;
            let firstCoverPath = null;
            let hasAnyCover = false;
            let primarySourceDir = 'tmp_downloads';
            const releases = [];

            responses.forEach((item, index) => {
                if (!item || !item.data) return;
                const job = readyJobs[index];
                const jobId = job.id;
                allJobIds.push(jobId);

                if (item.isBackfill) {
                    const data = item.data;
                    const album = {
                        album_artist: data.album?.album_artist || job.artist_name || '',
                        album: data.album?.album || job.album_title || '',
                        year: data.album?.year || null,
                        genre: data.album?.genre || '',
                        disc_total: data.album?.disc_total || null,
                        track_total: data.album?.track_total || (data.tracks || []).length,
                        cover_url: null,
                        cover_path: data.cover_path || null,
                    };

                    const bTracks = data.tracks || [];
                    const coverPreview = data.cover_path ? `/tags/cover?path=${encodeURIComponent(data.cover_path)}&t=${Date.now()}` : null;
                    const jobHasCover = !!data.has_cover || !!data.cover_path;
                    if (jobHasCover) hasAnyCover = true;

                    releases.push({
                        jobId: jobId,
                        album: album.album || 'Unknown Album',
                        album_artist: album.album_artist || 'Unknown Artist',
                        albumArtist: album.album_artist || 'Unknown Artist',
                        year: album.year,
                        genre: album.genre,
                        coverUrl: null,
                        coverPath: data.cover_path || null,
                        tracksCount: bTracks.length,
                    });

                    if (!firstAlbum && (album.album || album.album_artist)) {
                        firstAlbum = album;
                        firstCoverPath = data.cover_path || null;
                        primarySourceDir = data.source_dir || job.target_album_path || 'tmp_downloads';
                    }

                    bTracks.forEach((t) => {
                        allTracks.push({
                            ...t,
                            job_id: jobId,
                            has_cover: t.has_cover !== undefined ? t.has_cover : jobHasCover,
                            cover_path: t.cover_path || data.cover_path,
                            cover_preview_url: t.cover_url || coverPreview,
                        });
                    });
                } else {
                    const data = item.data;
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

                    if (validTracks.length === 1) {
                        const single = validTracks[0];
                        if (!album.album_artist && single.artist) album.album_artist = single.artist;
                        if ((!album.album || album.album.toLowerCase() === 'single' || album.album.toLowerCase() === 'singles') && single.title) {
                            album.album = single.title;
                        }
                        if (!album.year && single.year) album.year = single.year;
                        if (!album.genre && single.genre) album.genre = single.genre;
                    }

                    releases.push({
                        jobId: jobId,
                        album: album.album || job.album_title || 'Unknown Album',
                        album_artist: album.album_artist || job.artist_name || 'Unknown Artist',
                        albumArtist: album.album_artist || job.artist_name || 'Unknown Artist',
                        year: album.year,
                        genre: album.genre,
                        coverUrl: album.cover_url,
                        coverPath: album.cover_path,
                        tracksCount: validTracks.length,
                    });

                    if (!firstAlbum && (album.album || album.album_artist)) {
                        firstAlbum = album;
                        firstCoverPath = album.cover_path;
                    }

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
                            track_id: t.track_id,
                            version_status: t.version_status,
                            candidates: t.candidates,
                            audio_source_url: t.audio_source_url,
                        });
                    });
                }
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
                sourceDir: (readyJobs.length === 1 && readyJobs[0].is_backfill && readyJobs[0].target_album_path) ? readyJobs[0].target_album_path : primarySourceDir,
                coverPath: firstCoverPath || (allTracks[0]?.cover_path || null),
                hasCover: hasAnyCover,
                jobIds: allJobIds,
                releases: releases,
            });
            queueMgr.isOpen = false;
            Alpine.store('app').toast(`Открыто релизов: ${allJobIds.length}, треков: ${allTracks.length}`, 'success');
        } catch (err) {
            Alpine.store('app').toast(`Ошибка загрузки очереди: ${err.message}`, 'error');
        }
    }
};
