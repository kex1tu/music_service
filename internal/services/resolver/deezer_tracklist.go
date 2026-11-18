package resolver

import (
	"fmt"
	"strconv"

	"music-manager/internal/db"
	"music-manager/internal/id3"
)

// DeezerCatalogTracks builds an ordered, complete tracklist with disc-local positions.
func DeezerCatalogTracks(album *DeezerAlbum) ([]db.CatalogTrack, error) {
	if album.Tracks == nil || len(album.Tracks.Data) == 0 ||
		(album.NbTracks > 0 && len(album.Tracks.Data) != album.NbTracks) {
		return nil, fmt.Errorf("incomplete tracklist for catalog album %d", album.ID)
	}
	tracks := make([]db.CatalogTrack, 0, len(album.Tracks.Data))
	next := make(map[int]int)
	positions := make(map[db.TrackPosition]bool)
	for _, t := range album.Tracks.Data {
		disc := max(1, t.DiskNumber)
		number := t.TrackPosition
		if number <= 0 {
			number = next[disc] + 1
		}
		next[disc] = max(next[disc], number)
		position := db.ReleasePosition(disc, number)
		if positions[position] {
			return nil, fmt.Errorf("duplicate catalog position %+v in album %d", position, album.ID)
		}
		positions[position] = true
		artist := t.Artist.Name
		if artist == "" {
			artist = album.Artist.Name
		}
		if len(t.Contributors) > 0 {
			var names []string
			for _, contributor := range t.Contributors {
				if contributor.Name != "" {
					names = append(names, contributor.Name)
				}
			}
			if len(names) > 0 {
				artist = id3.FormatMultiValue(names)
			}
		}
		track := db.CatalogTrack{
			BackfillSelection: db.BackfillSelection{TrackPosition: position},
			Title:             id3.CleanTitle(t.Title, ""), Artist: artist, Duration: t.Duration, ISRC: t.ISRC,
		}
		if t.ID > 0 {
			track.DeezerID = strconv.FormatInt(t.ID, 10)
			track.DeezerURL = fmt.Sprintf("https://www.deezer.com/track/%d", t.ID)
		}
		tracks = append(tracks, track)
	}
	return tracks, nil
}
