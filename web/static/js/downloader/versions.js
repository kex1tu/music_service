/**
 * Модуль селектора версий треков (Candidate Version Switcher) для Downloader UI.
 * Предоставляет методы для раскрытия списка кандидатов, отображения бейджей
 * оригинальности и отправки POST /api/downloads/tracks/{trackId}/switch-version.
 */
window.DownloaderVersions = {
    activeVersionsTrackId: null,
    switchingTrackId: null,
    expandedJobTracksId: null,

    toggleJobTracks(jobId) {
        if (this.expandedJobTracksId === jobId) {
            this.expandedJobTracksId = null;
        } else {
            this.expandedJobTracksId = jobId;
        }
    },

    isJobTracksExpanded(jobId) {
        return this.expandedJobTracksId === jobId;
    },

    toggleVersions(trackId) {
        if (this.activeVersionsTrackId === trackId) {
            this.activeVersionsTrackId = null;
        } else {
            this.activeVersionsTrackId = trackId;
        }
    },

    isVersionsOpen(trackId) {
        return this.activeVersionsTrackId === trackId;
    },

    async switchTrackVersion(track, targetUrl, jobId) {
        if (!track || !track.track_id || !targetUrl) return;
        if (this.switchingTrackId === track.track_id) return;

        this.switchingTrackId = track.track_id;
        try {
            const res = await window.api.raw(`/api/downloads/tracks/${encodeURIComponent(track.track_id)}/switch-version`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ target_url: targetUrl, job_id: jobId }),
            });

            if (!res.ok) {
                const errData = await res.json().catch(() => ({}));
                throw new Error(errData.error || `HTTP ${res.status}: Ошибка переключения версии`);
            }

            const data = await res.json();
            track.version_status = 'manual';
            track.audio_source_url = targetUrl;
            if (data.duration && data.duration > 0) {
                track.duration = data.duration;
            }

            this.activeVersionsTrackId = null;
            if (window.Alpine && Alpine.store('app')) {
                Alpine.store('app').toast('Версия трека переключена на ручной выбор!', 'success');
            }
        } catch (err) {
            console.error('Ошибка switchTrackVersion:', err);
            if (window.Alpine && Alpine.store('app')) {
                Alpine.store('app').toast(err.message || 'Сбой смены версии', 'error');
            }
        } finally {
            this.switchingTrackId = null;
        }
    },

    getTrackSourceState(track) {
        const url = track.audio_source_url || '';
        const selected = !!url && !url.includes('deezer.com');
        const waiting = track.status === 'waiting_source';
        const failed = track.status === 'error';
        let message = '';
        if (waiting) {
            message = selected
                ? 'Источник выбран, но скачивание не удалось.'
                : 'Для этого трека источник не назначен. Выберите его отдельно или в сопоставлении релиза.';
        } else if (failed) {
            message = selected
                ? 'Ошибка обработки трека. Выбранный источник сохранён.'
                : 'Ошибка обработки трека.';
        }
        return {
            selected,
            attention: waiting || failed,
            action: selected ? 'Сменить источник' : 'Выбрать источник',
            message,
        };
    },

    getVersionBadge(status) {
        switch (status) {
            case 'marked':
                return {
                    label: 'Заявлен оригинал',
                    tooltip: 'Помечена в названии как оригинал',
                    badgeClass: 'bg-emerald-500/15 text-emerald-400 border border-emerald-500/30',
                    dotClass: 'bg-emerald-400',
                };
            case 'likely':
                return {
                    label: 'Совпадение',
                    tooltip: 'Близкая длительность; версия не проверена',
                    badgeClass: 'bg-amber-500/15 text-amber-400 border border-amber-500/30',
                    dotClass: 'bg-amber-400',
                };
            case 'unverified':
                return {
                    label: 'Не проверено',
                    tooltip: 'Версия не подтверждена эталоном',
                    badgeClass: 'bg-surface-elevated text-content-muted border border-surface-border',
                    dotClass: 'bg-gray-400',
                };
            case 'manual':
                return {
                    label: 'Ручной выбор',
                    tooltip: 'Выбрано вручную',
                    badgeClass: 'bg-blue-500/15 text-blue-400 border border-blue-500/30',
                    dotClass: 'bg-blue-400',
                };
            case 'link_dead':
                return {
                    label: 'Недоступна',
                    tooltip: 'Ссылка недоступна на SoundCloud',
                    badgeClass: 'bg-red-500/15 text-red-400 border border-red-500/30',
                    dotClass: 'bg-red-400',
                };
            default:
                return {
                    label: status || 'Стандарт',
                    tooltip: 'Версия аудиофайла',
                    badgeClass: 'bg-surface-elevated text-content-muted border border-surface-border',
                    dotClass: 'bg-gray-400',
                };
        }
    },

    formatDelta(delta) {
        if (delta === undefined || delta === null || isNaN(delta)) return '0с';
        if (delta > 0) return `+${delta}с`;
        return `${delta}с`;
    },

    formatUploadDate(raw) {
        if (!raw) return '';
        const s = String(raw).trim();
        // YYYYMMDD
        if (/^\d{8}$/.test(s)) {
            const y = s.substring(0, 4);
            const m = s.substring(4, 6);
            const d = s.substring(6, 8);
            return `${d}.${m}.${y}`;
        }
        // YYYY-MM-DD
        if (/^\d{4}-\d{2}-\d{2}/.test(s)) {
            const parts = s.substring(0, 10).split('-');
            return `${parts[2]}.${parts[1]}.${parts[0]}`;
        }
        return s;
    },
};
