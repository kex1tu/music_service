package handlers

import (
	"encoding/json"
	"net/http"
	"path/filepath"
)

type sqlInt struct {
	Int64 int64
	Valid bool
}

func (s *sqlInt) Scan(value any) error {
	if value == nil {
		s.Int64, s.Valid = 0, false
		return nil
	}
	if v, ok := value.(int64); ok {
		s.Int64, s.Valid = v, true
		return nil
	}
	s.Int64, s.Valid = 0, false
	return nil
}

type sqlStr struct {
	String string
	Valid  bool
}

func (s *sqlStr) Scan(value any) error {
	if value == nil {
		s.String, s.Valid = "", false
		return nil
	}
	if v, ok := value.(string); ok {
		s.String, s.Valid = v, true
		return nil
	}
	if b, ok := value.([]byte); ok {
		s.String, s.Valid = string(b), true
		return nil
	}
	s.String, s.Valid = "", false
	return nil
}

func (h *FileHandler) AuditSummary(w http.ResponseWriter, r *http.Request) {
	var totalTracks, totalAlbums int
	_ = h.database.Conn().QueryRow("SELECT COUNT(*) FROM tracks").Scan(&totalTracks)
	_ = h.database.Conn().QueryRow("SELECT COUNT(*) FROM albums").Scan(&totalAlbums)

	var missingLyrics, missingCovers int
	_ = h.database.Conn().QueryRow("SELECT COUNT(*) FROM tracks WHERE lyrics_status = 'none'").Scan(&missingLyrics)
	_ = h.database.Conn().QueryRow("SELECT COUNT(*) FROM tracks WHERE has_embedded_cover = 0").Scan(&missingCovers)

	var incompleteAlbums int
	_ = h.database.Conn().QueryRow("SELECT COUNT(*) FROM albums WHERE is_complete = 0").Scan(&incompleteAlbums)

	invalidPaths, _ := h.database.GetInvalidPaths(r.Context())

	var tagIssuesCount int
	if tRows, tErr := h.database.Conn().QueryContext(r.Context(), `
		SELECT t.id, t.file_path, t.title, a.name, al.title, COALESCE(al.year, 0), t.track_number, t.duration
		FROM tracks t
		JOIN artists a ON t.artist_id = a.id
		JOIN albums al ON t.album_id = al.id;
	`); tErr == nil {
		for tRows.Next() {
			var id int64
			var filePath, title, artist, album string
			var year, trackNumber int
			var duration float64
			if err := tRows.Scan(&id, &filePath, &title, &artist, &album, &year, &trackNumber, &duration); err == nil {
				if analyzeTrackIssues(id, filePath, title, artist, album, year, trackNumber, duration) != nil {
					tagIssuesCount++
				}
			}
		}
		tRows.Close()
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"total_tracks":            totalTracks,
		"total_albums":            totalAlbums,
		"missing_lyrics_count":    missingLyrics,
		"missing_covers_count":    missingCovers,
		"duplicates_count":        0,
		"incomplete_albums_count": incompleteAlbums,
		"invalid_paths_count":     len(invalidPaths),
		"non_en_genres_count":     0,
		"tag_issues_count":        tagIssuesCount,
	})
}

func (h *FileHandler) RefreshAudit(w http.ResponseWriter, r *http.Request) {
	if h.scanner != nil {
		_, _ = h.scanner.ScanAll(r.Context())
	}
	h.AuditSummary(w, r)
}

func (h *FileHandler) InvalidPaths(w http.ResponseWriter, r *http.Request) {
	paths, err := h.database.GetInvalidPaths(r.Context())
	if err != nil {
		http.Error(w, `{"error":"failed to get invalid paths"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(paths)
}

func (h *FileHandler) MissingLyrics(w http.ResponseWriter, r *http.Request) {
	rows, err := h.database.Conn().Query(`
		SELECT t.file_path, t.title, a.name, al.title
		FROM tracks t
		JOIN artists a ON t.artist_id = a.id
		JOIN albums al ON t.album_id = al.id
		WHERE t.lyrics_status = 'none'
		LIMIT 100;
	`)
	if err != nil {
		http.Error(w, `{"error":"database query failed"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type missingLyricsItem struct {
		Path      string `json:"path"`
		Title     string `json:"title"`
		Artist    string `json:"artist"`
		Album     string `json:"album"`
		Directory string `json:"directory"`
	}

	var items []missingLyricsItem
	for rows.Next() {
		var it missingLyricsItem
		if err := rows.Scan(&it.Path, &it.Title, &it.Artist, &it.Album); err == nil {
			it.Directory = filepath.Dir(it.Path)
			items = append(items, it)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(items)
}

func (h *FileHandler) MissingCovers(w http.ResponseWriter, r *http.Request) {
	rows, err := h.database.Conn().Query(`
		SELECT al.folder_path, al.title, a.name, COUNT(t.id)
		FROM albums al
		JOIN artists a ON al.artist_id = a.id
		LEFT JOIN tracks t ON t.album_id = al.id
		WHERE al.has_cover = 0
		GROUP BY al.id
		LIMIT 100;
	`)
	if err != nil {
		http.Error(w, `{"error":"database query failed"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type missingCoverItem struct {
		Path       string `json:"path"`
		Title      string `json:"title"`
		Artist     string `json:"artist"`
		Album      string `json:"album"`
		IsAlbumDir bool   `json:"is_album_dir"`
		TrackCount int    `json:"track_count"`
	}

	var items []missingCoverItem
	for rows.Next() {
		var it missingCoverItem
		if err := rows.Scan(&it.Path, &it.Title, &it.Artist, &it.TrackCount); err == nil {
			it.Album = it.Title
			it.IsAlbumDir = true
			items = append(items, it)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(items)
}

func (h *FileHandler) IncompleteAlbums(w http.ResponseWriter, r *http.Request) {
	rows, err := h.database.Conn().QueryContext(r.Context(), `
		SELECT al.id, al.folder_path, al.title, a.name, al.year, al.has_cover, al.cover_path,
		       al.present_tracks_count, al.expected_tracks_count, al.total_tracks, al.deezer_id
		FROM albums al
		JOIN artists a ON al.artist_id = a.id
		WHERE al.present_tracks_count > 0
		  AND (al.is_complete = 0 OR (al.expected_tracks_count > 0 AND al.expected_tracks_count > al.present_tracks_count) OR (al.total_tracks > 1 AND al.present_tracks_count < al.total_tracks))
		ORDER BY al.title
		LIMIT 100;
	`)
	if err != nil {
		http.Error(w, `{"error":"database query failed"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type incompleteAlbumItem struct {
		Path                string  `json:"path"`
		Album               string  `json:"album"`
		Artist              string  `json:"artist"`
		Year                *int    `json:"year"`
		HasCover            bool    `json:"has_cover"`
		CoverURL            *string `json:"cover_url"`
		PresentCount        int     `json:"present_count"`
		ExpectedCount       int     `json:"expected_count"`
		MissingTrackNumbers []int   `json:"missing_track_numbers"`
		IsCatalogMatched    bool    `json:"is_catalog_matched"`
		DeezerAlbumID       *string `json:"deezer_album_id"`
	}

	type albumRow struct {
		id          int64
		item        incompleteAlbumItem
		totalTracks int
	}

	var albumRows []albumRow
	for rows.Next() {
		var a albumRow
		var year, expTracks, totTracks sqlInt
		var deezerID, coverPath sqlStr
		if err := rows.Scan(
			&a.id, &a.item.Path, &a.item.Album, &a.item.Artist, &year, &a.item.HasCover, &coverPath,
			&a.item.PresentCount, &expTracks, &totTracks, &deezerID,
		); err == nil {
			if year.Valid {
				y := int(year.Int64)
				a.item.Year = &y
			}
			exp := int(expTracks.Int64)
			if exp <= 0 && totTracks.Valid {
				exp = int(totTracks.Int64)
			}
			a.item.ExpectedCount = exp
			a.totalTracks = int(totTracks.Int64)
			a.item.MissingTrackNumbers = []int{}

			if deezerID.Valid && deezerID.String != "" {
				s := deezerID.String
				a.item.DeezerAlbumID = &s
				a.item.IsCatalogMatched = true
			}
			albumRows = append(albumRows, a)
		}
	}

	var items []incompleteAlbumItem
	for _, a := range albumRows {
		it := a.item
		// Query missing track numbers from missing_tracks table
		mRows, mErr := h.database.Conn().QueryContext(r.Context(), "SELECT track_number FROM release_tracklist WHERE is_present = 0 AND album_id = ? ORDER BY track_number", a.id)
		if mErr == nil {
			for mRows.Next() {
				var tn int
				if err := mRows.Scan(&tn); err == nil {
					it.MissingTrackNumbers = append(it.MissingTrackNumbers, tn)
				}
			}
			mRows.Close()
		}

		// Fallback: if missing_tracks table empty but expected > present, compute gaps
		if len(it.MissingTrackNumbers) == 0 && it.ExpectedCount > it.PresentCount {
			tRows, tErr := h.database.Conn().QueryContext(r.Context(), "SELECT track_number FROM tracks WHERE album_id = ?", a.id)
			if tErr == nil {
				presentNums := make(map[int]bool)
				for tRows.Next() {
					var tn int
					if err := tRows.Scan(&tn); err == nil {
						presentNums[tn] = true
					}
				}
				tRows.Close()

				for n := 1; n <= it.ExpectedCount; n++ {
					if !presentNums[n] {
						it.MissingTrackNumbers = append(it.MissingTrackNumbers, n)
					}
				}
			}
		}

		items = append(items, it)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"albums":                  items,
		"total_incomplete_albums": len(items),
		"total_missing_tracks":    0,
	})
}
