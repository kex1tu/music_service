/**
 * Модуль фонового пакетного сбора текстов песен (Audit Lyrics Batch).
 */
window.AuditLyrics = {
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
            console.error('Ошибка проверки прогресса текстов:', e);
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
                        if (this.loadSummary) await this.loadSummary();
                        if (this.loadMissingLyrics) await this.loadMissingLyrics();
                    }
                }
            } catch (e) {
                console.error('Ошибка опроса прогресса текстов:', e);
            }
        }, 1000);
    },

    openLyricsForTrack(track) {
        Alpine.store('app').openLyricsModal(track);
    }
};
