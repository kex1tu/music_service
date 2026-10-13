/**
 * Puddletag Patterns Engine (window.PuddletagPatterns)
 * 
 * Модуль компиляции шаблонов, парсинга имен файлов в ID3-теги,
 * генерации имен файлов из метаданных и расчета Diff превью.
 * Полностью совместим со спецификацией токенов Puddletag / Mp3tag.
 */
(function() {
    'use strict';

    // Таблица соответствия токенов полям трека
    const TOKEN_MAP = {
        '%artist%': 'artist',
        '%title%': 'title',
        '%album%': 'album',
        '%track%': 'track_number',
        '%year%': 'year',
        '%genre%': 'genre',
        '%disc%': 'disc_number',
        '%albumartist%': 'album_artist',
        // Синонимы и расширенные токены
        '%album_artist%': 'album_artist',
        '%tracknumber%': 'track_number',
        '%track_number%': 'track_number',
        '%discnumber%': 'disc_number',
        '%disc_number%': 'disc_number',
        '%totaltracks%': 'track_total',
        '%tracktotal%': 'track_total',
        '%totaldiscs%': 'disc_total',
        '%disctotal%': 'disc_total'
    };

    /**
     * Экранирует специальные символы регулярных выражений.
     */
    function escapeRegExp(str) {
        return str.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    }

    /**
     * Санитизирует недопустимые символы файловой системы.
     * Запрещенные символы: / \ : * ? " < > | и нулевые байты.
     */
    function sanitizeForFilename(str) {
        if (!str) return '';
        return String(str)
            .replace(/[\/\\]/g, '-')     // Слеши заменяем на дефис
            .replace(/:/g, ' - ')        // Двоеточие на ' - '
            .replace(/[*"<>|?]/g, '')    // Удаляем невалидные символы
            .replace(/\x00/g, ' ')       // Нулевые байты заменяем на пробел
            .replace(/\s+/g, ' ')        // Схлопываем повторяющиеся пробелы
            .replace(/^[\s.-]+|[\s.-]+$/g, '') // Срезаем точки и пробелы по краям
            .trim();
    }

    const PuddletagPatterns = {
        /**
         * Стандартные пресеты шаблонов Puddletag / Mp3tag
         */
        presets: [
            { label: "%track% - %title%", pattern: "%track% - %title%" },
            { label: "%artist% - %title%", pattern: "%artist% - %title%" },
            { label: "%track% - %artist% - %title%", pattern: "%track% - %artist% - %title%" },
            { label: "%artist% - %album% - %track% - %title%", pattern: "%artist% - %album% - %track% - %title%" }
        ],

        /**
         * Токены, поддерживаемые движком
         */
        supportedTokens: Object.keys(TOKEN_MAP),

        /**
         * Компилирует строку шаблона (например, "%track% - %artist% - %title%") в RegExp
         * с группами захвата для каждого токена и экранированием спецсимволов вне токенов.
         * 
         * @param {string} pattern - Строка шаблона с токенами вида %token%
         * @returns {RegExp} Скомпилированное регулярное выражение с метаданными tokens и fields
         */
        compilePattern(pattern) {
            if (!pattern || typeof pattern !== 'string') {
                const emptyRegex = /^$/;
                emptyRegex.tokens = [];
                emptyRegex.fields = [];
                emptyRegex.pattern = '';
                return emptyRegex;
            }

            // Защита от ReDoS и переполнения памяти при избыточно длинном вводе
            if (pattern.length > 500) {
                pattern = pattern.slice(0, 500);
            }

            const tokenRegex = /%[a-zA-Z_]+%/g;
            const tokens = [];
            const fields = [];
            const parts = [];

            let lastIndex = 0;
            let match;

            while ((match = tokenRegex.exec(pattern)) !== null) {
                // Литеральная часть перед токеном
                const literalPart = pattern.slice(lastIndex, match.index);
                parts.push({ type: 'literal', value: literalPart });

                const rawToken = match[0];
                const lowerToken = rawToken.toLowerCase();
                const fieldName = TOKEN_MAP[lowerToken];

                if (fieldName) {
                    tokens.push(rawToken);
                    fields.push(fieldName);
                    parts.push({ type: 'token', token: rawToken, field: fieldName });
                } else {
                    // Нераспознанный токен экранируем как текст
                    parts.push({ type: 'literal', value: rawToken });
                }

                lastIndex = tokenRegex.lastIndex;
            }

            // Литеральная часть после последнего токена
            if (lastIndex < pattern.length) {
                parts.push({ type: 'literal', value: pattern.slice(lastIndex) });
            }

            // Построение регулярного выражения
            let regexStr = '^';
            const groupNameCounts = {};

            parts.forEach((part, idx) => {
                if (part.type === 'literal') {
                    if (part.value) {
                        // Экранируем спецсимволы и делаем пробелы гибкими (\s+)
                        let escaped = escapeRegExp(part.value);
                        escaped = escaped.replace(/\s+/g, '\\s+');
                        regexStr += escaped;
                    }
                } else if (part.type === 'token') {
                    // Проверяем, последний ли это элемент шаблона
                    const isLast = (idx === parts.length - 1) ||
                        (idx === parts.length - 2 && parts[parts.length - 1].type === 'literal' && !parts[parts.length - 1].value.trim());

                    // Уникальное имя именованной группы захвата
                    let groupName = part.field;
                    groupNameCounts[groupName] = (groupNameCounts[groupName] || 0) + 1;
                    if (groupNameCounts[groupName] > 1) {
                        groupName = `${groupName}_${groupNameCounts[groupName]}`;
                    }

                    // Для последнего токена захватываем все до конца строки greedy (.+),
                    // для промежуточных — non-greedy (.+?)
                    const capturePattern = isLast ? '(.+)' : '(.+?)';
                    regexStr += `(?<${groupName}>${capturePattern})`;
                }
            });

            regexStr += '$';

            let compiled;
            try {
                compiled = new RegExp(regexStr, 'i');
            } catch (e) {
                // Запасной вариант без именованных групп при синтаксической ошибке
                const fallbackStr = regexStr.replace(/\?<[a-zA-Z0-9_]+>/g, '');
                compiled = new RegExp(fallbackStr, 'i');
            }

            compiled.tokens = tokens;
            compiled.fields = fields;
            compiled.pattern = pattern;

            return compiled;
        },

        /**
         * Парсит имя файла (например, "01 - The Midnight - Sunset.mp3") по шаблону,
         * отрезает расширение и возвращает объект распарсенных тегов { track_number, artist, title, ... }.
         * 
         * @param {string|RegExp} pattern - Шаблон или скомпилированный RegExp
         * @param {string} filename - Имя файла или относительный путь
         * @returns {Object} Объект тегов
         */
        parseFilename(pattern, filename) {
            if (!pattern || !filename) return {};

            // 1. Оставляем только имя файла без пути
            let name = String(filename).replace(/\\/g, '/').split('/').pop();

            // 2. Отрезаем расширение (.mp3, .flac, .m4a и т.д.)
            name = name.replace(/\.[a-zA-Z0-9]{1,8}$/, '').trim();

            // 3. Получаем скомпилированное регулярное выражение
            const regex = (pattern instanceof RegExp) ? pattern : this.compilePattern(pattern);
            if (!regex) return {};

            // 4. Применяем регулярку
            const match = name.match(regex);
            if (!match) return {};

            const result = {};
            const groups = match.groups || {};
            const fields = regex.fields || [];

            fields.forEach((field, idx) => {
                let val = groups[field];
                if (val === undefined && match[idx + 1] !== undefined) {
                    val = match[idx + 1];
                }

                if (val !== undefined && val !== null) {
                    val = String(val).trim();

                    // Числовые поля конвертируем в int для совместимости со структурами Go
                    if (field === 'track_number' || field === 'track_total' ||
                        field === 'disc_number' || field === 'disc_total' ||
                        field === 'year') {
                        const parsedNum = parseInt(val, 10);
                        result[field] = !isNaN(parsedNum) ? parsedNum : val;
                    } else {
                        result[field] = val;
                    }
                }
            });

            return result;
        },

        /**
         * Форматирует имя файла из метаданных тегов по заданному шаблону.
         * Санитизирует невалидные символы ОС и сохраняет расширение файла (.mp3).
         * 
         * @param {string} pattern - Шаблон имени (например, "%track% - %artist% - %title%")
         * @param {Object} tags - Объект тегов трека
         * @param {string} [ext='.mp3'] - Расширение файла
         * @returns {string} Безопасное отформатированное имя файла с расширением
         */
        formatFilename(pattern, tags, ext) {
            const pat = pattern || '%track% - %title%';
            const safeTags = tags || {};

            let formatted = pat.replace(/%[a-zA-Z_]+%/gi, (token) => {
                const lower = token.toLowerCase();
                let val = '';

                if (lower === '%artist%') {
                    val = safeTags.artist;
                } else if (lower === '%title%') {
                    val = safeTags.title;
                } else if (lower === '%album%') {
                    val = safeTags.album;
                } else if (lower === '%track%' || lower === '%tracknumber%' || lower === '%track_number%') {
                    val = safeTags.track_number ?? safeTags.track;
                    if (val !== undefined && val !== null && val !== '') {
                        const num = parseInt(val, 10);
                        if (!isNaN(num) && num < 10 && !String(val).startsWith('0')) {
                            val = String(num).padStart(2, '0');
                        } else if (!isNaN(num)) {
                            val = String(val);
                        }
                    }
                } else if (lower === '%year%') {
                    val = safeTags.year;
                } else if (lower === '%genre%') {
                    val = safeTags.genre;
                } else if (lower === '%disc%' || lower === '%discnumber%' || lower === '%disc_number%') {
                    val = safeTags.disc_number ?? safeTags.disc;
                } else if (lower === '%albumartist%' || lower === '%album_artist%') {
                    val = safeTags.album_artist ?? safeTags.albumartist ?? safeTags.albumArtist;
                } else if (lower === '%tracktotal%' || lower === '%totaltracks%') {
                    val = safeTags.track_total;
                } else if (lower === '%disctotal%' || lower === '%totaldiscs%') {
                    val = safeTags.disc_total;
                }

                if (val === undefined || val === null) {
                    val = '';
                }

                return sanitizeForFilename(String(val));
            });

            // Санитизируем всю полученную строку (дефисы, пробелы, спецсимволы)
            formatted = sanitizeForFilename(formatted);
            if (!formatted) {
                formatted = 'Unknown Track';
            }

            // Обработка расширения
            let extension = ext || '.mp3';
            if (!extension.startsWith('.')) {
                extension = '.' + extension;
            }

            if (!formatted.toLowerCase().endsWith(extension.toLowerCase())) {
                formatted += extension;
            }

            return formatted;
        },

        /**
         * Рассчитывает предпросмотр изменений (Diff) для массива треков по шаблону.
         * 
         * @param {Array<Object>} tracks - Массив объектов треков
         * @param {string} pattern - Шаблон конвертации
         * @param {'filename_to_tag'|'tag_to_filename'} mode - Режим работы
         * @returns {Array<Object>} Массив результатов Diff
         */
        calculateDiff(tracks, pattern, mode) {
            if (!Array.isArray(tracks) || tracks.length === 0) {
                return [];
            }

            if (mode === 'filename_to_tag') {
                const compiledRegex = this.compilePattern(pattern);

                return tracks.map((track, index) => {
                    const trackPath = track.path || track.track_path || '';
                    const filename = track.name || track.filename || (trackPath ? trackPath.replace(/\\/g, '/').split('/').pop() : '');
                    const parsed = this.parseFilename(compiledRegex, filename);

                    const changes = {};
                    Object.keys(parsed).forEach(field => {
                        const oldVal = track[field];
                        const newVal = parsed[field];

                        let isDifferent = false;
                        if (field === 'track_number' || field === 'disc_number' || field === 'year') {
                            const oldNum = parseInt(oldVal, 10);
                            const newNum = parseInt(newVal, 10);
                            if (!isNaN(newNum)) {
                                isDifferent = isNaN(oldNum) || oldNum !== newNum;
                            } else {
                                isDifferent = String(oldVal ?? '').trim() !== String(newVal ?? '').trim();
                            }
                        } else {
                            isDifferent = String(oldVal ?? '').trim() !== String(newVal ?? '').trim();
                        }

                        if (isDifferent) {
                            changes[field] = {
                                old: oldVal !== undefined && oldVal !== null ? oldVal : '',
                                new: newVal
                            };
                        }
                    });

                    const hasChange = Object.keys(changes).length > 0;

                    // Человекочитаемые строки текущего и нового состояния
                    const curArtist = track.artist || '';
                    const curTitle = track.title || filename;
                    const currentDisplay = curArtist ? `${curArtist} - ${curTitle}` : curTitle;

                    const newArtist = parsed.artist !== undefined ? parsed.artist : track.artist;
                    const newTitle = parsed.title !== undefined ? parsed.title : (track.title || filename);
                    const previewDisplay = newArtist ? `${newArtist} - ${newTitle}` : (newTitle || '—');

                    return {
                        index,
                        track_path: trackPath,
                        current_display: currentDisplay,
                        preview_display: previewDisplay,
                        changes,
                        has_change: hasChange
                    };
                });
            }

            if (mode === 'tag_to_filename') {
                const items = tracks.map((track, index) => {
                    const trackPath = track.path || track.track_path || '';
                    const currentFilename = track.name || track.filename || (trackPath ? trackPath.replace(/\\/g, '/').split('/').pop() : '');

                    const ext = currentFilename.includes('.') ? ('.' + currentFilename.split('.').pop()) : '.mp3';
                    const newFilename = this.formatFilename(pattern, track, ext);
                    const hasChange = currentFilename !== newFilename;

                    return {
                        index,
                        track_path: trackPath,
                        current_filename: currentFilename,
                        new_filename: newFilename,
                        has_change: hasChange,
                        has_conflict: false
                    };
                });

                // Проверка на дубликаты целевых имен файлов среди выбранных треков
                const targetCounts = {};
                items.forEach(it => {
                    const dir = it.track_path ? it.track_path.replace(/\\/g, '/').split('/').slice(0, -1).join('/') : '';
                    const key = `${dir}/${it.new_filename}`.toLowerCase();
                    targetCounts[key] = (targetCounts[key] || 0) + 1;
                });

                items.forEach(it => {
                    const dir = it.track_path ? it.track_path.replace(/\\/g, '/').split('/').slice(0, -1).join('/') : '';
                    const key = `${dir}/${it.new_filename}`.toLowerCase();
                    it.has_conflict = targetCounts[key] > 1;
                });

                return items;
            }

            return [];
        }
    };

    window.PuddletagPatterns = PuddletagPatterns;
})();
