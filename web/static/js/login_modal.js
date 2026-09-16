/**
 * Компонент модального окна авторизации (Login Modal).
 */
function loginModalComponent() {
    return {
        loginUser: '',
        password: '',
        isLoggingIn: false,
        error: null,

        close() {
            Alpine.store('app').closeLoginModal();
        },

        async doLogin() {
            this.isLoggingIn = true;
            this.error = null;
            try {
                const res = await fetch('/login', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ login: this.loginUser, password: this.password })
                });
                if (!res.ok) {
                    const err = await res.json();
                    throw new Error(err.message || 'Неверный логин или пароль');
                }
                Alpine.store('app').user = this.loginUser;
                Alpine.store('app').toast('Авторизация успешна', 'success');
                this.close();
                if (Alpine.store('app').queue) {
                    Alpine.store('app').queue.wakeUpPolling();
                }
                window.dispatchEvent(new CustomEvent('refresh-file-manager'));
            } catch (e) {
                this.error = e.message;
            } finally {
                this.isLoggingIn = false;
            }
        }
    };
}
