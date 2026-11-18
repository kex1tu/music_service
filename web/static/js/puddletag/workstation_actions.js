const puddletagWorkstationActions = {
    // ==================== КОНВЕРТЕР ШАБЛОНОВ (PATTERNS) ====================

    openPatternModal(mode) {
        this.patternMode = mode;
        if (mode === 'filename_to_tag') {
            this.patternInput = '%artist% - %title%';
        } else {
            this.patternInput = '%track% - %title%';
        }
        this.updatePatternDiff();
        this.showPatternModal = true;
    },

    updatePatternDiff() {
        if (!window.PuddletagPatterns) return;
        const targetTracks = this.selectedIndices.length > 0
            ? this.selectedIndices.map(i => this.tracks[i])
            : this.tracks;

        this.patternDiffResults = window.PuddletagPatterns.calculateDiff(
            targetTracks,
            this.patternInput,
            this.patternMode
        );
    },

    async applyPatternDiff() {
        if (!this.patternDiffResults || this.patternDiffResults.length === 0) return;

        if (this.patternMode === 'filename_to_tag') {
            // Применяем распарсенные теги в стейдж
            let count = 0;
            for (const diff of this.patternDiffResults) {
                if (diff.has_change && diff.changes) {
                    // Ищем реальный индекс трека по пути
                    const realIdx = this.tracks.findIndex(t => t.path === diff.track_path);
                    if (realIdx > -1) {
                        const track = this.tracks[realIdx];
                        for (const [field, valObj] of Object.entries(diff.changes)) {
                            track[field] = valObj.new;
                        }
                        this.dirtyIndices.add(realIdx);
                        count++;
                    }
                }
            }
            this.syncTagPanel();
            this.showPatternModal = false;
            Alpine.store('app').toast(`Теги извлечены из имен файлов (${count} треков)`, 'success');
        } else {
            // Переименование файлов (Tag to Filename)
            const itemsToRename = [];
            for (const diff of this.patternDiffResults) {
                if (diff.has_change && !diff.has_conflict) {
                    itemsToRename.push({
                        current_path: diff.track_path,
                        new_name: diff.new_filename
                    });
                }
            }

            if (itemsToRename.length === 0) {
                Alpine.store('app').toast('Нет файлов для переименования', 'info');
                return;
            }

            this.isSaving = true;
            try {
                const res = await window.api.raw('/files/rename-pattern', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ items: itemsToRename, dry_run: false })
                });
                if (!res.ok) {
                    const err = await res.json().catch(() => ({}));
                    throw new Error(err.error || 'Ошибка переименования файлов');
                }

                const results = await res.json();
                let successCount = 0;
                for (const r of results) {
                    if (r.status === 'ok') {
                        successCount++;
                        const tIdx = this.tracks.findIndex(t => t.path === r.current_path);
                        if (tIdx > -1) {
                            this.tracks[tIdx].path = r.new_path;
                            this.tracks[tIdx].name = r.new_path.split('/').pop() || r.new_path;
                        }
                    }
                }

                this.showPatternModal = false;
                Alpine.store('app').toast(`Переименовано ${successCount} файлов`, 'success');
                // Перезагружаем текущий каталог для актуализации ФС
                await this.loadDirectory(this.currentPath);
            } catch (err) {
                Alpine.store('app').toast(`Ошибка: ${err.message}`, 'error');
            } finally {
                this.isSaving = false;
            }
        }
    },

    // ==================== БЫСТРЫЕ ДЕЙСТВИЯ (ACTIONS) ====================

    applyCaseConversion() {
        if (!window.PuddletagActions) return;
        const targetFields = Object.keys(this.caseFields).filter(f => this.caseFields[f]);
        const targetIndices = this.selectedIndices.length > 0 ? this.selectedIndices : this.tracks.map((_, i) => i);

        const count = window.PuddletagActions.applyCase(this.tracks, targetIndices, targetFields, this.caseStyle);
        for (const idx of targetIndices) {
            this.dirtyIndices.add(idx);
        }

        this.syncTagPanel();
        this.showCaseModal = false;
        Alpine.store('app').toast(`Регистр обновлен у ${count} треков`, 'success');
    },

    applyAutoNumbering() {
        if (!window.PuddletagActions) return;
        const targetIndices = this.selectedIndices.length > 0 ? this.selectedIndices : this.tracks.map((_, i) => i);

        window.PuddletagActions.autoNumber(this.tracks, targetIndices, {
            startNum: Number(this.autoNumberStart) || 1,
            leadingZeros: this.autoNumberLeadingZeros,
            resetPerDisc: this.autoNumberResetPerDisc,
            setTotal: this.autoNumberSetTotal
        });

        for (const idx of targetIndices) {
            this.dirtyIndices.add(idx);
        }

        this.syncTagPanel();
        this.showAutoNumberModal = false;
        Alpine.store('app').toast('Номера треков успешно расставлены', 'success');
    },

    cleanTitlesAction() {
        if (!window.PuddletagActions) return;
        const targetIndices = this.selectedIndices.length > 0 ? this.selectedIndices : this.tracks.map((_, i) => i);
        const count = window.PuddletagActions.cleanTitles(this.tracks, targetIndices);

        for (const idx of targetIndices) {
            this.dirtyIndices.add(idx);
        }

        this.syncTagPanel();
        Alpine.store('app').toast(`Очищены названия у ${count} треков`, 'success');
    },

    applySearchAndReplace() {
        if (!window.PuddletagActions) return;
        const targetFields = Object.keys(this.searchFields).filter(f => this.searchFields[f]);
        const targetIndices = this.selectedIndices.length > 0 ? this.selectedIndices : this.tracks.map((_, i) => i);

        const count = window.PuddletagActions.searchAndReplace(
            this.tracks,
            targetIndices,
            this.searchQueryText,
            this.replaceWithText,
            targetFields,
            this.searchIsRegex,
            this.searchCaseSensitive
        );

        for (const idx of targetIndices) {
            this.dirtyIndices.add(idx);
        }

        this.syncTagPanel();
        this.showSearchReplaceModal = false;
        Alpine.store('app').toast(`Выполнена замена в ${count} треках`, 'success');
    },
};
