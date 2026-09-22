package resolver

import (
	"context"
	"fmt"

	"music-manager/internal/id3"
)

// resolveYandex извлекает метаданные и список треков из открытого API Яндекс Музыки.
func (r *SourceResolver) resolveYandex(ctx context.Context, rawURL string) (*ReleaseMetadata, []ResolvedTrack, error) {
	if r == nil || r.yandexClient == nil {
		return nil, nil, fmt.Errorf("yandex client is not initialized")
	}

	resType, id, err := r.yandexClient.ParseURL(rawURL)
	if err != nil {
		return nil, nil, err
	}

	switch resType {
	case "album":
		albumWithTracks, err := r.yandexClient.GetAlbumWithTracks(ctx, id)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get yandex album: %w", err)
		}

		genre := albumWithTracks.Genre
		year := albumWithTracks.Year
		coverURL := albumWithTracks.BestCoverURL()
		albumArtist := albumWithTracks.PrimaryArtistName()
		total := len(albumWithTracks.Tracks)

		meta := &ReleaseMetadata{
			AlbumTitle:  albumWithTracks.Title,
			AlbumArtist: albumArtist,
			CoverURL:    coverURL,
			Year:        year,
			Genre:       genre,
			TotalTracks: total,
		}

		var tracks []ResolvedTrack
		for i, t := range albumWithTracks.Tracks {
			pos := t.TrackPosition
			if pos <= 0 {
				pos = i + 1
			}
			trackTot := total
			discNum := t.DiskNumber
			if discNum <= 0 {
				discNum = 1
			}

			// Все исполнители трека (основной + фиты), отформатированные через '\\'
			allArt := t.AllArtistNames()
			if len(allArt) == 0 {
				allArt = []string{albumArtist}
			}
			if r.featEnricher != nil {
				allArt = r.featEnricher.EnrichTrackArtists(ctx, t.Title, allArt)
			}
			formattedArtist := id3.FormatMultiValue(allArt)

			dur := t.DurationMs / 1000
			trTitle := id3.CleanTitle(t.Title, "")

			tr := ResolvedTrack{
				TrackID:     fmt.Sprintf("ya_%d", t.ID),
				TrackNumber: pos,
				TrackTotal:  &trackTot,
				DiscNumber:  discNum,
				Title:       trTitle,
				Artist:      formattedArtist,
				Album:       albumWithTracks.Title,
				AlbumArtist: albumArtist,
				Duration:    dur,
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

			tracks = append(tracks, tr)
		}

		return meta, tracks, nil

	case "track":
		// При ссылке на одиночный трек: ищем трек и формируем одиночный релиз
		// Для Яндекс Музыки /track/{id} часто проще найти через SearchTrack или получить альбом трека
		tracksFound, err := r.yandexClient.SearchTrack(ctx, fmt.Sprintf("%d", id))
		if err != nil || len(tracksFound) == 0 {
			return nil, nil, fmt.Errorf("track %d not found in yandex music", id)
		}

		tr := tracksFound[0]
		allArt := tr.AllArtistNames()
		if r.featEnricher != nil {
			allArt = r.featEnricher.EnrichTrackArtists(ctx, tr.Title, allArt)
		}
		formattedArtist := id3.FormatMultiValue(allArt)
		albumTitle := tr.Title
		year := 0
		coverURL := ""
		if len(tr.Albums) > 0 {
			albumTitle = tr.Albums[0].Title
			year = tr.Albums[0].Year
			coverURL = tr.Albums[0].BestCoverURL()
		}

		cleanTit := id3.CleanTitle(tr.Title, "")
		tot := 1

		meta := &ReleaseMetadata{
			AlbumTitle:  albumTitle,
			AlbumArtist: formattedArtist,
			CoverURL:    coverURL,
			Year:        year,
			TotalTracks: 1,
		}

		resolvedTr := ResolvedTrack{
			TrackID:     fmt.Sprintf("ya_%d", tr.ID),
			TrackNumber: 1,
			TrackTotal:  &tot,
			DiscNumber:  1,
			Title:       cleanTit,
			Artist:      formattedArtist,
			Album:       albumTitle,
			AlbumArtist: formattedArtist,
			Duration:    tr.DurationMs / 1000,
			Status:      "pending",
		}
		if year > 0 {
			resolvedTr.Year = &year
		}
		if coverURL != "" {
			resolvedTr.CoverURL = &coverURL
		}

		return meta, []ResolvedTrack{resolvedTr}, nil

	default:
		return nil, nil, fmt.Errorf("unsupported yandex resource type: %s", resType)
	}
}

// Yandex returns the underlying YandexMusicClient.
func (r *SourceResolver) Yandex() *YandexMusicClient {
	return r.yandexClient
}
