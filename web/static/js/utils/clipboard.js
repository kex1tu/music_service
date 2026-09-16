/**
 * Безопасная работа с буфером обмена браузера.
 */
window.AppClipboard = {
    /**
     * Читает текстовое содержимое из буфера обмена.
     * Возвращает строку или null при отказе доступа.
     */
    async readText() {
        try {
            if (navigator.clipboard && navigator.clipboard.readText) {
                const text = await navigator.clipboard.readText();
                if (text) return text.trim();
            }
        } catch (err) {
            console.warn('AppClipboard: Clipboard API недоступен или заблокирован:', err);
        }
        return null;
    }
};
