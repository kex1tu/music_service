document.addEventListener('alpine:init', () => {
    Alpine.store('soundCloudSearch', {
        query: '',
        searchedQuery: '',
        loading: false,
        error: '',
        serial: 0,
        groups: [],
        labels: { track: 'Треки', album: 'Альбомы', playlist: 'Плейлисты' },

        hasMore() {
            return this.groups.some(group => group.next_offset >= 0);
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
                for (const group of this.groups) params.set(`${group.type}_offset`, group.next_offset);
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
});
