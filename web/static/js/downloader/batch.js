/**
 * Модуль пакетного ввода и парсинга ссылок для Downloader.
 */
window.DownloaderBatch = {
    async pasteBatchUrls(downloader) {
        try {
            const text = window.AppClipboard ? await window.AppClipboard.readText() : null;
            if (text) {
                if (downloader.batchUrlsText && downloader.batchUrlsText.trim()) {
                    downloader.batchUrlsText += '\n' + text;
                } else {
                    downloader.batchUrlsText = text;
                }
                Alpine.store('app').toast('Ссылки вставлены из буфера обмена', 'info');
            }
        } catch (err) {
            Alpine.store('app').toast('Не удалось получить текст из буфера обмена', 'error');
        }
    },

    getParsedBatchUrls(downloader) {
        const raw = (downloader.batchUrlsText || '').trim();
        if (!raw) return [];

        const lines = raw.split(/[\r\n]+/);
        const urls = [];
        for (const line of lines) {
            const trimmed = line.trim();
            if (!trimmed) continue;

            const tokens = trimmed.split(/[\s,]+/);
            if (
                tokens.length > 1 &&
                tokens.every((t) => /^https?:\/\//i.test(t) || /^magnet:/i.test(t) || t.includes('.'))
            ) {
                urls.push(...tokens);
            } else {
                urls.push(trimmed);
            }
        }
        return urls.filter((u) => u.length > 0);
    },

    async submitBatchDownloads(downloader) {
        const rawUrls = this.getParsedBatchUrls(downloader);
        if (!rawUrls.length) {
            Alpine.store('app').toast('Введите хотя бы одну ссылку в список', 'error');
            return;
        }

        const urls = rawUrls.map((u) =>
            !/^https?:\/\//i.test(u) && !/^magnet:/i.test(u) && u.includes('.') ? 'https://' + u : u
        );

        downloader.isSubmitting = true;
        try {
            const response = await fetch('/downloads/batch', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    urls: urls,
                    source: downloader.source,
                }),
            });

            if (!response.ok) {
                const err = await response.json();
                throw new Error(err.message || err.error || 'Ошибка пакетной загрузки');
            }

            const batchData = await response.json();
            downloader.batchUrlsText = '';
            Alpine.store('app').toast(`Поставлено в очередь задач: ${batchData.total_queued}`, 'success');
            if (Alpine.store('app').queue) {
                Alpine.store('app').queue.isOpen = true;
                await Alpine.store('app').queue.fetchQueue();
            }
        } catch (err) {
            Alpine.store('app').toast(err.message, 'error');
        } finally {
            downloader.isSubmitting = false;
        }
    }
};
