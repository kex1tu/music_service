/**
 * Центральное реактивное состояние приложения (Alpine Global Store).
 * Управляет авторизацией, активными вкладками, модальными окнами,
 * уведомлениями (тостами) и очередью фоновых загрузок.
 */
document.addEventListener('alpine:init', () => {
    Alpine.store('app', {
        user: null,
        activeTab: 'downloader',
        activeModal: null,
        toasts: [],
        currentJobId: null,
        isDesktopLayout: false,

        tagEditorData: null,
        deleteConfirmData: null,
        redownloadConfirmData: null,
        redownloadReplacement: null,

        // Подключаем менеджер очереди
        queue: null,

        init() {
            const desktopLayout = window.matchMedia('(min-width: 768px)');
            this.isDesktopLayout = desktopLayout.matches;
            desktopLayout.addEventListener('change', (event) => {
                this.isDesktopLayout = event.matches;
            });
            this.queue = window.createQueueManager();
            Alpine.store('queue', this.queue);
            this.checkAuth();
            this.queue.init();
        },

        async checkAuth() {
            try {
                const data = window.api ? await window.api.auth.me() : await window.api.raw('/api/auth/me').then(r => r.json());
                if (data && data.authenticated && data.login) {
                    this.user = data.login;
                } else {
                    this.user = null;
                }
            } catch (e) {
                this.user = null;
            }
        },

        async logout() {
            try {
                if (window.api) {
                    await window.api.auth.logout();
                } else {
                    await window.api.raw('/logout', { method: 'POST' });
                }
            } catch (e) {
                console.error('Ошибка выхода:', e);
            }
            this.user = null;
            this.toast('Вы вышли из системы', 'info');
        },

        async executeDeleteConfirm() {
            const item = this.deleteConfirmData;
            if (!item) return;
            try {
                if (item.paths && Array.isArray(item.paths) && item.paths.length > 0) {
                    if (window.api && window.api.files) {
                        await window.api.files.deleteBatch(item.paths);
                    } else {
                        const res = await window.api.raw('/files', {
                            method: 'DELETE',
                            headers: { 'Content-Type': 'application/json' },
                            body: JSON.stringify({ paths: item.paths })
                        });
                        if (!res.ok) {
                            const err = await res.json().catch(() => ({}));
                            throw new Error(err.error || err.detail || err.message || 'Сбой при пакетном удалении');
                        }
                    }
                } else if (item.path) {
                    if (window.api && window.api.files) {
                        await window.api.files.delete(item.path);
                    } else {
                        const res = await window.api.raw(`/files?path=${encodeURIComponent(item.path)}`, { method: 'DELETE' });
                        if (!res.ok) {
                            const err = await res.json().catch(() => ({}));
                            throw new Error(err.error || err.detail || err.message || 'Сбой при удалении');
                        }
                    }
                } else {
                    return;
                }
                this.toast(`Удалено: ${item.name}`, 'success');
                this.activeModal = null;
                this.deleteConfirmData = null;
                if (item.onDeleted && typeof item.onDeleted === 'function') {
                    await item.onDeleted();
                }
                window.dispatchEvent(new CustomEvent('refresh-file-manager', { detail: '' }));
            } catch (e) {
                this.toast(`Ошибка: ${e.message}`, 'error');
            }
        },

        async executeRedownloadConfirm(chooseSource = false) {
            const item = this.redownloadConfirmData;
            if (!item || !item.path) return;
            try {
                const payload = {
                    path: item.path,
                    type: item.type || 'track',
                    delete_existing: false,
                    choose_source: chooseSource,
                };
                let resData;
                if (window.api && window.api.files && window.api.files.redownload) {
                    resData = await window.api.files.redownload(payload);
                } else {
                    const res = await window.api.raw('/files/redownload', {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify(payload)
                    });
                    if (!res.ok) {
                        const err = await res.json().catch(() => ({}));
                        throw new Error(err.error || err.detail || err.message || 'Сбой при повторном скачивании');
                    }
                    resData = await res.json();
                }

                this.toast(resData.message || `Поставлено в очередь: ${item.name}`, 'success');
                this.activeModal = null;
                this.redownloadConfirmData = null;
                if (resData.job_id && (resData.type === 'album' || !resData.source_url)) {
                    const response = await window.api.raw(`/downloads/${encodeURIComponent(resData.job_id)}`);
                    if (!response.ok) throw new Error('Не удалось открыть задачу');
                    const job = await response.json();
                    if (resData.type === 'album') Alpine.store('releaseSourcePicker').openForJob(job);
                    else if (job.tracks?.length) Alpine.store('sourcePicker').openForQueueTrack(job, job.tracks[0]);
                }

                if (this.queue && this.queue.fetchQueue) {
                    await this.queue.fetchQueue();
                }
                if (item.onSuccess && typeof item.onSuccess === 'function') {
                    await item.onSuccess(resData);
                }
                window.dispatchEvent(new CustomEvent('refresh-file-manager', { detail: '' }));
            } catch (e) {
                this.toast(`Ошибка: ${e.message}`, 'error');
            }
        },

        async openRedownloadReplacement(jobID) {
            try {
                const response = await window.api.raw(`/downloads/${encodeURIComponent(jobID)}`);
                if (!response.ok) throw new Error('Не удалось открыть задачу');
                const job = await response.json();
                const confirmed = job.confirmed_redownload_tracks || [];
                const tracks = (job.tracks || []).filter(t => t.status === 'ready' && t.converted_path && !confirmed.includes(t.track_id));
                if (!tracks.length) {
                    this.toast('Нет новых дорожек для подтверждения', 'info');
                    return;
                }
                this.redownloadReplacement = { jobID, tracks, selected: tracks.map(t => t.track_id), busy: false, errors: {} };
                this.activeModal = 'redownload_replace';
            } catch (error) {
                this.toast(error.message, 'error');
            }
        },

        async confirmRedownloadReplacement() {
            const data = this.redownloadReplacement;
            if (!data || data.busy || !data.selected.length) return;
            data.busy = true;
            try {
                const response = await window.api.raw(`/files/redownload/${encodeURIComponent(data.jobID)}/confirm`, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ track_ids: data.selected }),
                });
                const payload = await response.json();
                if (!response.ok) throw new Error(payload.error || 'Не удалось сохранить дорожки');
                const applied = new Set((payload.results || []).filter(r => !r.error).map(r => r.track_id));
                const errors = (payload.results || []).filter(r => r.error);
                data.tracks = data.tracks.filter(t => !applied.has(t.track_id));
                data.selected = data.selected.filter(id => !applied.has(id));
                data.errors = Object.fromEntries(errors.map(r => [r.track_id, r.error]));
                if (!errors.length) this.activeModal = null;
                if (applied.size) {
                    this.toast(`Сохранено дорожек: ${applied.size}`, 'success');
                    await this.queue?.fetchQueue?.();
                    window.dispatchEvent(new CustomEvent('refresh-file-manager', { detail: '' }));
                }
            } catch (error) {
                this.toast(error.message, 'error');
            } finally {
                data.busy = false;
            }
        },

        toast(message, type = 'info') {
            const id = Date.now() + Math.random();
            this.toasts.push({ id, message, type });
            setTimeout(() => {
                this.toasts = this.toasts.filter(t => t.id !== id);
            }, 4000);
        },

        showToast(message, type = 'info') {
            this.toast(message, type);
        },

        openTagEditor(data) {
            this.tagEditorData = data;
            this.activeModal = 'tag_editor';
            window.dispatchEvent(new CustomEvent('puddletag-load-job', { detail: data }));
        },

        closeTagEditor() {
            this.tagEditorData = null;
            if (this.activeModal === 'tag_editor') {
                this.activeModal = null;
            }
        },

        openLyricsModal(track) {
            this.activeModal = 'lyrics';
            window.dispatchEvent(new CustomEvent('lyrics-modal-opened', { detail: track }));
        },

        closeLyricsModal() {
            if (this.activeModal === 'lyrics') {
                if (this.tagEditorData) {
                    this.activeModal = 'tag_editor';
                } else {
                    this.activeModal = null;
                }
            }
        },

        openLoginModal() {
            this.activeModal = 'login';
        },

        closeLoginModal() {
            if (this.activeModal === 'login') {
                this.activeModal = null;
            }
        },

        isMobileSearchOpen: false,

        toggleMobileSearch() {
            this.isMobileSearchOpen = !this.isMobileSearchOpen;
        },

        closeMobileSearch() {
            this.isMobileSearchOpen = false;
        }
    });
});
