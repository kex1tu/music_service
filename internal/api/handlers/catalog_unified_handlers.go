package handlers

import (
	"context"
	"encoding/json"
	"net/http"

	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"music-manager/internal/core/safefs"
	"music-manager/internal/services/resolver"
)

// SearchCatalog выполняет параллельный поиск релизов по трем базам (Deezer, MusicBrainz, Shazam).
func (h *TagHandler) SearchCatalog(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("query"))
	audioPath := strings.TrimSpace(r.URL.Query().Get("audio_path"))

	if q == "" && audioPath == "" {
		http.Error(w, `{"error":"missing query or audio_path parameter"}`, http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	var (
		wg sync.WaitGroup

		deezerRes resolver.CatalogSourceResult
		mbRes     resolver.CatalogSourceResult
		shazamRes resolver.CatalogSourceResult
	)

	// 1. Deezer API (поиск альбомов)
	if h.deezer != nil && q != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			albums, err := h.deezer.SearchAlbums(ctx, q)
			if err != nil {
				deezerRes.Error = err.Error()
				return
			}
			items := make([]resolver.CatalogReleaseSummary, 0, len(albums))
			for i, a := range albums {
				cover := a.BestCoverURL()
				year := a.ReleaseYear()
				score := 100 - (i * 3)
				if score < 10 {
					score = 10
				}
				items = append(items, resolver.CatalogReleaseSummary{
					ID:         strconv.FormatInt(a.ID, 10),
					Source:     "deezer",
					Title:      a.Title,
					Artist:     a.Artist.Name,
					CoverURL:   cover,
					Year:       year,
					Date:       strconv.Itoa(year),
					TrackCount: a.NbTracks,
					Score:      score,
				})
			}
			deezerRes.Count = len(items)
			deezerRes.Items = items
		}()
	}

	// 2. MusicBrainz API v2 (поиск релизов)
	if h.musicbrainz != nil && q != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			releases, err := h.musicbrainz.SearchReleases(ctx, q)
			if err != nil {
				mbRes.Error = err.Error()
				return
			}
			mbRes.Count = len(releases)
			mbRes.Items = releases
		}()
	}

	// 3. Shazam (акустическое распознавание первого аудиофайла + каталог Apple/Shazam)
	if h.shazam != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var items []resolver.CatalogReleaseSummary

			// Режим A: Акустический фингерпринтинг, если передан audio_path
			if audioPath != "" {
				fullPath, err := h.resolvePath(audioPath)
				if err == nil {
					if info, statErr := safefs.Stat(fullPath); statErr == nil && !info.IsDir() {
						rec, recErr := h.shazam.RecognizeAudioFile(ctx, fullPath)
						if recErr == nil && rec != nil {
							items = append(items, resolver.CatalogReleaseSummary{
								ID:         rec.ID,
								Source:     "shazam",
								Title:      rec.Title,
								Artist:     rec.Artist,
								CoverURL:   rec.CoverURL,
								Year:       rec.Year,
								Date:       rec.Date,
								TrackCount: len(rec.Tracks),
								Label:      rec.Label,
								Score:      100, // 100% акустическое совпадение
							})
						}
					}
				}
			}

			// Режим B: Текстовый поиск по каталогу Shazam
			if q != "" {
				matches, err := h.shazam.Search(ctx, q)
				if err != nil && len(items) == 0 {
					shazamRes.Error = err.Error()
				} else {
					items = append(items, matches...)
				}
			}

			shazamRes.Count = len(items)
			shazamRes.Items = items
		}()
	}

	wg.Wait()

	resp := resolver.CatalogSearchResponse{
		Query:       q,
		Deezer:      deezerRes,
		MusicBrainz: mbRes,
		Shazam:      shazamRes,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// GetCatalogDeezerAlbum возвращает детальный альбом Deezer с параллельным 8-поточным опросом /track/{id} для всех соавторов.
func (h *TagHandler) GetCatalogDeezerAlbum(w http.ResponseWriter, r *http.Request) {
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

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	album, err := h.deezer.GetAlbum(ctx, id)
	if err != nil {
		http.Error(w, `{"error":"deezer album fetch failed"}`, http.StatusInternalServerError)
		return
	}

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
		sem := make(chan struct{}, 8) // максимум 8 параллельных воркеров

		for i, t := range album.Tracks.Data {
			if t.ID <= 0 {
				continue
			}
			wg.Add(1)
			go func(idx int, trackID int64) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				trCtx, trCancel := context.WithTimeout(ctx, 4*time.Second)
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

	var tracks []resolver.CatalogTrackDetail
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
			if len(artists) > 0 {
				tArtist = strings.Join(artists, "\\\\")
			} else if tArtist != "" {
				artists = []string{tArtist}
			}

			discNum := 1
			isrc := ""
			if i < len(details) {
				if details[i].diskNumber > 0 {
					discNum = details[i].diskNumber
				}
				isrc = details[i].isrc
			}

			tracks = append(tracks, resolver.CatalogTrackDetail{
				Position:     pos,
				TrackNumber:  pos,
				DiscNumber:   discNum,
				Title:        t.Title,
				Artist:       tArtist,
				Artists:      artists,
				Duration:     t.Duration,
				DurationMs:   t.Duration * 1000,
				ISRC:         isrc,
				ReleaseTitle: album.Title,
			})
		}
	}

	res := resolver.CatalogReleaseDetails{
		ID:         strconv.FormatInt(album.ID, 10),
		Source:     "deezer",
		Title:      album.Title,
		Artist:     album.Artist.Name,
		CoverURL:   album.BestCoverURL(),
		Year:       album.ReleaseYear(),
		Date:       album.ReleaseDate,
		Genre:      album.PrimaryGenre(),
		Genres:     []string{album.PrimaryGenre()},
		TrackCount: len(tracks),
		Tracks:     tracks,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

// GetCatalogMusicBrainzRelease возвращает детальный релиз MusicBrainz с треклистом.
func (h *TagHandler) GetCatalogMusicBrainzRelease(w http.ResponseWriter, r *http.Request) {
	if h.musicbrainz == nil {
		http.Error(w, `{"error":"musicbrainz client not initialized"}`, http.StatusInternalServerError)
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

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	details, err := h.musicbrainz.GetRelease(ctx, id)
	if err != nil {
		http.Error(w, `{"error":"musicbrainz release fetch failed"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(details)
}

// GetCatalogShazamTrack возвращает детальный трек Shazam (по акустическому отпечатку или ID).
func (h *TagHandler) GetCatalogShazamTrack(w http.ResponseWriter, r *http.Request) {
	if h.shazam == nil {
		http.Error(w, `{"error":"shazam provider not initialized"}`, http.StatusInternalServerError)
		return
	}

	audioPath := strings.TrimSpace(r.URL.Query().Get("audio_path"))
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	if audioPath != "" {
		fullPath, err := h.resolvePath(audioPath)
		if err != nil {
			http.Error(w, `{"error":"invalid audio path"}`, http.StatusBadRequest)
			return
		}
		if info, statErr := safefs.Stat(fullPath); statErr != nil || info.IsDir() {
			http.Error(w, `{"error":"audio file not found"}`, http.StatusNotFound)
			return
		}

		rel, err := h.shazam.RecognizeAudioFile(ctx, fullPath)
		if err != nil {
			http.Error(w, `{"error":"acoustic recognition failed"}`, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rel)
		return
	}

	id := chi.URLParam(r, "id")
	if id == "" {
		id = r.URL.Query().Get("id")
	}

	// Поиск по названию трека из запроса
	q := r.URL.Query().Get("query")
	if q == "" {
		q = id
	}
	if q == "" {
		http.Error(w, `{"error":"missing id or query parameter"}`, http.StatusBadRequest)
		return
	}

	matches, err := h.shazam.Search(ctx, q)
	if err != nil || len(matches) == 0 {
		http.Error(w, `{"error":"shazam track not found"}`, http.StatusNotFound)
		return
	}

	first := matches[0]
	rel := resolver.CatalogReleaseDetails{
		ID:         first.ID,
		Source:     "shazam",
		Title:      first.Title,
		Artist:     first.Artist,
		CoverURL:   first.CoverURL,
		Year:       first.Year,
		Date:       first.Date,
		TrackCount: 1,
		Tracks: []resolver.CatalogTrackDetail{
			{
				Position:     1,
				TrackNumber:  1,
				DiscNumber:   1,
				Title:        first.Title,
				Artist:       first.Artist,
				Artists:      []string{first.Artist},
				ReleaseTitle: first.Title,
			},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rel)
}
