/**
 * Модуль нормализации и очистки метаданных тегов (Tag Cleaner).
 * Отвечает за парсинг множественных артистов с защитой составных псевдонимов,
 * извлечение фитов (feat/ft), удаление мусорных суффиксов (Official Video, 1080p, Remastered).
 */
window.TagCleaner = {
    /**
     * Делегирует форматирование жанров в AppFormatters.
     */
    formatGenres(raw) {
        return window.AppFormatters ? window.AppFormatters.formatGenres(raw) : raw;
    },

    /**
     * Парсит строку артистов, учитывая составные имена с запятыми и союзами.
     */
    parseArtistsString(raw) {
        if (!raw) return [];
        let text = String(raw).trim();
        if (!text) return [];

        const knownCompounds = [
            "Tyler, The Creator",
            "Tyler, the Creator",
            "Earth, Wind & Fire",
            "Earth, Wind and Fire",
            "Crosby, Stills, Nash & Young",
            "Crosby, Stills, Nash and Young",
            "Crosby, Stills & Nash",
            "Blood, Sweat & Tears",
            "Blood, Sweat and Tears",
            "Emerson, Lake & Palmer",
            "Peter, Paul and Mary",
            "Peter, Paul & Mary",
            "Bell, Book & Candle",
            "You, Me, and Everyone We Know",
            "Me First and the Gimme Gimmes",
            "Me, First and the Gimme Gimmes",
        ];

        const placeholders = {};
        knownCompounds.forEach((cmp, idx) => {
            const lowerText = text.toLowerCase();
            const lowerCmp = cmp.toLowerCase();
            const pos = lowerText.indexOf(lowerCmp);
            if (pos !== -1) {
                const matched = text.substr(pos, cmp.length);
                const token = `__COMPOUND_${idx}__`;
                placeholders[token] = matched;
                text = text.replace(matched, token);
            }
        });

        const splitRegex = /\x00|\\+|\s*;\s*|\s*,\s*|\s+&\s+|\s+\/\s+|\s+(?:feat\.?|ft\.?|featuring|with|pres\.?|presents)\s+|\s+(?:vs\.?|v\.)\s+|\s+[xX×]\s+|\s+(?:and|и)\s+/i;
        const tokens = text.split(splitRegex);
        const results = [];

        tokens.forEach(tok => {
            let t = tok.trim();
            if (!t) return;
            Object.keys(placeholders).forEach(token => {
                if (t.includes(token)) {
                    t = t.replace(token, placeholders[token]);
                }
            });
            t = t.replace(/^[\s.]+|[\s.]+$/g, '');
            if (t && !results.some(r => r.toLowerCase() === t.toLowerCase())) {
                results.push(t);
            }
        });

        return results;
    },

    /**
     * Очищает название трека: убирает трек-номера, префиксы исполнителя,
     * извлекает feat/ft в список артистов и чистит мусорные теги.
     */
    cleanTrackTitle(track) {
        if (!track || !track.title) return;
        let rawTitle = String(track.title).trim();
        if (!rawTitle) return;

        let artists = track.artist ? this.parseArtistsString(track.artist) : [];

        // 1. Ведущие порядковые номера: "01. ", "01 - ", "01 "
        rawTitle = rawTitle.replace(/^\s*\d{1,3}\s*[\.\-–—]\s*/, '').replace(/^\s*\d{1,3}\s+/, '');

        // 2. Артист в начале названия: "Artist - Title"
        const artistMatch = rawTitle.match(/^([^\-–—]+?)\s*[-–—]\s*(.+)$/);
        if (artistMatch) {
            const candArtist = artistMatch[1].trim();
            const restTitle = artistMatch[2].trim();
            if (artists.length > 0) {
                const lowerArtists = artists.map((a) => a.toLowerCase());
                if (lowerArtists.includes(candArtist.toLowerCase())) {
                    rawTitle = restTitle;
                }
            } else {
                const candParts = this.parseArtistsString(candArtist);
                for (const cp of candParts) {
                    if (!artists.some((a) => a.toLowerCase() === cp.toLowerCase())) {
                        artists.push(cp);
                    }
                }
                rawTitle = restTitle;
            }
        }

        // 3. Извлечение feat / ft / featuring
        // 3.1. Внутри скобок (...) или [...]
        const bracketFeatRegex = /[\(\[]\s*(?:feat\.?|ft\.?|featuring|with)\s+([^\]\)]+?)[\)\]]/gi;
        let m;
        while ((m = bracketFeatRegex.exec(rawTitle)) !== null) {
            const featContent = m[1].trim();
            rawTitle = rawTitle.slice(0, m.index) + ' ' + rawTitle.slice(m.index + m[0].length);
            bracketFeatRegex.lastIndex = m.index;
            const parts = this.parseArtistsString(featContent);
            for (const p of parts) {
                const pClean = p.trim();
                if (pClean && !artists.some((a) => a.toLowerCase() === pClean.toLowerCase())) {
                    artists.push(pClean);
                }
            }
        }

        // 3.2. В конце названия без скобок: "Song feat. Artist2"
        const plainFeatMatch = rawTitle.match(/\b(?:feat\.?|ft\.?|featuring|with)\s+([^\[\]\(\)\-]+)$/i);
        if (plainFeatMatch) {
            const featContent = plainFeatMatch[1].trim();
            rawTitle = rawTitle.slice(0, plainFeatMatch.index).trim();
            const parts = this.parseArtistsString(featContent);
            for (const p of parts) {
                const pClean = p.trim();
                if (pClean && !artists.some((a) => a.toLowerCase() === pClean.toLowerCase())) {
                    artists.push(pClean);
                }
            }
        }

        // 4. Удаление мусорных суффиксов в скобках
        const junkRegex = /[\(\[]\s*(?:(?:(?:official|music|4k|hd|hq|1080p|720p)\s+)*(?:video|audio|visualizer|clip\s*officiel|lyric\s*video|lyrics)|official|audio|video|visualizer|lyrics|clip\s*officiel|hd|hq|4k|1080p|720p|prod\.?\s*by[^\)\]]+|remaster(?:ed)?(?:\s*\d{4})?)\s*[\)\]]/gi;
        rawTitle = rawTitle.replace(junkRegex, '');

        // 5. Очистка пустых скобок, лишних пробелов и дефисов
        rawTitle = rawTitle.replace(/[\(\[]\s*[\)\]]/g, '');
        rawTitle = rawTitle.replace(/\s+/g, ' ').replace(/^[\s\-–—._]+|[\s\-–—._]+$/g, '');

        if (rawTitle) {
            track.title = rawTitle;
        }
        if (artists.length > 0) {
            track.artist = artists.join('\\\\');
        }
    },

    /**
     * Очищает названия и извлекает фиты для всех треков текущей сессии.
     */
    cleanTitles() {
        if (!this.tracks || this.tracks.length === 0) return;
        let modified = 0;
        for (const track of this.tracks) {
            const oldTitle = track.title;
            const oldArtist = track.artist;
            this.cleanTrackTitle(track);
            if (track.title !== oldTitle || track.artist !== oldArtist) {
                modified++;
            }
        }
        Alpine.store('app').toast(`Очищено названий и извлечено фитов: ${modified}`, 'info');
    },

    /**
     * Очищает названия треков внутри конкретной группы/релиза.
     */
    cleanGroupTitles(group) {
        if (!group || !group.items || group.items.length === 0) return;
        let modified = 0;
        group.items.forEach(({ track }) => {
            const oldTitle = track.title;
            const oldArtist = track.artist;
            this.cleanTrackTitle(track);
            if (track.title !== oldTitle || track.artist !== oldArtist) {
                modified++;
            }
        });
        Alpine.store('app').toast(`В релизе очищено треков: ${modified}`, 'info');
    }
};
