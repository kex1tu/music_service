package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"music-manager/internal/core/safefs"
	"music-manager/internal/core/sandbox"
	"music-manager/internal/id3"
	"music-manager/internal/services/resolver"
)

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

// CatalogMatchTrack выполняет поиск трека по онлайн-каталогам (Yandex Music, YouTube Music, Deezer, iTunes) для Diff-сверки.
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

	if currentItem.Title == "" {
		currentItem.Title = title
	}
	if currentItem.Artist == "" {
		currentItem.Artist = artist
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
	if h.yandex != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ymTracks, err := h.yandex.SearchTrack(searchCtx, query); err == nil {
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
	}

	// 2. Поиск в YouTube Music
	if h.ytmusic != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ytmTracks, err := h.ytmusic.Search(searchCtx, query); err == nil {
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
	}

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
	if h.itunes != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if itunesResults, err := h.itunes.SearchTracks(searchCtx, query); err == nil {
				mu.Lock()
				for _, im := range itunesResults {
					if len(matches) >= 30 {
						break
					}
					addMatch(CatalogTrackMatch{
						Source:      "itunes",
						Title:       im.Title,
						Artist:      id3.FormatMultiValue(im.Artists),
						Artists:     im.Artists,
						Album:       im.Album,
						Year:        im.Year,
						TrackNumber: im.TrackNumber,
						Duration:    im.Duration,
						CoverURL:    im.CoverURL,
					})
				}
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	// Рассчитываем композитный MatchScore (0..100%) для каждого кандидата
	for i := range matches {
		matches[i].MatchScore = resolver.ComputeCatalogMatchScore(
			currentItem.Title,
			currentItem.Artist,
			currentItem.Duration,
			resolver.CatalogCandidate{
				Title:    matches[i].Title,
				Artist:   matches[i].Artist,
				Duration: matches[i].Duration,
			},
		)
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
		if errors.Is(err, sandbox.ErrPathTraversal) {
			http.Error(w, `{"error":"access denied"}`, http.StatusForbidden)
		} else {
			http.Error(w, `{"error":"file not found"}`, http.StatusNotFound)
		}
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
		// Скачиваем обложку для вшивания через переиспользуемый HTTP клиент с лимитами
		client := h.httpClient
		if client == nil {
			client = &http.Client{Timeout: 15 * time.Second}
		}
		if b, mime, cErr := downloadImageWithMime(r.Context(), client, req.CoverURL); cErr == nil && len(b) > 0 {
			coverInfo = &id3.CoverInfo{
				MimeType: mime,
				Data:     b,
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
		if _, statErr := safefs.Stat(coverFile); os.IsNotExist(statErr) {
			_ = safefs.WriteFile(coverFile, coverInfo.Data, 0644)
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
