/**
 * Модуль сохранения метаданных альбомов и треков в TagEditor.
 */
window.TagSaver = {
    async saveAlbum(component) {
        if (!component.tracks || component.tracks.length === 0) {
            Alpine.store('app').toast('Нет треков для сохранения', 'error');
            return;
        }

        component.isSaving = true;
        const formatGenres = component.formatGenres || (window.AppFormatters ? window.AppFormatters.formatGenres : (g) => g);

        try {
            if (component.viewMode === 'grouped' && component.releases) {
                component.releases.forEach((group) => {
                    group.items.forEach((itemObj) => {
                        const trk = itemObj.track;
                        if (group.isSingles) {
                            if (!trk.album || trk.album.toLowerCase() === 'single' || trk.album.toLowerCase() === 'singles') {
                                trk.album = trk.title || 'Single';
                            }
                        } else if (group.albumTitle !== undefined && group.albumTitle !== null) {
                            trk.album = group.albumTitle;
                        }
                        if (group.albumArtist !== undefined && group.albumArtist !== null) {
                            trk.album_artist = group.albumArtist;
                        }
                        if (group.year !== undefined && group.year !== null && group.year !== '') {
                            const parsedYear = parseInt(group.year, 10);
                            trk.year = !isNaN(parsedYear) && parsedYear > 0 ? parsedYear : null;
                        }
                        if (group.genre !== undefined && group.genre !== null) {
                            trk.genre = formatGenres(group.genre);
                        }
                        if (group.discTotal !== undefined && group.discTotal !== null && group.discTotal !== '') {
                            const parsedDiscTotal = parseInt(group.discTotal, 10);
                            trk.disc_total = !isNaN(parsedDiscTotal) && parsedDiscTotal > 0 ? parsedDiscTotal : null;
                        }
                        const cov = group.selectedCoverUrl || group.coverPreviewUrl;
                        if (cov) {
                            if (group.selectedCoverUrl) trk.cover_url = group.selectedCoverUrl;
                            else if (group.coverBase64) trk.cover_base64 = group.coverBase64;
                            else if (cov.startsWith('http')) trk.cover_url = cov;
                            else if (cov.startsWith('data:')) trk.cover_base64 = cov;
                            trk.has_cover = true;
                        }
                        if (group.coverPath) {
                            trk.cover_path = group.coverPath;
                            trk.has_cover = true;
                        }
                    });
                });
            }

            const firstRelease = component.releases && component.releases.length > 0 ? component.releases[0] : null;
            const isSingleRelease = component.releases && component.releases.length === 1;

            const payload = {
                album: {
                    album: isSingleRelease && firstRelease ? (firstRelease.albumTitle || '') : (component.album.album || ''),
                    album_artist: isSingleRelease && firstRelease ? (firstRelease.albumArtist || '') : (component.album.album_artist || ''),
                    year: isSingleRelease && firstRelease && firstRelease.year ? (parseInt(firstRelease.year, 10) || null) : (component.album.year ? parseInt(component.album.year, 10) : null),
                    genre: formatGenres(isSingleRelease && firstRelease ? (firstRelease.genre || '') : (component.album.genre || '')),
                    cover_path: isSingleRelease && firstRelease ? firstRelease.coverPath : component.coverPath,
                    disc_total: isSingleRelease && firstRelease && firstRelease.discTotal ? (parseInt(firstRelease.discTotal, 10) || null) : (component.album.disc_total ? parseInt(component.album.disc_total, 10) : null),
                },
                tracks: component.tracks.map((t) => {
                    const parsedTrackNum = parseInt(t.track_number, 10);
                    const parsedTrackTotal = parseInt(t.track_total, 10);
                    const parsedDiscNum = parseInt(t.disc_number, 10);
                    const parsedDiscTotal = parseInt(t.disc_total, 10);
                    const parsedYear = parseInt(t.year, 10);
                    let trkAlbum = (t.album || '').trim();
                    if (!trkAlbum || trkAlbum.toLowerCase() === 'single' || trkAlbum.toLowerCase() === 'singles') {
                        trkAlbum = (t.title || '').trim() || 'Single';
                    }
                    return {
                        path: t.path,
                        track_number: !isNaN(parsedTrackNum) && parsedTrackNum >= 1 ? parsedTrackNum : 1,
                        track_total: !isNaN(parsedTrackTotal) && parsedTrackTotal >= 1 ? parsedTrackTotal : null,
                        disc_number: !isNaN(parsedDiscNum) && parsedDiscNum >= 1 ? parsedDiscNum : 1,
                        disc_total: !isNaN(parsedDiscTotal) && parsedDiscTotal >= 1 ? parsedDiscTotal : null,
                        title: t.title || '',
                        artist: t.artist || '',
                        album: trkAlbum,
                        album_artist: t.album_artist || '',
                        year: !isNaN(parsedYear) && parsedYear >= 1000 && parsedYear <= 2999 ? parsedYear : null,
                        genre: formatGenres(t.genre || ''),
                        lyrics_status: t.lyrics_status || 'none',
                        has_cover: t.has_cover || !!t.cover_preview_url || !!t.cover_url || !!t.cover_base64 || !!t.cover_path,
                        cover_url: t.cover_url || null,
                        cover_base64: t.cover_base64 || null,
                        cover_path: t.cover_path || null,
                    };
                }),
                source_dir: component.sourceDir || 'tmp_downloads',
                target_album_path: firstRelease?.targetAlbumPath || undefined,
                cover_path: isSingleRelease && firstRelease ? firstRelease.coverPath : component.coverPath,
                cover_base64: isSingleRelease && firstRelease ? firstRelease.coverBase64 : (component.coverBase64 || null),
                cover_url: isSingleRelease && firstRelease ? firstRelease.selectedCoverUrl : (component.selectedCoverUrl || null),
                apply_album_to_tracks: false,
                reorganize: true,
                singles_mode: component.singlesMode,
                group_by_album: component.groupByAlbum,
            };

            const res = await window.api.raw('/tags/batch', {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(payload),
            });

            if (!res.ok) {
                const err = await res.json().catch(() => ({}));
                const detailMsg = err.detail
                    ? (Array.isArray(err.detail) ? err.detail.map(d => d.msg || JSON.stringify(d)).join(', ') : String(err.detail))
                    : (err.message || 'Ошибка сохранения альбома');
                throw new Error(detailMsg);
            }

            const result = await res.json();
            window.api.tags.requireCompleteBatch(result);
            const totalSavedReleases = component.releases.length;
            const toastMsg = totalSavedReleases > 1
                ? `Успешно сохранено ${totalSavedReleases} релизов (${component.tracks.length} треков) в медиатеку!`
                : 'Теги успешно сохранены и треки размещены в медиатеке!';
            Alpine.store('app').toast(toastMsg, 'success');

            if (component.jobIds && component.jobIds.length > 0) {
                try {
                    await Promise.allSettled(
                        component.jobIds.map((jid) => window.api.downloader.finishImport(jid))
                    );
                    if (Alpine.store('app').queue) {
                        await Alpine.store('app').queue.fetchQueue();
                    }
                } catch (cleanupErr) {
                    console.debug('Сбой очистки сохраненных задач из очереди:', cleanupErr);
                }
            }

            Alpine.store('app').closeTagEditor();
            Alpine.store('app').activeTab = 'files';
            window.dispatchEvent(new CustomEvent('refresh-file-manager', { detail: result.destination_dir }));
            window.dispatchEvent(new CustomEvent('refresh-audit', { detail: { force: true } }));
        } catch (err) {
            Alpine.store('app').toast(`Ошибка: ${err.message}`, 'error');
        } finally {
            component.isSaving = false;
        }
    }
};
