/**
 * Модуль загрузки и перетаскивания .torrent файлов (Downloader Torrent).
 */
window.DownloaderTorrent = {
    async uploadTorrentFile(event) {
        const file = event.target?.files?.[0] || event.dataTransfer?.files?.[0];
        if (!file) return;

        if (!file.name.toLowerCase().endsWith('.torrent')) {
            Alpine.store('app').toast('Допускаются только файлы .torrent', 'error');
            return;
        }

        this.isSubmitting = true;
        this.job = null;
        if (this.pollInterval) {
            clearInterval(this.pollInterval);
            this.pollInterval = null;
        }

        try {
            const formData = new FormData();
            formData.append('file', file);

            const response = await fetch('/downloads/torrent', {
                method: 'POST',
                body: formData,
            });

            if (!response.ok) {
                const err = await response.json();
                throw new Error(err.message || 'Ошибка загрузки .torrent файла');
            }

            const jobData = await response.json();
            this.job = jobData;
            Alpine.store('app').currentJobId = jobData.id;
            Alpine.store('app').toast(`Торрент "${file.name}" поставлен в очередь`, 'success');

            if (this.startPolling) {
                this.startPolling(jobData.id);
            }
        } catch (err) {
            Alpine.store('app').toast(err.message, 'error');
        } finally {
            this.isSubmitting = false;
            if (event.target && event.target.value) {
                event.target.value = '';
            }
        }
    },

    handleTorrentDrop(event) {
        event.preventDefault();
        this.uploadTorrentFile(event);
    },

    handleTorrentDragOver(event) {
        event.preventDefault();
    }
};
