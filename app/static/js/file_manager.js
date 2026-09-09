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

        init() {
            this.loadDirectory('');

            // Слушаем событие обновления из других компонентов
            window.addEventListener('refresh-file-manager', (e) => {
                const targetPath = e.detail || this.currentPath;
                this.loadDirectory(targetPath);
            });
        },

        async loadDirectory(path) {
            this.isLoading = true;

            try {
                const res = await fetch(`/files?path=${encodeURIComponent(path || '')}`);
                if (!res.ok) {
                    if (res.status === 401) {
                        return; // Не спамить ошибками неавторизованному пользователю при старте
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

        async openInTagEditor(item) {
            this.isLoading = true;

            try {
                const asAlbum = item.type === 'file';
                const url = asAlbum
                    ? `/tags?path=${encodeURIComponent(item.path)}&as_album=true`
                    : `/tags?path=${encodeURIComponent(item.path)}`;

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

                // Перезагружаем текущую директорию
                await this.loadDirectory(this.currentPath);
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

        formatDuration(seconds) {
            if (!seconds) return '--:--';
            const mins = Math.floor(seconds / 60);
            const secs = seconds % 60;
            return `${mins}:${secs < 10 ? '0' : ''}${secs}`;
        },

        formatSize(bytes) {
            if (!bytes) return '';
            const mb = bytes / (1024 * 1024);
            return `${mb.toFixed(1)} МБ`;
        },
    };
}
