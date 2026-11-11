document.addEventListener('alpine:init', () => {
    // 1. Автономный поиск SoundCloud в главном загрузчике
    Alpine.store('soundCloudSearch', {
        query: '',
        searchedQuery: '',
        loading: false,
        error: '',
        serial: 0,
        groups: [],
        labels: { track: 'Треки', album: 'Альбомы', playlist: 'Плейлисты' },

        hasMore() {
            return this.groups.some(group => group.next_offset >= 0 || Boolean(group.next_token));
        },

        total() {
            return this.groups.reduce((count, group) => count + group.items.length, 0);
        },

        async search(more = false) {
            const query = (more ? this.searchedQuery : this.query).trim();
            if (!query || (more && this.loading)) return;
            const serial = ++this.serial;
            this.loading = true;
            this.error = '';
            if (!more) {
                this.groups = [];
                this.searchedQuery = query;
            }
            const params = new URLSearchParams({ q: query });
            if (more) {
                for (const group of this.groups) {
                    if (group.next_token) {
                        params.set(`${group.type}_token`, group.next_token);
                    } else if (group.next_offset >= 0) {
                        params.set(`${group.type}_offset`, String(group.next_offset));
                    }
                }
            }
            try {
                const response = await window.api.raw(`/api/downloads/soundcloud/search?${params}`);
                const data = await response.json();
                if (!response.ok) throw new Error(data.error || data.message || 'Не удалось выполнить поиск');
                if (serial !== this.serial) return;
                for (const incoming of data.groups || []) {
                    const existing = this.groups.find(group => group.type === incoming.type);
                    if (more && existing) {
                        const seen = new Set(existing.items.map(item => item.url));
                        existing.items.push(...incoming.items.filter(item => !seen.has(item.url)));
                        existing.next_offset = incoming.next_offset;
                        existing.next_token = incoming.next_token || '';
                        existing.error = incoming.error || '';
                    } else {
                        this.groups.push(incoming);
                    }
                }
            } catch (error) {
                if (serial === this.serial) this.error = error.message;
            } finally {
                if (serial === this.serial) this.loading = false;
            }
        },

        preview(item) {
            Alpine.store('preflight').open(item.url, 'soundcloud');
        },
    });

    // 2. Интерактивный модальный поиск SoundCloud внутри Source Pickers
    Alpine.store('soundCloudPicker', {
        isOpen: false,
        mode: 'track', // 'track' | 'release'
        parentContext: null,
        query: '',
        searchedQuery: '',
        activeTab: 'track', // 'track' | 'album' | 'playlist'
        labels: { track: 'Треки', album: 'Альбомы', playlist: 'Плейлисты' },
        loading: false,
        error: '',
        detailsLoadingId: null,
        detailsError: '',
        serial: 0,
        openerElement: null,

        groups: [
            { type: 'track', items: [], next_offset: -1, next_token: '', error: '', loading: false, exhausted: false },
            { type: 'album', items: [], next_offset: -1, next_token: '', error: '', loading: false, exhausted: false },
            { type: 'playlist', items: [], next_offset: -1, next_token: '', error: '', loading: false, exhausted: false },
        ],

        getTabCount(type) {
            const g = this.groups.find(group => group.type === type);
            return g ? g.items.length : 0;
        },

        currentGroup() {
            return this.groups.find(group => group.type === this.activeTab) || this.groups[0];
        },

        canSelect(item, type) {
            if (this.mode === 'track') return type === 'track';
            if (this.mode === 'release') return type === 'album' || type === 'playlist';
            return false;
        },

        switchTab(tab) {
            this.activeTab = tab;
        },

        openForTrack(ctx, openerEl) {
            this.reset();
            this.mode = 'track';
            this.activeTab = 'track';
            this.parentContext = ctx;
            this.openerElement = openerEl || document.activeElement;
            this.query = `${ctx.artist || ''} ${ctx.title || ''}`.trim();
            this.isOpen = true;
            this.search();
            setTimeout(() => {
                const el = document.getElementById('soundcloud-picker-query-input');
                if (el) el.focus();
            }, 60);
        },

        openForRelease(ctx, openerEl) {
            this.reset();
            this.mode = 'release';
            this.activeTab = 'album';
            this.parentContext = ctx;
            this.openerElement = openerEl || document.activeElement;
            this.query = `${ctx.artist || ''} ${ctx.album || ''}`.trim();
            this.isOpen = true;
            this.search();
            setTimeout(() => {
                const el = document.getElementById('soundcloud-picker-query-input');
                if (el) el.focus();
            }, 60);
        },

        close() {
            this.serial++;
            this.isOpen = false;
            this.detailsLoadingId = null;
            if (this.openerElement && typeof this.openerElement.focus === 'function') {
                try { this.openerElement.focus(); } catch (_) {}
            }
        },

        reset() {
            this.serial++;
            this.query = '';
            this.searchedQuery = '';
            this.loading = false;
            this.error = '';
            this.detailsLoadingId = null;
            this.detailsError = '';
            this.groups = [
                { type: 'track', items: [], next_offset: -1, next_token: '', error: '', loading: false, exhausted: false },
                { type: 'album', items: [], next_offset: -1, next_token: '', error: '', loading: false, exhausted: false },
                { type: 'playlist', items: [], next_offset: -1, next_token: '', error: '', loading: false, exhausted: false },
            ];
        },

        async search() {
            const query = this.query.trim();
            if (!query) return;
            this.searchedQuery = query;
            const serial = ++this.serial;
            this.loading = true;
            this.error = '';
            this.detailsError = '';
            for (const g of this.groups) {
                g.items = [];
                g.next_offset = -1;
                g.next_token = '';
                g.error = '';
                g.loading = false;
                g.exhausted = false;
            }

            try {
                const response = await window.api.raw('/api/downloads/soundcloud/search?q=' + encodeURIComponent(query));
                const data = await response.json();
                if (!response.ok) throw new Error(data.error || data.message || 'Не удалось выполнить поиск');
                if (serial !== this.serial) return;

                for (const incoming of data.groups || []) {
                    const g = this.groups.find(group => group.type === incoming.type);
                    if (g) {
                        g.items = incoming.items || [];
                        g.next_offset = incoming.next_offset;
                        g.next_token = incoming.next_token || '';
                        g.error = incoming.error || '';
                        g.exhausted = incoming.next_offset < 0 && !incoming.next_token;
                    }
                }
            } catch (err) {
                if (serial === this.serial) this.error = err.message || 'Сбой выполнения поиска SoundCloud';
            } finally {
                if (serial === this.serial) this.loading = false;
            }
        },

        async loadMoreActiveTab() {
            const group = this.currentGroup();
            if (!group || group.loading || group.exhausted) return;
            group.loading = true;
            group.error = '';
            this.detailsError = '';
            const serial = ++this.serial;
            const params = new URLSearchParams({ q: this.searchedQuery, type: this.activeTab });
            if (group.next_token) {
                params.set(this.activeTab + '_token', group.next_token);
            } else if (group.next_offset >= 0) {
                params.set(this.activeTab + '_offset', String(group.next_offset));
            }

            try {
                const response = await window.api.raw('/api/downloads/soundcloud/search?' + params.toString());
                const data = await response.json();
                if (!response.ok) throw new Error(data.error || data.message || 'Ошибка загрузки страницы');
                if (serial !== this.serial) return;

                const incoming = (data.groups || []).find(g => g.type === this.activeTab);
                if (incoming) {
                    const seen = new Set(group.items.map(it => it.url));
                    for (const item of incoming.items || []) {
                        if (item.url && !seen.has(item.url)) {
                            group.items.push(item);
                            seen.add(item.url);
                        }
                    }
                    group.next_offset = incoming.next_offset;
                    group.next_token = incoming.next_token || '';
                    group.error = incoming.error || '';
                    group.exhausted = incoming.next_offset < 0 && !incoming.next_token;
                }
            } catch (err) {
                if (serial === this.serial) group.error = err.message || 'Не удалось догрузить страницу';
            } finally {
                if (serial === this.serial) group.loading = false;
            }
        },

        retryActiveTab() {
            const group = this.currentGroup();
            if (!group) return;
            if (group.items.length === 0) {
                this.search();
            } else {
                this.loadMoreActiveTab();
            }
        },

        async selectItem(item, type) {
            if (!this.canSelect(item, type)) {
                if (this.mode === 'track') {
                    this.detailsError = 'Коллекцию (альбом или плейлист) нельзя привязать как одиночный трек. Переключитесь на вкладку «Треки» или откройте окно выбора релиза.';
                } else {
                    this.detailsError = 'Одиночный трек нельзя привязать как цельный релиз. Выберите альбом или плейлист.';
                }
                return;
            }

            if (this.mode === 'track') {
                // Добавление выбранного трека в родительский sourcePicker
                const sp = Alpine.store('sourcePicker');
                if (sp && typeof sp.addCustomSoundCloudTrack === 'function') {
                    sp.addCustomSoundCloudTrack(item);
                }
                this.close();
                return;
            }

            if (this.mode === 'release') {
                // Загрузка треклиста и сопоставление через штатный эндпоинт
                this.detailsLoadingId = item.url;
                this.detailsError = '';
                try {
                    const rsp = Alpine.store('releaseSourcePicker');
                    const payload = {
                        url: item.url,
                        artist: rsp ? rsp.artist : '',
                        album: rsp ? rsp.album : '',
                        target_tracks: rsp ? rsp.targetTracks : [],
                    };
                    const res = await window.api.raw('/api/downloads/release-candidates/parse-url', {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify(payload),
                    });
                    if (!res.ok) {
                        const errData = await res.json().catch(() => ({}));
                        throw new Error(errData.error || ('HTTP ' + res.status + ': Ошибка парсинга треклиста релиза'));
                    }
                    const cand = await res.json();
                    if (rsp && typeof rsp.applyCustomCandidate === 'function') {
                        rsp.applyCustomCandidate(cand);
                    }
                    this.close();
                } catch (err) {
                    this.detailsError = err.message || 'Не удалось разобрать треклист релиза. Попробуйте другой результат.';
                } finally {
                    this.detailsLoadingId = null;
                }
            }
        },
    });
});
