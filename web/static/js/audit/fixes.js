/**
 * Модуль исправления проблем медиатеки (Audit Fixes).
 * Отвечает за реорганизацию файлов по альбомам, автотегирование жанров,
 * исправление путей и безопасное удаление дубликатов.
 */
window.AuditFixes = {
    confirmReorganizeLibrary() {
        Alpine.store('app').activeModal = 'reorganize_confirm';
    },

    async reorganizeLibrary() {
        if (this.isReorganizing) return;
        this.isReorganizing = true;
        Alpine.store('app').activeModal = null;
        Alpine.store('app').toast('Запущена реорганизация медиатеки по альбомам...', 'info');
        try {
            const res = await window.api.raw('/files/audit/reorganize', { method: 'POST' });
            if (res.ok) {
                const data = await res.json();
                if (data.errors && data.errors.length > 0) {
                    Alpine.store('app').toast(
                        `Реорганизация завершена с предупреждениями: перемещено ${data.moved_tracks} треков из ${data.total_tracks}`,
                        'warning'
                    );
                } else {
                    Alpine.store('app').toast(
                        `Реорганизация завершена: перемещено ${data.moved_tracks} из ${data.total_tracks} треков (${data.total_albums} альбомов)`,
                        'success'
                    );
                }
                if (this.loadAll) await this.loadAll(false);
                window.dispatchEvent(new CustomEvent('refresh-file-manager', { detail: '' }));
            } else {
                let msg = 'Ошибка при реорганизации медиатеки';
                try {
                    const errData = await res.json();
                    if (errData && errData.detail) msg = errData.detail;
                } catch (_) {}
                Alpine.store('app').toast(msg, 'error');
            }
        } catch (e) {
            console.error('Ошибка реорганизации:', e);
            Alpine.store('app').toast('Сбой сетевого запроса при реорганизации', 'error');
        } finally {
            this.isReorganizing = false;
        }
    },

    async autoRetagGenres() {
        if (this.isRetaggingGenres) return;
        this.isRetaggingGenres = true;
        Alpine.store('app').toast('Запущена автоматическая перезапись тегов жанров...', 'info');
        try {
            const res = await window.api.raw('/files/audit/retag-genres', { method: 'POST' });
            if (res.ok) {
                const data = await res.json();
                if (data.updated_count > 0) {
                    Alpine.store('app').toast(`Успешно перезаписаны теги у ${data.updated_count} треков`, 'success');
                } else {
                    Alpine.store('app').toast('Все жанры уже соответствуют каноническому стандарту', 'info');
                }
                if (this.loadNonEnGenres) await this.loadNonEnGenres();
                if (this.loadSummary) await this.loadSummary();
            } else {
                let msg = 'Ошибка при перезаписи тегов жанров';
                try {
                    const errData = await res.json();
                    if (errData && errData.detail) msg = errData.detail;
                } catch (_) {}
                Alpine.store('app').toast(msg, 'error');
            }
        } catch (e) {
            console.error('Ошибка автоперезаписи жанров:', e);
            Alpine.store('app').toast('Сбой сетевого запроса при перезаписи жанров', 'error');
        } finally {
            this.isRetaggingGenres = false;
        }
    },

    async fixPath(item) {
        if (item._isFixing) return;
        item._isFixing = true;
        try {
            const res = await window.api.raw('/files/audit/fix-path', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ path: item.path, overwrite: true })
            });
            if (res.ok) {
                const data = await res.json();
                Alpine.store('app').toast(`Файл перемещен в: ${data.target_path}`, 'success');
                if (this.loadInvalidPaths) await this.loadInvalidPaths();
                if (this.loadSummary) await this.loadSummary();
                window.dispatchEvent(new CustomEvent('refresh-file-manager', { detail: '' }));
            } else {
                let msg = 'Ошибка исправления пути';
                try {
                    const err = await res.json();
                    if (err && err.detail) msg = err.detail;
                } catch (_) {}
                Alpine.store('app').toast(msg, 'error');
            }
        } catch (e) {
            console.error(e);
            Alpine.store('app').toast('Сбой сетевого запроса при исправлении пути', 'error');
        } finally {
            item._isFixing = false;
        }
    },

    async fixAllPaths() {
        if (this.isFixingPaths) return;
        this.isFixingPaths = true;
        Alpine.store('app').toast('Исправление путей всех некорректных файлов...', 'info');
        try {
            const res = await window.api.raw('/files/audit/fix-all-paths', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ overwrite: true })
            });
            if (res.ok) {
                const data = await res.json();
                if (data.errors && data.errors.length > 0) {
                    Alpine.store('app').toast(
                        `Исправлено путей: ${data.fixed_count} из ${data.total_invalid} (с ошибками: ${data.errors.length})`,
                        'warning'
                    );
                } else {
                    Alpine.store('app').toast(`Успешно исправлено путей: ${data.fixed_count} из ${data.total_invalid}`, 'success');
                }
                if (this.loadInvalidPaths) await this.loadInvalidPaths();
                if (this.loadSummary) await this.loadSummary();
                window.dispatchEvent(new CustomEvent('refresh-file-manager', { detail: '' }));
            } else {
                let msg = 'Ошибка при пакетном исправлении путей';
                try {
                    const err = await res.json();
                    if (err && err.detail) msg = err.detail;
                } catch (_) {}
                Alpine.store('app').toast(msg, 'error');
            }
        } catch (e) {
            console.error(e);
            Alpine.store('app').toast('Сбой сетевого запроса при исправлении путей', 'error');
        } finally {
            this.isFixingPaths = false;
        }
    },

    async fixDuplicateGroup(grp) {
        if (grp._isFixing) return;
        grp._isFixing = true;
        Alpine.store('app').toast(`Удаление дубликатов для "${grp.artist} — ${grp.title}"...`, 'info');
        try {
            const res = await window.api.raw('/files/audit/duplicates/fix', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ group_key: grp.key, album_path: grp.album_dir || null }),
            });
            if (res.ok) {
                const data = await res.json();
                const review = data.review_groups || 0;
                const suffix = review ? `. Групп для ручной проверки — ${review}` : '';
                Alpine.store('app').toast(`Удалено копий — ${data.deleted_count}${suffix}`, review ? 'warning' : 'success');
                if (this.loadDuplicates) await this.loadDuplicates();
                if (this.loadSummary) await this.loadSummary();
                window.dispatchEvent(new CustomEvent('refresh-file-manager', { detail: '' }));
            } else {
                let msg = 'Ошибка при удалении дубликатов';
                try {
                    const err = await res.json();
                    if (err && err.detail) msg = err.detail;
                } catch (_) {}
                Alpine.store('app').toast(msg, 'error');
            }
        } catch (e) {
            console.error(e);
            Alpine.store('app').toast('Сетевая ошибка при удалении дубликатов', 'error');
        } finally {
            grp._isFixing = false;
        }
    },

    async fixAllDuplicates() {
        if (this.isFixingDuplicates) return;
        this.isFixingDuplicates = true;
        Alpine.store('app').toast('Проверка групп и удаление совпадающих копий...', 'info');
        try {
            const res = await window.api.raw('/files/audit/duplicates/fix-all', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
            });
            if (res.ok) {
                const data = await res.json();
                const review = data.review_groups || 0;
                const suffix = review ? `. Групп для ручной проверки — ${review}` : '';
                Alpine.store('app').toast(`Удалено копий — ${data.deleted_count}${suffix}`, review ? 'warning' : 'success');
                if (this.loadDuplicates) await this.loadDuplicates();
                if (this.loadSummary) await this.loadSummary();
                window.dispatchEvent(new CustomEvent('refresh-file-manager', { detail: '' }));
            } else {
                let msg = 'Ошибка при удалении дубликатов';
                try {
                    const err = await res.json();
                    if (err && err.detail) msg = err.detail;
                } catch (_) {}
                Alpine.store('app').toast(msg, 'error');
            }
        } catch (e) {
            console.error(e);
            Alpine.store('app').toast('Сетевая ошибка при удалении всех дубликатов', 'error');
        } finally {
            this.isFixingDuplicates = false;
        }
    },

    openDirectory(path) {
        Alpine.store('app').activeTab = 'files';
        window.dispatchEvent(new CustomEvent('refresh-file-manager', { detail: path }));
    },

    promptDeleteDuplicate(track) {
        Alpine.store('app').deleteConfirmData = {
            path: track.path,
            name: track.title || track.path.split('/').pop(),
            isDirectory: false,
        };
        Alpine.store('app').activeModal = 'delete_confirm';
    },

    async normalizeNavidrome() {
        if (this.isNormalizingNavidrome) return;
        this.isNormalizingNavidrome = true;
        Alpine.store('app').toast('Запущена синхронизация медиатеки для Navidrome (права, теги и нормализация синглов)...', 'info');
        try {
            const res = await window.api.raw('/files/audit/normalize-navidrome', { method: 'POST' });
            if (res.ok) {
                const data = await res.json();
                const rep = data.report || {};
                const scanned = rep.total_scanned || 0;
                const perms = (rep.files_perms_fixed || 0) + (rep.directories_fixed || 0);
                const tags = rep.tags_normalized || 0;
                const singles = rep.singles_fixed || 0;
                Alpine.store('app').toast(
                    `Синхронизация Navidrome завершена: проверено ${scanned} файлов, исправлено прав: ${perms}, нормализовано тегов: ${tags}, исправлено синглов: ${singles}`,
                    'success'
                );
                if (this.loadAll) await this.loadAll(true);
                window.dispatchEvent(new CustomEvent('refresh-file-manager', { detail: '' }));
            } else {
                let msg = 'Ошибка синхронизации Navidrome';
                try {
                    const err = await res.json();
                    if (err && err.error) msg = err.error;
                } catch (_) {}
                Alpine.store('app').toast(msg, 'error');
            }
        } catch (e) {
            console.error('Ошибка normalizeNavidrome:', e);
            Alpine.store('app').toast('Сетевая ошибка при синхронизации Navidrome', 'error');
        } finally {
            this.isNormalizingNavidrome = false;
        }
    }
};
