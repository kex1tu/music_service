/**
 * Компонент глобального поиска по медиатеке (Header Global Search).
 * Обрабатывает горячую клавишу Ctrl+K, поиск артистов/альбомов/треков
 * и переход к каталогу в файловом менеджере.
 */
function headerSearchComponent() {
    return {
        searchQuery: '',
        isOpen: false,
        isLoading: false,
        results: { query: '', artists: [], albums: [], tracks: [], total_matches: 0 },

        async performSearch() {
            const q = this.searchQuery.trim();
            if (!q) {
                this.isOpen = false;
                this.results = { query: '', artists: [], albums: [], tracks: [], total_matches: 0 };
                return;
            }
            this.isLoading = true;
            try {
                const res = await window.api.raw('/files/search?query=' + encodeURIComponent(q));
                if (res.ok) {
                    this.results = await res.json();
                    this.isOpen = true;
                }
            } catch (e) {
                console.error('Ошибка глобального поиска:', e);
            } finally {
                this.isLoading = false;
            }
        },

        getTrackDir(path) {
            if (!path || typeof path !== 'string') return '';
            const idx = path.lastIndexOf('/');
            return idx !== -1 ? path.substring(0, idx) : '';
        },

        navigateToDir(path) {
            this.isOpen = false;
            Alpine.store('app').activeTab = 'files';
            window.dispatchEvent(new CustomEvent('refresh-file-manager', { detail: path || '' }));
        }
    };
}
