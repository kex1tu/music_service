package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"music-manager/internal/id3"
	"music-manager/internal/services/resolver"
)

// TagIssueItem представляет трек с дефектами тегов и предлагаемым исправлением.
type TagIssueItem struct {
	ID          int64             `json:"id"`
	FilePath    string            `json:"file_path"`
	Title       string            `json:"title"`
	Artist      string            `json:"artist"`
	Album       string            `json:"album"`
	Year        int               `json:"year"`
	TrackNumber int               `json:"track_number"`
	Duration    float64           `json:"duration"`
	Issues      []string          `json:"issues"`
	Severity    string            `json:"severity"` // "safe_autofix", "review_needed", "info"
	Proposed    ProposedTagChange `json:"proposed"`
}

// ProposedTagChange содержит вычисленные корректные теги для автоисправления.
type ProposedTagChange struct {
	Title       string   `json:"title"`
	Artist      string   `json:"artist"`
	Artists     []string `json:"artists"`
	Album       string   `json:"album"`
	AlbumArtist string   `json:"album_artist,omitempty"`
	Year        int      `json:"year"`
}

// CatalogTrackMatch представляет результат поиска трека в онлайн-каталогах.
type CatalogTrackMatch struct {
	Source      string   `json:"source"` // "yandex", "youtube_music", "deezer", "itunes"
	Title       string   `json:"title"`
	Artist      string   `json:"artist"`
	Artists     []string `json:"artists,omitempty"`
	Album       string   `json:"album"`
	Year        int      `json:"year"`
	TrackNumber int      `json:"track_number"`
	Duration    int      `json:"duration,omitempty"`
	CoverURL    string   `json:"cover_url"`
	IsBestFeat  bool     `json:"is_best_feat,omitempty"`
	MatchScore  int      `json:"match_score"` // 0..100%
}

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
		case "unparsed_feat", "artist_in_title", "missing_album":
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

// TagIssues возвращает список треков медиатеки с дефектами тегов.
func (h *FileHandler) TagIssues(w http.ResponseWriter, r *http.Request) {
	filter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("filter")))

	rows, err := h.database.Conn().QueryContext(r.Context(), `
		SELECT t.id, t.file_path, t.title, a.name, al.title, COALESCE(al.year, 0), t.track_number, t.duration
		FROM tracks t
		JOIN artists a ON t.artist_id = a.id
		JOIN albums al ON t.album_id = al.id
		ORDER BY t.id DESC;
	`)
	if err != nil {
		http.Error(w, `{"error":"database query failed"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var issues []TagIssueItem
	totalCount := 0

	for rows.Next() {
		var id int64
		var filePath, title, artist, album string
		var year, trackNumber int
		var duration float64

		if err := rows.Scan(&id, &filePath, &title, &artist, &album, &year, &trackNumber, &duration); err != nil {
			continue
		}

		item := analyzeTrackIssues(id, filePath, title, artist, album, year, trackNumber, duration)
		if item == nil {
			continue
		}

		// Применяем фильтр
		if filter != "" && filter != "all" {
			matched := false
			if filter == "safe" || filter == "safe_autofix" {
				matched = (item.Severity == "safe_autofix")
			} else if filter == "review" || filter == "review_needed" {
				matched = (item.Severity == "review_needed")
			} else {
				for _, iss := range item.Issues {
					if iss == filter {
						matched = true
						break
					}
					if filter == "missing_tags" && (iss == "missing_year" || iss == "missing_album") {
						matched = true
						break
					}
				}
			}
			if !matched {
				continue
			}
		}

		totalCount++
		if len(issues) < 200 {
			issues = append(issues, *item)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"issues":      issues,
		"total_count": totalCount,
	})
}

// FixTagIssueRequest представляет тело запроса на исправление тегов трека.
type FixTagIssueRequest struct {
	Path   string `json:"path"`
	Title  string `json:"title"`
	Artist string `json:"artist"`
	Album  string `json:"album"`
	Year   int    `json:"year"`
}

// FixTagIssue исправляет теги одного трека и обновляет SQLite индекс.
func (h *FileHandler) FixTagIssue(w http.ResponseWriter, r *http.Request) {
	var req FixTagIssueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}

	if req.Path == "" {
		http.Error(w, `{"error":"missing path"}`, http.StatusBadRequest)
		return
	}

	fullPath, err := h.musicSandbox.Resolve(req.Path, false)
	if err != nil {
		http.Error(w, `{"error":"file not found in sandbox"}`, http.StatusNotFound)
		return
	}

	tags, err := id3.ReadTags(fullPath, req.Path)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"failed to read tags: %v"}`, err), http.StatusInternalServerError)
		return
	}

	if strings.TrimSpace(req.Title) != "" {
		tags.Title = strings.TrimSpace(req.Title)
	}
	if strings.TrimSpace(req.Artist) != "" {
		tags.Artist = strings.TrimSpace(req.Artist)
	}
	if strings.TrimSpace(req.Album) != "" {
		tags.Album = strings.TrimSpace(req.Album)
	}
	if req.Year > 0 {
		tags.Year = req.Year
	}

	// Сверка и дообогащение соавторов через FeatEnricher (Yandex + YouTube Music)
	if h.downloader != nil && h.downloader.Resolver() != nil && h.downloader.Resolver().Enricher() != nil {
		currentArts := id3.ParseMultiValue(tags.Artist)
		enriched := h.downloader.Resolver().Enricher().EnrichTrackArtists(r.Context(), tags.Title, currentArts)
		if len(enriched) > len(currentArts) {
			tags.Artist = id3.FormatMultiValue(enriched)
		}
	}

	if err := id3.WriteTags(fullPath, *tags, nil); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"failed to write tags: %v"}`, err), http.StatusInternalServerError)
		return
	}

	if h.scanner != nil {
		_ = h.scanner.IndexFile(fullPath, req.Path)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"path":    req.Path,
		"tags":    tags,
	})
}

// FixSafeTagIssues выполняет быстрое и детерминированное локальное исправление дефектов категории safe_autofix
// (клиповый мусор, номер трека в названии, топик-каналы, литеральные слеши Navidrome) без внешних сетевых вызовов.
func (h *FileHandler) FixSafeTagIssues(w http.ResponseWriter, r *http.Request) {
	rows, err := h.database.Conn().QueryContext(r.Context(), `
		SELECT t.id, t.file_path, t.title, a.name, al.title, COALESCE(al.year, 0), t.track_number, t.duration
		FROM tracks t
		JOIN artists a ON t.artist_id = a.id
		JOIN albums al ON t.album_id = al.id;
	`)
	if err != nil {
		http.Error(w, `{"error":"database query failed"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var toFix []TagIssueItem
	for rows.Next() {
		var id int64
		var filePath, title, artist, album string
		var year, trackNumber int
		var duration float64

		if err := rows.Scan(&id, &filePath, &title, &artist, &album, &year, &trackNumber, &duration); err != nil {
			continue
		}

		item := analyzeTrackIssues(id, filePath, title, artist, album, year, trackNumber, duration)
		if item == nil {
			continue
		}

		// Исправляем только треки с дефектами категории safe_autofix
		hasSafeIssue := false
		for _, iss := range item.Issues {
			if iss == "dirty_title" || iss == "track_num_in_title" || iss == "literal_backslash_artist" || iss == "label_as_artist" {
				hasSafeIssue = true
				break
			}
		}
		if hasSafeIssue {
			toFix = append(toFix, *item)
		}
	}

	fixedCount := 0
	var errs []string

	for _, item := range toFix {
		fullPath, err := h.musicSandbox.Resolve(item.FilePath, false)
		if err != nil {
			continue
		}

		tags, err := id3.ReadTags(fullPath, item.FilePath)
		if err != nil {
			continue
		}

		// Применяем строго безопасные исправления
		if item.Proposed.Title != "" {
			tags.Title = item.Proposed.Title
		}
		if item.Proposed.Artist != "" {
			tags.Artist = item.Proposed.Artist
		}
		if item.Proposed.AlbumArtist != "" && strings.TrimSpace(tags.AlbumArtist) == "" {
			tags.AlbumArtist = item.Proposed.AlbumArtist
		}

		if err := id3.WriteTags(fullPath, *tags, nil); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", item.FilePath, err))
			continue
		}

		if h.scanner != nil {
			_ = h.scanner.IndexFile(fullPath, item.FilePath)
		}
		fixedCount++
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":     true,
		"fixed_count": fixedCount,
		"total_found": len(toFix),
		"errors":      errs,
	})
}

// FixSelectedTagsRequest представляет запрос на пакетное исправление выбранных чекбоксами треков.
type FixSelectedTagsRequest struct {
	Items []FixTagIssueRequest `json:"items"`
}

// FixSelectedTagIssues применяет предложенные исправления только к выбранным пользователем трекам.
func (h *FileHandler) FixSelectedTagIssues(w http.ResponseWriter, r *http.Request) {
	var req FixSelectedTagsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}

	fixedCount := 0
	var errs []string

	for _, item := range req.Items {
		if item.Path == "" {
			continue
		}
		fullPath, err := h.musicSandbox.Resolve(item.Path, false)
		if err != nil {
			continue
		}

		tags, err := id3.ReadTags(fullPath, item.Path)
		if err != nil {
			continue
		}

		if strings.TrimSpace(item.Title) != "" {
			tags.Title = strings.TrimSpace(item.Title)
		}
		if strings.TrimSpace(item.Artist) != "" {
			tags.Artist = strings.TrimSpace(item.Artist)
			if strings.TrimSpace(tags.AlbumArtist) == "" {
				tags.AlbumArtist = resolver.ExtractPrimaryArtist(tags.Artist)
			}
		}
		if strings.TrimSpace(item.Album) != "" {
			tags.Album = strings.TrimSpace(item.Album)
		}
		if item.Year > 0 {
			tags.Year = item.Year
		}

		if err := id3.WriteTags(fullPath, *tags, nil); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", item.Path, err))
			continue
		}

		if h.scanner != nil {
			_ = h.scanner.IndexFile(fullPath, item.Path)
		}
		fixedCount++
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":     true,
		"fixed_count": fixedCount,
		"total_found": len(req.Items),
		"errors":      errs,
	})
}

// FixAllTagIssues выполняет пакетное исправление всех явных дефектов тегов в медиатеке.
func (h *FileHandler) FixAllTagIssues(w http.ResponseWriter, r *http.Request) {
	rows, err := h.database.Conn().QueryContext(r.Context(), `
		SELECT t.id, t.file_path, t.title, a.name, al.title, COALESCE(al.year, 0), t.track_number, t.duration
		FROM tracks t
		JOIN artists a ON t.artist_id = a.id
		JOIN albums al ON t.album_id = al.id;
	`)
	if err != nil {
		http.Error(w, `{"error":"database query failed"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var toFix []TagIssueItem
	for rows.Next() {
		var id int64
		var filePath, title, artist, album string
		var year, trackNumber int
		var duration float64

		if err := rows.Scan(&id, &filePath, &title, &artist, &album, &year, &trackNumber, &duration); err != nil {
			continue
		}

		item := analyzeTrackIssues(id, filePath, title, artist, album, year, trackNumber, duration)
		if item == nil {
			continue
		}

		// Исправляем дефекты, для которых вычислено безопасное или проверенное решение
		hasFixableIssue := false
		for _, iss := range item.Issues {
			if iss == "dirty_title" || iss == "track_num_in_title" || iss == "literal_backslash_artist" ||
				iss == "unparsed_feat" || iss == "artist_in_title" || iss == "label_as_artist" {
				hasFixableIssue = true
				break
			}
		}
		if hasFixableIssue {
			toFix = append(toFix, *item)
		}
	}

	fixedCount := 0
	var errs []string

	for _, item := range toFix {
		fullPath, err := h.musicSandbox.Resolve(item.FilePath, false)
		if err != nil {
			continue
		}

		tags, err := id3.ReadTags(fullPath, item.FilePath)
		if err != nil {
			continue
		}

		if item.Proposed.Title != "" {
			tags.Title = item.Proposed.Title
		}
		if item.Proposed.Artist != "" {
			tags.Artist = item.Proposed.Artist
		}
		if item.Proposed.AlbumArtist != "" && strings.TrimSpace(tags.AlbumArtist) == "" {
			tags.AlbumArtist = item.Proposed.AlbumArtist
		}
		if item.Proposed.Album != "" && (tags.Album == "" || strings.EqualFold(tags.Album, "unknown album")) {
			tags.Album = item.Proposed.Album
		}
		if item.Proposed.Year > 0 && tags.Year == 0 {
			tags.Year = item.Proposed.Year
		}

		if err := id3.WriteTags(fullPath, *tags, nil); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", item.FilePath, err))
			continue
		}

		if h.scanner != nil {
			_ = h.scanner.IndexFile(fullPath, item.FilePath)
		}
		fixedCount++
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":     true,
		"fixed_count": fixedCount,
		"total_found": len(toFix),
		"errors":      errs,
	})
}

// CatalogMatchTrack выполняет поиск трека по онлайн-каталогам (Yandex Music, Deezer, iTunes) для Diff-сверки.
func (h *FileHandler) CatalogMatchTrack(w http.ResponseWriter, r *http.Request) {
	trackPath := strings.TrimSpace(r.URL.Query().Get("path"))
	title := strings.TrimSpace(r.URL.Query().Get("title"))
	artist := strings.TrimSpace(r.URL.Query().Get("artist"))

	var currentItem struct {
		Path        string  `json:"path"`
		Title       string  `json:"title"`
		Artist      string  `json:"artist"`
		Album       string  `json:"album"`
		Year        int     `json:"year"`
		TrackNumber int     `json:"track_number"`
		Duration    float64 `json:"duration"`
	}

	if trackPath != "" {
		currentItem.Path = trackPath
		_ = h.database.Conn().QueryRowContext(r.Context(), `
			SELECT t.title, a.name, al.title, COALESCE(al.year, 0), t.track_number, t.duration
			FROM tracks t
			JOIN artists a ON t.artist_id = a.id
			JOIN albums al ON t.album_id = al.id
			WHERE t.file_path = ?;
		`, trackPath).Scan(&currentItem.Title, &currentItem.Artist, &currentItem.Album, &currentItem.Year, &currentItem.TrackNumber, &currentItem.Duration)

		if currentItem.Title == "" {
			if fullPath, err := h.musicSandbox.Resolve(trackPath, false); err == nil {
				if tags, err := id3.ReadTags(fullPath, trackPath); err == nil {
					currentItem.Title = tags.Title
					currentItem.Artist = tags.Artist
					currentItem.Album = tags.Album
					currentItem.Year = tags.Year
					currentItem.TrackNumber = tags.TrackNumber
					currentItem.Duration = tags.Duration
				}
			}
		}

		if title == "" {
			title = currentItem.Title
		}
		if artist == "" {
			artist = currentItem.Artist
		}
	}

	query := fmt.Sprintf("%s %s", resolver.ExtractPrimaryArtist(artist), resolver.CleanQueryText(title))
	query = strings.TrimSpace(query)
	if query == "" {
		http.Error(w, `{"error":"missing search query (path or artist/title required)"}`, http.StatusBadRequest)
		return
	}

	var matches []CatalogTrackMatch
	seen := make(map[string]bool)
	var mu sync.Mutex

	addMatch := func(m CatalogTrackMatch) {
		key := strings.ToLower(fmt.Sprintf("%s|%s|%s", m.Artist, m.Title, m.Album))
		if !seen[key] {
			seen[key] = true
			matches = append(matches, m)
		}
	}

	searchCtx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()

	var wg sync.WaitGroup

	// 1. Поиск в Яндекс Музыке
	wg.Add(1)
	go func() {
		defer wg.Done()
		yandexClient := resolver.NewYandexMusicClient("")
		if ymTracks, err := yandexClient.SearchTrack(searchCtx, query); err == nil {
			mu.Lock()
			for _, yt := range ymTracks {
				if len(matches) >= 30 {
					break
				}
				artists := yt.AllArtistNames()
				albumTitle := ""
				year := 0
				coverURL := ""
				trackNum := yt.TrackPosition
				if trackNum <= 0 {
					trackNum = 1
				}
				if len(yt.Albums) > 0 {
					albumTitle = yt.Albums[0].Title
					year = yt.Albums[0].Year
					coverURL = yt.Albums[0].BestCoverURL()
				}

				addMatch(CatalogTrackMatch{
					Source:      "yandex",
					Title:       yt.Title,
					Artist:      id3.FormatMultiValue(artists),
					Artists:     artists,
					Album:       albumTitle,
					Year:        year,
					TrackNumber: trackNum,
					Duration:    int(yt.DurationMs / 1000),
					CoverURL:    coverURL,
				})
			}
			mu.Unlock()
		}
	}()

	// 2. Поиск в YouTube Music
	wg.Add(1)
	go func() {
		defer wg.Done()
		ytmusicClient := resolver.NewYouTubeMusicClient("")
		if ytmTracks, err := ytmusicClient.Search(searchCtx, query); err == nil {
			mu.Lock()
			for _, yt := range ytmTracks {
				if len(matches) >= 30 {
					break
				}
				addMatch(CatalogTrackMatch{
					Source:      "youtube_music",
					Title:       yt.Title,
					Artist:      yt.Artist,
					Artists:     yt.Artists,
					Album:       yt.Album,
					Year:        yt.Year,
					TrackNumber: 1,
					Duration:    yt.Duration,
					CoverURL:    yt.CoverURL,
				})
			}
			mu.Unlock()
		}
	}()

	// 3. Поиск в Deezer
	if h.deezer != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if dzTracks, err := h.deezer.SearchTracks(searchCtx, query); err == nil {
				mu.Lock()
				for _, dt := range dzTracks {
					if len(matches) >= 30 {
						break
					}
					var artists []string
					if len(dt.Contributors) > 0 {
						for _, c := range dt.Contributors {
							if c.Name != "" {
								artists = append(artists, c.Name)
							}
						}
					}
					if len(artists) == 0 && dt.Artist.Name != "" {
						artists = []string{dt.Artist.Name}
					}
					albumTitle := ""
					year := 0
					coverURL := ""
					if dt.Album != nil {
						albumTitle = dt.Album.Title
						year = dt.Album.ReleaseYear()
						coverURL = dt.Album.BestCoverURL()
					}
					pos := dt.TrackPosition
					if pos <= 0 {
						pos = 1
					}

					addMatch(CatalogTrackMatch{
						Source:      "deezer",
						Title:       dt.Title,
						Artist:      id3.FormatMultiValue(artists),
						Artists:     artists,
						Album:       albumTitle,
						Year:        year,
						TrackNumber: pos,
						Duration:    dt.Duration,
						CoverURL:    coverURL,
					})
				}
				mu.Unlock()
			}
		}()
	}

	// 4. Поиск в iTunes Search API
	wg.Add(1)
	go func() {
		defer wg.Done()
		if itunesMatches, err := searchTrackItunes(searchCtx, query); err == nil {
			mu.Lock()
			for _, im := range itunesMatches {
				if len(matches) >= 30 {
					break
				}
				addMatch(im)
			}
			mu.Unlock()
		}
	}()

	wg.Wait()

	// Рассчитываем композитный MatchScore (0..100%) для каждого кандидата
	for i := range matches {
		matches[i].MatchScore = computeMatchScore(currentItem.Title, currentItem.Artist, currentItem.Duration, matches[i])
	}

	// Сортируем: строго по убыванию MatchScore
	sort.SliceStable(matches, func(i, j int) bool {
		return matches[i].MatchScore > matches[j].MatchScore
	})

	// Лучший результат помечаем флагом IsBestFeat
	if len(matches) > 0 && matches[0].MatchScore >= 65 {
		matches[0].IsBestFeat = true
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"current": currentItem,
		"matches": matches,
	})
}

// computeMatchScore вычисляет процент соответствия (0..100%) между текущим треком и кандидатом из каталога.
func computeMatchScore(targetTitle, targetArtist string, targetDuration float64, match CatalogTrackMatch) int {
	score := 0

	// 1. Сходство названий (0-45)
	tTitle := strings.ToLower(resolver.CleanQueryText(targetTitle))
	mTitle := strings.ToLower(resolver.CleanQueryText(match.Title))
	if tTitle != "" && mTitle != "" {
		if tTitle == mTitle {
			score += 45
		} else if strings.Contains(mTitle, tTitle) || strings.Contains(tTitle, mTitle) {
			score += 35
		} else {
			tWords := strings.Fields(tTitle)
			matchedWords := 0
			for _, w := range tWords {
				if len(w) > 2 && strings.Contains(mTitle, w) {
					matchedWords++
				}
			}
			if len(tWords) > 0 {
				ratio := float64(matchedWords) / float64(len(tWords))
				score += int(ratio * 30.0)
			}
		}
	}

	// 2. Сходство артистов (0-35)
	if resolver.AreArtistsEquivalent(targetArtist, match.Artist) {
		score += 35
	} else if strings.Contains(strings.ToLower(match.Artist), strings.ToLower(resolver.ExtractPrimaryArtist(targetArtist))) {
		score += 25
	} else {
		tArtWords := strings.Fields(strings.ToLower(resolver.CleanQueryText(targetArtist)))
		mArt := strings.ToLower(match.Artist)
		matchedArt := 0
		for _, w := range tArtWords {
			if len(w) > 2 && strings.Contains(mArt, w) {
				matchedArt++
			}
		}
		if len(tArtWords) > 0 {
			score += int((float64(matchedArt) / float64(len(tArtWords))) * 18.0)
		}
	}

	// 3. Совпадение длительности (0-20)
	if targetDuration > 0 && match.Duration > 0 {
		diff := math.Abs(targetDuration - float64(match.Duration))
		if diff <= 3 {
			score += 20
		} else if diff <= 8 {
			score += 15
		} else if diff <= 15 {
			score += 8
		} else if diff > 60 {
			score -= 15
		}
	} else {
		score += 12
	}

	if score > 100 {
		score = 100
	}
	if score < 0 {
		score = 0
	}
	return score
}

// searchTrackItunes ищет треки через публичный API iTunes.
func searchTrackItunes(ctx context.Context, query string) ([]CatalogTrackMatch, error) {
	client := &http.Client{Timeout: 8 * time.Second}
	u := fmt.Sprintf("https://itunes.apple.com/search?term=%s&entity=song&limit=10", url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("itunes search returned %d", resp.StatusCode)
	}

	var res struct {
		Results []struct {
			TrackName       string `json:"trackName"`
			ArtistName      string `json:"artistName"`
			CollectionName  string `json:"collectionName"`
			ReleaseDate     string `json:"releaseDate"`
			TrackNumber     int    `json:"trackNumber"`
			TrackTimeMillis int    `json:"trackTimeMillis"`
			ArtworkURL100   string `json:"artworkUrl100"`
		} `json:"results"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}

	var list []CatalogTrackMatch
	for _, r := range res.Results {
		year := 0
		if len(r.ReleaseDate) >= 4 {
			year, _ = strconv.Atoi(r.ReleaseDate[:4])
		}
		cover := r.ArtworkURL100
		if cover != "" {
			cover = strings.Replace(cover, "100x100bb.jpg", "1000x1000bb.jpg", 1)
		}
		num := r.TrackNumber
		if num <= 0 {
			num = 1
		}
		dur := 0
		if r.TrackTimeMillis > 0 {
			dur = r.TrackTimeMillis / 1000
		}
		cleanTit, feats := resolver.ExtractFeatFromText(r.TrackName)
		artists := []string{r.ArtistName}
		artists = append(artists, feats...)
		list = append(list, CatalogTrackMatch{
			Source:      "itunes",
			Title:       cleanTit,
			Artist:      id3.FormatMultiValue(artists),
			Artists:     artists,
			Album:       r.CollectionName,
			Year:        year,
			TrackNumber: num,
			Duration:    dur,
			CoverURL:    cover,
		})
	}
	return list, nil
}

// ApplyCatalogTagsRequest представляет запрос на применение каталожных тегов к файлу.
type ApplyCatalogTagsRequest struct {
	Path        string `json:"path"`
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	Album       string `json:"album"`
	Year        int    `json:"year"`
	TrackNumber int    `json:"track_number"`
	CoverURL    string `json:"cover_url"`
}

// ApplyCatalogTags применяет выбранные из каталога теги и опционально обложку к MP3 файлу.
func (h *FileHandler) ApplyCatalogTags(w http.ResponseWriter, r *http.Request) {
	var req ApplyCatalogTagsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}

	if req.Path == "" {
		http.Error(w, `{"error":"missing path"}`, http.StatusBadRequest)
		return
	}

	fullPath, err := h.musicSandbox.Resolve(req.Path, false)
	if err != nil {
		http.Error(w, `{"error":"file not found"}`, http.StatusNotFound)
		return
	}

	tags, err := id3.ReadTags(fullPath, req.Path)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"failed to read tags: %v"}`, err), http.StatusInternalServerError)
		return
	}

	if req.Title != "" {
		tags.Title = req.Title
	}
	if req.Artist != "" {
		tags.Artist = req.Artist
	}
	if req.Album != "" {
		tags.Album = req.Album
	}
	if req.Year > 0 {
		tags.Year = req.Year
	}
	if req.TrackNumber > 0 {
		tags.TrackNumber = req.TrackNumber
	}

	// Синхронизируем AlbumArtist
	if strings.TrimSpace(tags.AlbumArtist) == "" || resolver.AreArtistsEquivalent(tags.AlbumArtist, tags.Artist) {
		primaryArt := resolver.ExtractPrimaryArtist(tags.Artist)
		if primaryArt != "" {
			tags.AlbumArtist = primaryArt
		}
	}

	var coverInfo *id3.CoverInfo
	if req.CoverURL != "" {
		// Скачиваем обложку для вшивания
		client := &http.Client{Timeout: 10 * time.Second}
		if cResp, cErr := client.Get(req.CoverURL); cErr == nil && cResp.StatusCode == http.StatusOK {
			b, _ := io.ReadAll(cResp.Body)
			cResp.Body.Close()
			if len(b) > 0 {
				coverInfo = &id3.CoverInfo{
					MimeType: "image/jpeg",
					Data:     b,
				}
			}
		}
	}

	if err := id3.WriteTags(fullPath, *tags, coverInfo); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"failed to write tags: %v"}`, err), http.StatusInternalServerError)
		return
	}

	// Сохраняем cover.jpg в папку альбома, если обложка была скачана
	if coverInfo != nil && len(coverInfo.Data) > 0 {
		albumDir := filepath.Dir(fullPath)
		coverFile := filepath.Join(albumDir, "cover.jpg")
		if _, statErr := os.Stat(coverFile); os.IsNotExist(statErr) {
			_ = os.WriteFile(coverFile, coverInfo.Data, 0644)
		}
	}

	if h.scanner != nil {
		_ = h.scanner.IndexFile(fullPath, req.Path)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"path":    req.Path,
		"tags":    tags,
	})
}
