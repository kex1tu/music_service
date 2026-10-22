/**
 * Pre-Flight Check & Track Selection Controller (Alpine.js Store).
 * Предварительная проверка релиза, дедупликация и выборочная загрузка дорожек.
 */
document.addEventListener('alpine:init', () => {
    Alpine.store('preflight', {
        isOpen: false,
        isLoading: false,
        isStartingDownload: false,
        url: '',
        source: 'auto',
        release: null,
        tracks: [],
        summary: null,
        selectedIndices: {},
        hideExisting320k: false,

        open(rawUrl, source = 'auto') {
            this.reset();
            this.url = (rawUrl || '').trim();
            this.source = source || 'auto';
            if (!this.url) {
                if (Alpine.store('app')) {
                    Alpine.store('app').toast('Введите корректный URL для предпросмотра', 'warning');
                }
                return;
            }
            this.isOpen = true;
            this.fetchPreflight();
        },

        reset() {
            this.isLoading = false;
            this.isStartingDownload = false;
            this.release = null;
            this.tracks = [];
            this.summary = null;
            this.selectedIndices = {};
            this.hideExisting320k = false;
        },

        close() {
            this.isOpen = false;
        },

        setPreflightData(url, source, data) {
            this.reset();
            this.url = url;
            this.source = source;
            this.release = data.release || null;
            this.tracks = data.tracks || [];
            this.summary = data.summary || null;

            const sel = {};
            for (const tr of this.tracks) {
                if (tr.default_selected) {
                    sel[tr.index] = true;
                }
            }
            this.selectedIndices = sel;
            this.isOpen = true;
        },

        async fetchPreflight() {
            if (!this.url) return;
            this.isLoading = true;
            try {
                const resp = await fetch('/downloads/preflight', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ url: this.url, source: this.source }),
                });

                if (!resp.ok) {
                    const err = await resp.json().catch(() => ({}));
                    throw new Error(err.message || err.error || 'Не удалось получить предпросмотр релиза');
                }

                const data = await resp.json();
                this.release = data.release;
                this.tracks = data.tracks || [];
                this.summary = data.summary;

                // По умолчанию отмечены NEW и EXISTS_UPGRADE_AVAILABLE
                const sel = {};
                for (const tr of this.tracks) {
                    if (tr.default_selected) {
                        sel[tr.index] = true;
                    }
                }
                this.selectedIndices = sel;
            } catch (err) {
                if (Alpine.store('app')) {
                    Alpine.store('app').toast(err.message, 'error');
                }
                this.close();
            } finally {
                this.isLoading = false;
            }
        },

        get visibleTracks() {
            if (!this.hideExisting320k) {
                return this.tracks;
            }
            return this.tracks.filter(t => t.status !== 'EXISTS_HIGH_QUALITY');
        },

        get selectedCount() {
            return Object.keys(this.selectedIndices).filter(k => this.selectedIndices[k]).length;
        },

        get isAllSelected() {
            if (this.tracks.length === 0) return false;
            return this.tracks.every(t => this.selectedIndices[t.index]);
        },

        get isIndeterminate() {
            const count = this.selectedCount;
            return count > 0 && count < this.tracks.length;
        },

        toggleSelectAll(checked) {
            const next = {};
            if (checked) {
                for (const tr of this.tracks) {
                    next[tr.index] = true;
                }
            }
            this.selectedIndices = next;
        },

        selectOnlyNewAndUpgrade() {
            const next = {};
            for (const tr of this.tracks) {
                if (tr.status === 'NEW' || tr.status === 'EXISTS_UPGRADE_AVAILABLE') {
                    next[tr.index] = true;
                }
            }
            this.selectedIndices = next;
        },

        selectOnlyNew() {
            const next = {};
            for (const tr of this.tracks) {
                if (tr.status === 'NEW') {
                    next[tr.index] = true;
                }
            }
            this.selectedIndices = next;
        },

        toggleTrack(index) {
            const copy = { ...this.selectedIndices };
            if (copy[index]) {
                delete copy[index];
            } else {
                copy[index] = true;
            }
            this.selectedIndices = copy;
        },

        isSelected(index) {
            return !!this.selectedIndices[index];
        },

        async startDownload() {
            const selected = Object.keys(this.selectedIndices)
                .filter(k => this.selectedIndices[k])
                .map(k => parseInt(k, 10))
                .sort((a, b) => a - b);

            if (selected.length === 0) {
                if (Alpine.store('app')) {
                    Alpine.store('app').toast('Выберите хотя бы один трек для загрузки', 'warning');
                }
                return;
            }

            this.isStartingDownload = true;
            try {
                const resp = await fetch('/downloads', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({
                        url: this.url,
                        source: this.source,
                        selected_track_indices: selected,
                    }),
                });

                if (!resp.ok) {
                    const err = await resp.json().catch(() => ({}));
                    throw new Error(err.message || err.error || 'Ошибка постановки задачи');
                }

                const job = await resp.json();
                const totalTracks = this.tracks.length;
                this.close();

                if (Alpine.store('app')) {
                    Alpine.store('app').currentJobId = job.id;
                    Alpine.store('app').toast(`Задача добавлена в очередь (${selected.length} из ${totalTracks} треков)`, 'info');
                }

                // Уведомляем компонент загрузки о созданной задаче
                window.dispatchEvent(new CustomEvent('job-created', {
                    detail: { job, url: this.url }
                }));
            } catch (err) {
                if (Alpine.store('app')) {
                    Alpine.store('app').toast(err.message, 'error');
                }
            } finally {
                this.isStartingDownload = false;
            }
        }
    });
});

window.DownloaderPreflight = {
    open(url, source) {
        const store = Alpine.store('preflight');
        if (store) {
            store.open(url, source);
        }
    }
};
