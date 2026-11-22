package resolver

import (
	"context"
	"fmt"
	"strconv"
	"strings"
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

		genre := id3.CanonicalizeGenre(album.PrimaryGenre())
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
				explicit     bool
			}
			details := make([]trackDetail, total)

			var wg sync.WaitGroup
			sem := make(chan struct{}, 4) // Ограниченный пул: не более 3-4 параллельных запросов для защиты от rate-limit Deezer

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
							explicit:     tr.ExplicitLyrics,
						}
					}
				}(i, t.ID)
			}
			wg.Wait()

			catalog, err := DeezerCatalogTracks(album)
			if err != nil {
				return nil, nil, err
			}
			for i, t := range album.Tracks.Data {
				position := catalog[i].TrackPosition
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

				tr := ResolvedTrack{
					TrackID:     fmt.Sprintf("dz_%d", t.ID),
					TrackNumber: position.TrackNumber,
					TrackTotal:  &trackTot,
					DiscNumber:  position.DiscNumber,
					Title:       id3.CleanTitle(t.Title, ""),
					Artist:      formattedArtist,
					Album:       album.Title,
					AlbumArtist: album.Artist.Name,
					Duration:    t.Duration,
					Status:      "pending",
				}
				if t.ExplicitLyrics || details[i].explicit {
					tr.VersionClaim = string(PolicyUncensored)
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
		genre := ""
		if track.Album != nil {
			albumTitle = track.Album.Title
			coverURL = track.Album.BestCoverURL()
			year = track.Album.ReleaseYear()
			if track.Album.ID > 0 {
				albCtx, albCancel := context.WithTimeout(ctx, 3*time.Second)
				if alb, albErr := r.deezerClient.GetAlbum(albCtx, strconv.FormatInt(track.Album.ID, 10)); albErr == nil && alb != nil {
					genre = id3.CanonicalizeGenre(alb.PrimaryGenre())
					if year == 0 {
						year = alb.ReleaseYear()
					}
				}
				albCancel()
			}
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
			enriched := r.featEnricher.EnrichTrackDetails(ctx, track.Title, artists)
			artists = enriched.Artists
			if genre == "" && enriched.Genre != "" {
				genre = enriched.Genre
			}
			if year == 0 && enriched.Year > 0 {
				year = enriched.Year
			}
		}
		formattedArtist := id3.FormatMultiValue(artists)

		cleanTitle := id3.CleanTitle(track.Title, "")
		if albumTitle == "" || strings.EqualFold(albumTitle, "single") || strings.EqualFold(albumTitle, "singles") {
			albumTitle = cleanTitle
		}

		meta := &ReleaseMetadata{
			AlbumTitle:  albumTitle,
			AlbumArtist: formattedArtist,
			CoverURL:    coverURL,
			Year:        year,
			Genre:       genre,
			TotalTracks: 1,
		}

		tot := 1
		tr := ResolvedTrack{
			TrackID:     fmt.Sprintf("dz_%d", track.ID),
			TrackNumber: 1,
			TrackTotal:  &tot,
			DiscNumber:  1,
			Title:       cleanTitle,
			Artist:      formattedArtist,
			Album:       albumTitle,
			AlbumArtist: formattedArtist,
			Duration:    track.Duration,
			CoverURL:    &coverURL,
			Status:      "pending",
		}
		if track.ExplicitLyrics {
			tr.VersionClaim = string(PolicyUncensored)
		}
		if year > 0 {
			tr.Year = &year
		}
		if genre != "" {
			tr.Genre = &genre
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
