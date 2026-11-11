package resolver

import (
	"context"
	"fmt"
	"strings"

	"music-manager/internal/id3"
)

// resolveYandex извлекает метаданные и список треков из открытого API Яндекс Музыки.
func (r *SourceResolver) resolveYandex(ctx context.Context, rawURL string) (*ReleaseMetadata, []ResolvedTrack, error) {
	if r == nil || r.yandexClient == nil {
		return nil, nil, fmt.Errorf("yandex client is not initialized")
	}

	ref, err := r.yandexClient.ParseResource(rawURL)
	if err != nil {
		return nil, nil, err
	}

	switch ref.Type {
	case "album":
		albumWithTracks, err := r.yandexClient.GetAlbumWithTracks(ctx, ref.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get yandex album: %w", err)
		}

		genre := id3.CanonicalizeGenre(albumWithTracks.Genre)
		year := albumWithTracks.Year
		coverURL := albumWithTracks.BestCoverURL()
		albumArtist := albumWithTracks.PrimaryArtistName()
		total := len(albumWithTracks.Tracks)

		meta := &ReleaseMetadata{
			AlbumTitle:    albumWithTracks.Title,
			AlbumArtist:   albumArtist,
			CoverURL:      coverURL,
			Year:          year,
			Genre:         genre,
			TotalTracks:   total,
			FetchedTracks: total,
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
				TrackID:        fmt.Sprintf("ya_%d", t.ID),
				CatalogTrackID: fmt.Sprintf("ya_%d", t.ID),
				YandexTrackID:  t.ID,
				TrackNumber:    pos,
				TrackTotal:     &trackTot,
				DiscNumber:     discNum,
				Title:          trTitle,
				Artist:         formattedArtist,
				Album:          albumWithTracks.Title,
				AlbumArtist:    albumArtist,
				Duration:       dur,
				Status:         "pending",
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
		tracksFound, err := r.yandexClient.SearchTrack(ctx, fmt.Sprintf("%d", ref.ID))
		if err != nil || len(tracksFound) == 0 {
			return nil, nil, fmt.Errorf("track %d not found in yandex music", ref.ID)
		}

		tr := tracksFound[0]
		allArt := tr.AllArtistNames()
		cleanTit := id3.CleanTitle(tr.Title, "")
		albumTitle := cleanTit
		year := 0
		coverURL := ""
		genre := ""

		if len(tr.Albums) > 0 {
			alb := tr.Albums[0]
			if alb.Title != "" && !strings.EqualFold(alb.Title, "single") && !strings.EqualFold(alb.Title, "singles") {
				albumTitle = alb.Title
			}
			year = alb.Year
			coverURL = alb.BestCoverURL()
			genre = alb.Genre
		}

		if r.featEnricher != nil {
			enriched := r.featEnricher.EnrichTrackDetails(ctx, tr.Title, allArt)
			allArt = enriched.Artists
			if genre == "" && enriched.Genre != "" {
				genre = enriched.Genre
			}
			if year == 0 && enriched.Year > 0 {
				year = enriched.Year
			}
		}
		formattedArtist := id3.FormatMultiValue(allArt)
		genre = id3.CanonicalizeGenre(genre)

		tot := 1
		meta := &ReleaseMetadata{
			AlbumTitle:    albumTitle,
			AlbumArtist:   formattedArtist,
			CoverURL:      coverURL,
			Year:          year,
			Genre:         genre,
			TotalTracks:   1,
			FetchedTracks: 1,
		}

		resolvedTr := ResolvedTrack{
			TrackID:        fmt.Sprintf("ya_%d", tr.ID),
			CatalogTrackID: fmt.Sprintf("ya_%d", tr.ID),
			YandexTrackID:  tr.ID,
			TrackNumber:    1,
			TrackTotal:     &tot,
			DiscNumber:     1,
			Title:          cleanTit,
			Artist:         formattedArtist,
			Album:          albumTitle,
			AlbumArtist:    formattedArtist,
			Duration:       tr.DurationMs / 1000,
			Status:         "pending",
		}
		if year > 0 {
			resolvedTr.Year = &year
		}
		if genre != "" {
			resolvedTr.Genre = &genre
		}
		if coverURL != "" {
			resolvedTr.CoverURL = &coverURL
		}

		return meta, []ResolvedTrack{resolvedTr}, nil

	case "playlist":
		var playlist *YandexPlaylist
		if ref.PlaylistUUID != "" {
			pl, err := r.yandexClient.GetPlaylistByUUID(ctx, ref.PlaylistUUID)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to get yandex playlist by uuid: %w", err)
			}
			playlist = pl
		} else if ref.Owner != "" && ref.Kind > 0 {
			pl, err := r.yandexClient.GetPlaylist(ctx, ref.Owner, ref.Kind)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to get yandex playlist: %w", err)
			}
			playlist = pl
		} else {
			return nil, nil, fmt.Errorf("не удалось определить идентификаторы плейлиста Яндекс Музыки (owner/kind или UUID)")
		}

		if playlist == nil {
			return nil, nil, fmt.Errorf("playlist not found or empty")
		}

		// Догрузка треков, у которых отсутствует вложенный объект Track
		var missingIDs []string
		for _, it := range playlist.Tracks {
			if it.Track == nil && it.ID > 0 {
				if it.AlbumID > 0 {
					missingIDs = append(missingIDs, fmt.Sprintf("%d:%d", it.ID, it.AlbumID))
				} else {
					missingIDs = append(missingIDs, fmt.Sprintf("%d", it.ID))
				}
			}
		}

		trackMap := make(map[int64]*YandexTrack)
		if len(missingIDs) > 0 {
			hydrated, hErr := r.yandexClient.GetTracks(ctx, missingIDs)
			if hErr == nil {
				for i := range hydrated {
					tCopy := hydrated[i]
					trackMap[tCopy.ID] = &tCopy
				}
			}
		}

		playlistOwner := playlist.OwnerName()
		coverURL := playlist.BestCoverURL()
		totalInPlaylist := len(playlist.Tracks)
		if playlist.TrackCount > totalInPlaylist {
			totalInPlaylist = playlist.TrackCount
		}

		var tracks []ResolvedTrack
		var fetchedCount, skippedCount int

		for i, item := range playlist.Tracks {
			pos := i + 1
			t := item.Track
			if t == nil && item.ID > 0 {
				t = trackMap[item.ID]
			}

			// Если метаданные трека отсутствуют или недоступны — трек пропускается
			if t == nil || t.Title == "" {
				skippedCount++
				continue
			}

			fetchedCount++

			allArt := t.AllArtistNames()
			if len(allArt) == 0 {
				allArt = []string{playlistOwner}
			}
			if r.featEnricher != nil {
				allArt = r.featEnricher.EnrichTrackArtists(ctx, t.Title, allArt)
			}
			formattedArtist := id3.FormatMultiValue(allArt)

			var trackAlbum string
			var trackAlbumArtist string
			var trackYear int
			var trackGenre string
			var trackCover string

			if len(t.Albums) > 0 {
				alb := t.Albums[0]
				trackAlbum = strings.TrimSpace(alb.Title)
				trackAlbumArtist = strings.TrimSpace(alb.PrimaryArtistName())
				trackYear = alb.Year
				trackGenre = id3.CanonicalizeGenre(alb.Genre)
				trackCover = alb.BestCoverURL()
			}

			if trackAlbum == "" {
				trackAlbum = id3.CleanTitle(t.Title, "")
			}
			if trackAlbumArtist == "" {
				trackAlbumArtist = formattedArtist
			}
			if trackCover == "" {
				trackCover = coverURL
			}

			dur := t.DurationMs / 1000
			trTitle := id3.CleanTitle(t.Title, "")

			// Важно: ключ элемента очереди TrackID уникален для каждого слота в плейлисте (ya_{id}_{pos}),
			// что сохраняет повторы и порядок в очереди, а оригинальный ID каталога сохраняется в CatalogTrackID.
			trackIDStr := fmt.Sprintf("ya_%d_%d", t.ID, pos)
			catalogIDStr := fmt.Sprintf("ya_%d", t.ID)

			discNum := t.DiskNumber
			if discNum <= 0 {
				discNum = 1
			}

			tr := ResolvedTrack{
				TrackID:        trackIDStr,
				CatalogTrackID: catalogIDStr,
				YandexTrackID:  t.ID,
				TrackNumber:    pos,
				TrackTotal:     &totalInPlaylist,
				DiscNumber:     discNum,
				Title:          trTitle,
				Artist:         formattedArtist,
				Album:          trackAlbum,       // Собственный альбом трека, не название плейлиста!
				AlbumArtist:    trackAlbumArtist, // Собственный артист альбома трека!
				Duration:       dur,
				Status:         "pending",
			}

			if trackYear > 0 {
				tr.Year = &trackYear
			}
			if trackGenre != "" {
				tr.Genre = &trackGenre
			}
			if trackCover != "" {
				tr.CoverURL = &trackCover
			}

			tracks = append(tracks, tr)
		}

		meta := &ReleaseMetadata{
			AlbumTitle:    playlist.Title,
			AlbumArtist:   playlistOwner,
			CoverURL:      coverURL,
			Year:          0,
			Genre:         "",
			TotalTracks:   len(playlist.Tracks),
			FetchedTracks: fetchedCount,
			SkippedTracks: skippedCount,
			IsPlaylist:    true,
		}

		return meta, tracks, nil

	default:
		return nil, nil, fmt.Errorf("unsupported yandex resource type: %s", ref.Type)
	}
}

// Yandex returns the underlying YandexMusicClient.
func (r *SourceResolver) Yandex() *YandexMusicClient {
	return r.yandexClient
}
