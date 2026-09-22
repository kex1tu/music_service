/**
 * Модуль сверки тегов аудита медиатеки (Audit Tag Verification & Online Match).
 * Отвечает за:
 * - детекцию дефектов тегов (клиповый мусор, нераспарсенные фиты, Topic-каналы, пропущенный год/альбом)
 * - быстрое локальное автоисправление (одиночное и пакетное)
 * - Diff-сверку с онлайн-каталогами (Яндекс Музыка, Deezer, iTunes) с превью обложек и применением
 */
window.AuditTagAudit = {
    tagIssues: [],
    tagIssuesTotalCount: 0,
    tagIssuesFilter: 'all',
    tagIssuesLoading: false,
    isFixingTagIssue: {},
    isFixingAllTags: false,
    tagDiffModalOpen: false,
    activeDiffItem: null,
    catalogDiffMatches: [],
    selectedMatchIndex: 0,
    catalogSearchLoading: false,
    isApplyingCatalog: false,
    selectedPaths: [],
    isFixingSafeTags: false,
    isFixingSelectedTags: false,

    safeIssuesCount() {
        return (this.tagIssues || []).filter((i) => i.severity === 'safe_autofix').length;
    },

    isSelected(filePath) {
        return this.selectedPaths.includes(filePath);
    },

    toggleSelectItem(filePath) {
        const idx = this.selectedPaths.indexOf(filePath);
        if (idx > -1) {
            this.selectedPaths.splice(idx, 1);
        } else {
            this.selectedPaths.push(filePath);
        }
    },

    toggleSelectAll() {
        if (this.selectedPaths.length === this.tagIssues.length) {
            this.selectedPaths = [];
        } else {
            this.selectedPaths = this.tagIssues.map((i) => i.file_path);
        }
    },

    async loadTagIssues() {
        this.tagIssuesLoading = true;
        this.selectedPaths = [];
        try {
            const filterParam = encodeURIComponent(this.tagIssuesFilter || 'all');
            const res = await fetch(`/files/audit/tag-issues?filter=${filterParam}&_t=${Date.now()}`);
            if (res.ok) {
                const data = await res.json();
                this.tagIssues = data.issues || [];
                this.tagIssuesTotalCount = data.total_count || 0;
            }
        } catch (e) {
            console.error('Ошибка загрузки проблемных тегов:', e);
        } finally {
            this.tagIssuesLoading = false;
        }
    },

    async filterTagIssues(filter) {
        this.tagIssuesFilter = filter;
        await this.loadTagIssues();
    },

    async fixTagIssue(item) {
        if (!item || !item.file_path) return;
        const key = item.file_path;
        this.isFixingTagIssue = { ...this.isFixingTagIssue, [key]: true };

        try {
            const res = await fetch('/files/audit/tag-issues/fix', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    path: item.file_path,
                    title: item.proposed.title,
                    artist: item.proposed.artist,
                    album: item.proposed.album,
                    year: item.proposed.year,
                }),
            });

            if (res.ok) {
                Alpine.store('app').toast(`Теги трека "${item.proposed.title}" успешно исправлены`, 'success');
                // Удаляем из локального списка для мгновенного отклика UI
                this.tagIssues = this.tagIssues.filter((i) => i.file_path !== item.file_path);
                this.tagIssuesTotalCount = Math.max(0, this.tagIssuesTotalCount - 1);
                if (this.loadSummary) this.loadSummary();
            } else {
                let errDetail = 'Ошибка исправления тегов';
                try {
                    const errData = await res.json();
                    if (errData && errData.error) errDetail = errData.error;
                } catch (_) {}
                Alpine.store('app').toast(errDetail, 'error');
            }
        } catch (e) {
            console.error('Сбой при исправлении тегов:', e);
            Alpine.store('app').toast('Сетевая ошибка при исправлении тегов', 'error');
        } finally {
            this.isFixingTagIssue = { ...this.isFixingTagIssue, [key]: false };
        }
    },

    async fixSafeTagIssues() {
        if (this.isFixingSafeTags) return;
        this.isFixingSafeTags = true;
        Alpine.store('app').toast('Запущено исправление безопасных дефектов тегов...', 'info');

        try {
            const res = await fetch('/files/audit/tag-issues/fix-safe', { method: 'POST' });
            if (res.ok) {
                const data = await res.json();
                Alpine.store('app').toast(
                    `Успешно исправлены безопасные дефекты у ${data.fixed_count} треков из ${data.total_found}`,
                    'success'
                );
                await this.loadTagIssues();
                if (this.loadSummary) await this.loadSummary();
            } else {
                Alpine.store('app').toast('Ошибка при исправлении безопасных тегов', 'error');
            }
        } catch (e) {
            console.error('Ошибка fixSafeTagIssues:', e);
            Alpine.store('app').toast('Сетевой сбой при исправлении', 'error');
        } finally {
            this.isFixingSafeTags = false;
        }
    },

    async fixSelectedTagIssues() {
        if (this.isFixingSelectedTags || this.selectedPaths.length === 0) return;
        this.isFixingSelectedTags = true;
        Alpine.store('app').toast(`Исправление ${this.selectedPaths.length} выбранных треков...`, 'info');

        try {
            const selectedItems = this.tagIssues.filter((i) => this.selectedPaths.includes(i.file_path));
            const payload = {
                items: selectedItems.map((item) => ({
                    path: item.file_path,
                    title: item.proposed.title,
                    artist: item.proposed.artist,
                    album: item.proposed.album,
                    year: item.proposed.year,
                })),
            };

            const res = await fetch('/files/audit/tag-issues/fix-selected', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(payload),
            });

            if (res.ok) {
                const data = await res.json();
                Alpine.store('app').toast(
                    `Успешно исправлены теги у ${data.fixed_count} выбранных треков`,
                    'success'
                );
                this.selectedPaths = [];
                await this.loadTagIssues();
                if (this.loadSummary) await this.loadSummary();
            } else {
                Alpine.store('app').toast('Ошибка при исправлении выбранных тегов', 'error');
            }
        } catch (e) {
            console.error('Ошибка fixSelectedTagIssues:', e);
            Alpine.store('app').toast('Сетевой сбой при исправлении выбранных', 'error');
        } finally {
            this.isFixingSelectedTags = false;
        }
    },

    async fixAllTagIssues() {
        if (this.isFixingAllTags) return;
        this.isFixingAllTags = true;
        Alpine.store('app').toast('Запущено пакетное исправление дефектов тегов медиатеки...', 'info');

        try {
            const res = await fetch('/files/audit/tag-issues/fix-all', { method: 'POST' });
            if (res.ok) {
                const data = await res.json();
                Alpine.store('app').toast(
                    `Успешно исправлены теги у ${data.fixed_count} треков из ${data.total_found}`,
                    'success'
                );
                await this.loadTagIssues();
                if (this.loadSummary) await this.loadSummary();
            } else {
                Alpine.store('app').toast('Ошибка при пакетном исправлении тегов', 'error');
            }
        } catch (e) {
            console.error('Ошибка fixAllTagIssues:', e);
            Alpine.store('app').toast('Сетевой сбой при пакетном исправлении', 'error');
        } finally {
            this.isFixingAllTags = false;
        }
    },

    async openTagDiffModal(item) {
        if (!item) return;
        this.activeDiffItem = item;
        this.tagDiffModalOpen = true;
        this.catalogDiffMatches = [];
        this.selectedMatchIndex = 0;
        this.catalogSearchLoading = true;

        try {
            const queryParams = new URLSearchParams({
                path: item.file_path,
                title: item.title,
                artist: item.artist,
            });
            const res = await fetch(`/files/audit/tag-issues/catalog-match?${queryParams.toString()}&_t=${Date.now()}`);
            if (res.ok) {
                const data = await res.json();
                this.catalogDiffMatches = data.matches || [];
            }
        } catch (e) {
            console.error('Ошибка поиска в каталогах:', e);
        } finally {
            this.catalogSearchLoading = false;
        }
    },

    closeTagDiffModal() {
        this.tagDiffModalOpen = false;
        this.activeDiffItem = null;
        this.catalogDiffMatches = [];
    },

    async applyDiffMatch(match) {
        if (!match || !this.activeDiffItem) return;
        this.isApplyingCatalog = true;

        try {
            const res = await fetch('/files/audit/tag-issues/apply-catalog', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    path: this.activeDiffItem.file_path,
                    title: match.title,
                    artist: match.artist,
                    album: match.album,
                    year: match.year,
                    track_number: match.track_number,
                    cover_url: match.cover_url || '',
                }),
            });

            if (res.ok) {
                Alpine.store('app').toast(`Каталожные теги (${match.source}) успешно применены к треку`, 'success');
                const filePath = this.activeDiffItem.file_path;
                this.closeTagDiffModal();
                this.tagIssues = this.tagIssues.filter((i) => i.file_path !== filePath);
                this.tagIssuesTotalCount = Math.max(0, this.tagIssuesTotalCount - 1);
                if (this.loadSummary) this.loadSummary();
            } else {
                let errDetail = 'Ошибка применения каталожных тегов';
                try {
                    const errData = await res.json();
                    if (errData && errData.error) errDetail = errData.error;
                } catch (_) {}
                Alpine.store('app').toast(errDetail, 'error');
            }
        } catch (e) {
            console.error('Ошибка применения тегов из каталога:', e);
            Alpine.store('app').toast('Сетевая ошибка при применении тегов', 'error');
        } finally {
            this.isApplyingCatalog = false;
        }
    }
};
