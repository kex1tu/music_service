/**
 * Модуль множественного выбора и пакетного редактирования треков (Tag Selection & Bulk Edit).
 * Предоставляет поддержку чекбоксов строк, диапазонного выбора Shift+Click,
 * и быстрых массовых действий над выбранными треками.
 */
window.TagSelection = {
    selectedIndices: [],
    lastClickedIndex: null,
    bulkActiveField: null, // 'genre' | 'year' | 'disc' | 'album_artist' | 'feat' | null
    bulkGenreInput: '',
    bulkYearInput: '',
    bulkDiscInput: '',
    bulkAlbumArtistInput: '',
    bulkFeatInput: '',

    /**
     * Проверяет, выбран ли трек по его глобальному индексу в tracks.
     */
    isTrackSelected(index) {
        return (this.selectedIndices || []).includes(index);
    },

    /**
     * Переключает выбор трека с поддержкой Shift+Click для диапазона.
     */
    toggleTrackSelection(index, event) {
        if (!this.tracks || index < 0 || index >= this.tracks.length) return;
        if (!this.selectedIndices) this.selectedIndices = [];

        if (event && event.shiftKey && this.lastClickedIndex !== null && this.lastClickedIndex !== index) {
            const start = Math.min(this.lastClickedIndex, index);
            const end = Math.max(this.lastClickedIndex, index);
            const shouldSelect = !this.selectedIndices.includes(index);

            for (let i = start; i <= end; i++) {
                const idx = this.selectedIndices.indexOf(i);
                if (shouldSelect && idx === -1) {
                    this.selectedIndices.push(i);
                } else if (!shouldSelect && idx !== -1) {
                    this.selectedIndices.splice(idx, 1);
                }
            }
        } else {
            const idx = this.selectedIndices.indexOf(index);
            if (idx === -1) {
                this.selectedIndices.push(index);
            } else {
                this.selectedIndices.splice(idx, 1);
            }
        }

        this.lastClickedIndex = index;
    },

    /**
     * Выбирает все треки или сбрасывает выбор, если все уже выбраны.
     */
    toggleSelectAllTracks() {
        if (!this.tracks || this.tracks.length === 0) return;
        if (!this.selectedIndices) this.selectedIndices = [];

        if (this.selectedIndices.length === this.tracks.length) {
            this.clearTrackSelection();
        } else {
            this.selectedIndices = this.tracks.map((_, i) => i);
        }
    },

    /**
     * Проверяет, все ли треки выбраны.
     */
    isAllTracksSelected() {
        return this.tracks && this.tracks.length > 0 && this.selectedIndices && this.selectedIndices.length === this.tracks.length;
    },

    /**
     * Сбрасывает текущий выбор и скрывает панель ввода.
     */
    clearTrackSelection() {
        this.selectedIndices = [];
        this.lastClickedIndex = null;
        this.bulkActiveField = null;
    },

    /**
     * Открывает/закрывает панель редактирования для конкретного поля.
     */
    toggleBulkField(fieldName) {
        if (this.bulkActiveField === fieldName) {
            this.bulkActiveField = null;
        } else {
            this.bulkActiveField = fieldName;
        }
    },

    /**
     * Применяет указанный жанр ко всем выбранным трекам.
     */
    applyBulkGenre() {
        if (!this.bulkGenreInput || !this.selectedIndices || this.selectedIndices.length === 0) return;
        const formatGenres = this.formatGenres || (window.AppFormatters ? window.AppFormatters.formatGenres : (g) => g);
        const formatted = formatGenres(this.bulkGenreInput.trim());

        this.selectedIndices.forEach((idx) => {
            if (this.tracks[idx]) {
                this.tracks[idx].genre = formatted;
            }
        });

        Alpine.store('app').toast(`Жанр «${formatted}» применен к ${this.selectedIndices.length} трекам`, 'success');
        this.bulkGenreInput = '';
        this.bulkActiveField = null;
    },

    /**
     * Применяет указанный год ко всем выбранным трекам.
     */
    applyBulkYear() {
        const parsed = parseInt(this.bulkYearInput, 10);
        if (isNaN(parsed) || parsed < 1000 || parsed > 2999 || !this.selectedIndices || this.selectedIndices.length === 0) {
            Alpine.store('app').toast('Введите корректный 4-значный год', 'error');
            return;
        }

        this.selectedIndices.forEach((idx) => {
            if (this.tracks[idx]) {
                this.tracks[idx].year = parsed;
            }
        });

        Alpine.store('app').toast(`Год ${parsed} применен к ${this.selectedIndices.length} трекам`, 'success');
        this.bulkYearInput = '';
        this.bulkActiveField = null;
    },

    /**
     * Применяет номер диска ко всем выбранным трекам.
     */
    applyBulkDisc() {
        const parsed = parseInt(this.bulkDiscInput, 10);
        if (isNaN(parsed) || parsed < 1 || !this.selectedIndices || this.selectedIndices.length === 0) {
            Alpine.store('app').toast('Введите корректный номер диска (>= 1)', 'error');
            return;
        }

        this.selectedIndices.forEach((idx) => {
            if (this.tracks[idx]) {
                this.tracks[idx].disc_number = parsed;
            }
        });

        Alpine.store('app').toast(`Диск #${parsed} применен к ${this.selectedIndices.length} трекам`, 'success');
        this.bulkDiscInput = '';
        this.bulkActiveField = null;
    },

    /**
     * Применяет альбомного исполнителя ко всем выбранным трекам.
     */
    applyBulkAlbumArtist() {
        const val = (this.bulkAlbumArtistInput || '').trim();
        if (!val || !this.selectedIndices || this.selectedIndices.length === 0) return;

        this.selectedIndices.forEach((idx) => {
            if (this.tracks[idx]) {
                this.tracks[idx].album_artist = val;
            }
        });

        Alpine.store('app').toast(`Исполнитель альбома «${val}» применен к ${this.selectedIndices.length} трекам`, 'success');
        this.bulkAlbumArtistInput = '';
        this.bulkActiveField = null;
    },

    /**
     * Добавляет гостевого артиста (feat) ко всем выбранным трекам.
     */
    applyBulkFeat() {
        const feat = (this.bulkFeatInput || '').trim();
        if (!feat || !this.selectedIndices || this.selectedIndices.length === 0) return;

        let appliedCount = 0;
        this.selectedIndices.forEach((idx) => {
            const trk = this.tracks[idx];
            if (!trk) return;

            const curArt = (trk.artist || '').trim();
            if (!curArt) {
                trk.artist = feat;
                appliedCount++;
                return;
            }

            // Проверяем, есть ли артист уже в списке
            const parts = curArt.split(/\\+|\x00|;\s*/).map(p => p.trim());
            const exists = parts.some(p => p.toLowerCase() === feat.toLowerCase());
            if (!exists) {
                trk.artist = `${curArt}\\${feat}`;
                appliedCount++;
            }
        });

        Alpine.store('app').toast(`Соавтор «${feat}» добавлен к ${appliedCount} трекам`, 'success');
        this.bulkFeatInput = '';
        this.bulkActiveField = null;
    },

    /**
     * Пакетно очищает названия всех выбранных треков от клипового мусора и извлекает соавторов.
     */
    applyBulkCleanTitles() {
        if (!this.selectedIndices || this.selectedIndices.length === 0) return;

        let count = 0;
        this.selectedIndices.forEach((idx) => {
            const trk = this.tracks[idx];
            if (trk && this.cleanTrackTitle) {
                this.cleanTrackTitle(trk);
                count++;
            }
        });

        Alpine.store('app').toast(`Очищены названия и извлечены соавторы у ${count} треков`, 'success');
    },

    /**
     * Исключает выбранные треки из релиза.
     */
    applyBulkExcludeTracks() {
        if (!this.selectedIndices || this.selectedIndices.length === 0) return;

        const count = this.selectedIndices.length;
        if (!confirm(`Исключить ${count} выбранных треков из этого сохранения?`)) {
            return;
        }

        // Сортируем индексы по убыванию, чтобы безопасно удалять из массива
        const sortedDesc = [...this.selectedIndices].sort((a, b) => b - a);
        sortedDesc.forEach((idx) => {
            if (idx >= 0 && idx < this.tracks.length) {
                this.tracks.splice(idx, 1);
            }
        });

        this.clearTrackSelection();
        if (this.buildReleasesFromTracks) {
            this.buildReleasesFromTracks();
        }

        Alpine.store('app').toast(`Исключено треков: ${count}`, 'info');
    },
};
