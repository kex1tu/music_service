package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"music-manager/internal/id3"
	"music-manager/internal/services/resolver"
)

type AlbumCatalogTrackDiff struct {
	TrackNumber        int      `json:"track_number"`
	LocalTitle         string   `json:"local_title"`
	LocalArtist        string   `json:"local_artist"`
	LocalPath          string   `json:"local_path"`
	CatalogTitle       string   `json:"catalog_title"`
	CatalogArtist      string   `json:"catalog_artist"`
	CatalogArtists     []string `json:"catalog_artists"`
	CatalogDuration    int      `json:"catalog_duration"`
	CatalogTrackNumber int      `json:"catalog_track_number"`
	IsMatched          bool     `json:"is_matched"`
}

type AlbumCatalogMatchResponse struct {
	FolderPath      string                  `json:"folder_path"`
	AlbumTitle      string                  `json:"album_title"`
	AlbumArtist     string                  `json:"album_artist"`
	CatalogAlbum    string                  `json:"catalog_album"`
	CatalogArtist   string                  `json:"catalog_artist"`
	CatalogCoverURL string                  `json:"catalog_cover_url"`
	CatalogYear     int                     `json:"catalog_year"`
	CatalogGenre    string                  `json:"catalog_genre"`
	Source          string                  `json:"source"`
	Tracks          []AlbumCatalogTrackDiff `json:"tracks"`
}

type ApplyCatalogAlbumRequest struct {
	FolderPath  string                     `json:"folder_path"`
	Album       string                     `json:"album"`
	AlbumArtist string                     `json:"album_artist"`
	Year        int                        `json:"year,omitempty"`
	Genre       string                     `json:"genre,omitempty"`
	CoverURL    string                     `json:"cover_url,omitempty"`
	Tracks      []ApplyCatalogAlbumTrackItem `json:"tracks"`
}

type ApplyCatalogAlbumTrackItem struct {
	Path        string `json:"path"`
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	TrackNumber int    `json:"track_number"`
}

// CatalogMatchAlbum сопоставляет весь локальный альбом с официальным каталогом Deezer/Apple Music.
func (h *FileHandler) CatalogMatchAlbum(w http.ResponseWriter, r *http.Request) {
	folderPath := strings.TrimSpace(r.URL.Query().Get("folder_path"))
	if folderPath == "" {
		http.Error(w, `{"error":"missing folder_path parameter"}`, http.StatusBadRequest)
		return
	}

	fullDir, err := h.musicSandbox.Resolve(folderPath, false)
	if err != nil {
		http.Error(w, `{"error":"folder not found in library"}`, http.StatusNotFound)
		return
	}

	// 1. Получаем локальные треки папки
	type localTrack struct {
		relPath     string
		fullPath    string
		title       string
		artist      string
		trackNumber int
	}

	entries, err := os.ReadDir(fullDir)
	if err != nil {
		http.Error(w, `{"error":"failed to read folder"}`, http.StatusInternalServerError)
		return
	}

	var localTracks []localTrack
	detectedAlbum := ""
	detectedArtist := ""

	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".mp3") {
			continue
		}
		mp3Path := filepath.Join(fullDir, e.Name())
		rel, _ := h.musicSandbox.ToRelative(mp3Path)
		tags, _ := id3.ReadTags(mp3Path, rel)

		tit := e.Name()
		art := ""
		num := 1
		if tags != nil {
			if tags.Title != "" {
				tit = tags.Title
			}
			art = tags.Artist
			if tags.TrackNumber > 0 {
				num = tags.TrackNumber
			}
			if detectedAlbum == "" && tags.Album != "" && !strings.EqualFold(tags.Album, "singles") && !strings.EqualFold(tags.Album, "single") {
				detectedAlbum = tags.Album
			}
			if detectedArtist == "" {
				if tags.AlbumArtist != "" {
					detectedArtist = tags.AlbumArtist
				} else if tags.Artist != "" {
					detectedArtist = resolver.ExtractPrimaryArtist(tags.Artist)
				}
			}
		}

		localTracks = append(localTracks, localTrack{
			relPath:     rel,
			fullPath:    mp3Path,
			title:       tit,
			artist:      art,
			trackNumber: num,
		})
	}

	sort.Slice(localTracks, func(i, j int) bool {
		return localTracks[i].trackNumber < localTracks[j].trackNumber
	})

	if len(localTracks) == 0 {
		http.Error(w, `{"error":"no mp3 tracks found in directory"}`, http.StatusNotFound)
		return
	}

	if detectedAlbum == "" {
		detectedAlbum = filepath.Base(fullDir)
		// Убираем префикс/суффикс артиста если есть ("Album - Artist" -> "Album")
		if idx := strings.Index(detectedAlbum, " - "); idx != -1 {
			detectedAlbum = strings.TrimSpace(detectedAlbum[:idx])
		}
	}

	// 2. Ищем альбом в Deezer
	searchQuery := fmt.Sprintf("%s %s", detectedArtist, detectedAlbum)
	var matchedDeezerAlbum *resolver.DeezerAlbum

	if h.deezer != nil {
		albums, sErr := h.deezer.SearchAlbums(r.Context(), searchQuery)
		if sErr == nil && len(albums) > 0 {
			// Берём первый/наилучший результат
			fullAlbum, gErr := h.deezer.GetAlbum(r.Context(), strconv.FormatInt(albums[0].ID, 10))
			if gErr == nil {
				matchedDeezerAlbum = fullAlbum
			}
		}
	}

	if matchedDeezerAlbum == nil || matchedDeezerAlbum.Tracks == nil || len(matchedDeezerAlbum.Tracks.Data) == 0 {
		http.Error(w, fmt.Sprintf(`{"error":"album not found in catalog for %q"}`, searchQuery), http.StatusNotFound)
		return
	}

	catalogTracks := matchedDeezerAlbum.Tracks.Data
	catalogCover := matchedDeezerAlbum.BestCoverURL()
	catalogGenre := matchedDeezerAlbum.PrimaryGenre()
	catalogYear := matchedDeezerAlbum.ReleaseYear()

	// 3. Сопоставляем локальные дорожки с каталожными
	var diffs []AlbumCatalogTrackDiff
	for i, lt := range localTracks {
		var matchedCatTrack *resolver.DeezerTrack

		// Попытка 1: сопоставление по номеру трека (1-based)
		for _, ct := range catalogTracks {
			cPos := ct.TrackPosition
			if cPos <= 0 {
				cPos = 1
			}
			if cPos == lt.trackNumber {
				cCopy := ct
				matchedCatTrack = &cCopy
				break
			}
		}

		// Попытка 2: если по номеру не совпало, ищем по индексу в массиве
		if matchedCatTrack == nil && i < len(catalogTracks) {
			cCopy := catalogTracks[i]
			matchedCatTrack = &cCopy
		}

		diffItem := AlbumCatalogTrackDiff{
			TrackNumber: lt.trackNumber,
			LocalTitle:  lt.title,
			LocalArtist: lt.artist,
			LocalPath:   lt.relPath,
		}

		if matchedCatTrack != nil {
			catArt := matchedCatTrack.Artist.Name
			var catArtists []string
			if len(matchedCatTrack.Contributors) > 0 {
				for _, c := range matchedCatTrack.Contributors {
					if c.Name != "" {
						catArtists = append(catArtists, c.Name)
					}
				}
			}
			if len(catArtists) == 0 && catArt != "" {
				catArtists = []string{catArt}
			}

			diffItem.CatalogTitle = matchedCatTrack.Title
			diffItem.CatalogArtist = id3.FormatMultiValue(catArtists)
			diffItem.CatalogArtists = catArtists
			diffItem.CatalogDuration = matchedCatTrack.Duration
			diffItem.CatalogTrackNumber = matchedCatTrack.TrackPosition
			diffItem.IsMatched = true
		}

		diffs = append(diffs, diffItem)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(AlbumCatalogMatchResponse{
		FolderPath:      folderPath,
		AlbumTitle:      detectedAlbum,
		AlbumArtist:     detectedArtist,
		CatalogAlbum:    matchedDeezerAlbum.Title,
		CatalogArtist:   matchedDeezerAlbum.Artist.Name,
		CatalogCoverURL: catalogCover,
		CatalogYear:     catalogYear,
		CatalogGenre:    catalogGenre,
		Source:          "deezer",
		Tracks:          diffs,
	})
}

// ApplyCatalogAlbum применяет согласованные каталожные метаданные ко всем трекам альбома сразу.
func (h *FileHandler) ApplyCatalogAlbum(w http.ResponseWriter, r *http.Request) {
	var req ApplyCatalogAlbumRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}

	if req.FolderPath == "" {
		http.Error(w, `{"error":"missing folder_path"}`, http.StatusBadRequest)
		return
	}

	fullDir, err := h.musicSandbox.Resolve(req.FolderPath, false)
	if err != nil {
		http.Error(w, `{"error":"folder not found"}`, http.StatusNotFound)
		return
	}

	// Загружаем обложку если есть
	var coverInfo *id3.CoverInfo
	if req.CoverURL != "" {
		client := h.httpClient
		if client == nil {
			client = http.DefaultClient
		}
		if data, mime, cErr := downloadImageWithMime(r.Context(), client, req.CoverURL); cErr == nil && len(data) > 0 {
			coverInfo = &id3.CoverInfo{
				MimeType: mime,
				Data:     data,
			}
			_ = os.WriteFile(filepath.Join(fullDir, "cover.jpg"), data, 0644)
			_ = os.Chmod(filepath.Join(fullDir, "cover.jpg"), 0644)
		}
	}

	tracksUpdated := 0
	for _, tr := range req.Tracks {
		fullPath, rErr := h.musicSandbox.Resolve(tr.Path, false)
		if rErr != nil {
			continue
		}

		tags, _ := id3.ReadTags(fullPath, tr.Path)
		if tags == nil {
			tags = &id3.TrackTags{Path: tr.Path}
		}

		if tr.Title != "" {
			tags.Title = tr.Title
		}
		if tr.Artist != "" {
			tags.Artist = tr.Artist
		}
		if req.Album != "" {
			tags.Album = req.Album
		}
		if req.AlbumArtist != "" {
			tags.AlbumArtist = req.AlbumArtist
		} else if tr.Artist != "" {
			tags.AlbumArtist = resolver.ExtractPrimaryArtist(tr.Artist)
		}
		if tr.TrackNumber > 0 {
			tags.TrackNumber = tr.TrackNumber
		}
		if req.Year > 0 {
			tags.Year = req.Year
		}
		if req.Genre != "" {
			tags.Genre = req.Genre
		}

		// Записываем ID3v2.4 с гарантией прав 0644 и null-байтами
		if wErr := id3.WriteTags(fullPath, *tags, coverInfo); wErr == nil {
			tracksUpdated++
			if h.scanner != nil {
				_ = h.scanner.IndexFile(fullPath, tr.Path)
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":        true,
		"tracks_updated": tracksUpdated,
		"folder_path":    req.FolderPath,
	})
}
