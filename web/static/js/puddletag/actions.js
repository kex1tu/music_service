/**
 * Puddletag Actions Engine (window.PuddletagActions)
 * 
 * Набор автоматизаций и пакетных операций над метаданными аудиотреков:
 * - Конвертация регистра (Title Case, UPPERCASE, lowercase, Sentence case)
 * - Мастер автонумерации (с ведущими нулями 01..N, сбросом по дискам и расчетом total)
 * - Очистка названий от мусорных префиксов и извлечение фитов (feat./ft.) в поле Artist (\)
 * - Поиск и замена по полям метаданных (с поддержкой Regex и регистра)
 */
(function() {
    'use strict';

    /**
     * Экранирует специальные символы регулярных выражений.
     */
    function escapeRegExp(str) {
        return str.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    }

    /**
     * Парсит строку артистов в массив, разбивая по разделителям \, \x00, ;, , и &.
     */
    function parseArtists(raw) {
        if (!raw) return [];
        const str = String(raw).trim();
        if (!str) return [];

        // Разделители: двойной бекслеш \\, нуль-байт, точка с запятой
        return str
            .split(/\\\\|\x00|\s*;\s*/)
            .map(a => a.trim())
            .filter(Boolean);
    }

    /**
     * Разбивает строку фита (например, "Giorgia & Ollie Wride" или "Artist A, Artist B")
     * на отдельных артистов.
     */
    function splitGuestArtists(featStr) {
        if (!featStr) return [];
        return featStr
            .split(/\s*(?:&|and|,|\/|\+|feat\.?|ft\.?)\s*/i)
            .map(a => a.trim().replace(/^[\s.]+|[\s.]+$/g, ''))
            .filter(Boolean);
    }

    const PuddletagActions = {
        /**
         * Преобразует регистр переданной строки.
         * 
         * @param {string} str - Исходная строка
         * @param {'title'|'upper'|'lower'|'sentence'} style - Стиль регистра
         * @returns {string} Преобразованная строка
         */
        convertCase(str, style) {
            if (str === null || str === undefined) return '';
            const text = String(str);
            if (!text) return '';

            switch (style) {
                case 'upper':
                    return text.toUpperCase();

                case 'lower':
                    return text.toLowerCase();

                case 'sentence': {
                    // Переводим всю строку в lowercase, затем капитализируем первую букву
                    // в начале строки и после знаков окончания предложения (. ! ?),
                    // с учетом открывающих скобок и кавычек.
                    const lower = text.toLowerCase();
                    return lower.replace(/(^|[.!?]\s+)([\s([{'"\u201c\u201d]*?)(\p{L})/gu, (match, boundary, punctLead, letter) => {
                        return boundary + punctLead + letter.toUpperCase();
                    });
                }

                case 'title': {
                    // Title Case: Капитализирует первую букву каждого слова.
                    // Учитывает дефисы (Blink-182), скобки (Live At Wembley), слэши (Rock/Pop)
                    // и сохраняет апострофы внутри слов (Don't, It's, Rock'n'roll).
                    const lower = text.toLowerCase();
                    return lower.replace(/(?:^|[\s\/([{\]'"\u201c\u201d\-]|\p{Pd})(\p{L})/gu, (match, letter, offset, fullStr) => {
                        // Если это апостроф внутри слова (don't, it's), не капитализируем следующую букву
                        if (match.startsWith("'") && offset > 0 && /\p{L}/u.test(fullStr[offset - 1])) {
                            return match;
                        }
                        return match.slice(0, -1) + letter.toUpperCase();
                    });
                }

                default:
                    return text;
            }
        },

        /**
         * Применяет конвертацию регистра к указанным полям для выбранных треков.
         * 
         * @param {Array<Object>} tracks - Массив всех треков
         * @param {Array<number>} [selectedIndices] - Индексы выбранных треков (если пусто, то ко всем)
         * @param {Array<string>} [fields=['title', 'artist']] - Поля для обработки
         * @param {'title'|'upper'|'lower'|'sentence'} style - Стиль регистра
         * @returns {number} Количество модифицированных треков
         */
        applyCase(tracks, selectedIndices, fields, style) {
            if (!Array.isArray(tracks) || tracks.length === 0) return 0;

            const targetIndices = (Array.isArray(selectedIndices) && selectedIndices.length > 0)
                ? selectedIndices
                : tracks.map((_, i) => i);

            const targetFields = (Array.isArray(fields) && fields.length > 0)
                ? fields
                : ['title', 'artist'];

            let modifiedCount = 0;

            targetIndices.forEach(idx => {
                const track = tracks[idx];
                if (!track) return;

                let trackModified = false;
                targetFields.forEach(field => {
                    if (track[field] !== undefined && track[field] !== null && String(track[field]).trim() !== '') {
                        const original = String(track[field]);
                        const converted = this.convertCase(original, style);
                        if (converted !== original) {
                            track[field] = converted;
                            trackModified = true;
                        }
                    }
                });

                if (trackModified) {
                    modifiedCount++;
                }
            });

            return modifiedCount;
        },

        /**
         * Мастер автоматической нумерации треков.
         * 
         * @param {Array<Object>} tracks - Массив всех треков
         * @param {Array<number>} [selectedIndices] - Индексы выбранных треков (если пусто, то ко всем)
         * @param {Object} [options] - Настройки нумерации
         * @param {number} [options.startNum=1] - Начальный номер
         * @param {boolean} [options.leadingZeros=true] - Использовать ведущие нули (01, 02)
         * @param {boolean} [options.resetPerDisc=false] - Сбрасывать нумерацию для каждого диска
         * @param {boolean} [options.setTotal=true] - Проставлять общее количество треков (track_total)
         * @returns {number} Количество модифицированных треков
         */
        autoNumber(tracks, selectedIndices, options) {
            if (!Array.isArray(tracks) || tracks.length === 0) return 0;

            const opts = Object.assign({
                startNum: 1,
                leadingZeros: true,
                resetPerDisc: false,
                setTotal: true
            }, options || {});

            const targetIndices = (Array.isArray(selectedIndices) && selectedIndices.length > 0)
                ? selectedIndices
                : tracks.map((_, i) => i);

            if (targetIndices.length === 0) return 0;

            let modifiedCount = 0;

            if (opts.resetPerDisc) {
                // Группировка выбранных треков по номеру диска
                const discGroups = new Map();
                targetIndices.forEach(idx => {
                    const track = tracks[idx];
                    if (!track) return;
                    const disc = track.disc_number || track.disc || 1;
                    if (!discGroups.has(disc)) {
                        discGroups.set(disc, []);
                    }
                    discGroups.get(disc).push({ track, idx });
                });

                discGroups.forEach((groupTracks) => {
                    const totalTracks = groupTracks.length + opts.startNum - 1;
                    const padDigits = totalTracks >= 100 ? 3 : 2;

                    groupTracks.forEach((item, index) => {
                        const num = index + opts.startNum;
                        const formattedNum = opts.leadingZeros
                            ? String(num).padStart(padDigits, '0')
                            : num;

                        let trackChanged = false;
                        if (item.track.track_number !== formattedNum) {
                            item.track.track_number = formattedNum;
                            trackChanged = true;
                        }

                        if (opts.setTotal && item.track.track_total !== totalTracks) {
                            item.track.track_total = totalTracks;
                            trackChanged = true;
                        }

                        if (trackChanged) {
                            modifiedCount++;
                        }
                    });
                });
            } else {
                // Сквозная последовательная нумерация по всей выборке
                const totalTracks = targetIndices.length + opts.startNum - 1;
                const padDigits = totalTracks >= 100 ? 3 : 2;

                targetIndices.forEach((idx, index) => {
                    const track = tracks[idx];
                    if (!track) return;

                    const num = index + opts.startNum;
                    const formattedNum = opts.leadingZeros
                        ? String(num).padStart(padDigits, '0')
                        : num;

                    let trackChanged = false;
                    if (track.track_number !== formattedNum) {
                        track.track_number = formattedNum;
                        trackChanged = true;
                    }

                    if (opts.setTotal && track.track_total !== totalTracks) {
                        track.track_total = totalTracks;
                        trackChanged = true;
                    }

                    if (trackChanged) {
                        modifiedCount++;
                    }
                });
            }

            return modifiedCount;
        },

        /**
         * Очищает названия треков от мусорных префиксов и извлекает приглашенных артистов (feat./ft.) в поле Artist.
         * Использует \\ как разделитель мультиартистов в соответствии с инвариантами проекта.
         * 
         * @param {Array<Object>} tracks - Массив всех треков
         * @param {Array<number>} [selectedIndices] - Индексы выбранных треков (если пусто, то ко всем)
         * @returns {number} Количество модифицированных треков
         */
        cleanTitles(tracks, selectedIndices) {
            if (!Array.isArray(tracks) || tracks.length === 0) return 0;

            const targetIndices = (Array.isArray(selectedIndices) && selectedIndices.length > 0)
                ? selectedIndices
                : tracks.map((_, i) => i);

            let modifiedCount = 0;

            targetIndices.forEach(idx => {
                const track = tracks[idx];
                if (!track || !track.title) return;

                let title = String(track.title).trim();
                if (!title) return;

                const origTitle = title;
                const origArtist = track.artist || '';

                let artists = parseArtists(origArtist);

                // 1. Удаление мусорных префиксов трек-номеров:
                // "01. ", "01 - ", "01_ ", "01. 01. "
                title = title.replace(/^(?:\d{1,3}\s*[\.\-_–—]\s+)+/, '');
                // "track01_", "track_01 - ", "track 01. "
                title = title.replace(/^(?:track[\s._\-–—]*\d{1,3}[\s._\-–—]+)/i, '');
                // "01_Song", "01 - Song", "01.Song"
                title = title.replace(/^(?:\d{1,3}[\s._\-–—]+)/, '');

                // 2. Извлечение feat / ft / featuring / with в скобках (...) или [...]
                const bracketFeatRegex = /[\(\[]\s*(?:feat\.?|ft\.?|featuring|with)\s+([^\]\)]+?)[\)\]]/gi;
                let featMatch;
                while ((featMatch = bracketFeatRegex.exec(title)) !== null) {
                    const guestRaw = featMatch[1].trim();
                    title = title.slice(0, featMatch.index) + ' ' + title.slice(featMatch.index + featMatch[0].length);
                    bracketFeatRegex.lastIndex = featMatch.index;

                    const guests = splitGuestArtists(guestRaw);
                    guests.forEach(g => {
                        if (g && !artists.some(a => a.toLowerCase() === g.toLowerCase())) {
                            artists.push(g);
                        }
                    });
                }

                // 3. Извлечение feat / ft в конце названия без скобок: "Song feat. Artist"
                const trailingFeatRegex = /\b(?:feat\.?|ft\.?|featuring|with)\s+([^\[\]\(\)\-]+)$/i;
                const plainMatch = title.match(trailingFeatRegex);
                if (plainMatch) {
                    const guestRaw = plainMatch[1].trim();
                    title = title.slice(0, plainMatch.index).trim();

                    const guests = splitGuestArtists(guestRaw);
                    guests.forEach(g => {
                        if (g && !artists.some(a => a.toLowerCase() === g.toLowerCase())) {
                            artists.push(g);
                        }
                    });
                }

                // 4. Удаление мусорных суффиксов в скобках [Official Video], (Remastered) и т.д.
                const junkRegex = /[\(\[]\s*(?:(?:(?:official|music|4k|hd|hq|1080p|720p)\s+)*(?:video|audio|visualizer|clip\s*officiel|lyric\s*video|lyrics)|official|audio|video|visualizer|lyrics|clip\s*officiel|hd|hq|4k|1080p|720p|prod\.?\s*by[^\)\]]+|remaster(?:ed)?(?:\s*\d{4})?)\s*[\)\]]/gi;
                title = title.replace(junkRegex, '');

                // 5. Очистка пустых скобок, лишних пробелов и граничных символов
                title = title.replace(/[\(\[]\s*[\)\]]/g, '');
                title = title.replace(/\s+/g, ' ').replace(/^[\s\-–—._]+|[\s\-–—._]+$/g, '').trim();

                let changed = false;
                if (title && title !== origTitle) {
                    track.title = title;
                    changed = true;
                }

                // Объединяем артистов через двойной обратный слэш \\ для ввода
                if (artists.length > 0) {
                    const newArtistStr = artists.join('\\\\');
                    if (newArtistStr !== origArtist) {
                        track.artist = newArtistStr;
                        changed = true;
                    }
                }

                if (changed) {
                    modifiedCount++;
                }
            });

            return modifiedCount;
        },

        /**
         * Поиск и замена текста в выбранных полях треков.
         * 
         * @param {Array<Object>} tracks - Массив всех треков
         * @param {Array<number>} [selectedIndices] - Индексы выбранных треков (если пусто, то ко всем)
         * @param {string} searchStr - Строка поиска (или RegExp паттерн)
         * @param {string} [replaceStr=''] - Строка замены
         * @param {Array<string>} [fields=['title', 'artist', 'album']] - Поля для замены
         * @param {boolean} [isRegex=false] - Обрабатывать searchStr как регулярное выражение
         * @param {boolean} [caseSensitive=false] - Учитывать регистр
         * @returns {number} Количество модифицированных треков
         */
        searchAndReplace(tracks, selectedIndices, searchStr, replaceStr, fields, isRegex, caseSensitive) {
            if (!Array.isArray(tracks) || tracks.length === 0) return 0;
            if (searchStr === undefined || searchStr === null || searchStr === '') return 0;

            const targetIndices = (Array.isArray(selectedIndices) && selectedIndices.length > 0)
                ? selectedIndices
                : tracks.map((_, i) => i);

            const targetFields = (Array.isArray(fields) && fields.length > 0)
                ? fields
                : ['title', 'artist', 'album'];

            const replacement = (replaceStr !== undefined && replaceStr !== null) ? String(replaceStr) : '';

            let regex;
            try {
                const flags = (caseSensitive ? '' : 'i') + 'g';
                if (isRegex) {
                    regex = new RegExp(searchStr, flags);
                } else {
                    regex = new RegExp(escapeRegExp(String(searchStr)), flags);
                }
            } catch (err) {
                console.error('[PuddletagActions] searchAndReplace: невалидное регулярное выражение', err);
                return 0;
            }

            let modifiedCount = 0;

            targetIndices.forEach(idx => {
                const track = tracks[idx];
                if (!track) return;

                let trackModified = false;
                targetFields.forEach(field => {
                    if (track[field] !== undefined && track[field] !== null) {
                        const original = String(track[field]);
                        const updated = original.replace(regex, replacement);
                        if (updated !== original) {
                            track[field] = updated;
                            trackModified = true;
                        }
                    }
                });

                if (trackModified) {
                    modifiedCount++;
                }
            });

            return modifiedCount;
        }
    };

    window.PuddletagActions = PuddletagActions;
})();
