/**
 * Компонент файлового менеджера (File Manager UI).
 * Отвечает за навигацию по постоянной медиатеке MUSIC_DIR, ленивую подгрузку
 * подкаталогов, открытие альбомов и треков в Tag Editor и безопасное удаление файлов.
 */

function fileManagerComponent() {
    return {
        currentPath: '',
        parentPath: null,
        breadcrumbs: [],
        items: [],
        totalDirectories: 0,
        totalFiles: 0,
        isLoading: false,

        // Пагинация и фильтрация
        searchQuery: '',
        currentPage: 1,
        pageSize: 50,
        lastLoadedAt: 0,

        get filteredItems() {
            if (!this.searchQuery || !this.searchQuery.trim()) {
                return this.items || [];
            }
            const q = this.searchQuery.toLowerCase().trim();
            return (this.items || []).filter(item => {
                return (item.name && item.name.toLowerCase().includes(q)) ||
                       (item.title && item.title.toLowerCase().includes(q)) ||
                       (item.artist && item.artist.toLowerCase().includes(q)) ||
                       (item.album && item.album.toLowerCase().includes(q));
            });
        },

        get totalFiltered() {
            return this.filteredItems.length;
        },

        get totalPages() {
            if (this.pageSize === 0) return 1;
            const len = this.totalFiltered;
            return Math.max(1, Math.ceil(len / this.pageSize));
        },

        get hasAudioTracksInCurrentFolder() {
            return (this.items || []).some(item => item.type === 'file' && item.extension === '.mp3');
        },

        get paginatedItems() {
            const list = this.filteredItems;
            if (this.pageSize === 0) return list;
            const start = (this.currentPage - 1) * this.pageSize;
            return list.slice(start, start + this.pageSize);
        },

        get paginationInfo() {
            const total = this.totalFiltered;
            if (total === 0) return '0 из 0';
            if (this.pageSize === 0) return `1–${total} из ${total}`;
            const start = (this.currentPage - 1) * this.pageSize + 1;
            const end = Math.min(this.currentPage * this.pageSize, total);
            return `${start}–${end} из ${total}`;
        },

        setPage(page) {
            const p = Math.max(1, Math.min(this.totalPages, page));
            this.currentPage = p;
        },

        prevPage() {
            if (this.currentPage > 1) {
                this.currentPage--;
            }
        },

        nextPage() {
            if (this.currentPage < this.totalPages) {
                this.currentPage++;
            }
        },

        setPageSize(size) {
            this.pageSize = size === 'all' || size === '0' || size === 0 ? 0 : Number(size);
            this.currentPage = 1;
        },

        onSearchInput() {
            this.currentPage = 1;
        },

        clearSearch() {
            this.searchQuery = '';
            this.currentPage = 1;
        },

        init() {
            this.loadDirectory('');

            // Слушаем событие обновления из других компонентов
            window.addEventListener('refresh-file-manager', (e) => {
                let targetPath = '';
                if (typeof e?.detail === 'string') {
                    targetPath = e.detail;
                } else if (e?.detail && typeof e.detail.path === 'string') {
                    targetPath = e.detail.path;
                } else if (typeof this.currentPath === 'string' && this.currentPath !== '[object Object]') {
                    targetPath = this.currentPath;
                }
                this.loadDirectory(targetPath);
            });

            // Автообновление при возврате на вкладку Файлы (с кулдауном 30 секунд)
            if (this.$watch) {
                this.$watch('$store.app.activeTab', (tab) => {
                    if (tab === 'files') {
                        if (Date.now() - this.lastLoadedAt > 30000) {
                            this.loadDirectory(this.currentPath);
                        }
                    }
                });
            }

            // Автообновление при переключении фокуса на окно браузера (с кулдауном 30 секунд)
            window.addEventListener('focus', () => {
                if (Alpine.store('app').activeTab === 'files') {
                    if (Date.now() - this.lastLoadedAt > 30000) {
                        this.loadDirectory(this.currentPath);
                    }
                }
            });
        },

        async loadDirectory(path) {
            if (typeof path !== 'string' || path === '[object Object]') {
                path = (typeof this.currentPath === 'string' && this.currentPath !== '[object Object]') ? this.currentPath : '';
            }
            this.isLoading = true;
            this.currentPage = 1;
            this.searchQuery = '';
            this.lastLoadedAt = Date.now();

            try {
                const res = await fetch(`/files?path=${encodeURIComponent(path || '')}&_t=${Date.now()}`);
                if (!res.ok) {
                    if (res.status === 401) {
                        return;
                    }
                    if (res.status === 404 && path) {
                        await this.loadDirectory('');
                        return;
                    }
                    const err = await res.json();
                    throw new Error(err.message || 'Ошибка загрузки каталога');
                }

                const data = await res.json();
                this.currentPath = data.current_path;
                this.parentPath = data.parent_path;
                this.breadcrumbs = data.breadcrumbs || [];
                this.items = data.items || [];
                this.totalDirectories = data.total_directories;
                this.totalFiles = data.total_files;
            } catch (err) {
                Alpine.store('app').toast(`Ошибка: ${err.message}`, 'error');
            } finally {
                this.isLoading = false;
            }
        },

        navigateTo(path) {
            this.loadDirectory(path);
        },

        navigateUp() {
            if (this.parentPath !== null && this.parentPath !== undefined) {
                this.loadDirectory(this.parentPath);
            }
        },

        editCurrentFolderTags() {
            if (!this.currentPath) return;
            this.openInTagEditor({
                type: 'directory',
                path: this.currentPath,
                name: this.currentPath.split('/').pop() || this.currentPath
            });
        },

        async openInTagEditor(item) {
            this.isLoading = true;

            try {
                const asAlbum = item.type === 'file';
                const url = asAlbum
                    ? `/tags?path=${encodeURIComponent(item.path)}&as_album=true&_t=${Date.now()}`
                    : `/tags?path=${encodeURIComponent(item.path)}&_t=${Date.now()}`;

                const res = await fetch(url);
                if (!res.ok) {
                    const err = await res.json();
                    throw new Error(err.message || 'Не удалось прочитать теги');
                }

                const data = await res.json();

                Alpine.store('app').openTagEditor({
                    album: data.album,
                    tracks: data.tracks,
                    sourceDir: item.path,
                    coverPath: data.cover_path,
                    hasCover: data.has_cover,
                });
            } catch (err) {
                Alpine.store('app').toast(`Ошибка: ${err.message}`, 'error');
            } finally {
                this.isLoading = false;
            }
        },

        promptDelete(item) {
            Alpine.store('app').deleteConfirmData = {
                path: item.path,
                name: item.name,
                isDirectory: item.type === 'directory',
            };
            Alpine.store('app').activeModal = 'delete_confirm';
        },

        async confirmDelete() {
            const data = Alpine.store('app').deleteConfirmData;
            if (!data) return;

            this.isLoading = true;

            try {
                const res = await fetch(`/files?path=${encodeURIComponent(data.path)}`, {
                    method: 'DELETE',
                });

                if (!res.ok) {
                    const err = await res.json();
                    throw new Error(err.message || 'Ошибка удаления');
                }

                Alpine.store('app').toast(`Удалено: ${data.name}`, 'success');
                Alpine.store('app').activeModal = null;
                Alpine.store('app').deleteConfirmData = null;

                await this.loadDirectory(this.currentPath);
                window.dispatchEvent(new CustomEvent('refresh-audit', { detail: { force: true } }));
            } catch (err) {
                Alpine.store('app').toast(`Ошибка: ${err.message}`, 'error');
            } finally {
                this.isLoading = false;
            }
        },

        cancelDelete() {
            Alpine.store('app').activeModal = null;
            Alpine.store('app').deleteConfirmData = null;
        },

        matchInAudit(item) {
            Alpine.store('app').activeTab = 'audit';
            window.dispatchEvent(new CustomEvent('match-album-audit', { detail: { path: item.path } }));
        },

        formatDuration(seconds) {
            if (window.AppFormatters) return window.AppFormatters.formatDuration(seconds);
            if (!seconds) return '--:--';
            const mins = Math.floor(seconds / 60);
            const secs = seconds % 60;
            return `${mins}:${secs < 10 ? '0' : ''}${secs}`;
        },

        formatSize(bytes) {
            if (window.AppFormatters) return window.AppFormatters.formatSize(bytes);
            if (!bytes) return '';
            const mb = bytes / (1024 * 1024);
            return `${mb.toFixed(1)} МБ`;
        },
    };
}
