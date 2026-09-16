/**
 * Модуль акустического распознавания треков Shazam (Tag Recognition).
 * Отвечает за генерацию аудио-отпечатков на бэкенде и автозаполнение
 * тегов распознанных треков.
 */
window.TagRecognition = {
    async recognizeTrack(track) {
        if (!track || !track.path) return;
        this.recognizingTrackPath = track.path;

        try {
            const res = await fetch('/tags/recognize-audio', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ path: track.path }),
            });

            if (!res.ok) {
                const err = await res.json();
                throw new Error(err.message || 'Ошибка распознавания');
            }

            const data = await res.json();
            if (data.recognized) {
                if (data.title) track.title = data.title;
                if (data.artist) track.artist = data.artist;
                if (data.album) track.album = data.album;
                if (data.year) track.year = data.year;
                if (!this.album.album && data.album) this.album.album = data.album;
                if (!this.album.album_artist && data.artist) this.album.album_artist = data.artist;
                if (!this.album.year && data.year) this.album.year = data.year;
                if (!this.album.genre && data.genre) this.album.genre = this.formatGenres ? this.formatGenres(data.genre) : data.genre;
                if (data.genre) track.genre = this.formatGenres ? this.formatGenres(data.genre) : data.genre;

                if (!this.coverPreviewUrl && data.cover_url) {
                    this.coverPreviewUrl = data.cover_url;
                    this.coverPath = null;
                    if (this.fetchCoverBase64) {
                        this.fetchCoverBase64(data.cover_url);
                    }
                }

                Alpine.store('app').toast(`Shazam: ${data.artist} — ${data.title}`, 'success');
            } else {
                Alpine.store('app').toast(`Shazam не нашел совпадений для "${track.title || track.path}"`, 'info');
            }
        } catch (err) {
            Alpine.store('app').toast(`Ошибка Shazam: ${err.message}`, 'error');
        } finally {
            this.recognizingTrackPath = null;
        }
    },

    async recognizeAllTracks() {
        if (!this.tracks || this.tracks.length === 0 || this.isRecognizingAll) return;

        this.isRecognizingAll = true;
        this.shazamProgress = { current: 0, total: this.tracks.length };
        let recognizedCount = 0;

        Alpine.store('app').toast(`Запущен поиск через Shazam для ${this.tracks.length} треков...`, 'info');

        try {
            for (const track of this.tracks) {
                this.shazamProgress.current++;
                try {
                    const res = await fetch('/tags/recognize-audio', {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({ path: track.path }),
                    });
                    if (res.ok) {
                        const data = await res.json();
                        if (data.recognized) {
                            if (data.title) track.title = data.title;
                            if (data.artist) track.artist = data.artist;
                            if (data.album) track.album = data.album;
                            if (data.year) track.year = data.year;
                            if (!this.album.album && data.album) this.album.album = data.album;
                            if (!this.album.album_artist && data.artist) this.album.album_artist = data.artist;
                            if (!this.album.year && data.year) this.album.year = data.year;
                            if (!this.album.genre && data.genre) this.album.genre = this.formatGenres ? this.formatGenres(data.genre) : data.genre;
                            if (data.genre) track.genre = this.formatGenres ? this.formatGenres(data.genre) : data.genre;

                            if (!this.coverPreviewUrl && data.cover_url) {
                                this.coverPreviewUrl = data.cover_url;
                                this.coverPath = null;
                                if (this.fetchCoverBase64) {
                                    this.fetchCoverBase64(data.cover_url);
                                }
                            }
                            recognizedCount++;
                        }
                    }
                } catch (trackErr) {
                    console.error('Ошибка распознавания трека:', trackErr);
                }
                await new Promise((resolve) => setTimeout(resolve, 200));
            }

            const msg = recognizedCount > 0
                ? `Shazam: успешно распознано ${recognizedCount} из ${this.tracks.length} треков!`
                : 'Shazam не нашел совпадений для треков';
            Alpine.store('app').toast(msg, recognizedCount > 0 ? 'success' : 'info');

            if (recognizedCount > 0 && this.buildReleasesFromTracks) {
                this.buildReleasesFromTracks();
            }
        } finally {
            this.isRecognizingAll = false;
        }
    }
};
