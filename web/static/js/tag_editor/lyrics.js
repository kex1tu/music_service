/**
 * Модуль поиска и привязки текстов песен в редакторе тегов (Tag Lyrics).
 * Выполняет пакетный опрос LRCLIB/Genius для альбомов или конкретных релизов
 * с сохранением внешних .lrc/.txt файлов и обновлением статуса треков.
 */
window.TagLyrics = {
    async fetchLyricsForAlbum(forceAll = false) {
        if (!this.tracks || this.tracks.length === 0) {
            Alpine.store('app').toast('Нет треков для поиска текстов', 'error');
            return;
        }
        if (this.isFetchingLyrics) return;

        const missing = this.tracks.filter(
            (t) => !t.lyrics_status || t.lyrics_status === 'none'
        );

        if (missing.length === 0 && !forceAll) {
            if (
                confirm(
                    'У всех треков уже есть тексты. Выполнить повторный поиск и обновить тексты для всех треков?'
                )
            ) {
                return this.fetchLyricsForAlbum(true);
            }
            return;
        }

        const targets = forceAll ? this.tracks : missing;
        this.isFetchingLyrics = true;
        this.lyricsProgress = { current: 0, total: targets.length };
        let foundCount = 0;

        Alpine.store('app').toast(
            `Запущен поиск текстов для ${targets.length} треков...`,
            'info'
        );

        try {
            for (const track of targets) {
                this.lyricsProgress.current++;
                try {
                    const params = new URLSearchParams({
                        artist: track.artist || this.album.album_artist || '',
                        title: track.title || '',
                    });
                    if (track.album || this.album.album) {
                        params.append('album', track.album || this.album.album);
                    }
                    if (track.duration) {
                        params.append('duration', track.duration);
                    }

                    const res = await window.api.raw(`/lyrics/search?${params.toString()}`);
                    if (res.ok) {
                        const data = await res.json();
                        const text = data.lyrics || data.content;
                        if (data.source && data.source !== 'none' && text) {
                            const kind = data.synced ? 'lrc' : 'txt';
                            const saveRes = await window.api.raw('/lyrics', {
                                method: 'POST',
                                headers: { 'Content-Type': 'application/json' },
                                body: JSON.stringify({
                                    track_path: track.path,
                                    content: text,
                                    kind: kind,
                                }),
                            });
                            if (saveRes.ok) {
                                track.lyrics_status = kind;
                                foundCount++;
                            }
                        }
                    }
                } catch (trackErr) {
                    console.error(`Ошибка при поиске текста для "${track.title}":`, trackErr);
                }

                // Задержка между запросами для защиты от rate limit
                await new Promise((resolve) => setTimeout(resolve, 150));
            }

            const msg =
                foundCount > 0
                    ? `Поиск текстов завершён: успешно найдено ${foundCount} из ${targets.length}!`
                    : 'Поиск завершён: тексты для треков не были найдены во внешних базах';
            Alpine.store('app').toast(msg, foundCount > 0 ? 'success' : 'info');
        } catch (err) {
            Alpine.store('app').toast(`Ошибка поиска текстов: ${err.message}`, 'error');
        } finally {
            this.isFetchingLyrics = false;
        }
    },

    async fetchLyricsForGroup(group, forceAll = false) {
        if (!group || !group.items || group.items.length === 0) return;
        const groupTracks = group.items.map((it) => it.track);
        if (this.isFetchingLyrics) return;

        const missing = groupTracks.filter(
            (t) => !t.lyrics_status || t.lyrics_status === 'none'
        );

        if (missing.length === 0 && !forceAll) {
            if (
                confirm(
                    `У всех треков релиза "${group.albumTitle || group.title}" уже есть тексты. Выполнить повторный поиск?`
                )
            ) {
                return this.fetchLyricsForGroup(group, true);
            }
            return;
        }

        const targets = forceAll ? groupTracks : missing;
        this.isFetchingLyrics = true;
        this.lyricsProgress = { current: 0, total: targets.length };
        let foundCount = 0;

        Alpine.store('app').toast(
            `Поиск текстов для релиза "${group.albumTitle || group.title}" (${targets.length} треков)...`,
            'info'
        );

        try {
            for (const track of targets) {
                this.lyricsProgress.current++;
                try {
                    const params = new URLSearchParams({
                        artist: track.artist || group.albumArtist || this.album.album_artist || '',
                        title: track.title || '',
                    });
                    const alb = track.album || group.albumTitle || (!group.isSingles ? group.title : '');
                    if (alb) {
                        params.append('album', alb);
                    }
                    if (track.duration) {
                        params.append('duration', track.duration);
                    }

                    const res = await window.api.raw(`/lyrics/search?${params.toString()}`);
                    if (res.ok) {
                        const data = await res.json();
                        const text = data.lyrics || data.content;
                        if (data.source && data.source !== 'none' && text) {
                            const kind = data.synced ? 'lrc' : 'txt';
                            const saveRes = await window.api.raw('/lyrics', {
                                method: 'POST',
                                headers: { 'Content-Type': 'application/json' },
                                body: JSON.stringify({
                                    track_path: track.path,
                                    content: text,
                                    kind: kind,
                                }),
                            });
                            if (saveRes.ok) {
                                track.lyrics_status = kind;
                                foundCount++;
                            }
                        }
                    }
                } catch (trackErr) {
                    console.error(`Ошибка при поиске текста для "${track.title}":`, trackErr);
                }
                await new Promise((resolve) => setTimeout(resolve, 150));
            }

            const msg =
                foundCount > 0
                    ? `Поиск текстов для "${group.albumTitle || group.title}": найдено ${foundCount} из ${targets.length}!`
                    : `Поиск завершён: тексты для треков релиза "${group.albumTitle || group.title}" не найдены`;
            Alpine.store('app').toast(msg, foundCount > 0 ? 'success' : 'info');
        } catch (err) {
            Alpine.store('app').toast(`Ошибка поиска текстов: ${err.message}`, 'error');
        } finally {
            this.isFetchingLyrics = false;
        }
    },

    openLyrics(track, index) {
        Alpine.store('app').openLyricsModal(track);
    }
};
