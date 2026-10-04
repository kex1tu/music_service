package resolver

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"music-manager/internal/id3"
)

// resolveDeezer извлекает метаданные и список треков из Deezer API.
func (r *SourceResolver) resolveDeezer(ctx context.Context, rawURL string) (*ReleaseMetadata, []ResolvedTrack, error) {
	if r == nil || r.deezerClient == nil {
		return nil, nil, fmt.Errorf("deezer client is not initialized")
	}
	resType, id, err := r.deezerClient.ParseURL(ctx, rawURL)
	if err != nil {
		return nil, nil, err
	}

	switch resType {
	case "album":
		album, err := r.deezerClient.GetAlbum(ctx, id)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get deezer album: %w", err)
		}

		genre := album.PrimaryGenre()
		year := album.ReleaseYear()
		coverURL := album.BestCoverURL()

		meta := &ReleaseMetadata{
			AlbumTitle:  album.Title,
			AlbumArtist: album.Artist.Name,
			CoverURL:    coverURL,
			Year:        year,
			Genre:       genre,
			TotalTracks: album.NbTracks,
		}

		var tracks []ResolvedTrack
		if album.Tracks != nil {
			total := len(album.Tracks.Data)

			// Параллельный дозапрос деталей треков для извлечения официального contributors (соавторов) и ISRC
			type trackDetail struct {
				contributors []DeezerArtist
				isrc         string
			}
			details := make([]trackDetail, total)

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

					tr, trErr := r.deezerClient.GetTrack(trCtx, strconv.FormatInt(trackID, 10))
					if trErr == nil && tr != nil {
						details[idx] = trackDetail{
							contributors: tr.Contributors,
							isrc:         tr.ISRC,
						}
					}
				}(i, t.ID)
			}
			wg.Wait()

			for i, t := range album.Tracks.Data {
				pos := t.TrackPosition
				if pos == 0 {
					pos = i + 1
				}
				trackTot := total
				tArtist := t.Artist.Name
				if tArtist == "" {
					tArtist = album.Artist.Name
				}

				// Сначала проверяем соавторов из детального запроса трека Deezer
				var artists []string
				if len(details[i].contributors) > 0 {
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
				if r.featEnricher != nil {
					artists = r.featEnricher.EnrichTrackArtists(ctx, t.Title, artists)
				}
				formattedArtist := id3.FormatMultiValue(artists)

				discNum := t.DiskNumber
				if discNum <= 0 {
					discNum = 1
				}

				tr := ResolvedTrack{
					TrackID:     fmt.Sprintf("dz_%d", t.ID),
					TrackNumber: pos,
					TrackTotal:  &trackTot,
					DiscNumber:  discNum,
					Title:       id3.CleanTitle(t.Title, ""),
					Artist:      formattedArtist,
					Album:       album.Title,
					AlbumArtist: album.Artist.Name,
					Duration:    t.Duration,
					Status:      "pending",
				}
				if year > 0 {
					tr.Year = &year
				}
				if genre != "" {
					tr.Genre = &genre
				}
				if coverURL != "" {
					tr.CoverURL = &coverURL
				}
				isrc := details[i].isrc
				if isrc == "" {
					isrc = t.ISRC
				}
				if isrc != "" {
					tr.ISRC = &isrc
				}

				tracks = append(tracks, tr)
			}
		}

		return meta, tracks, nil

	case "track":
		track, err := r.deezerClient.GetTrack(ctx, id)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get deezer track: %w", err)
		}

		albumTitle := ""
		coverURL := ""
		year := 0
		if track.Album != nil {
			albumTitle = track.Album.Title
			coverURL = track.Album.BestCoverURL()
			year = track.Album.ReleaseYear()
		}

		meta := &ReleaseMetadata{
			AlbumTitle:  albumTitle,
			AlbumArtist: track.Artist.Name,
			CoverURL:    coverURL,
			Year:        year,
			TotalTracks: 1,
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
		if r.featEnricher != nil {
			artists = r.featEnricher.EnrichTrackArtists(ctx, track.Title, artists)
		}
		formattedArtist := id3.FormatMultiValue(artists)

		tot := 1
		tr := ResolvedTrack{
			TrackID:     fmt.Sprintf("dz_%d", track.ID),
			TrackNumber: 1,
			TrackTotal:  &tot,
			DiscNumber:  1,
			Title:       id3.CleanTitle(track.Title, ""),
			Artist:      formattedArtist,
			Album:       albumTitle,
			AlbumArtist: track.Artist.Name,
			Duration:    track.Duration,
			CoverURL:    &coverURL,
			Status:      "pending",
		}
		if year > 0 {
			tr.Year = &year
		}
		if track.ISRC != "" {
			isrcCopy := track.ISRC
			tr.ISRC = &isrcCopy
		}

		return meta, []ResolvedTrack{tr}, nil

	default:
		return nil, nil, fmt.Errorf("unsupported deezer resource type: %s", resType)
	}
}
