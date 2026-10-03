/**
 * Модуль фонового пакетного сбора обложек альбомов (Audit Covers Batch).
 * Отвечает за:
 * - автопоиск обложек высокого разрешения в iTunes, Deezer, Yandex Music
 * - пакетный режим "Докачать все" с отображением прогресса и отменой
 * - одиночный режим "Скачать арт" для выбранного релиза
 */
window.AuditCovers = {
    isBackfillingCovers: false,
    coversProgressStatus: '',
    coversProgressPercent: 0,
    coversPollInterval: null,

    async backfillAllCovers() {
        if (this.isBackfillingCovers) return;
        this.isBackfillingCovers = true;
        this.coversProgressStatus = 'Запуск процесса дозагрузки обложек...';
        this.coversProgressPercent = 0;
        Alpine.store('app').toast('Запущен пакетный поиск обложек альбомов', 'info');

        try {
            const res = await fetch('/files/audit/missing-covers/backfill-all', { method: 'POST' });
            if (res.ok) {
                const data = await res.json();
                this.updateCoversProgress(data);
                this.startCoversPolling();
            } else {
                this.isBackfillingCovers = false;
                Alpine.store('app').toast('Ошибка запуска автопоиска обложек', 'error');
            }
        } catch (e) {
            this.isBackfillingCovers = false;
            console.error('Ошибка backfillAllCovers:', e);
            Alpine.store('app').toast('Сетевая ошибка при запуске автопоиска обложек', 'error');
        }
    },

    async checkCoversProgress() {
        try {
            const res = await fetch(`/files/audit/missing-covers/progress?_t=${Date.now()}`);
            if (res.ok) {
                const data = await res.json();
                if (data.is_running) {
                    this.isBackfillingCovers = true;
                    this.updateCoversProgress(data);
                    if (!this.coversPollInterval) {
                        this.startCoversPolling();
                    }
                }
            }
        } catch (e) {
            console.error('Ошибка проверки прогресса обложек:', e);
        }
    },

    updateCoversProgress(data) {
        if (!data) return;
        const total = data.total || 0;
        const current = data.current || 0;
        const pct = total > 0 ? Math.min(100, Math.round((current / total) * 100)) : 0;
        this.coversProgressPercent = pct;
        if (data.current_album) {
            this.coversProgressStatus = `[${current}/${total}] ${data.current_album} (${pct}%)`;
        } else {
            this.coversProgressStatus = `Обработано ${current} из ${total} (${pct}%)`;
        }
    },

    startCoversPolling() {
        if (this.coversPollInterval) clearInterval(this.coversPollInterval);
        this.coversPollInterval = setInterval(async () => {
            try {
                const res = await fetch(`/files/audit/missing-covers/progress?_t=${Date.now()}`);
                if (res.ok) {
                    const data = await res.json();
                    this.updateCoversProgress(data);
                    if (!data.is_running) {
                        clearInterval(this.coversPollInterval);
                        this.coversPollInterval = null;
                        this.isBackfillingCovers = false;
                        Alpine.store('app').toast(
                            `Дозагрузка обложек завершена: найдено ${data.found || 0}, не удалось ${data.failed || 0}`,
                            'success'
                        );
                        if (this.loadSummary) await this.loadSummary();
                        if (this.loadMissingCovers) await this.loadMissingCovers();
                        window.dispatchEvent(new CustomEvent('refresh-file-manager', { detail: '' }));
                    }
                }
            } catch (e) {
                console.error('Ошибка опроса прогресса обложек:', e);
            }
        }, 1200);
    },

    async cancelCoversBackfill() {
        try {
            const res = await fetch('/files/audit/missing-covers/cancel', { method: 'POST' });
            if (res.ok) {
                if (this.coversPollInterval) {
                    clearInterval(this.coversPollInterval);
                    this.coversPollInterval = null;
                }
                this.isBackfillingCovers = false;
                Alpine.store('app').toast('Пакетная дозагрузка обложек остановлена', 'warning');
                if (this.loadSummary) await this.loadSummary();
                if (this.loadMissingCovers) await this.loadMissingCovers();
            }
        } catch (e) {
            console.error('Ошибка отмены дозагрузки обложек:', e);
            Alpine.store('app').toast('Ошибка при отмене процесса', 'error');
        }
    },

    async backfillSingleCover(item) {
        if (!item || item._isFetchingCover) return;
        item._isFetchingCover = true;
        Alpine.store('app').toast(`Поиск обложки для "${item.album || item.path}"...`, 'info');

        try {
            const res = await fetch('/files/audit/missing-covers/backfill', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ folder_path: item.path }),
            });

            if (res.ok) {
                const data = await res.json();
                Alpine.store('app').toast(
                    `Обложка для "${item.album || item.path}" успешно сохранена (${data.tracks_updated} треков обновлено)`,
                    'success'
                );
                this.missingCovers = (this.missingCovers || []).filter((c) => c.path !== item.path);
                if (this.summary && this.summary.missing_covers_count > 0) {
                    this.summary.missing_covers_count--;
                }
                window.dispatchEvent(new CustomEvent('refresh-file-manager', { detail: item.path }));
            } else {
                let msg = 'Не удалось найти обложку в каталогах';
                try {
                    const err = await res.json();
                    if (err && err.error) msg = err.error;
                } catch (_) {}
                Alpine.store('app').toast(msg, 'error');
            }
        } catch (e) {
            console.error('Ошибка backfillSingleCover:', e);
            Alpine.store('app').toast('Сетевая ошибка при загрузке обложки', 'error');
        } finally {
            item._isFetchingCover = false;
        }
    }
};
