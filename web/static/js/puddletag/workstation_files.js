const puddletagWorkstationFiles = {
    // ==================== УДАЛЕНИЕ ФАЙЛОВ И ПАПОК ====================

    promptDeleteTrack(track) {
        if (!track || !track.path) return;
        const displayName = (track.artist && track.title) ? `${track.artist} - ${track.title}` : (track.name || track.path);
        const app = Alpine.store('app');
        if (app) {
            app.deleteConfirmData = {
                name: displayName,
                path: track.path,
                type: 'file',
                onDeleted: async () => {
                    await this.loadDirectory(this.currentPath);
                }
            };
            app.activeModal = 'delete_confirm';
        }
    },

    promptDeleteDirectory(dir) {
        if (!dir || !dir.path) return;
        const app = Alpine.store('app');
        if (app) {
            app.deleteConfirmData = {
                name: dir.name || dir.path,
                path: dir.path,
                type: 'directory',
                onDeleted: async () => {
                    await this.loadDirectory(this.currentPath);
                }
            };
            app.activeModal = 'delete_confirm';
        }
    },

    promptDeleteCurrentFolder() {
        if (!this.currentPath) return;
        const app = Alpine.store('app');
        if (app) {
            const folderName = this.currentPath.split('/').pop() || this.currentPath;
            app.deleteConfirmData = {
                name: folderName,
                path: this.currentPath,
                type: 'directory',
                onDeleted: async () => {
                    const target = this.parentPath || '';
                    await this.loadDirectory(target);
                }
            };
            app.activeModal = 'delete_confirm';
        }
    },

    promptDeleteSelected() {
        if (!this.selectedIndices || this.selectedIndices.length === 0) {
            const app = Alpine.store('app');
            if (app && app.toast) {
                app.toast('Нет выбранных треков для удаления', 'warning');
            }
            return;
        }
        const paths = this.selectedIndices.map(i => this.tracks[i] && this.tracks[i].path).filter(Boolean);
        if (paths.length === 0) return;

        const app = Alpine.store('app');
        if (app) {
            app.deleteConfirmData = {
                name: `${paths.length} выбранных треков`,
                paths: paths,
                type: 'batch',
                onDeleted: async () => {
                    this.selectedIndices = [];
                    this._selectedSet = new Set();
                    await this.loadDirectory(this.currentPath);
                }
            };
            app.activeModal = 'delete_confirm';
        }
    },

    // ==================== ПОВТОРНОЕ СКАЧИВАНИЕ (REDOWNLOAD) ====================

    promptRedownloadTrack(track) {
        if (!track || !track.path) return;
        const displayName = (track.artist && track.title) ? `${track.artist} - ${track.title}` : (track.name || track.path);
        const app = Alpine.store('app');
        if (app) {
            app.redownloadConfirmData = {
                name: displayName,
                path: track.path,
                type: 'track',
                delete_existing: true,
                onSuccess: async () => {
                    await this.loadDirectory(this.currentPath);
                }
            };
            app.activeModal = 'redownload_confirm';
        }
    },

    promptRedownloadDirectory(dir) {
        if (!dir || !dir.path) return;
        const app = Alpine.store('app');
        if (app) {
            app.redownloadConfirmData = {
                name: dir.name || dir.path,
                path: dir.path,
                type: 'album',
                delete_existing: true,
                onSuccess: async () => {
                    await this.loadDirectory(this.currentPath);
                }
            };
            app.activeModal = 'redownload_confirm';
        }
    },

    promptRedownloadCurrentFolder() {
        if (!this.currentPath) return;
        const app = Alpine.store('app');
        if (app) {
            const folderName = this.currentPath.split('/').pop() || this.currentPath;
            app.redownloadConfirmData = {
                name: folderName,
                path: this.currentPath,
                type: 'album',
                delete_existing: true,
                onSuccess: async () => {
                    const target = this.parentPath || '';
                    await this.loadDirectory(target);
                }
            };
            app.activeModal = 'redownload_confirm';
        }
    },
};
