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

        const resp = await window.api.raw(`/api/catalog/search?${params.toString()}`, {
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

        const resp = await window.api.raw(endpoint, {
            headers: { 'Accept': 'application/json' }
        });

        if (!resp.ok) {
            const errData = await resp.json().catch(() => ({}));
            throw new Error(errData.error || `Не удалось загрузить треклист (HTTP ${resp.status})`);
        }

        return await resp.json();
    }

    /**
     * Очищает и нормализует строку для нечеткого сравнения названий песен.
     * @param {string} text 
     * @returns {string}
     */
    function normalizeTitleForMatching(text) {
        if (!text) return '';
        let s = String(text);
        // 1. Отрезаем расширение файла, если есть
        s = s.replace(/\.(mp3|flac|wav|m4a|ogg|opus|aac)$/i, '');
        // 2. Отрезаем ведущие номера треков (например "01. ", "1 - ", "01 ", "01_")
        s = s.replace(/^[\s\d]+[\.\-_\s]+/i, '');
        // 3. Отрезаем префикс исполнителя "Artist - Title", если есть дефис с пробелами
        if (s.includes(' - ')) {
            const parts = s.split(' - ');
            if (parts.length >= 2 && parts[1].trim().length > 0) {
                s = parts.slice(1).join(' - ');
            }
        }
        // 4. Отрезаем клиповый мусор в скобках
        s = s.replace(/\s*[\(\[](?:official\s*(?:video|audio|music\s*video|lyric\s*video)?|audio|remaster(?:ed)?|clip|клип|премьера|hd|4k|hq)[\)\]]/gi, '');
        // 5. Нижний регистр, замена ё -> е, удаление спецсимволов и пунктуации
        s = s.toLowerCase().replace(/ё/g, 'е');
        s = s.replace(/[^\p{L}\p{N}\s]/gu, ' ');
        // 6. Схлопывание пробелов
        s = s.replace(/\s+/g, ' ').trim();
        return s;
    }

    /**
     * Вычисляет коэффициент похожести двух названий (от 0.0 до 1.0).
     * Сочетает точное совпадение, вхождение подстроки (containment) и биграммный коэффициент Дайса.
     * @param {string} strA 
     * @param {string} strB 
     * @returns {number}
     */
    function calculateTitleSimilarity(strA, strB) {
        const normA = normalizeTitleForMatching(strA);
        const normB = normalizeTitleForMatching(strB);

        if (!normA || !normB) return 0;
        if (normA === normB) return 1.0;

        // Если одно название целиком содержится в другом
        if (normA.includes(normB) || normB.includes(normA)) {
            const minLen = Math.min(normA.length, normB.length);
            const maxLen = Math.max(normA.length, normB.length);
            if (minLen / maxLen >= 0.5) {
                return 0.80 + 0.20 * (minLen / maxLen);
            }
            return 0.65;
        }

        // Вычисляем биграммы для Dice's coefficient
        const getBigrams = (str) => {
            const s = str.replace(/\s+/g, '');
            const bigrams = new Set();
            for (let i = 0; i < s.length - 1; i++) {
                bigrams.add(s.substring(i, i + 2));
            }
            return bigrams;
        };

        const bigramsA = getBigrams(normA);
        const bigramsB = getBigrams(normB);
        if (bigramsA.size === 0 || bigramsB.size === 0) return 0;

        let intersection = 0;
        for (const bg of bigramsA) {
            if (bigramsB.has(bg)) intersection++;
        }

        return (2.0 * intersection) / (bigramsA.size + bigramsB.size);
    }

    /**
     * Сопоставляет треки найденного релиза с локальными треками в таблице Puddletag.
     * Выполняет нечеткое сопоставление (fuzzy matching) по названиям треков и именам файлов.
     * Сортирует треклист релиза по DiscNumber и TrackNumber.
     * 
     * @param {Array} releaseTracks Треклист из каталога
     * @param {Array} localTracks Локальные треки, выбранные пользователем
     * @param {Array} localIndices Оригинальные индексы локальных треков
     * @returns {Array} Массив пар { index, releaseTrack, localTrack, originalIndex, isMatched, isChecked }
     */
    function matchTracks(releaseTracks, localTracks, localIndices) {
        if (!releaseTracks) return [];
        const sortedRelease = [...releaseTracks].sort((a, b) => {
            const discDiff = (a.disc_number || 1) - (b.disc_number || 1);
            if (discDiff !== 0) return discDiff;
            return (a.track_number || 0) - (b.track_number || 0);
        });

        // 1. Подготавливаем локальные треки с их оригинальными индексами
        const candidates = [];
        if (localTracks && localIndices) {
            for (let i = 0; i < localTracks.length; i++) {
                const tr = localTracks[i];
                if (!tr) continue;
                const origIdx = localIndices[i];
                const fname = tr.filename || (tr.path ? tr.path.split('/').pop() : '') || '';
                candidates.push({
                    localTrack: tr,
                    originalIndex: origIdx,
                    title: tr.title || '',
                    filename: fname,
                    normTitle: normalizeTitleForMatching(tr.title || ''),
                    normFilename: normalizeTitleForMatching(fname),
                    used: false
                });
            }
        }

        // 2. Строим матрицу совпадений и выполняем жадное сопоставление (Best Match First)
        const matches = [];
        sortedRelease.forEach((relTrack, relIdx) => {
            const relTitle = relTrack.title || '';
            candidates.forEach((cand, candIdx) => {
                const scoreTitle = calculateTitleSimilarity(relTitle, cand.title);
                const scoreFile = calculateTitleSimilarity(relTitle, cand.filename);
                const bestScore = Math.max(scoreTitle, scoreFile);
                if (bestScore >= 0.50) {
                    matches.push({
                        relIdx,
                        candIdx,
                        score: bestScore
                    });
                }
            });
        });

        // Сортируем пары по убыванию уверенности
        matches.sort((a, b) => b.score - a.score);

        const assignedRel = new Map(); // relIdx -> candIdx
        const assignedCand = new Set(); // candIdx

        for (const m of matches) {
            if (!assignedRel.has(m.relIdx) && !assignedCand.has(m.candIdx)) {
                assignedRel.set(m.relIdx, m.candIdx);
                assignedCand.add(m.candIdx);
            }
        }

        // 3. Формируем финальный массив пар matchedPairs
        return sortedRelease.map((relTrack, idx) => {
            const candIdx = assignedRel.get(idx);
            const cand = candIdx !== undefined ? candidates[candIdx] : null;

            return {
                index: idx,
                releaseTrack: relTrack,
                localTrack: cand ? cand.localTrack : null,
                originalIndex: cand ? cand.originalIndex : null,
                isMatched: !!cand,
                isChecked: !!cand
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
            if (!pair.isChecked || !pair.localTrack || pair.originalIndex === null || pair.originalIndex === undefined) {
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
            const resp = await window.api.raw('/api/auth/musicbrainz/status', {
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
            const resp = await window.api.raw('/api/auth/musicbrainz/disconnect', {
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
        normalizeTitleForMatching,
        calculateTitleSimilarity,
        formatDuration,
        searchAllSources,
        fetchReleaseDetails,
        matchTracks,
        applyTagsToTracks,
        getMusicBrainzOAuthStatus,
        disconnectMusicBrainzOAuth
    };
})();
