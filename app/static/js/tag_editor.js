/**
 * Двухуровневый редактор тегов ID3v2.4 (Tag Editor).
 * Позволяет редактировать общие метаданные альбома, массово применять их ко всем трекам,
 * настраивать индивидуальные поля композиций, открывать модальное окно текстов песен
 * и отправлять пакетный запрос PUT /tags/batch на сохранение и организацию файлов.
 */

function tagEditorComponent() {
    return {
        album: {
            album_artist: '',
            album: '',
            year: null,
            genre: '',
        },
        tracks: [],
        sourceDir: '',
        coverPath: null,
        coverPreviewUrl: null,
        coverBase64: null,
        isSaving: false,

        init() {
            // Следим за открытием данных редактора из глобального хранилища
            this.$watch('$store.app.tagEditorData', (newData) => {
                if (newData) {
                    this.loadData(newData);
                }
            });

            if (Alpine.store('app').tagEditorData) {
                this.loadData(Alpine.store('app').tagEditorData);
            }

            // Реактивное обновление бейджа текста песни из модального окна lyrics
            window.addEventListener('lyrics-updated', (e) => {
                const { path, kind } = e.detail;
                if (!this.tracks) return;
                const track = this.tracks.find((t) => t.path === path);
                if (track) {
                    track.lyrics_status = kind;
                }
            });
        },

        loadData(data) {
            this.coverBase64 = null;
            this.album = {
                album_artist: data.album?.album_artist || '',
                album: data.album?.album || '',
                year: data.album?.year || null,
                genre: data.album?.genre || '',
            };

            this.tracks = (data.tracks || []).map((t) => ({ ...t }));
            this.sourceDir = data.sourceDir || '';
            this.coverPath = data.coverPath || null;

            if (this.coverPath) {
                this.coverPreviewUrl = `/tags/cover?path=${encodeURIComponent(this.coverPath)}&t=${Date.now()}`;
            } else if (data.hasCover && this.sourceDir) {
                this.coverPreviewUrl = `/tags/cover?path=${encodeURIComponent(this.sourceDir)}&t=${Date.now()}`;
            } else {
                this.coverPreviewUrl = null;
            }
        },

        applyAlbumToAll() {
            if (!this.tracks || this.tracks.length === 0) return;

            this.tracks.forEach((track) => {
                if (this.album.album) track.album = this.album.album;
                if (this.album.album_artist) track.album_artist = this.album.album_artist;
                if (this.album.year !== null && this.album.year !== undefined && this.album.year !== '') {
                    track.year = parseInt(this.album.year, 10);
                }
                if (this.album.genre) track.genre = this.album.genre;
            });

            Alpine.store('app').toast('Параметры альбома скопированы во все треки', 'info');
        },

        handleCoverUpload(event) {
            const file = event.target.files[0];
            if (!file) return;

            if (!file.type.startsWith('image/')) {
                Alpine.store('app').toast('Пожалуйста, выберите изображение (JPEG или PNG)', 'error');
                return;
            }

            const reader = new FileReader();
            reader.onload = (e) => {
                this.coverPreviewUrl = e.target.result;
                this.coverBase64 = e.target.result;
                Alpine.store('app').toast('Новая обложка выбрана для альбома', 'info');
            };
            reader.readAsDataURL(file);
        },

        openLyrics(track, index) {
            Alpine.store('app').openLyricsModal(track);
        },

        async saveAlbum() {
            if (!this.tracks || this.tracks.length === 0) {
                Alpine.store('app').toast('Нет треков для сохранения', 'error');
                return;
            }

            this.isSaving = true;

            try {
                const payload = {
                    album: {
                        album: this.album.album || '',
                        album_artist: this.album.album_artist || '',
                        year: this.album.year ? parseInt(this.album.year, 10) : null,
                        genre: this.album.genre || '',
                        cover_path: this.coverPath,
                    },
                    tracks: this.tracks.map((t) => ({
                        path: t.path,
                        track_number: parseInt(t.track_number, 10) || 1,
                        title: t.title || '',
                        artist: t.artist || '',
                        album: t.album || this.album.album || '',
                        album_artist: t.album_artist || this.album.album_artist || '',
                        year: t.year ? parseInt(t.year, 10) : (this.album.year ? parseInt(this.album.year, 10) : null),
                        genre: t.genre || this.album.genre || '',
                        lyrics_status: t.lyrics_status || 'none',
                        has_cover: t.has_cover || !!this.coverPreviewUrl,
                    })),
                    source_dir: this.sourceDir,
                    cover_path: this.coverPath,
                    cover_base64: this.coverBase64 || null,
                    apply_album_to_tracks: false, // уже применены пользователем
                    reorganize: true, // переместить в каноническую структуру MUSIC_DIR
                };

                const res = await fetch('/tags/batch', {
                    method: 'PUT',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(payload),
                });

                if (!res.ok) {
                    const err = await res.json();
                    throw new Error(err.message || 'Ошибка сохранения альбома');
                }

                const result = await res.json();
                Alpine.store('app').toast('Альбом успешно сохранен и размещен в медиатеке!', 'success');

                // Закрываем редактор
                Alpine.store('app').closeTagEditor();

                // Переключаемся на вкладку файлового менеджера и обновляем список
                Alpine.store('app').activeTab = 'files';
                window.dispatchEvent(new CustomEvent('refresh-file-manager', { detail: result.destination_dir }));
            } catch (err) {
                Alpine.store('app').toast(`Ошибка: ${err.message}`, 'error');
            } finally {
                this.isSaving = false;
            }
        },

        close() {
            Alpine.store('app').closeTagEditor();
        },
    };
}
