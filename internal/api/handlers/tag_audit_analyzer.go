package handlers

import (
	"regexp"
	"strings"

	"music-manager/internal/id3"
	"music-manager/internal/services/resolver"
)

var (
	// badRegexAudit используется для детекции цензурированных версий в аудите тегов
	// Упрощённая версия без Anti-Collision, так как у нас нет эталонного названия для сравнения
	badRegexAudit = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(clean|radio\s+edit|censored|censored version|radio version|tv edit|с\s+цензурой|цензурная)(?:$|[^\p{L}\p{N}])`)
)

// analyzeTrackIssues анализирует метаданные трека и возвращает обнаруженные дефекты.
func analyzeTrackIssues(id int64, filePath, title, artist, album string, year, trackNumber int, duration float64) *TagIssueItem {
	var issues []string
	cleanTitle := resolver.CleanYouTubeVideoTitle(title)

	// 1. Проверка на префикс номера трека в названии ("01. Song" -> "Song")
	hasTrackPrefix := id3.CleanTitle(title, "") != title
	if hasTrackPrefix {
		cleanTitle = id3.CleanTitle(cleanTitle, "")
		issues = append(issues, "track_num_in_title")
	}

	// 2. Проверка на клиповый мусор и артефакты в названии
	isDirtyTitle := cleanTitle != title && !hasTrackPrefix
	if cleanTitle != title && hasTrackPrefix {
		if cleanTitle != id3.CleanTitle(title, "") {
			isDirtyTitle = true
		}
	}
	if isDirtyTitle {
		issues = append(issues, "dirty_title")
	}

	// 3. Проверка на разделители Navidrome (литеральный '\\' в исполнителе)
	hasBackslashArtist := strings.Contains(artist, "\\")
	if hasBackslashArtist {
		issues = append(issues, "literal_backslash_artist")
	}

	// 4. Проверка на служебный Topic-канал или лейбл вместо артиста
	lowerArtist := strings.ToLower(strings.TrimSpace(artist))
	hasTopicChannel := strings.HasSuffix(lowerArtist, "- topic") || strings.HasSuffix(lowerArtist, " - topic") || resolver.IsLabelChannel(lowerArtist)
	if hasTopicChannel {
		issues = append(issues, "label_as_artist")
	}

	// 5. Проверка на соавторов feat. в названии (только если они ОТСУТСТВУЮТ в исполнителях)
	_, titleFeats := resolver.ExtractFeatFromText(title)
	currentArtistList := id3.ParseMultiValue(artist)
	var missingTitleFeats []string
	for _, tf := range titleFeats {
		alreadyPresent := false
		for _, curArt := range currentArtistList {
			if resolver.AreArtistsEquivalent(curArt, tf) {
				alreadyPresent = true
				break
			}
		}
		if !alreadyPresent {
			missingTitleFeats = append(missingTitleFeats, tf)
		}
	}
	if len(missingTitleFeats) > 0 {
		issues = append(issues, "unparsed_feat")
	}

	// 6. Проверка на дублирование артиста в названии ("Artist - Track")
	// СТРОГАЯ: только если левая часть до дефиса/тире совпадает с артистом,
	// либо если артист в тегах — это служебный Topic-канал или лейбл (где видео называется "Исполнитель - Трек")
	isArtistInTitle := false
	proposedTitleFromSep := ""
	for _, sep := range []string{" - ", " – ", " — "} {
		if idx := strings.Index(title, sep); idx != -1 {
			leftPart := strings.TrimSpace(title[:idx])
			rightPart := strings.TrimSpace(title[idx+len(sep):])
			if leftPart != "" && rightPart != "" {
				if resolver.AreArtistsEquivalent(leftPart, artist) || hasTopicChannel || resolver.IsLabelChannel(artist) {
					isArtistInTitle = true
					proposedTitleFromSep = rightPart
					break
				}
			}
		}
	}
	if isArtistInTitle {
		issues = append(issues, "artist_in_title")
	}

	// 7. Пропущенный год
	if year <= 0 {
		issues = append(issues, "missing_year")
	}

	// 8. Неизвестный или пропущенный альбом (ВНИМАНИЕ: "single" / "singles" - это нормальный альбом для синглов, НЕ ошибка!)
	normAlbum := strings.ToLower(strings.TrimSpace(album))
	if normAlbum == "" || normAlbum == "unknown album" || normAlbum == "." {
		issues = append(issues, "missing_album")
	}

	// 9. Проверка на цензурированную версию (используем badRegexAudit)
	// Для аудита используем упрощённую проверку без Anti-Collision, так как у нас нет эталонного названия
	badMatches := badRegexAudit.FindAllStringSubmatchIndex(title, -1)
	if len(badMatches) > 0 {
		issues = append(issues, "censored_version")
	}

	if len(issues) == 0 {
		return nil
	}

	// Вычисляем предлагаемые исправления
	proposedTitle := title
	if isArtistInTitle && proposedTitleFromSep != "" {
		proposedTitle = proposedTitleFromSep
	}
	if hasTrackPrefix {
		proposedTitle = id3.CleanTitle(proposedTitle, "")
	}
	if isDirtyTitle {
		proposedTitle = resolver.CleanYouTubeVideoTitle(proposedTitle)
	}
	// Если соавторы были извлечены из названия, удаляем feat-блок из предлагаемого названия
	if len(missingTitleFeats) > 0 {
		cleanFeatTitle, _ := resolver.ExtractFeatFromText(proposedTitle)
		if cleanFeatTitle != "" {
			proposedTitle = cleanFeatTitle
		}
	}
	proposedTitle = strings.TrimSpace(proposedTitle)

	// Артисты
	proposedArtists := id3.ParseMultiValue(artist)
	if len(proposedArtists) == 0 && artist != "" {
		proposedArtists = []string{artist}
	}
	// Очищаем Topic суффиксы
	for i, a := range proposedArtists {
		aClean := strings.TrimSpace(a)
		aClean = strings.TrimSuffix(aClean, "- Topic")
		aClean = strings.TrimSuffix(aClean, " - Topic")
		aClean = strings.TrimSuffix(aClean, "-Topic")
		proposedArtists[i] = strings.TrimSpace(aClean)
	}
	// Добавляем недостающие соавторы
	for _, mf := range missingTitleFeats {
		proposedArtists = append(proposedArtists, mf)
	}
	proposedArtist := id3.FormatMultiValue(proposedArtists)
	if proposedArtist == "" {
		proposedArtist = artist
	}

	proposedAlbum := album
	if normAlbum == "" || normAlbum == "unknown album" || normAlbum == "." {
		proposedAlbum = proposedTitle
	}

	// Классификация серьезности (Severity)
	hasReviewNeeded := false
	hasSafeIssues := false

	for _, iss := range issues {
		switch iss {
		case "literal_backslash_artist", "track_num_in_title", "dirty_title", "label_as_artist":
			hasSafeIssues = true
		case "unparsed_feat", "artist_in_title", "missing_album", "censored_version":
			hasReviewNeeded = true
		case "missing_year":
			// мягкое предупреждение
		}
	}

	severity := "info"
	if hasReviewNeeded {
		severity = "review_needed"
	} else if hasSafeIssues {
		severity = "safe_autofix"
	}

	return &TagIssueItem{
		ID:          id,
		FilePath:    filePath,
		Title:       title,
		Artist:      artist,
		Album:       album,
		Year:        year,
		TrackNumber: trackNumber,
		Duration:    duration,
		Issues:      issues,
		Severity:    severity,
		Proposed: ProposedTagChange{
			Title:       proposedTitle,
			Artist:      proposedArtist,
			Artists:     proposedArtists,
			Album:       proposedAlbum,
			AlbumArtist: resolver.ExtractPrimaryArtist(proposedArtist),
			Year:        year,
		},
	}
}
