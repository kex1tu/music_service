package resolver

import (
	"path/filepath"
	"regexp"
	"strings"

	"music-manager/internal/id3"
)

var (
	// Регулярка для удаления клиповых и служебных маркеров в скобках
	ytVideoJunkRegex = regexp.MustCompile(`(?i)\s*[\(\[]\s*(?:official\s+(?:music\s+)?video|official\s+audio|official\s+hd\s+video|official\s+lyric\s+video|lyric\s+video|lyrics|клип|официальный\s+клип|премьера(?:\s+клипа)?(?:\s+\d{4})?|премьера\s+трека|video|audio|4k|hd|remastered(?:\s+\d{4})?|visualizer|prod\.(?:\s+by)?\s+[^\]\)]+)\s*[\)\]]`)

	// Регулярка для удаления суффиксов после слэша или пайпа в конце заголовка: " / Премьера клипа", " | Official Video"
	ytTrailingJunkRegex = regexp.MustCompile(`(?i)\s*(?:[/|]\s*(?:official\s+(?:music\s+)?video|official\s+audio|премьера(?:\s+клипа)?(?:\s+\d{4})?|клип|lyrics|audio).*)$`)

	// Регулярка для суффикса Topic в автогенерируемых каналах YouTube Music
	ytTopicSuffixRegex = regexp.MustCompile(`(?i)\s*-\s*Topic$`)

	// Регулярка для обнаружения и извлечения соавторов (feat. / ft. / with / при уч.)
	ytFeatParenRegex = regexp.MustCompile(`(?i)[\(\[]\s*(?:feat\.?|ft\.?|featuring|with|при\s+уч\.?)\s+([^\)\]]+)[\)\]]`)
	ytFeatInlineRegex = regexp.MustCompile(`(?i)\s+(?:feat\.?|ft\.?|featuring|при\s+уч\.?)\s+([^—–:\(\[]+)`)

	// Разделители списка соавторов внутри feat блока: "Rem Digga, Miyagi & Andy Panda"
	featSeparatorRegex = regexp.MustCompile(`(?i)\s*(?:,|&|\band\b|\bx\b|\bи\b)\s*`)

	// Список известных каналов-лейблов и агрегаторов (не являются исполнителями трека)
	knownLabels = []string{
		"gazgolder", "то \"gazgolder\"", "то gazgolder", "starpro", "vevo",
		"sony music", "sony music russia", "warner music", "warner music russia",
		"universal music", "rhymes music", "black star", "zhara music",
		"музыка первого", "melon music records", "label", "records",
		"golden sound", "velvet music", "soyuz music", "союз мьюзик",
		"русская рэп-сцена", "рэп сцена", "новинки музыки", "russian rap",
	}

	streamingJunkInParenRegex = regexp.MustCompile(`(?i)\s*[\(\[]\s*(?:official\s+(?:music\s+)?video|official\s+audio|official\s+hd\s+video|official\s+lyric\s+video|lyric\s+video|lyrics|клип|официальный\s+клип|премьера(?:\s+клипа)?(?:\s+\d{4})?|премьера\s+трека|video|audio|4k|hd|remastered(?:\s+\d{4})?|visualizer|prod\.(?:\s+by)?\s+[^\]\)]+|feat\.?|ft\.?|featuring|with|при\s+уч\.?|explicit|clean|censored|uncensored|uncut|до\s+цензуры|без\s+цензуры|с\s+цензурой|цензурная|sped\s+up|slowed)[^\]\)]*[\)\]]`)
	streamingInlineFeatRegex  = regexp.MustCompile(`(?i)\s+(?:feat\.?|ft\.?|featuring|with|при\s+уч\.?)\s+[^—–:\(\[]+`)
	punctuationRegex          = regexp.MustCompile(`[^\p{L}\p{N}\s]`)
	multiSpaceRegex           = regexp.MustCompile(`\s+`)
)

// ParsedYouTubeTrack содержит результаты очистки и парсинга видео YouTube.
type ParsedYouTubeTrack struct {
	Title        string   // Очищенное название песни (без клипового мусора и без имени артиста)
	Artist       string   // Основной артист или объединенная строка с соавторами для UI (\\)
	Artists      []string // Полный слайс всех исполнителей (основной + соавторы)
	Album        string   // Название альбома/сингла
	Featured     []string // Только приглашенные артисты (соавторы)
	IsStudioClip bool     // Был ли обнаружен клиповый маркер
}

// CleanYouTubeVideoTitle удаляет клиповый мусор ("(Official Video)", "[Клип 4K]", "| Official Audio")
func CleanYouTubeVideoTitle(title string) string {
	t := strings.TrimSpace(title)
	t = ytTrailingJunkRegex.ReplaceAllString(t, "")
	t = ytVideoJunkRegex.ReplaceAllString(t, "")
	t = strings.TrimSpace(t)
	// Дополнительно применяем стандартную очистку префиксов дорожек и расширений
	return id3.CleanTitle(t, filepath.Base(t))
}

// ExtractFeatFromText извлекает список соавторов из строки и возвращает очищенную строку и список артистов.
func ExtractFeatFromText(text string) (cleanText string, feats []string) {
	cleanText = text

	// 1. Ищем в скобках: "(feat. Rem Digga)" или "[ft. Eminem, 50 Cent]"
	if m := ytFeatParenRegex.FindStringSubmatch(cleanText); len(m) > 1 {
		rawFeat := strings.TrimSpace(m[1])
		cleanText = strings.TrimSpace(ytFeatParenRegex.ReplaceAllString(cleanText, ""))
		for _, part := range featSeparatorRegex.Split(rawFeat, -1) {
			p := strings.TrimSpace(part)
			if p != "" {
				feats = append(feats, p)
			}
		}
	}

	// 2. Ищем inline: "feat. Rem Digga" (только если в скобках ничего не было найдено)
	if len(feats) == 0 {
		if m := ytFeatInlineRegex.FindStringSubmatch(cleanText); len(m) > 1 {
			rawFeat := strings.TrimSpace(m[1])
			cleanText = strings.TrimSpace(ytFeatInlineRegex.ReplaceAllString(cleanText, ""))
			for _, part := range featSeparatorRegex.Split(rawFeat, -1) {
				p := strings.TrimSpace(part)
				if p != "" {
					feats = append(feats, p)
				}
			}
		}
	}

	return cleanText, feats
}

// IsLabelChannel checks if a channel name is a known record label or aggregator.
func IsLabelChannel(channel string) bool {
	return isLabelChannel(channel)
}

// isLabelChannel проверяет, является ли имя канала известным лейблом или агрегатором.
func isLabelChannel(channel string) bool {
	lower := strings.ToLower(strings.TrimSpace(channel))
	for _, l := range knownLabels {
		if lower == l || strings.Contains(lower, l) {
			return true
		}
	}
	return false
}

// CleanChannelName очищает имя канала от служебных суффиксов (например, " - Topic", " VEVO", " Official").
func CleanChannelName(channel string) string {
	ch := strings.TrimSpace(channel)
	ch = ytTopicSuffixRegex.ReplaceAllString(ch, "")
	ch = strings.TrimSuffix(ch, " - Topic")
	ch = strings.TrimSuffix(ch, "VEVO")
	ch = strings.TrimSuffix(ch, "vevo")
	ch = strings.TrimSuffix(ch, " Official")
	ch = strings.TrimSuffix(ch, " Music")
	return strings.TrimSpace(ch)
}

// ParseYouTubeTitle производит глубокий семантический разбор заголовка YouTube и канала.
func ParseYouTubeTitle(rawTitle, channelName, uploader string) ParsedYouTubeTrack {
	// Сначала очищаем имя канала
	cleanChannel := CleanChannelName(channelName)
	if cleanChannel == "" {
		cleanChannel = CleanChannelName(uploader)
	}

	// Очищаем клиповый мусор из заголовка
	hadJunk := ytVideoJunkRegex.MatchString(rawTitle) || ytTrailingJunkRegex.MatchString(rawTitle)
	cleanedTitle := CleanYouTubeVideoTitle(rawTitle)

	var artistCandidate, titleCandidate string

	// Ищем стандартные разделители между артистом и названием трека (" — ", " – ", " - ", " : ")
	// Внимание: требуются пробелы с обеих сторон, чтобы не резать слова вида "T-Fest", "Jay-Z", "AC/DC".
	dashDelimiters := []string{" — ", " – ", " - ", " : "}
	splitDone := false

	for _, delim := range dashDelimiters {
		if idx := strings.Index(cleanedTitle, delim); idx != -1 {
			left := strings.TrimSpace(cleanedTitle[:idx])
			right := strings.TrimSpace(cleanedTitle[idx+len(delim):])
			if len(left) >= 2 && len(right) >= 1 {
				artistCandidate = left
				titleCandidate = right
				splitDone = true
				break
			}
		}
	}

	// Если разделение по тире не произошло:
	if !splitDone {
		titleCandidate = cleanedTitle
		// Если канал не является лейблом, канал и есть наш артист
		if cleanChannel != "" && !isLabelChannel(cleanChannel) {
			artistCandidate = cleanChannel
		}
	}

	// Если левая часть определилась, но она совпадает с лейблом:
	if isLabelChannel(artistCandidate) && cleanChannel != "" && !isLabelChannel(cleanChannel) {
		artistCandidate = cleanChannel
	}

	// Если артист до сих пор пуст:
	if artistCandidate == "" {
		if cleanChannel != "" {
			artistCandidate = cleanChannel
		} else {
			artistCandidate = "Unknown Artist"
		}
	}

	// Извлекаем соавторов (feat.) из названия и из строки артиста
	var allArtists []string
	seen := make(map[string]bool)

	addArtist := func(name string) {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			return
		}
		lower := strings.ToLower(trimmed)
		if !seen[lower] {
			seen[lower] = true
			allArtists = append(allArtists, trimmed)
		}
	}

	// 1. Проверяем artistCandidate на feat
	cleanArt, artFeats := ExtractFeatFromText(artistCandidate)
	// Возможно, артист содержал " & " или " x " (коллаборация главных артистов)
	// Разделяем только если не ломает имена вида "Miyagi & Эндшпиль"
	addArtist(cleanArt)
	for _, f := range artFeats {
		addArtist(f)
	}

	// 2. Проверяем titleCandidate на feat
	cleanTit, titFeats := ExtractFeatFromText(titleCandidate)
	var featuredList []string
	for _, f := range artFeats {
		featuredList = append(featuredList, f)
	}
	for _, f := range titFeats {
		addArtist(f)
		featuredList = append(featuredList, f)
	}

	// Нормализуем чистый заголовок трека
	finalTitle := strings.TrimSpace(cleanTit)
	if finalTitle == "" {
		finalTitle = cleanedTitle
	}

	// Форматируем строку артистов для UI стандарта (\\)
	formattedArtist := id3.FormatMultiValue(allArtists)
	if formattedArtist == "" {
		formattedArtist = artistCandidate
	}

	return ParsedYouTubeTrack{
		Title:        finalTitle,
		Artist:       formattedArtist,
		Artists:      allArtists,
		Album:        finalTitle, // Для синглов по умолчанию альбом равен названию трека
		Featured:     featuredList,
		IsStudioClip: hadJunk,
	}
}

// NormalizeTrackTitleForMatching очищает название от клипового и стримингового мусора (Official Audio, feat, prod, explicit)
// и пунктуации для точного семантического сопоставления.
func NormalizeTrackTitleForMatching(title string) string {
	t := strings.TrimSpace(title)
	t = ytTrailingJunkRegex.ReplaceAllString(t, "")
	t = streamingJunkInParenRegex.ReplaceAllString(t, "")
	t = streamingInlineFeatRegex.ReplaceAllString(t, "")
	t = punctuationRegex.ReplaceAllString(t, " ")
	t = multiSpaceRegex.ReplaceAllString(t, " ")
	return strings.ToLower(strings.TrimSpace(t))
}

// CheckTitleAndArtistMatch проверяет семантическое соответствие кандидата целевому треку.
// Учитывает нормализацию мусорных суффиксов, транслитерацию и вхождение коротких названий (3-4 буквы).
func CheckTitleAndArtistMatch(candidateTitle, candidateUploader, targetTitle, targetArtist string) (match bool, titleScore float64, artistScore float64) {
	normTargetTitle := NormalizeTrackTitleForMatching(targetTitle)
	normTargetArtist := NormalizeTrackTitleForMatching(targetArtist)

	candTitle := candidateTitle
	candPartArtist := ""
	candPartTitle := ""

	dashDelimiters := []string{" — ", " – ", " - ", " : "}
	for _, delim := range dashDelimiters {
		if idx := strings.Index(candTitle, delim); idx != -1 {
			candPartArtist = strings.TrimSpace(candTitle[:idx])
			candPartTitle = strings.TrimSpace(candTitle[idx+len(delim):])
			break
		}
	}

	normCandFull := NormalizeTrackTitleForMatching(candTitle)
	normCandTitle := NormalizeTrackTitleForMatching(candPartTitle)
	normCandPartArt := NormalizeTrackTitleForMatching(candPartArtist)
	normCandUploader := NormalizeTrackTitleForMatching(candidateUploader)

	// 1. Вычисление titleScore
	if normCandTitle != "" {
		titleScore = LevenshteinRatio(normCandTitle, normTargetTitle)
	} else {
		titleScore = LevenshteinRatio(normCandFull, normTargetTitle)
	}
	if fullRatio := LevenshteinRatio(normCandFull, normTargetTitle); fullRatio > titleScore {
		titleScore = fullRatio
	}

	// Проверка вхождения подстроки для коротких и длинных названий
	if len(normTargetTitle) >= 3 {
		if strings.Contains(normCandFull, normTargetTitle) ||
			(normCandTitle != "" && strings.Contains(normCandTitle, normTargetTitle)) {
			if titleScore < 0.90 {
				titleScore = 0.90
			}
		}
	}
	if len(normCandTitle) >= 3 && strings.Contains(normTargetTitle, normCandTitle) {
		if titleScore < 0.85 {
			titleScore = 0.85
		}
	}

	// 2. Вычисление artistScore
	if normTargetArtist != "" {
		if normCandUploader != "" {
			artistScore = LevenshteinRatio(normCandUploader, normTargetArtist)
			if strings.Contains(normCandUploader, normTargetArtist) || strings.Contains(normTargetArtist, normCandUploader) {
				if artistScore < 0.90 {
					artistScore = 0.90
				}
			}
		}
		if normCandPartArt != "" {
			r := LevenshteinRatio(normCandPartArt, normTargetArtist)
			if r > artistScore {
				artistScore = r
			}
			if strings.Contains(normCandPartArt, normTargetArtist) || strings.Contains(normTargetArtist, normCandPartArt) {
				if artistScore < 0.90 {
					artistScore = 0.90
				}
			}
		}
		if strings.Contains(normCandFull, normTargetArtist) {
			if artistScore < 0.85 {
				artistScore = 0.85
			}
		}

		// Проверка транслитерированных вариантов (например, "h8.hood", "апология")
		if artistScore < 0.60 {
			for _, variant := range TransliterateArtist(targetArtist) {
				normVar := NormalizeTrackTitleForMatching(variant)
				if normVar == "" || normVar == normTargetArtist {
					continue
				}
				if normCandUploader != "" {
					if r := LevenshteinRatio(normCandUploader, normVar); r > artistScore {
						artistScore = r
					}
					if strings.Contains(normCandUploader, normVar) {
						if artistScore < 0.85 {
							artistScore = 0.85
						}
					}
				}
				if normCandPartArt != "" {
					if r := LevenshteinRatio(normCandPartArt, normVar); r > artistScore {
						artistScore = r
					}
					if strings.Contains(normCandPartArt, normVar) {
						if artistScore < 0.85 {
							artistScore = 0.85
						}
					}
				}
				if strings.Contains(normCandFull, normVar) {
					if artistScore < 0.85 {
						artistScore = 0.85
					}
				}
			}
		}
	} else {
		// Если артист не задан, ориентируемся только на название
		artistScore = 1.0
	}

	// Критерий успешного совпадения:
	// - Хорошее совпадение названия (>= 0.65) И подтвержденный артист (>= 0.40)
	// - ЛИБО очень высокое совпадение названия (>= 0.88)
	match = (titleScore >= 0.65 && artistScore >= 0.40) || (titleScore >= 0.88)
	return match, titleScore, artistScore
}

