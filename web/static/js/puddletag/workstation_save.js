const puddletagWorkstationSave = {
    // ==================== СОХРАНЕНИЕ И ОТКАТ (PERSISTENCE) ====================

    saveChanges() {
        return this.saveAllChanges();
    },

    async saveAllChanges() {
        if (this.isSaving) return;
        if (!this.isDownloaderMode && this.dirtyIndices.size === 0) {
            Alpine.store('app').toast('Нет несохраненных изменений', 'info');
            return;
        }
        if (!this.tracks || this.tracks.length === 0) {
            Alpine.store('app').toast('Нет треков для сохранения', 'warning');
            return;
        }

        this.isSaving = true;
        this.saveStatus = 'saving';
        try {
            // В зависимости от режима отправляем сохранение
            if (this.isDownloaderMode) {
                // Режим загрузчика: переносим в библиотеку треки релизов через BatchSaveTags с флагом reorganize
                const jobIds = (this.jobIds && this.jobIds.length > 0) ? this.jobIds : (this.jobId ? [this.jobId] : []);

                if (jobIds.length > 1) {
                    // Несколько релизов: сохраняем каждый релиз изолированно в свою целевую папку
                    for (const jid of jobIds) {
                        const relTracks = this.tracks.filter(t => t.job_id === jid);
                        if (relTracks.length === 0) continue;
                        const relInfo = (this.releases || []).find(r => r.jobId === jid) || {};

                        const releaseAlbum = {
                            album_artist: relTracks[0]?.album_artist || relInfo.album_artist || relTracks[0]?.artist || '',
                            album: relTracks[0]?.album || relInfo.album || '',
                            year: Number(relTracks[0]?.year) || Number(relInfo.year) || null,
                            genre: relTracks[0]?.genre || relInfo.genre || ''
                        };

                        const tracksToSave = relTracks.map((t, idx) => ({
                            ...t,
                            title: t.title || '',
                            artist: t.artist || releaseAlbum.album_artist || '',
                            album: t.album || releaseAlbum.album || '',
                            album_artist: t.album_artist || releaseAlbum.album_artist || '',
                            year: t.year || releaseAlbum.year || null,
                            genre: t.genre || releaseAlbum.genre || '',
                            track_number: t.track_number !== undefined && t.track_number !== null ? t.track_number : (idx + 1),
                            track_total: t.track_total || relTracks.length,
                        }));

                        const res = await window.api.raw('/tags/batch', {
                            method: 'POST',
                            headers: { 'Content-Type': 'application/json' },
                            body: JSON.stringify({
                                album: releaseAlbum,
                                tracks: tracksToSave,
                                source_dir: `tmp_downloads/${jid}/converted`,
                                target_album_path: relInfo.targetAlbumPath || undefined,
                                cover_base64: relTracks[0]?.cover_base64 || '',
                                cover_url: relTracks[0]?.cover_url || relInfo.coverUrl || '',
                                reorganize: true
                            })
                        });

                        if (!res.ok) {
                            const err = await res.json().catch(() => ({}));
                            throw new Error(err.error || `Ошибка сохранения релиза ${releaseAlbum.album}`);
                        }
                    }
                } else {
                    // Одиночный релиз
                    const tracksToSave = this.tracks.map((t, idx) => ({
                        ...t,
                        title: t.title || '',
                        artist: t.artist || this.tagPanel.artist || this.tagPanel.album_artist || '',
                        album: t.album || this.tagPanel.album || '',
                        album_artist: t.album_artist || this.tagPanel.album_artist || '',
                        year: t.year || Number(this.tagPanel.year) || null,
                        genre: t.genre || this.tagPanel.genre || '',
                        track_number: t.track_number !== undefined && t.track_number !== null ? t.track_number : (idx + 1),
                        track_total: t.track_total || this.tracks.length,
                    }));

                    const res = await window.api.raw('/tags/batch', {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({
                            album: {
                                album_artist: this.tagPanel.album_artist || tracksToSave[0]?.album_artist || '',
                                album: this.tagPanel.album || tracksToSave[0]?.album || '',
                                year: Number(this.tagPanel.year) || tracksToSave[0]?.year || null,
                                genre: this.tagPanel.genre || tracksToSave[0]?.genre || ''
                            },
                            tracks: tracksToSave,
                            source_dir: this.currentPath,
                            target_album_path: this.releases[0]?.targetAlbumPath || undefined,
                            cover_base64: this.tagPanel.cover_base64 || tracksToSave[0]?.cover_base64 || '',
                            cover_url: this.tagPanel.cover_url || tracksToSave[0]?.cover_url || '',
                            reorganize: true
                        })
                    });

                    if (!res.ok) {
                        const err = await res.json().catch(() => ({}));
                        throw new Error(err.error || 'Ошибка перемещения в медиатеку');
                    }
                }

                Alpine.store('app').toast(
                    jobIds.length > 1
                        ? `Успешно принято ${jobIds.length} релизов в медиатеку!`
                        : 'Релиз успешно принят в медиатеку!',
                    'success'
                );
                Alpine.store('app').closeTagEditor();

                // Очищаем завершенные задачи из очереди загрузок
                const jobIdsToClean = (this.jobIds && this.jobIds.length > 0) ? this.jobIds : (this.jobId ? [this.jobId] : []);
                if (jobIdsToClean.length > 0) {
                    try {
                        await Promise.allSettled(jobIdsToClean.map(jid => window.api.raw(`/downloads/${jid}`, { method: 'DELETE' })));
                        if (Alpine.store('app').queue) {
                            await Alpine.store('app').queue.fetchQueue();
                        }
                    } catch (cleanupErr) {
                        console.debug('[Puddletag] Сбой удаления задач из очереди:', cleanupErr);
                    }
                }
                window.dispatchEvent(new CustomEvent('refresh-file-manager', { detail: '' }));
                this.saveStatus = 'saved';
                setTimeout(() => { if (this.saveStatus === 'saved') this.saveStatus = 'idle'; }, 4000);
            } else {
                const dirtyTracksList = Array.from(this.dirtyIndices).map(idx => this.tracks[idx]);
                // Режим медиатеки: атомарно перезаписываем ID3v2.4 теги
                const res = await window.api.raw('/tags/batch', {
                    method: 'PUT',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({
                        album: {
                            album_artist: this.tagPanel.album_artist || '',
                            album: this.tagPanel.album || '',
                            year: Number(this.tagPanel.year) || null,
                            genre: this.tagPanel.genre || ''
                        },
                        tracks: dirtyTracksList,
                        source_dir: this.currentPath,
                        cover_base64: this.tagPanel.cover_base64 || '',
                        cover_url: this.tagPanel.cover_url || '',
                        reorganize: false
                    })
                });

                if (!res.ok) {
                    const err = await res.json().catch(() => ({}));
                    throw new Error(err.error || 'Ошибка записи ID3-тегов');
                }

                Alpine.store('app').toast(`Успешно сохранено ${dirtyTracksList.length} файлов! (ID3v2.4)`, 'success');
                this.dirtyIndices.clear();
                this.originalTracks = JSON.parse(JSON.stringify(this.tracks));
                window.dispatchEvent(new CustomEvent('refresh-file-manager', { detail: '' }));
                this.saveStatus = 'saved';
                setTimeout(() => { if (this.saveStatus === 'saved') this.saveStatus = 'idle'; }, 4000);
            }
        } catch (err) {
            this.saveStatus = 'error';
            Alpine.store('app').toast(`Ошибка: ${err.message}`, 'error');
        } finally {
            this.isSaving = false;
        }
    },

    revertAllChanges() {
        if (this.dirtyIndices.size === 0) return;
        this.tracks = JSON.parse(JSON.stringify(this.originalTracks));
        this.dirtyIndices.clear();
        this.syncTagPanel();
        Alpine.store('app').toast('Все изменения отменены', 'info');
    },

    promptCloseTagEditor() {
        if (Alpine.store('app').activeModal !== 'tag_editor' || this.isSaving) return;
        if (this.hasDirtyChanges) {
            if (!confirm(`Есть несохранённые изменения (${this.dirtyIndices.size} файлов). Закрыть без сохранения?`)) {
                return;
            }
        }
        Alpine.store('app').closeTagEditor();
    },
};
