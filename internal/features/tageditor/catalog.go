package tageditor

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"music-manager/internal/core/safefs"
	"music-manager/internal/services/resolver"
)

// SearchCatalog выполняет параллельный опрос Deezer, MusicBrainz и Shazam с ограничением таймаута.
func (s *Service) SearchCatalog(ctx context.Context, query, audioPath string) (*CatalogSearchResponse, error) {
	q := strings.TrimSpace(query)
	audioPath = strings.TrimSpace(audioPath)
	if q == "" && audioPath == "" {
		return nil, errors.New("missing query or audio_path parameter")
	}

	searchCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()

	var (
		wg        sync.WaitGroup
		deezerRes CatalogSourceResult
		mbRes     CatalogSourceResult
		shazamRes CatalogSourceResult
	)

	// 1. Deezer
	if s.deezer != nil && q != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			albums, err := s.deezer.SearchAlbums(searchCtx, q)
			if err != nil {
				deezerRes.Error = err.Error()
				return
			}
			items := make([]CatalogReleaseSummary, 0, len(albums))
			for i, a := range albums {
				cover := a.BestCoverURL()
				year := a.ReleaseYear()
				score := 100 - (i * 3)
				if score < 10 {
					score = 10
				}
				items = append(items, CatalogReleaseSummary{
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

	// 2. MusicBrainz
	if s.musicbrainz != nil && q != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			releases, err := s.musicbrainz.SearchReleases(searchCtx, q)
			if err != nil {
				mbRes.Error = err.Error()
				return
			}
			mbRes.Count = len(releases)
			mbRes.Items = releases
		}()
	}

	// 3. Shazam
	if s.shazam != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var items []CatalogReleaseSummary
			if audioPath != "" {
				if fullPath, _, err := s.ResolvePath(audioPath); err == nil {
					if info, statErr := safefs.Stat(fullPath); statErr == nil && !info.IsDir() {
						rec, recErr := s.shazam.RecognizeAudioFile(searchCtx, fullPath)
						if recErr == nil && rec != nil {
							items = append(items, CatalogReleaseSummary{
								ID:         rec.ID,
								Source:     "shazam",
								Title:      rec.Title,
								Artist:     rec.Artist,
								CoverURL:   rec.CoverURL,
								Year:       rec.Year,
								Date:       rec.Date,
								TrackCount: len(rec.Tracks),
								Label:      rec.Label,
								Score:      100,
							})
						}
					}
				}
			}
			if q != "" {
				matches, err := s.shazam.Search(searchCtx, q)
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

	return &CatalogSearchResponse{
		Query:       q,
		Deezer:      deezerRes,
		MusicBrainz: mbRes,
		Shazam:      shazamRes,
	}, nil
}

// GetCatalogDeezerAlbum возвращает релиз Deezer в едином формате каталогов с 8-поточным опросом соавторов.
func (s *Service) GetCatalogDeezerAlbum(ctx context.Context, id string) (*CatalogReleaseDetails, error) {
	if s.deezer == nil {
		return nil, ErrClientNotInitialized
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("empty album id")
	}

	album, err := s.deezer.GetAlbum(ctx, id)
	if err != nil {
		return nil, err
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
		sem := make(chan struct{}, 8)

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

				tr, trErr := s.deezer.GetTrack(trCtx, strconv.FormatInt(trackID, 10))
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

	var tracks []CatalogTrackDetail
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

			tracks = append(tracks, CatalogTrackDetail{
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

	return &CatalogReleaseDetails{
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
	}, nil
}

// GetCatalogMusicBrainzRelease возвращает детальный релиз MusicBrainz с треклистом.
func (s *Service) GetCatalogMusicBrainzRelease(ctx context.Context, id string) (*CatalogReleaseDetails, error) {
	if s.musicbrainz == nil {
		return nil, ErrClientNotInitialized
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("empty release id")
	}
	return s.musicbrainz.GetRelease(ctx, id)
}

// GetCatalogShazamTrack возвращает детальный трек Shazam (по акустическому отпечатку или ID/названию).
func (s *Service) GetCatalogShazamTrack(ctx context.Context, id, query, audioPath string) (*CatalogReleaseDetails, error) {
	if s.shazam == nil {
		return nil, ErrClientNotInitialized
	}

	audioPath = strings.TrimSpace(audioPath)
	if audioPath != "" {
		fullPath, _, err := s.ResolvePath(audioPath)
		if err != nil {
			return nil, err
		}
		if info, statErr := safefs.Stat(fullPath); statErr != nil || info.IsDir() {
			return nil, ErrFileNotFound
		}
		return s.shazam.RecognizeAudioFile(ctx, fullPath)
	}

	q := strings.TrimSpace(query)
	if q == "" {
		q = strings.TrimSpace(id)
	}
	if q == "" {
		return nil, errors.New("missing id or query parameter")
	}

	matches, err := s.shazam.Search(ctx, q)
	if err != nil || len(matches) == 0 {
		return nil, ErrFileNotFound
	}

	first := matches[0]
	return &CatalogReleaseDetails{
		ID:         first.ID,
		Source:     "shazam",
		Title:      first.Title,
		Artist:     first.Artist,
		CoverURL:   first.CoverURL,
		Year:       first.Year,
		Date:       first.Date,
		TrackCount: 1,
		Tracks: []CatalogTrackDetail{
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
	}, nil
}
