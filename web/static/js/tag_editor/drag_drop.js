/**
 * Модуль перетаскивания (Drag-and-Drop) и нумерации треков (Tag DragDrop).
 * Поддерживает многодисковые релизы (disc_number / disc_total)
 * и автоматическую перенумерацию как для всего списка, так и по группам.
 */
window.TagDragDrop = {
    onDragStart(event, index) {
        this.draggedIndex = index;
        if (event.dataTransfer) {
            event.dataTransfer.effectAllowed = 'move';
            event.dataTransfer.setData('text/plain', String(index));
        }
    },

    onDragOver(event, index) {
        event.preventDefault();
        if (event.dataTransfer) {
            event.dataTransfer.dropEffect = 'move';
        }
        this.dragOverIndex = index;
    },

    onDrop(event, targetIndex) {
        event.preventDefault();
        if (this.draggedIndex === null || this.draggedIndex === targetIndex) {
            this.dragOverIndex = null;
            return;
        }
        const [moved] = this.tracks.splice(this.draggedIndex, 1);
        this.tracks.splice(targetIndex, 0, moved);
        this.draggedIndex = null;
        this.dragOverIndex = null;

        if (this.buildReleasesFromTracks) {
            this.buildReleasesFromTracks();
        }

        // Автоперенумерация затронутых треков после перемещения
        if (this.viewMode === 'grouped' && this.releases) {
            this.releases.forEach((group) => {
                if (!group.isSingles) {
                    this.renumberGroupTracks(group);
                }
            });
        } else {
            this.renumberTracks();
        }

        Alpine.store('app').toast('Порядок треков изменён и перенумерован', 'info');
    },

    onDragEnd() {
        this.draggedIndex = null;
        this.dragOverIndex = null;
    },

    renumberGroupTracks(group) {
        if (!group || !group.items || group.items.length === 0) return;

        const hasMultipleDiscs = group.items.some((it) => it.track.disc_number && it.track.disc_number > 1);
        if (hasMultipleDiscs) {
            const counters = {};
            for (const it of group.items) {
                const disc = it.track.disc_number || 1;
                counters[disc] = (counters[disc] || 0) + 1;
                it.track.track_number = counters[disc];
            }
            for (const it of group.items) {
                const disc = it.track.disc_number || 1;
                it.track.track_total = counters[disc];
            }
            const maxDisc = Math.max(...Object.keys(counters).map(Number));
            group.discTotal = maxDisc;
            for (const it of group.items) {
                it.track.disc_total = maxDisc;
            }
        } else {
            const total = group.items.length;
            group.items.forEach((it, index) => {
                it.track.track_number = index + 1;
                it.track.track_total = total;
                it.track.disc_number = 1;
                it.track.disc_total = 1;
            });
            group.discTotal = 1;
        }
        Alpine.store('app').toast(
            `Треки релиза "${group.albumTitle || group.title}" перенумерованы (1..${group.items.length})!`,
            'success'
        );
    },

    renumberTracks() {
        if (!this.tracks || this.tracks.length === 0) return;

        if (this.viewMode === 'grouped' && this.releases && this.releases.length > 0) {
            this.releases.forEach((group) => {
                this.renumberGroupTracks(group);
            });
            Alpine.store('app').toast('Треки перенумерованы по релизам!', 'success');
            return;
        }

        const hasMultipleDiscs = this.tracks.some((t) => t.disc_number && t.disc_number > 1);
        if (hasMultipleDiscs) {
            const counters = {};
            for (const t of this.tracks) {
                const disc = t.disc_number || 1;
                counters[disc] = (counters[disc] || 0) + 1;
                t.track_number = counters[disc];
            }
            for (const t of this.tracks) {
                const disc = t.disc_number || 1;
                t.track_total = counters[disc];
            }
            const maxDisc = Math.max(...Object.keys(counters).map(Number));
            this.album.disc_total = maxDisc;
            for (const t of this.tracks) {
                t.disc_total = maxDisc;
            }
        } else {
            const total = this.tracks.length;
            this.tracks.forEach((track, index) => {
                track.track_number = index + 1;
                track.track_total = total;
                track.disc_number = 1;
                track.disc_total = 1;
            });
            this.album.disc_total = 1;
        }
        Alpine.store('app').toast('Треки успешно перенумерованы!', 'success');
    }
};
