package resolver

import (
	"context"
	"fmt"

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

				// Format artist with null-byte separation if contributors exist
				var artists []string
				if len(t.Contributors) > 0 {
					for _, c := range t.Contributors {
						if c.Name != "" {
							artists = append(artists, c.Name)
						}
					}
				}
				if len(artists) == 0 && tArtist != "" {
					artists = []string{tArtist}
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
				if t.ISRC != "" {
					isrcCopy := t.ISRC
					tr.ISRC = &isrcCopy
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

		tot := 1
		tr := ResolvedTrack{
			TrackID:     fmt.Sprintf("dz_%d", track.ID),
			TrackNumber: 1,
			TrackTotal:  &tot,
			DiscNumber:  1,
			Title:       id3.CleanTitle(track.Title, ""),
			Artist:      track.Artist.Name,
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
