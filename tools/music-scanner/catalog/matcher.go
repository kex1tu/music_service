package catalog

import (
	"regexp"
	"strings"

	"music-scanner/db"
)

var cleanMetaRegex = regexp.MustCompile(`[\p{P}\s_]+`)

func CleanString(s string) string {
	s = strings.ToLower(s)
	s = cleanMetaRegex.ReplaceAllString(s, "")
	return s
}

// FindMissingTracks сравнивает локальные треки релиза с официальным треклистом каталога
// и возвращает записи для таблицы missing_tracks.
func FindMissingTracks(albumID int64, localTracks []db.TrackAuditRecord, remote *RemoteAlbum) []db.MissingTrackRecord {
	if remote == nil || len(remote.Tracks) == 0 {
		return nil
	}

	// Индексируем локальные треки по номеру дорожки и очищенному названию
	presentByNumber := make(map[int]bool)
	presentByTitle := make(map[string]bool)

	for _, lt := range localTracks {
		if lt.TrackNumber > 0 {
			presentByNumber[lt.TrackNumber] = true
		}
		clean := CleanString(lt.Title)
		if clean != "" {
			presentByTitle[clean] = true
		}
	}

	var missing []db.MissingTrackRecord

	for _, rt := range remote.Tracks {
		cleanRemote := CleanString(rt.Title)

		// Проверяем: присутствует ли трек по номеру или названию
		isMatch := false
		if presentByNumber[rt.TrackNumber] {
			isMatch = true
		} else if presentByTitle[cleanRemote] {
			isMatch = true
		}

		if !isMatch {
			missing = append(missing, db.MissingTrackRecord{
				AlbumID:          albumID,
				Title:            rt.Title,
				Artist:           rt.Artist,
				TrackNumber:      rt.TrackNumber,
				DiscNumber:       rt.DiscNumber,
				ExpectedDuration: rt.Duration,
				DeezerID:         rt.DeezerID,
				DeezerURL:        rt.DeezerURL,
				SpotifyID:        rt.SpotifyID,
				GeniusURL:        rt.GeniusURL,
				ISRC:             rt.ISRC,
			})
		}
	}

	return missing
}
