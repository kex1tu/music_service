/**
 * Модальное окно управления текстами песен (Lyrics Modal).
 * Предоставляет поиск текстов в LRCLIB и Genius, ручное редактирование в textarea,
 * локальную загрузку файлов .lrc/.txt и сохранение внешним файлом рядом с MP3.
 */

function lyricsModalComponent() {
    return {
        track: null,
        content: '',
        kind: 'lrc', // 'lrc' | 'txt'
        source: 'none',
        isLoading: false,
        isSaving: false,

        init() {
            window.addEventListener('lyrics-modal-opened', async (e) => {
                this.track = e.detail;
                this.content = '';
                this.kind = 'lrc';
                this.source = 'none';
                await this.loadExistingOrSearch();
            });
        },

        async loadExistingOrSearch() {
            if (!this.track) return;
            this.isLoading = true;

            try {
                // 1. Пробуем загрузить существующий текст с диска
                const res = await fetch(`/lyrics?path=${encodeURIComponent(this.track.path)}`);
                if (res.ok) {
                    const data = await res.json();
                    const text = data.lyrics || data.content;
                    if (data.kind && data.kind !== 'none' && text) {
                        this.content = text;
                        this.kind = data.kind;
                        this.source = `Диск (.${this.kind})`;
                        this.isLoading = false;
                        return;
                    }
                }

                // 2. Если на диске нет — запускаем онлайн-поиск
                await this.searchOnline();
            } catch (err) {
                console.error('Ошибка загрузки текста:', err);
            } finally {
                this.isLoading = false;
            }
        },

        async searchOnline() {
            if (!this.track) return;
            this.isLoading = true;

            try {
                const params = new URLSearchParams({
                    artist: this.track.artist || '',
                    title: this.track.title || '',
                });
                if (this.track.album) params.append('album', this.track.album);
                if (this.track.duration) params.append('duration', this.track.duration);

                const res = await fetch(`/lyrics/search?${params.toString()}`);
                if (!res.ok) {
                    throw new Error('Сбой при поиске текста');
                }

                const data = await res.json();
                const text = data.lyrics || data.content;
                if (data.source && data.source !== 'none' && text) {
                    this.content = text;
                    this.kind = data.synced ? 'lrc' : 'txt';
                    this.source = this.formatSourceLabel(data.source, data.synced);
                    Alpine.store('app').toast(`Текст найден: ${this.source}`, 'info');
                } else {
                    this.source = 'Не найден';
                    Alpine.store('app').toast('Текст не найден во внешних каталогах', 'info');
                }
            } catch (err) {
                Alpine.store('app').toast(`Ошибка поиска: ${err.message}`, 'error');
            } finally {
                this.isLoading = false;
            }
        },

        formatSourceLabel(src, synced) {
            if (src === 'lrclib' || src === 'lrclib_synced' || src === 'lrclib_plain') {
                return synced ? 'LRCLIB (Синхронизированный)' : 'LRCLIB (Текст)';
            }
            if (src === 'genius') {
                return 'Genius (Fallback)';
            }
            return src || 'Онлайн';
        },

        setKind(newKind) {
            this.kind = newKind === 'txt' ? 'txt' : 'lrc';
        },

        autoDetectKind(text) {
            const val = text !== undefined ? text : this.content;
            if (!val || typeof val !== 'string') return;
            const hasTimecodes = /\[\d{2}:\d{2}/.test(val);
            this.kind = hasTimecodes ? 'lrc' : 'txt';
        },

        handleContentInput() {
            if (this.source === 'none' || this.source === 'Не найден' || !this.source) {
                this.source = 'Вручную';
            }
            if (this.content) {
                this.autoDetectKind();
            }
        },

        handleContentPaste(event) {
            if (this.source === 'none' || this.source === 'Не найден' || !this.source) {
                this.source = 'Вставлено вручную';
            }
            setTimeout(() => {
                if (this.content) {
                    this.autoDetectKind();
                }
            }, 50);
        },

        async pasteFromClipboard() {
            try {
                if (navigator.clipboard && navigator.clipboard.readText) {
                    const text = await navigator.clipboard.readText();
                    if (text && text.trim().length > 0) {
                        this.content = text;
                        this.source = 'Буфер обмена';
                        this.autoDetectKind(text);
                        Alpine.store('app').toast('Текст песни вставлен из буфера обмена', 'info');
                        return;
                    }
                }
            } catch (err) {
                console.warn('Не удалось прочитать буфер обмена через API:', err);
            }

            // Запасной вариант при блокировке clipboard API (HTTP/браузерные ограничения)
            const textarea = document.querySelector('textarea[x-model="content"]');
            if (textarea) {
                textarea.focus();
            }
            Alpine.store('app').toast('Вставьте текст комбинацией клавиш Ctrl+V в поле ввода', 'info');
        },

        clearContent() {
            this.content = '';
            this.source = 'Очищено';
        },

        handleFileUpload(event) {
            const file = event.target.files[0];
            if (!file) return;

            const isLrc = file.name.toLowerCase().endsWith('.lrc');
            this.kind = isLrc ? 'lrc' : 'txt';

            const reader = new FileReader();
            reader.onload = (e) => {
                this.content = e.target.result;
                this.source = `Файл (${file.name})`;
                Alpine.store('app').toast(`Файл ${file.name} загружен в редактор`, 'info');
            };
            reader.readAsText(file);
        },

        async saveLyrics() {
            if (!this.track) return;
            this.isSaving = true;

            try {
                const res = await fetch('/lyrics', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({
                        track_path: this.track.path,
                        content: this.content,
                        kind: this.kind,
                    }),
                });

                if (!res.ok) {
                    const err = await res.json();
                    throw new Error(err.message || 'Ошибка сохранения текста');
                }

                Alpine.store('app').toast('Текст песни успешно сохранен!', 'success');
                this.track.lyrics_status = this.kind;

                // Реактивно уведомляем другие компоненты (Tag Editor)
                window.dispatchEvent(
                    new CustomEvent('lyrics-updated', {
                        detail: { path: this.track.path, kind: this.kind },
                    })
                );

                if (Alpine.store('app').tagEditorData) {
                    const target = Alpine.store('app').tagEditorData.tracks.find(
                        (t) => t.path === this.track.path
                    );
                    if (target) {
                        target.lyrics_status = this.kind;
                    }
                }

                Alpine.store('app').closeLyricsModal();
            } catch (err) {
                Alpine.store('app').toast(`Ошибка: ${err.message}`, 'error');
            } finally {
                this.isSaving = false;
            }
        },

        async deleteLyrics() {
            if (!this.track) return;
            if (!confirm('Удалить файл текста песни?')) return;

            try {
                const res = await fetch(`/lyrics?path=${encodeURIComponent(this.track.path)}`, {
                    method: 'DELETE',
                });

                if (res.ok) {
                    this.content = '';
                    this.source = 'none';
                    this.track.lyrics_status = 'none';

                    // Реактивно уведомляем другие компоненты (Tag Editor)
                    window.dispatchEvent(
                        new CustomEvent('lyrics-updated', {
                            detail: { path: this.track.path, kind: 'none' },
                        })
                    );

                    if (Alpine.store('app').tagEditorData) {
                        const target = Alpine.store('app').tagEditorData.tracks.find(
                            (t) => t.path === this.track.path
                        );
                        if (target) {
                            target.lyrics_status = 'none';
                        }
                    }

                    Alpine.store('app').toast('Текст песни удален', 'info');
                    Alpine.store('app').closeLyricsModal();
                }
            } catch (err) {
                Alpine.store('app').toast(`Ошибка удаления: ${err.message}`, 'error');
            }
        },

        close() {
            Alpine.store('app').closeLyricsModal();
        },
    };
}
