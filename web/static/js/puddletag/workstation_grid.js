const puddletagWorkstationGrid = {
    // ==================== ВЫДЕЛЕНИЕ И НАВИГАЦИЯ ====================

    toggleSelectAll() {
        if (this.isAllSelected) {
            this.selectedIndices = [];
            this._selectedSet = new Set();
        } else {
            this.selectedIndices = this.displayTracks.map(t => t._trackIndex);
            this._selectedSet = new Set(this.selectedIndices);
        }
        this.syncTagPanel();
    },

    clearTrackSelection() {
        this.selectedIndices = [];
        this._selectedSet = new Set();
        this.lastClickedIndex = null;
        this.syncTagPanel();
    },

    selectRow(index, event) {
        if (event.shiftKey && this.lastClickedIndex !== null) {
            // Выделение диапазона строк (Shift+Click)
            const start = Math.min(this.lastClickedIndex, index);
            const end = Math.max(this.lastClickedIndex, index);
            const range = [];
            for (let i = start; i <= end; i++) {
                range.push(i);
            }
            // Объединяем с текущим выбором без дубликатов
            const set = new Set([...this.selectedIndices, ...range]);
            this.selectedIndices = Array.from(set);
            this._selectedSet = set;
        } else if (event.ctrlKey || event.metaKey) {
            // Одиночное переключение (Ctrl+Click)
            const pos = this.selectedIndices.indexOf(index);
            if (pos > -1) {
                this.selectedIndices.splice(pos, 1);
            } else {
                this.selectedIndices.push(index);
            }
            this._selectedSet = new Set(this.selectedIndices);
            this.lastClickedIndex = index;
        } else {
            // Обычный клик — выбор одной строки
            this.selectedIndices = [index];
            this._selectedSet = new Set([index]);
            this.lastClickedIndex = index;
        }

        this.syncTagPanel();
    },

    isTrackSelected(index) {
        if (!this._selectedSet || this._selectedSet.size !== this.selectedIndices.length) {
            this._selectedSet = new Set(this.selectedIndices);
        }
        return this._selectedSet.has(index);
    },

    isTrackDirty(index) {
        return this.dirtyIndices.has(index);
    },

    isCellDirty(index, field) {
        if (!this.dirtyIndices.has(index)) return false;
        const original = this.originalTracks[index];
        const current = this.tracks[index];
        if (!original || !current) return false;
        return String(original[field] ?? '') !== String(current[field] ?? '');
    },

    // ==================== ИНЛАЙН-РЕДАКТИРОВАНИЕ ЯЧЕЕК ====================

    startEdit(index, field, event) {
        if (event) event.stopPropagation();
        this.editingCell = {
            index,
            field,
            value: this.tracks[index][field] !== null && this.tracks[index][field] !== undefined
                   ? String(this.tracks[index][field])
                   : ''
        };
        // Фокусируем инпут на следующем тике
        this.$nextTick(() => {
            const el = this.$refs.cellEditorInput;
            if (el) {
                el.focus();
                el.select();
            }
        });
    },

    saveEdit() {
        if (!this.editingCell) return;
        const { index, field, value } = this.editingCell;
        const track = this.tracks[index];
        if (!track) return;

        let finalVal = value.trim();
        // Числовые поля
        if (field === 'track_number' || field === 'disc_number' || field === 'year' || field === 'track_total' || field === 'disc_total') {
            finalVal = finalVal === '' ? null : Number(finalVal);
        }

        const oldVal = track[field];
        if (oldVal !== finalVal) {
            track[field] = finalVal;
            this.dirtyIndices.add(index);
            this.syncTagPanel();
        }

        this.editingCell = null;
    },

    cancelEdit() {
        this.editingCell = null;
    },

    onCellKeyDown(event) {
        if (event.key === 'Enter') {
            event.preventDefault();
            const curIndex = this.editingCell ? this.editingCell.index : null;
            const curField = this.editingCell ? this.editingCell.field : null;
            this.saveEdit();

            // Переход на строку ниже в той же колонке
            if (curIndex !== null && curIndex + 1 < this.tracks.length) {
                this.startEdit(curIndex + 1, curField);
            }
        } else if (event.key === 'Tab') {
            event.preventDefault();
            const fieldsOrder = ['track_number', 'disc_number', 'title', 'artist', 'album', 'album_artist', 'year', 'genre'];
            const curIndex = this.editingCell ? this.editingCell.index : null;
            const curField = this.editingCell ? this.editingCell.field : null;
            this.saveEdit();

            if (curIndex !== null && curField !== null) {
                const fieldIdx = fieldsOrder.indexOf(curField);
                if (!event.shiftKey) {
                    // Вперед
                    if (fieldIdx + 1 < fieldsOrder.length) {
                        this.startEdit(curIndex, fieldsOrder[fieldIdx + 1]);
                    } else if (curIndex + 1 < this.tracks.length) {
                        this.startEdit(curIndex + 1, fieldsOrder[0]);
                    }
                } else {
                    // Назад
                    if (fieldIdx > 0) {
                        this.startEdit(curIndex, fieldsOrder[fieldIdx - 1]);
                    } else if (curIndex > 0) {
                        this.startEdit(curIndex - 1, fieldsOrder[fieldsOrder.length - 1]);
                    }
                }
            }
        } else if (event.key === 'Escape') {
            event.preventDefault();
            this.cancelEdit();
        }
    },

    // ==================== СИНХРОНИЗАЦИЯ ТЕГ-ПАНЕЛИ ====================

    syncTagPanel() {
        if (this.selectedIndices.length === 0) {
            // Если ничего не выбрано, очищаем поля
            this.tagPanel = {
                title: '',
                artist: '',
                album: '',
                album_artist: '',
                year: '',
                genre: '',
                track_number: '',
                track_total: '',
                disc_number: '',
                disc_total: '',
                cover_url: null,
                has_cover: false
            };
            this.tagPanelDiff = {};
            return;
        }

        const fields = ['title', 'artist', 'album', 'album_artist', 'year', 'genre', 'track_number', 'track_total', 'disc_number', 'disc_total'];
        const firstTrack = this.tracks[this.selectedIndices[0]] || {};
        const diffs = {};

        for (const f of fields) {
            const firstVal = firstTrack[f] ?? '';
            let isDiff = false;
            for (let i = 1; i < this.selectedIndices.length; i++) {
                const t = this.tracks[this.selectedIndices[i]];
                if (!t) continue;
                if (String(t[f] ?? '') !== String(firstVal)) {
                    isDiff = true;
                    break;
                }
            }
            diffs[f] = isDiff;
            this.tagPanel[f] = isDiff ? '' : (firstVal ?? '');
        }

        this.tagPanelDiff = diffs;
        this.tagPanel.cover_url = firstTrack.cover_preview_url || null;
        this.tagPanel.has_cover = !!firstTrack.has_cover;
    },

    onTagPanelInput(field, value) {
        if (this.selectedIndices.length === 0) return;

        let finalVal = value.trim();
        if (field === 'track_number' || field === 'disc_number' || field === 'year' || field === 'track_total' || field === 'disc_total') {
            finalVal = finalVal === '' ? null : Number(finalVal);
        }

        for (const idx of this.selectedIndices) {
            const track = this.tracks[idx];
            if (track) {
                track[field] = finalVal;
                this.dirtyIndices.add(idx);
            }
        }

        this.tagPanelDiff[field] = false;
    },

    openMobileTagInspector() {
        this.showMobileTagInspector = true;
    },

    closeMobileTagInspector() {
        this.showMobileTagInspector = false;
    },

    toggleMobileTrackMenu(trackIndex) {
        if (this.mobileTrackMenuIndex === trackIndex) {
            this.mobileTrackMenuIndex = null;
        } else {
            this.mobileTrackMenuIndex = trackIndex;
        }
    },

    closeMobileTrackMenu() {
        this.mobileTrackMenuIndex = null;
    },

    openTrackInInspector(trackIndex, event) {
        this.selectRow(trackIndex, event);
        this.showMobileTagInspector = true;
        this.mobileTrackMenuIndex = null;
    },

    setGridDensity(density) {
        this.gridDensity = density;
        try {
            localStorage.setItem('mm_grid_density', density);
        } catch (e) {}
    },

    toggleGridDensity() {
        this.setGridDensity(this.gridDensity === 'compact' ? 'normal' : 'compact');
    },

    saveColWidths() {
        try {
            localStorage.setItem('mm_col_widths', JSON.stringify(this.colWidths));
        } catch (e) {}
    },

    startColResize(colKey, event) {
        event.preventDefault();
        event.stopPropagation();
        this.resizingCol = colKey;
        const startX = event.pageX;
        const th = event.target.closest('th');
        const startWidth = th ? th.offsetWidth : 120;

        const onMouseMove = (moveEvent) => {
            const delta = moveEvent.pageX - startX;
            const newWidth = Math.max(40, startWidth + delta);
            this.colWidths[colKey] = newWidth + 'px';
        };

        const onMouseUp = () => {
            window.removeEventListener('mousemove', onMouseMove);
            window.removeEventListener('mouseup', onMouseUp);
            this.resizingCol = null;
            this.saveColWidths();
        };

        window.addEventListener('mousemove', onMouseMove);
        window.addEventListener('mouseup', onMouseUp);
    },

    // ==================== ВСПОМОГАТЕЛЬНЫЕ ФОРМАТТЕРЫ ====================

    formatDuration(seconds) {
        if (window.AppFormatters && window.AppFormatters.formatDuration) {
            return window.AppFormatters.formatDuration(seconds);
        }
        if (!seconds) return '0:00';
        const m = Math.floor(seconds / 60);
        const s = Math.floor(seconds % 60);
        return `${m}:${s < 10 ? '0' : ''}${s}`;
    }
};
