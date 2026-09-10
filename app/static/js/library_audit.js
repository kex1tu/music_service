/**
 * Компонент аудита медиатеки (Library Audit UI).
 * Отвечает за отображение дашборда метрик целостности, пакетный автопоиск текстов,
 * просмотр релизов без обложек и безопасное удаление дубликатов.
 */

function libraryAuditComponent() {
    return {
        activeSubTab: 'lyrics', // 'lyrics' | 'covers' | 'duplicates'
        isLoading: false,
        summary: {
            total_tracks: 0,
            total_albums: 0,
            missing_lyrics_count: 0,
            missing_covers_count: 0,
            duplicates_count: 0,
        },
        missingLyrics: [],
        missingCovers: [],
        duplicatesData: { groups: [], total_duplicate_files: 0 },
        lyricsProgress: {
            is_running: false,
            total: 0,
            current: 0,
            found: 0,
            failed: 0,
            current_track: '',
        },
        lyricsPollInterval: null,

        init() {
            this.loadAll();
            this.checkLyricsProgress();
        },

        async loadAll() {
            this.isLoading = true;
            try {
                await Promise.all([
                    this.loadSummary(),
                    this.loadMissingLyrics(),
                    this.loadMissingCovers(),
                    this.loadDuplicates(),
                ]);
            } finally {
                this.isLoading = false;
            }
        },

        async loadSummary() {
            try {
                const res = await fetch('/files/audit/summary');
                if (res.ok) {
                    this.summary = await res.json();
                }
            } catch (e) {
                console.error('Ошибка загрузки сводки аудита:', e);
            }
        },

        async loadMissingLyrics() {
            try {
                const res = await fetch('/files/audit/missing-lyrics');
                if (res.ok) {
                    this.missingLyrics = await res.json();
                }
            } catch (e) {
                console.error('Ошибка загрузки треков без слов:', e);
            }
        },

        async loadMissingCovers() {
            try {
                const res = await fetch('/files/audit/missing-covers');
                if (res.ok) {
                    this.missingCovers = await res.json();
                }
            } catch (e) {
                console.error('Ошибка загрузки релизов без обложек:', e);
            }
        },

        async loadDuplicates() {
            try {
                const res = await fetch('/files/audit/duplicates');
                if (res.ok) {
                    this.duplicatesData = await res.json();
                }
            } catch (e) {
                console.error('Ошибка загрузки дубликатов:', e);
            }
        },

        async startBatchLyricsFetch() {
            if (this.lyricsProgress.is_running) return;
            try {
                const res = await fetch('/files/audit/missing-lyrics/fetch-all', { method: 'POST' });
                if (res.ok) {
                    this.lyricsProgress = await res.json();
                    Alpine.store('app').toast('Запущен пакетный поиск текстов песен', 'info');
                    this.startLyricsPolling();
                }
            } catch (e) {
                Alpine.store('app').toast('Ошибка запуска автопоиска текстов', 'error');
            }
        },

        async checkLyricsProgress() {
            try {
                const res = await fetch('/files/audit/missing-lyrics/progress');
                if (res.ok) {
                    const data = await res.json();
                    this.lyricsProgress = data;
                    if (data.is_running && !this.lyricsPollInterval) {
                        this.startLyricsPolling();
                    }
                }
            } catch (e) {
                console.error(e);
            }
        },

        startLyricsPolling() {
            if (this.lyricsPollInterval) clearInterval(this.lyricsPollInterval);
            this.lyricsPollInterval = setInterval(async () => {
                try {
                    const res = await fetch('/files/audit/missing-lyrics/progress');
                    if (res.ok) {
                        const data = await res.json();
                        this.lyricsProgress = data;
                        if (!data.is_running) {
                            clearInterval(this.lyricsPollInterval);
                            this.lyricsPollInterval = null;
                            Alpine.store('app').toast(
                                `Поиск текстов завершен: найдено ${data.found}, не найдено ${data.failed}`,
                                'success'
                            );
                            await this.loadSummary();
                            await this.loadMissingLyrics();
                        }
                    }
                } catch (e) {
                    console.error(e);
                }
            }, 1000);
        },

        openLyricsForTrack(track) {
            Alpine.store('app').openLyricsModal(track);
        },

        openDirectory(path) {
            Alpine.store('app').activeTab = 'files';
            window.dispatchEvent(new CustomEvent('refresh-file-manager', { detail: path }));
        },

        promptDeleteDuplicate(track) {
            Alpine.store('app').deleteConfirmData = {
                path: track.path,
                name: track.filename,
                type: 'file',
                onDeleted: async () => {
                    await this.loadSummary();
                    await this.loadDuplicates();
                },
            };
            Alpine.store('app').activeModal = 'delete_confirm';
        },
    };
}
