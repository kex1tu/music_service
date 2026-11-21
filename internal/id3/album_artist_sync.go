package id3

import (
	"strings"
)

// IsCompilationArtist reports whether the album credit is a generic compilation label.
func IsCompilationArtist(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "various artists", "va", "soundtrack", "ost":
		return true
	default:
		return false
	}
}

// SyncAlbumArtistAndTrackArtists synchronizes AlbumArtist across the album and ensures
// that if AlbumArtist is known, it is included in each track's Artist metadata.
//  1. If AlbumArtist is empty, but all tracks share an artist (or clear majority),
//     AlbumArtist is populated with that common artist.
//  2. If AlbumArtist is present and not a generic compilation ("Various Artists", etc.),
//     it is ensured to be present in each track's Artist list (prepended if missing).
func SyncAlbumArtistAndTrackArtists(fallbackAlbumArtist string, tracks []TrackTags) (string, []TrackTags) {
	if len(tracks) == 0 {
		return strings.TrimSpace(fallbackAlbumArtist), tracks
	}

	albumArtist := strings.TrimSpace(fallbackAlbumArtist)

	// 1. If albumArtist is empty, find common artist across tracks
	if albumArtist == "" {
		artistCounts := make(map[string]int)
		for _, t := range tracks {
			art := strings.TrimSpace(t.AlbumArtist)
			if art == "" {
				parts := ParseMultiValue(t.Artist)
				if len(parts) > 0 {
					art = parts[0]
				} else if t.Artist != "" {
					art = strings.TrimSpace(t.Artist)
				}
			}
			if art != "" {
				artistCounts[art]++
			}
		}

		var bestArtist string
		var maxCount int
		for art, count := range artistCounts {
			if count > maxCount {
				maxCount = count
				bestArtist = art
			}
		}

		if bestArtist != "" && (maxCount == len(tracks) || float64(maxCount)/float64(len(tracks)) >= 0.5) {
			albumArtist = bestArtist
		}
	}

	// 2. Check if AlbumArtist is a generic compilation
	isCompilation := IsCompilationArtist(albumArtist)

	// 3. Apply AlbumArtist and ensure it's in track.Artist
	for i := range tracks {
		if albumArtist != "" {
			if strings.TrimSpace(tracks[i].AlbumArtist) == "" {
				tracks[i].AlbumArtist = albumArtist
			}
			if !isCompilation {
				trackArtists := ParseMultiValue(tracks[i].Artist)
				if len(trackArtists) == 0 {
					tracks[i].Artist = albumArtist
				} else {
					found := false
					for _, a := range trackArtists {
						if strings.EqualFold(strings.TrimSpace(a), albumArtist) {
							found = true
							break
						}
					}
					if !found {
						// Prepend main album artist to track artists
						trackArtists = append([]string{albumArtist}, trackArtists...)
						tracks[i].Artist = FormatMultiValue(trackArtists)
					}
				}
			}
		}
	}

	return albumArtist, tracks
}
