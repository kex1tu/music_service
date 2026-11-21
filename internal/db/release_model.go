package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"

	"modernc.org/sqlite"
	"music-manager/internal/core/textkey"
	"music-manager/internal/id3"
)

func init() {
	sqlite.MustRegisterDeterministicScalarFunction("music_artist_credit_parts", 1,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			credit, _ := args[0].(string)
			names := []string{credit}
			for _, part := range artistSeparator.Split(credit, -1) {
				if name := strings.TrimSpace(part); name != "" && name != credit {
					names = append(names, name)
				}
			}
			raw, err := json.Marshal(names)
			return string(raw), err
		})
	sqlite.MustRegisterDeterministicScalarFunction("music_search_key", 1,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			value, _ := args[0].(string)
			return textkey.Search(value), nil
		})
	sqlite.MustRegisterDeterministicScalarFunction("music_radar_artist_matches", 2,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			artist, _ := args[0].(string)
			candidate, _ := args[1].(string)
			return radarArtistMatches(artist, candidate), nil
		})
}

var discFolder = regexp.MustCompile(`(?i)^(?:cd|disc|disk)[ _.-]*([0-9]+)$`)

// DiscFolderNumber recognizes conventional disc subdirectories.
func DiscFolderNumber(name string) int {
	match := discFolder.FindStringSubmatch(name)
	if match == nil {
		return 0
	}
	n, _ := strconv.Atoi(match[1])
	return n
}

// LocalDiscNumber uses a numbered disc directory when present.
func LocalDiscNumber(directory string, tagged int) int {
	if AlbumRoot(directory) != path.Clean(directory) {
		return DiscFolderNumber(path.Base(directory))
	}
	return max(1, tagged)
}

// AlbumRoot returns the library-relative directory identifying a local release.
func AlbumRoot(directory string) string {
	directory = path.Clean(directory)
	parent := path.Dir(directory)
	if parent != "." && DiscFolderNumber(path.Base(directory)) > 0 {
		return parent
	}
	return directory
}

// UpsertLocalAlbum indexes a local edition by its root. The tags of the last
// indexed track provide the release metadata, including a cleared year and the ordered artist credits.
func UpsertLocalAlbum(ctx context.Context, writer artistStore, title string, artistID int64, albumArtist string, year int, directory string, hasCover bool) (int64, error) {
	if tx, ok := writer.(*sql.Tx); ok {
		return upsertLocalAlbum(ctx, tx, title, artistID, albumArtist, year, directory, hasCover)
	}
	pool, ok := writer.(interface {
		BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	})
	if !ok {
		return 0, fmt.Errorf("album indexing requires a SQLite transaction")
	}
	tx, err := pool.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	id, err := upsertLocalAlbum(ctx, tx, title, artistID, albumArtist, year, directory, hasCover)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

func upsertLocalAlbum(ctx context.Context, tx *sql.Tx, title string, artistID int64, albumArtist string, year int, directory string, hasCover bool) (int64, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Unknown Album"
	}
	cleanTitle := id3.CleanReleaseTitle(title)
	if cleanTitle == "" {
		cleanTitle = textkey.Search(title)
	}
	var id int64
	err := tx.QueryRowContext(ctx, `
		INSERT INTO albums (title, clean_title, artist_id, album_artist, year, folder_path, has_cover)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(folder_path) DO UPDATE SET
			title = excluded.title, clean_title = excluded.clean_title, artist_id = excluded.artist_id,
			album_artist = excluded.album_artist, year = excluded.year,
			has_cover = MAX(COALESCE(albums.has_cover, 0), excluded.has_cover),
			updated_at = CURRENT_TIMESTAMP
		RETURNING id`, title, cleanTitle, artistID, albumArtist, year, AlbumRoot(directory), hasCover).Scan(&id)
	if err != nil {
		return 0, err
	}
	if err := syncAlbumArtists(ctx, tx, id, albumArtist); err != nil {
		return 0, fmt.Errorf("album %d artist credits: %w", id, err)
	}
	return id, nil
}

// TrackPosition identifies a track within a release, independently of its title.
type TrackPosition struct {
	DiscNumber  int `json:"disc_number"`
	TrackNumber int `json:"track_number"`
}

func ReleasePosition(disc, track int) TrackPosition {
	return TrackPosition{DiscNumber: max(1, disc), TrackNumber: track}
}

// BackfillSelection retains the exact catalog identity and position selected for a job.
type BackfillSelection struct {
	TrackPosition
	DeezerID  string `json:"deezer_id,omitempty"`
	DeezerURL string `json:"deezer_url,omitempty"`
}

func RadarIdentityKey(artist, title, releaseType, deezerID, musicBrainzID, shazamID string) string {
	for _, id := range []struct{ provider, value string }{
		{"deezer", deezerID}, {"musicbrainz", musicBrainzID}, {"shazam", shazamID},
	} {
		if id.value != "" {
			return id.provider + ":" + id.value
		}
	}
	artist, title = textkey.Search(artist), textkey.Search(title)
	return fmt.Sprintf("local:%d:%s%d:%s%s", len(artist), artist, len(title), title, releaseType)
}
