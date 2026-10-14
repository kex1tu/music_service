/**
 * Puddletag Sources Engine (window.PuddletagSources)
 * 
 * Клиентский модуль интеграции с мульти-источниковыми каталогами метаданных:
 * - Deezer API (с 8-поточным извлечением соавторов и ISRC на бэкенде)
 * - MusicBrainz API v2 (с Cover Art Archive и поддержкой токена)
 * - Shazam (акустический фингерпринтинг songrec + каталог Apple Music)
 * - Сопоставление дорожек релиза с выделенными аудиофайлами
 * - Применение выбранных полей в staged dirty state рабочей станции
 */
(function() {
    'use strict';

    /**
     * Форматирует длительность в секундах в формат M:SS.
     */
    function formatDuration(seconds) {
        if (!seconds || seconds <= 0) return '--:--';
        const mins = Math.floor(seconds / 60);
        const secs = Math.floor(seconds % 60);
        return `${mins}:${secs < 10 ? '0' : ''}${secs}`;
    }

    /**
     * Выполняет поиск по всем базам данных через бэкенд-агрегатор.
     * @param {string} query Текстовый поисковый запрос (Артист - Альбом или название)
     * @param {string} audioPath Опциональный путь к аудиофайлу для акустического фингерпринтинга
     * @returns {Promise<Object>} Результаты поиска { query, deezer, musicbrainz, shazam }
     */
    async function searchAllSources(query, audioPath) {
        const params = new URLSearchParams();
        if (query && query.trim()) {
            params.append('query', query.trim());
        }
        if (audioPath && audioPath.trim()) {
            params.append('audio_path', audioPath.trim());
        }

        const resp = await fetch(`/api/catalog/search?${params.toString()}`, {
            headers: { 'Accept': 'application/json' }
        });

        if (!resp.ok) {
            const errData = await resp.json().catch(() => ({}));
            throw new Error(errData.error || `Ошибка поиска каталогов (HTTP ${resp.status})`);
        }

        return await resp.json();
    }

    /**
     * Загружает полные метаданные релиза с треклистом из указанного провайдера.
     * @param {string} source 'deezer' | 'musicbrainz' | 'shazam'
     * @param {string} id Идентификатор релиза в базе данных
     * @param {string} [audioPath] Путь к файлу при акустическом поиске Shazam
     * @returns {Promise<Object>} Объект CatalogReleaseDetails
     */
    async function fetchReleaseDetails(source, id, audioPath) {
        let endpoint = '';
        if (source === 'deezer') {
            endpoint = `/api/catalog/deezer/album/${encodeURIComponent(id)}`;
        } else if (source === 'musicbrainz') {
            endpoint = `/api/catalog/musicbrainz/release/${encodeURIComponent(id)}`;
        } else if (source === 'shazam') {
            const params = new URLSearchParams();
            if (audioPath) {
                params.append('audio_path', audioPath);
            }
            if (id) {
                params.append('id', id);
            }
            endpoint = `/api/catalog/shazam/track/${encodeURIComponent(id || '0')}?${params.toString()}`;
        } else {
            throw new Error(`Неизвестный источник метаданных: ${source}`);
        }

        const resp = await fetch(endpoint, {
            headers: { 'Accept': 'application/json' }
        });

        if (!resp.ok) {
            const errData = await resp.json().catch(() => ({}));
            throw new Error(errData.error || `Не удалось загрузить треклист (HTTP ${resp.status})`);
        }

        return await resp.json();
    }

    /**
     * Сопоставляет треки найденного релиза с локальными треками в таблице Puddletag.
     * Сортирует треклист релиза по DiscNumber и TrackNumber.
     * @param {Array} releaseTracks Треклист из каталога
     * @param {Array} localTracks Локальные треки, выбранные пользователем
     * @returns {Array} Массив пар { releaseTrack, localTrack, localIndex }
     */
    function matchTracks(releaseTracks, localTracks, localIndices) {
        if (!releaseTracks) return [];
        const sortedRelease = [...releaseTracks].sort((a, b) => {
            const discDiff = (a.disc_number || 1) - (b.disc_number || 1);
            if (discDiff !== 0) return discDiff;
            return (a.track_number || 0) - (b.track_number || 0);
        });

        return sortedRelease.map((relTrack, idx) => {
            const localTrack = localTracks && idx < localTracks.length ? localTracks[idx] : null;
            const originalIndex = localIndices && idx < localIndices.length ? localIndices[idx] : null;
            return {
                index: idx,
                releaseTrack: relTrack,
                localTrack: localTrack,
                originalIndex: originalIndex,
                isMatched: !!localTrack
            };
        });
    }

    /**
     * Применяет выбранные поля из каталога к массиву локальных треков.
     * Возвращает набор индексов измененных треков для добавления в dirtyIndices.
     * 
     * @param {Array} tracks Массив всех треков в компоненте
     * @param {Array} matchedPairs Массив сопоставленных пар из matchTracks
     * @param {Object} releaseDetails Полные метаданные релиза (обложка, альбом, год, жанр)
     * @param {Object} options Флаги применения { titleArtist, trackDisc, albumYear, genres, cover }
     * @returns {Set<number>} Множество индексов измененных треков
     */
    function applyTagsToTracks(tracks, matchedPairs, releaseDetails, options) {
        const modifiedIndices = new Set();
        if (!releaseDetails || !matchedPairs) return modifiedIndices;

        const opt = options || {
            titleArtist: true,
            trackDisc: true,
            albumYear: true,
            genres: true,
            cover: true
        };

        const totalTracks = releaseDetails.tracks ? releaseDetails.tracks.length : 0;

        for (const pair of matchedPairs) {
            if (!pair.localTrack || pair.originalIndex === null || pair.originalIndex === undefined) {
                continue;
            }

            const targetTrack = tracks[pair.originalIndex];
            if (!targetTrack) continue;

            const relTr = pair.releaseTrack;

            // 1. Названия и исполнители (с соавторами)
            if (opt.titleArtist && relTr) {
                if (relTr.title) {
                    targetTrack.title = relTr.title;
                }
                if (relTr.artist) {
                    targetTrack.artist = relTr.artist;
                }
            }

            // 2. Номера треков и дисков
            if (opt.trackDisc && relTr) {
                if (relTr.track_number) {
                    targetTrack.track_number = relTr.track_number;
                }
                if (relTr.disc_number) {
                    targetTrack.disc_number = relTr.disc_number;
                }
                if (totalTracks > 0) {
                    targetTrack.track_total = totalTracks;
                }
            }

            // 3. Альбом, альбомный исполнитель и год выпуска
            if (opt.albumYear) {
                if (releaseDetails.title) {
                    targetTrack.album = releaseDetails.title;
                }
                if (releaseDetails.artist) {
                    targetTrack.album_artist = releaseDetails.artist;
                }
                if (releaseDetails.year && releaseDetails.year > 0) {
                    targetTrack.year = releaseDetails.year;
                }
            }

            // 4. Жанры
            if (opt.genres && releaseDetails.genre) {
                targetTrack.genre = releaseDetails.genre;
            }

            // 5. Официальная обложка каталога
            if (opt.cover && releaseDetails.cover_url) {
                targetTrack.cover_preview_url = releaseDetails.cover_url;
                targetTrack.cover_base64 = null;
                targetTrack.has_cover = true;
            }

            modifiedIndices.add(pair.originalIndex);
        }

        return modifiedIndices;
    }

    /**
     * Запрашивает текущий статус подключения к MusicBrainz OAuth 2.0.
     * @returns {Promise<Object>} { configured: bool, connected: bool, username: string, expires_at: string }
     */
    async function getMusicBrainzOAuthStatus() {
        try {
            const resp = await fetch('/api/auth/musicbrainz/status', {
                headers: { 'Accept': 'application/json' }
            });
            if (!resp.ok) return { configured: false, connected: false };
            return await resp.json();
        } catch (e) {
            console.warn('[PuddletagSources] Failed to fetch MusicBrainz OAuth status:', e);
            return { configured: false, connected: false };
        }
    }

    /**
     * Отключает учетную запись MusicBrainz OAuth.
     * @returns {Promise<boolean>}
     */
    async function disconnectMusicBrainzOAuth() {
        try {
            const resp = await fetch('/api/auth/musicbrainz/disconnect', {
                method: 'POST',
                headers: { 'Accept': 'application/json' }
            });
            return resp.ok;
        } catch (e) {
            console.warn('[PuddletagSources] Failed to disconnect MusicBrainz OAuth:', e);
            return false;
        }
    }

    // Экспортируем во внешнее окружение
    window.PuddletagSources = {
        formatDuration,
        searchAllSources,
        fetchReleaseDetails,
        matchTracks,
        applyTagsToTracks,
        getMusicBrainzOAuthStatus,
        disconnectMusicBrainzOAuth
    };
})();
