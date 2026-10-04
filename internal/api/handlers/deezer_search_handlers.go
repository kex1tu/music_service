package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"music-manager/internal/id3"
	"music-manager/internal/services/resolver"
)

func (h *TagHandler) SearchDeezerAlbums(w http.ResponseWriter, r *http.Request) {
	if h.deezer == nil {
		http.Error(w, `{"error":"deezer client not initialized"}`, http.StatusInternalServerError)
		return
	}
	q := r.URL.Query().Get("query")
	if q == "" {
		http.Error(w, `{"error":"missing query parameter"}`, http.StatusBadRequest)
		return
	}

	albums, err := h.deezer.SearchAlbums(r.Context(), q)
	if err != nil {
		http.Error(w, `{"error":"deezer search failed"}`, http.StatusInternalServerError)
		return
	}

	type searchItem struct {
		ID       string  `json:"id"`
		Title    string  `json:"title"`
		Artist   string  `json:"artist"`
		CoverURL *string `json:"cover_url"`
		NbTracks int     `json:"nb_tracks"`
		Year     *int    `json:"year"`
	}

	items := make([]searchItem, len(albums))
	for i, a := range albums {
		cover := a.BestCoverURL()
		year := a.ReleaseYear()
		var pCover *string
		if cover != "" {
			pCover = &cover
		}
		var pYear *int
		if year > 0 {
			pYear = &year
		}

		items[i] = searchItem{
			ID:       strconv.FormatInt(a.ID, 10),
			Title:    a.Title,
			Artist:   a.Artist.Name,
			CoverURL: pCover,
			NbTracks: a.NbTracks,
			Year:     pYear,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(items)
}

func (h *TagHandler) GetDeezerAlbumDetails(w http.ResponseWriter, r *http.Request) {
	if h.deezer == nil {
		http.Error(w, `{"error":"deezer client not initialized"}`, http.StatusInternalServerError)
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		id = r.URL.Query().Get("id")
	}
	if id == "" {
		http.Error(w, `{"error":"missing id parameter"}`, http.StatusBadRequest)
		return
	}

	album, err := h.deezer.GetAlbum(r.Context(), id)
	if err != nil {
		http.Error(w, `{"error":"deezer request failed"}`, http.StatusInternalServerError)
		return
	}

	cover := album.BestCoverURL()
	year := album.ReleaseYear()
	genre := album.PrimaryGenre()

	type trackDetail struct {
		contributors []resolver.DeezerArtist
		isrc         string
		diskNumber   int
	}

	var details []trackDetail
	if album.Tracks != nil {
		total := len(album.Tracks.Data)
		details = make([]trackDetail, total)

		var wg sync.WaitGroup
		sem := make(chan struct{}, 8) // максимум 8 параллельных потоков

		for i, t := range album.Tracks.Data {
			if t.ID <= 0 {
				continue
			}
			wg.Add(1)
			go func(idx int, trackID int64) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				trCtx, trCancel := context.WithTimeout(r.Context(), 4*time.Second)
				defer trCancel()

				tr, trErr := h.deezer.GetTrack(trCtx, strconv.FormatInt(trackID, 10))
				if trErr == nil && tr != nil {
					details[idx] = trackDetail{
						contributors: tr.Contributors,
						isrc:         tr.ISRC,
						diskNumber:   tr.DiskNumber,
					}
				}
			}(i, t.ID)
		}
		wg.Wait()
	}

	type trackItem struct {
		TrackNumber  int      `json:"track_number"`
		DiscNumber   int      `json:"disc_number"`
		Title        string   `json:"title"`
		Artist       string   `json:"artist"`
		Duration     int      `json:"duration"`
		ISRC         string   `json:"isrc,omitempty"`
		Contributors []string `json:"contributors,omitempty"`
	}

	var tracks []trackItem
	if album.Tracks != nil {
		for i, t := range album.Tracks.Data {
			pos := t.TrackPosition
			if pos == 0 {
				pos = i + 1
			}
			tArtist := t.Artist.Name
			if tArtist == "" {
				tArtist = album.Artist.Name
			}

			// Сначала проверяем соавторов из детального запроса трека Deezer
			var artists []string
			if i < len(details) && len(details[i].contributors) > 0 {
				for _, c := range details[i].contributors {
					if c.Name != "" {
						artists = append(artists, c.Name)
					}
				}
			}
			if len(artists) == 0 && len(t.Contributors) > 0 {
				for _, c := range t.Contributors {
					if c.Name != "" {
						artists = append(artists, c.Name)
					}
				}
			}
			if len(artists) == 0 && tArtist != "" {
				artists = []string{tArtist}
			}
			if h.enricher != nil {
				artists = h.enricher.EnrichTrackArtists(r.Context(), t.Title, artists)
			}
			formattedArtist := id3.FormatMultiValue(artists)

			discNum := 1
			if i < len(details) && details[i].diskNumber > 0 {
				discNum = details[i].diskNumber
			} else if t.DiskNumber > 0 {
				discNum = t.DiskNumber
			}

			isrc := ""
			if i < len(details) && details[i].isrc != "" {
				isrc = details[i].isrc
			} else {
				isrc = t.ISRC
			}

			tracks = append(tracks, trackItem{
				TrackNumber:  pos,
				DiscNumber:   discNum,
				Title:        id3.CleanTitle(t.Title, ""),
				Artist:       formattedArtist,
				Duration:     t.Duration,
				ISRC:         isrc,
				Contributors: artists,
			})
		}
	}

	resp := map[string]any{
		"id":        id,
		"title":     album.Title,
		"artist":    album.Artist.Name,
		"year":      year,
		"genre":     genre,
		"cover_url": cover,
		"tracks":    tracks,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *TagHandler) SearchDeezerTracks(w http.ResponseWriter, r *http.Request) {
	if h.deezer == nil {
		http.Error(w, `{"error":"deezer client not initialized"}`, http.StatusInternalServerError)
		return
	}
	q := r.URL.Query().Get("query")
	if q == "" {
		http.Error(w, `{"error":"missing query parameter"}`, http.StatusBadRequest)
		return
	}

	tracks, err := h.deezer.SearchTracks(r.Context(), q)
	if err != nil {
		http.Error(w, `{"error":"deezer track search failed"}`, http.StatusInternalServerError)
		return
	}

	type trackSearchItem struct {
		ID          string  `json:"id"`
		Title       string  `json:"title"`
		Artist      string  `json:"artist"`
		Album       string  `json:"album"`
		AlbumArtist string  `json:"album_artist"`
		CoverURL    *string `json:"cover_url"`
		Duration    int     `json:"duration"`
		Year        *int    `json:"year"`
	}

	items := make([]trackSearchItem, len(tracks))
	for i, t := range tracks {
		var artists []string
		if len(t.Contributors) > 0 {
			for _, c := range t.Contributors {
				if c.Name != "" {
					artists = append(artists, c.Name)
				}
			}
		}
		if len(artists) == 0 && t.Artist.Name != "" {
			artists = []string{t.Artist.Name}
		}
		if h.enricher != nil {
			artists = h.enricher.EnrichTrackArtists(r.Context(), t.Title, artists)
		}
		formattedArtist := id3.FormatMultiValue(artists)

		var pCover *string
		var pYear *int
		albumTitle := ""
		albumArtist := t.Artist.Name

		if t.Album != nil {
			albumTitle = t.Album.Title
			cover := t.Album.BestCoverURL()
			if cover != "" {
				pCover = &cover
			}
			y := t.Album.ReleaseYear()
			if y > 0 {
				pYear = &y
			}
			if t.Album.Artist.Name != "" {
				albumArtist = t.Album.Artist.Name
			}
		}

		items[i] = trackSearchItem{
			ID:          strconv.FormatInt(t.ID, 10),
			Title:       id3.CleanTitle(t.Title, ""),
			Artist:      formattedArtist,
			Album:       albumTitle,
			AlbumArtist: albumArtist,
			CoverURL:    pCover,
			Duration:    t.Duration,
			Year:        pYear,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(items)
}

func (h *TagHandler) GetDeezerTrackDetails(w http.ResponseWriter, r *http.Request) {
	if h.deezer == nil {
		http.Error(w, `{"error":"deezer client not initialized"}`, http.StatusInternalServerError)
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		id = r.URL.Query().Get("id")
	}
	if id == "" {
		http.Error(w, `{"error":"missing id parameter"}`, http.StatusBadRequest)
		return
	}

	track, err := h.deezer.GetTrack(r.Context(), id)
	if err != nil {
		http.Error(w, `{"error":"deezer request failed"}`, http.StatusInternalServerError)
		return
	}

	var artists []string
	if len(track.Contributors) > 0 {
		for _, c := range track.Contributors {
			if c.Name != "" {
				artists = append(artists, c.Name)
			}
		}
	}
	if len(artists) == 0 && track.Artist.Name != "" {
		artists = []string{track.Artist.Name}
	}
	if h.enricher != nil {
		artists = h.enricher.EnrichTrackArtists(r.Context(), track.Title, artists)
	}
	formattedArtist := id3.FormatMultiValue(artists)

	albumTitle := ""
	albumArtist := track.Artist.Name
	coverURL := ""
	year := 0
	genre := ""

	if track.Album != nil {
		albumTitle = track.Album.Title
		coverURL = track.Album.BestCoverURL()
		year = track.Album.ReleaseYear()
		genre = track.Album.PrimaryGenre()
		if track.Album.Artist.Name != "" {
			albumArtist = track.Album.Artist.Name
		}
	}

	pos := track.TrackPosition
	if pos <= 0 {
		pos = 1
	}
	disc := track.DiskNumber
	if disc <= 0 {
		disc = 1
	}

	resp := map[string]any{
		"id":           id,
		"title":        id3.CleanTitle(track.Title, ""),
		"artist":       formattedArtist,
		"contributors": artists,
		"album":        albumTitle,
		"album_artist": albumArtist,
		"year":         year,
		"genre":        genre,
		"cover_url":    coverURL,
		"track_number": pos,
		"disc_number":  disc,
		"duration":     track.Duration,
		"isrc":         track.ISRC,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
