/**
 * Общие утилиты форматирования данных для медиатеки и тегов.
 */
window.AppFormatters = {
    /**
     * Форматирует длительность в секундах в строку 'MM:SS'.
     */
    formatDuration(seconds) {
        if (!seconds || isNaN(seconds)) return '--:--';
        const mins = Math.floor(seconds / 60);
        const secs = Math.floor(seconds % 60);
        return `${mins}:${secs < 10 ? '0' : ''}${secs}`;
    },

    /**
     * Форматирует размер файла в байтах в человекочитаемую строку 'X.X МБ'.
     */
    formatSize(bytes) {
        if (!bytes || isNaN(bytes)) return '';
        const mb = bytes / (1024 * 1024);
        return `${mb.toFixed(1)} МБ`;
    },

    /**
     * Нормализует строку или массив жанров: разделяет по запятым, слэшам, точкам с запятой,
     * очищает дубликаты, приводит Hip-Hop к единому регистру и объединяет через '\\'.
     */
    formatGenres(raw) {
        if (!raw) return '';
        let items = [];
        if (Array.isArray(raw)) {
            raw.forEach((el) => {
                if (el) items.push(...String(el).split(/[\x00/;,|]|\s*\\+\s*/));
            });
        } else {
            items = String(raw).split(/[\x00/;,|]|\s*\\+\s*/);
        }

        const cleaned = [];
        const seen = new Set();
        const hipHopRe = /(^|[^a-zA-Z0-9а-яА-ЯёЁ])(?:hip[- ]?hop|хип[- ]?хоп)(?=$|[^a-zA-Z0-9а-яА-ЯёЁ])/gi;

        for (const item of items) {
            let part = item.trim();
            if (!part) continue;
            part = part.replace(hipHopRe, (m, p1) => (p1 || '') + 'Hip-Hop');
            const lower = part.toLowerCase();
            if (!seen.has(lower)) {
                seen.add(lower);
                cleaned.push(part);
            }
        }
        return cleaned.join('\\\\');
    }
};
