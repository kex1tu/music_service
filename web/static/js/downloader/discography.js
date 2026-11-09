/**
 * Модуль выбора дискографии артиста (Downloader Discography).
 * Отвечает за парсинг ссылки на артиста, модальное окно выбора альбомов/синглов
 * и массовую отправку выбранных релизов в очередь загрузки.
 */
window.DownloaderDiscography = {
    checkIsArtistUrl(url) {
        if (!url) return false;
        const u = url.trim();
        if (/deezer\.com\/(?:[a-zA-Z]{2}\/)?artist\/\d+/i.test(u)) return true;
        if (/spotify\.com\/(?:[a-zA-Z]{2}\/)?artist\/[a-zA-Z0-9]+/i.test(u)) return true;
        if (/music\.yandex\.[a-zA-Z.]+\/artist\/\d+/i.test(u)) return true;
        if (/music\.apple\.com\/[a-zA-Z]{2}\/artist\/(?:[^/]+\/)?\d+/i.test(u)) return true;
        if (/(?:music\.)?youtube\.com\/(?:channel\/|@|c\/)/i.test(u)) return true;
        return false;
    },

    async openDiscographyModal(targetUrl) {
        const cleanUrl = (targetUrl || this.url || '').trim();
        if (!cleanUrl) {
            Alpine.store('app').toast('Введите ссылку на исполнителя', 'error');
            return;
        }

        this.isResolvingDiscography = true;
        try {
            const resp = await window.api.raw(`/downloads/discography?url=${encodeURIComponent(cleanUrl)}`);
            if (!resp.ok) {
                const err = await resp.json();
                throw new Error(err.detail || err.message || 'Не удалось получить дискографию');
            }
            const data = await resp.json();
            this.discographyData = data;
            this.selectedReleaseIds = {};
            if (data.releases) {
                data.releases.forEach((r) => {
                    this.selectedReleaseIds[r.id] = true;
                });
            }
            this.discographyFilter = 'all';
            this.discographySearch = '';
            this.showDiscographyModal = true;
            Alpine.store('app').toast(`Найдено релизов: ${data.total_releases}`, 'info');
        } catch (err) {
            Alpine.store('app').toast(err.message, 'error');
        } finally {
            this.isResolvingDiscography = false;
        }
    },

    closeDiscographyModal() {
        this.showDiscographyModal = false;
    },

    filteredReleases() {
        if (!this.discographyData || !this.discographyData.releases) return [];
        let list = this.discographyData.releases;
        if (this.discographyFilter !== 'all') {
            list = list.filter((r) => {
                const t = (r.type || 'album').toLowerCase();
                if (this.discographyFilter === 'album') return t === 'album';
                if (this.discographyFilter === 'single') return t === 'single' || t === 'ep';
                if (this.discographyFilter === 'compilation') return t === 'compilation' || t === 'compile';
                return true;
            });
        }
        if (this.discographySearch && this.discographySearch.trim()) {
            const q = this.discographySearch.trim().toLowerCase();
            list = list.filter((r) => {
                const titleMatch = (r.title || '').toLowerCase().includes(q);
                const yearMatch = r.year ? String(r.year).includes(q) : false;
                return titleMatch || yearMatch;
            });
        }
        return list;
    },

    toggleRelease(releaseId) {
        this.selectedReleaseIds[releaseId] = !this.selectedReleaseIds[releaseId];
    },

    selectAllFiltered(select = true) {
        const list = this.filteredReleases();
        list.forEach((r) => {
            this.selectedReleaseIds[r.id] = select;
        });
    },

    getSelectedCount() {
        if (!this.discographyData || !this.discographyData.releases) return 0;
        return this.discographyData.releases.filter((r) => this.selectedReleaseIds[r.id]).length;
    },

    async submitDiscographyBatch() {
        if (!this.discographyData || !this.discographyData.releases) return;
        const selectedUrls = this.discographyData.releases
            .filter((r) => this.selectedReleaseIds[r.id])
            .map((r) => r.url);

        if (selectedUrls.length === 0) {
            Alpine.store('app').toast('Выберите хотя бы один релиз для скачивания', 'error');
            return;
        }

        this.isSubmitting = true;
        try {
            const resp = await window.api.raw('/downloads/batch', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    urls: selectedUrls,
                    source: 'auto',
                }),
            });
            if (!resp.ok) {
                const err = await resp.json();
                throw new Error(err.detail || err.message || 'Ошибка пакетной постановки задач');
            }
            const batchData = await resp.json();
            Alpine.store('app').toast(`Поставлено в очередь релизов: ${batchData.total_queued}`, 'success');
            this.closeDiscographyModal();
            this.url = '';
            if (Alpine.store('app').queue) {
                Alpine.store('app').queue.isOpen = true;
                await Alpine.store('app').queue.fetchQueue();
            }
        } catch (err) {
            Alpine.store('app').toast(err.message, 'error');
        } finally {
            this.isSubmitting = false;
        }
    }
};
