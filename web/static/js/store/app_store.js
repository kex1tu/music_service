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

        tagEditorData: null,
        deleteConfirmData: null,

        // Подключаем менеджер очереди
        queue: null,

        init() {
            this.queue = window.createQueueManager();
            Alpine.store('queue', this.queue);
            this.checkAuth();
            this.queue.init();
        },

        async checkAuth() {
            try {
                const data = window.api ? await window.api.auth.me() : await fetch('/api/auth/me').then(r => r.json());
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
                    await fetch('/logout', { method: 'POST' });
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
                if (window.api && window.api.files) {
                    await window.api.files.delete(item.path);
                } else {
                    const res = await fetch(`/files?path=${encodeURIComponent(item.path)}`, { method: 'DELETE' });
                    if (!res.ok) {
                        const err = await res.json().catch(() => ({}));
                        throw new Error(err.message || 'Сбой при удалении');
                    }
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
        }
    });
});
