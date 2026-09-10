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
        isFetchingLyrics: false,
        lyricsProgress: { current: 0, total: 0 },

        // Shazam Audio Recognition
        recognizingTrackPath: null,
        isRecognizingAll: false,
        shazamProgress: { current: 0, total: 0 },

        // Drag-and-Drop
        draggedIndex: null,
        dragOverIndex: null,

        // Cover Gallery
        showCoverGalleryModal: false,
        coverSearchQuery: '',
        isSearchingCovers: false,
        coverGalleryResults: [],
        coverSourceFilter: 'all',
        selectedCoverUrl: null,

        // Deezer Album Search
        showDeezerModal: false,
        deezerQuery: '',
        isSearchingDeezer: false,
        deezerResults: [],
        isLoadingDeezerDetails: false,

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
            this.selectedCoverUrl = null;
            this.draggedIndex = null;
            this.dragOverIndex = null;
            this.showCoverGalleryModal = false;
            this.coverSearchQuery = '';
            this.coverGalleryResults = [];
            this.isSearchingCovers = false;
            this.isFetchingLyrics = false;
            this.lyricsProgress = { current: 0, total: 0 };
            this.recognizingTrackPath = null;
            this.isRecognizingAll = false;
            this.shazamProgress = { current: 0, total: 0 };
            this.showDeezerModal = false;
            this.deezerQuery = '';
            this.deezerResults = [];
            this.isSearchingDeezer = false;
            this.isLoadingDeezerDetails = false;

            this.album = {
                album_artist: data.album?.album_artist || '',
                album: data.album?.album || '',
                year: data.album?.year || null,
                genre: data.album?.genre || '',
                disc_total: data.album?.disc_total || null,
            };

            this.tracks = (data.tracks || []).map((t) => ({
                ...t,
                track_number: t.track_number !== undefined && t.track_number !== null ? t.track_number : 1,
                track_total: t.track_total || null,
                disc_number: t.disc_number !== undefined && t.disc_number !== null ? t.disc_number : 1,
                disc_total: t.disc_total || (data.album?.disc_total || null),
                album: (t.album !== undefined && t.album !== null && t.album !== '') ? t.album : (this.album.album || ''),
                year: (t.year !== undefined && t.year !== null && t.year !== '') ? t.year : (this.album.year || null),
            }));
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

                        const res = await fetch(`/lyrics/search?${params.toString()}`);
                        if (res.ok) {
                            const data = await res.json();
                            const text = data.lyrics || data.content;
                            if (data.source && data.source !== 'none' && text) {
                                const kind = data.synced ? 'lrc' : 'txt';
                                const saveRes = await fetch('/lyrics', {
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

                    // Небольшая задержка между запросами для защиты от rate limit внешних API
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
                        disc_total: this.album.disc_total ? parseInt(this.album.disc_total, 10) : null,
                    },
                    tracks: this.tracks.map((t) => ({
                        path: t.path,
                        track_number: parseInt(t.track_number, 10) || 1,
                        track_total: t.track_total ? parseInt(t.track_total, 10) : null,
                        disc_number: parseInt(t.disc_number, 10) || 1,
                        disc_total: t.disc_total ? parseInt(t.disc_total, 10) : null,
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
                    cover_url: this.selectedCoverUrl || null,
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

        async fetchCoverBase64(url) {
            try {
                const res = await fetch(url);
                if (res.ok) {
                    const blob = await res.blob();
                    const reader = new FileReader();
                    reader.onload = (e) => {
                        this.coverBase64 = e.target.result;
                    };
                    reader.readAsDataURL(blob);
                }
            } catch (e) {
                console.warn('Не удалось конвертировать обложку в base64:', e);
            }
        },

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
                    if (!this.album.genre && data.genre) this.album.genre = data.genre;

                    if (!this.coverPreviewUrl && data.cover_url) {
                        this.coverPreviewUrl = data.cover_url;
                        this.coverPath = null;
                        this.fetchCoverBase64(data.cover_url);
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
                                if (!this.album.genre && data.genre) this.album.genre = data.genre;

                                if (!this.coverPreviewUrl && data.cover_url) {
                                    this.coverPreviewUrl = data.cover_url;
                                    this.coverPath = null;
                                    this.fetchCoverBase64(data.cover_url);
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
            } finally {
                this.isRecognizingAll = false;
            }
        },

        openDeezerSearch() {
            this.showDeezerModal = true;
            this.deezerResults = [];
            const albumPart = this.album.album || '';
            const artistPart = this.album.album_artist || '';
            this.deezerQuery = (albumPart ? `${artistPart} ${albumPart}` : (this.tracks[0]?.artist ? `${this.tracks[0].artist} ${this.tracks[0].title || ''}` : '')).trim();
            if (this.deezerQuery) {
                this.searchDeezerAlbums();
            }
        },

        closeDeezerSearch() {
            this.showDeezerModal = false;
        },

        async searchDeezerAlbums() {
            if (!this.deezerQuery || !this.deezerQuery.trim()) return;
            this.isSearchingDeezer = true;
            this.deezerResults = [];

            try {
                const res = await fetch(`/tags/deezer/albums?query=${encodeURIComponent(this.deezerQuery.trim())}`);
                if (res.ok) {
                    this.deezerResults = await res.json();
                    if (this.deezerResults.length === 0) {
                        Alpine.store('app').toast('Альбомы в Deezer не найдены', 'info');
                    }
                } else {
                    const err = await res.json();
                    throw new Error(err.message || 'Ошибка поиска в Deezer');
                }
            } catch (err) {
                Alpine.store('app').toast(`Ошибка: ${err.message}`, 'error');
            } finally {
                this.isSearchingDeezer = false;
            }
        },

        async applyDeezerAlbum(albumId) {
            if (!albumId) return;
            this.isLoadingDeezerDetails = true;

            try {
                const res = await fetch(`/tags/deezer/albums/${albumId}`);
                if (!res.ok) {
                    const err = await res.json();
                    throw new Error(err.message || 'Не удалось загрузить данные альбома');
                }

                const data = await res.json();
                this.album.album = data.title || this.album.album;
                this.album.album_artist = data.artist || this.album.album_artist;
                if (data.year) this.album.year = data.year;
                if (data.genre) this.album.genre = data.genre;

                if (data.cover_url) {
                    this.coverPreviewUrl = data.cover_url;
                    this.coverPath = null;
                    this.fetchCoverBase64(data.cover_url);
                }

                if (data.tracks && data.tracks.length > 0) {
                    this.tracks.forEach((track, idx) => {
                        const dt = data.tracks[idx];
                        if (dt) {
                            track.track_number = dt.track_number;
                            track.title = dt.title;
                            track.artist = dt.artist;
                            track.album = data.title;
                            track.album_artist = data.artist;
                            if (data.year) track.year = data.year;
                            if (data.genre) track.genre = data.genre;
                        }
                    });
                }

                this.closeDeezerSearch();
                Alpine.store('app').toast(`Альбом "${data.title}" успешно применен!`, 'success');
            } catch (err) {
                Alpine.store('app').toast(`Ошибка: ${err.message}`, 'error');
            } finally {
                this.isLoadingDeezerDetails = false;
            }
        },

        cleanTrackTitle(track) {
            if (!track || !track.title) return;
            let rawTitle = String(track.title).trim();
            if (!rawTitle) return;

            let artists = track.artist
                ? track.artist.split(/\\+|\x00/).map((a) => a.trim()).filter(Boolean)
                : [];

            // 1. Ведущие порядковые номера: "01. ", "01 - ", "01 "
            rawTitle = rawTitle.replace(/^\s*\d{1,3}\s*[\.\-–—]\s*/, '').replace(/^\s*\d{1,3}\s+/, '');

            // 2. Артист в начале названия: "Artist - Title"
            const artistMatch = rawTitle.match(/^([^\-–—]+?)\s*[-–—]\s*(.+)$/);
            if (artistMatch) {
                const candArtist = artistMatch[1].trim();
                const restTitle = artistMatch[2].trim();
                if (artists.length > 0) {
                    const lowerArtists = artists.map((a) => a.toLowerCase());
                    if (lowerArtists.includes(candArtist.toLowerCase())) {
                        rawTitle = restTitle;
                    }
                } else {
                    artists.push(candArtist);
                    rawTitle = restTitle;
                }
            }

            // 3. Извлечение feat / ft / featuring
            // 3.1. Внутри скобок (...) или [...]
            const bracketFeatRegex = /[\(\[]\s*(?:feat\.?|ft\.?|featuring)\s+([^\]\)]+?)[\)\]]/gi;
            let m;
            while ((m = bracketFeatRegex.exec(rawTitle)) !== null) {
                const featContent = m[1].trim();
                rawTitle = rawTitle.slice(0, m.index) + ' ' + rawTitle.slice(m.index + m[0].length);
                bracketFeatRegex.lastIndex = m.index;
                const parts = featContent.split(/\s*(?:,|&|\/|\\+|\band\b|\bи\b|\bx\b)\s*/i);
                for (const p of parts) {
                    const pClean = p.trim();
                    if (pClean && !artists.some((a) => a.toLowerCase() === pClean.toLowerCase())) {
                        artists.push(pClean);
                    }
                }
            }

            // 3.2. В конце названия без скобок: "Song feat. Artist2"
            const plainFeatMatch = rawTitle.match(/\b(?:feat\.?|ft\.?|featuring)\s+([^\[\]\(\)\-]+)$/i);
            if (plainFeatMatch) {
                const featContent = plainFeatMatch[1].trim();
                rawTitle = rawTitle.slice(0, plainFeatMatch.index).trim();
                const parts = featContent.split(/\s*(?:,|&|\/|\\+|\band\b|\bи\b|\bx\b)\s*/i);
                for (const p of parts) {
                    const pClean = p.trim();
                    if (pClean && !artists.some((a) => a.toLowerCase() === pClean.toLowerCase())) {
                        artists.push(pClean);
                    }
                }
            }

            // 4. Удаление мусорных суффиксов в скобках
            const junkRegex = /[\(\[]\s*(?:(?:(?:official|music|4k|hd|hq|1080p|720p)\s+)*(?:video|audio|visualizer|clip\s*officiel|lyric\s*video|lyrics)|official|audio|video|visualizer|lyrics|clip\s*officiel|hd|hq|4k|1080p|720p|prod\.?\s*by[^\)\]]+|remaster(?:ed)?(?:\s*\d{4})?)\s*[\)\]]/gi;
            rawTitle = rawTitle.replace(junkRegex, '');

            // 5. Очистка пустых скобок, лишних пробелов и дефисов
            rawTitle = rawTitle.replace(/[\(\[]\s*[\)\]]/g, '');
            rawTitle = rawTitle.replace(/\s+/g, ' ').replace(/^[\s\-–—._]+|[\s\-–—._]+$/g, '');

            if (rawTitle) {
                track.title = rawTitle;
            }
            if (artists.length > 0) {
                track.artist = artists.join('\\\\');
            }
        },

        cleanTitles() {
            if (!this.tracks || this.tracks.length === 0) return;
            let modified = 0;
            for (const track of this.tracks) {
                const oldTitle = track.title;
                const oldArtist = track.artist;
                this.cleanTrackTitle(track);
                if (track.title !== oldTitle || track.artist !== oldArtist) {
                    modified++;
                }
            }
            Alpine.store('app').toast(`Очищено названий и извлечено фитов: ${modified}`, 'info');
        },

        onDragStart(event, index) {
            this.draggedIndex = index;
            if (event.dataTransfer) {
                event.dataTransfer.effectAllowed = 'move';
                event.dataTransfer.setData('text/plain', String(index));
            }
        },

        onDragOver(event, index) {
            event.preventDefault();
            if (event.dataTransfer) {
                event.dataTransfer.dropEffect = 'move';
            }
            this.dragOverIndex = index;
        },

        onDrop(event, targetIndex) {
            event.preventDefault();
            if (this.draggedIndex === null || this.draggedIndex === targetIndex) {
                this.dragOverIndex = null;
                return;
            }
            const [moved] = this.tracks.splice(this.draggedIndex, 1);
            this.tracks.splice(targetIndex, 0, moved);
            this.draggedIndex = null;
            this.dragOverIndex = null;
            Alpine.store('app').toast('Порядок треков изменён', 'info');
        },

        onDragEnd() {
            this.draggedIndex = null;
            this.dragOverIndex = null;
        },

        renumberTracks() {
            if (!this.tracks || this.tracks.length === 0) return;
            const hasMultipleDiscs = this.tracks.some((t) => t.disc_number && t.disc_number > 1);
            if (hasMultipleDiscs) {
                const counters = {};
                for (const t of this.tracks) {
                    const disc = t.disc_number || 1;
                    counters[disc] = (counters[disc] || 0) + 1;
                    t.track_number = counters[disc];
                }
                for (const t of this.tracks) {
                    const disc = t.disc_number || 1;
                    t.track_total = counters[disc];
                }
                const maxDisc = Math.max(...Object.keys(counters).map(Number));
                this.album.disc_total = maxDisc;
                for (const t of this.tracks) {
                    t.disc_total = maxDisc;
                }
            } else {
                const total = this.tracks.length;
                this.tracks.forEach((track, index) => {
                    track.track_number = index + 1;
                    track.track_total = total;
                    track.disc_number = 1;
                    track.disc_total = 1;
                });
                this.album.disc_total = 1;
            }
            Alpine.store('app').toast('Треки успешно перенумерованы!', 'success');
        },

        openCoverGallery() {
            this.showCoverGalleryModal = true;
            this.coverSourceFilter = 'all';
            const albumPart = this.album.album || '';
            const artistPart = this.album.album_artist || '';
            this.coverSearchQuery = (
                albumPart
                    ? `${artistPart} ${albumPart}`
                    : this.tracks[0]?.artist
                    ? `${this.tracks[0].artist} ${this.tracks[0].title || ''}`
                    : ''
            ).trim();
            if (this.coverSearchQuery) {
                this.searchCovers();
            }
        },

        closeCoverGallery() {
            this.showCoverGalleryModal = false;
        },

        async searchCovers() {
            if (!this.coverSearchQuery || !this.coverSearchQuery.trim()) return;
            this.isSearchingCovers = true;
            this.coverGalleryResults = [];

            try {
                const res = await fetch(
                    `/tags/covers/search?query=${encodeURIComponent(this.coverSearchQuery.trim())}&limit=24`
                );
                if (res.ok) {
                    this.coverGalleryResults = await res.json();
                    if (this.coverGalleryResults.length === 0) {
                        Alpine.store('app').toast('Обложки не найдены', 'info');
                    }
                } else {
                    const err = await res.json();
                    throw new Error(err.message || 'Ошибка поиска обложек');
                }
            } catch (err) {
                Alpine.store('app').toast(`Ошибка: ${err.message}`, 'error');
            } finally {
                this.isSearchingCovers = false;
            }
        },

        filteredCoverResults() {
            if (this.coverSourceFilter === 'all') {
                return this.coverGalleryResults;
            }
            return this.coverGalleryResults.filter((item) => item.source === this.coverSourceFilter);
        },

        selectCover(item) {
            if (!item || !item.cover_url) return;
            this.coverPreviewUrl = item.cover_url;
            this.selectedCoverUrl = item.cover_url;
            this.coverPath = null;
            this.coverBase64 = null;
            this.fetchCoverBase64(item.cover_url);
            this.closeCoverGallery();
            Alpine.store('app').toast(
                `Выбрана обложка (${item.source.toUpperCase()} ${item.width}x${item.height})`,
                'success'
            );
        },

        close() {
            Alpine.store('app').closeTagEditor();
        },
    };
}
