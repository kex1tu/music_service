package tageditor

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"music-manager/internal/id3"
	"music-manager/internal/services/resolver"
)

// SearchDeezerAlbums ищет альбомы в каталоге Deezer.
func (s *Service) SearchDeezerAlbums(ctx context.Context, query string) ([]DeezerSearchAlbumItem, error) {
	if s.deezer == nil {
		return nil, ErrClientNotInitialized
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return []DeezerSearchAlbumItem{}, nil
	}

	albums, err := s.deezer.SearchAlbums(ctx, q)
	if err != nil {
		return nil, err
	}

	items := make([]DeezerSearchAlbumItem, len(albums))
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
		items[i] = DeezerSearchAlbumItem{
			ID:       strconv.FormatInt(a.ID, 10),
			Title:    a.Title,
			Artist:   a.Artist.Name,
			CoverURL: pCover,
			NbTracks: a.NbTracks,
			Year:     pYear,
		}
	}
	return items, nil
}

// GetDeezerAlbumDetails возвращает детальную информацию об альбоме Deezer с 8-поточным опросом соавторов.
func (s *Service) GetDeezerAlbumDetails(ctx context.Context, id string) (*DeezerAlbumDetailsResponse, error) {
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

	var tracks []DeezerAlbumTrackItem
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
			if len(artists) == 0 && tArtist != "" {
				artists = []string{tArtist}
			}
			if s.enricher != nil {
				artists = s.enricher.EnrichTrackArtists(ctx, t.Title, artists)
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

			tracks = append(tracks, DeezerAlbumTrackItem{
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

	return &DeezerAlbumDetailsResponse{
		ID:         strconv.FormatInt(album.ID, 10),
		Title:      album.Title,
		Artist:     album.Artist.Name,
		Year:       year,
		Genre:      genre,
		CoverURL:   cover,
		TrackCount: len(tracks),
		Tracks:     tracks,
	}, nil
}

// SearchDeezerTracks ищет треки в каталоге Deezer.
func (s *Service) SearchDeezerTracks(ctx context.Context, query string) ([]DeezerSearchTrackItem, error) {
	if s.deezer == nil {
		return nil, ErrClientNotInitialized
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return []DeezerSearchTrackItem{}, nil
	}

	tracks, err := s.deezer.SearchTracks(ctx, q)
	if err != nil {
		return nil, err
	}

	items := make([]DeezerSearchTrackItem, len(tracks))
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
		if s.enricher != nil {
			artists = s.enricher.EnrichTrackArtists(ctx, t.Title, artists)
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

		items[i] = DeezerSearchTrackItem{
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
	return items, nil
}

// GetDeezerTrackDetails возвращает полную информацию о треке Deezer.
func (s *Service) GetDeezerTrackDetails(ctx context.Context, id string) (map[string]any, error) {
	if s.deezer == nil {
		return nil, ErrClientNotInitialized
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("empty track id")
	}

	track, err := s.deezer.GetTrack(ctx, id)
	if err != nil {
		return nil, err
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
	if s.enricher != nil {
		artists = s.enricher.EnrichTrackArtists(ctx, track.Title, artists)
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

	return map[string]any{
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
	}, nil
}
