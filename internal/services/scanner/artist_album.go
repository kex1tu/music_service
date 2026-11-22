package scanner

import (
	"context"
	"database/sql"

	"music-manager/internal/db"
)

func getOrCreateArtist(ctx context.Context, tx *sql.Tx, name string) (int64, error) {
	return db.GetOrCreateLibraryArtist(ctx, tx, name)
}

func getOrCreateAlbum(ctx context.Context, tx *sql.Tx, title string, artistID int64, albumArtistName string, year int, albumDir string, hasCover bool) (int64, error) {
	var previousArtist int64
	var previousCredit string
	var previousYear int
	var sameTitle, verified bool
	err := tx.QueryRowContext(ctx, `SELECT artist_id, COALESCE(album_artist, ''), COALESCE(year, 0),
		search_title = music_search_key(?2), catalog_checked_at IS NOT NULL FROM albums WHERE folder_path = ?1`,
		db.AlbumRoot(albumDir), title).Scan(&previousArtist, &previousCredit, &previousYear, &sameTitle, &verified)
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	if err == nil {
		// Without an album credit, reuse the artist of the existing edition.
		if albumArtistName == "" && (previousCredit != "" || sameTitle) {
			artistID, albumArtistName = previousArtist, previousCredit
		}
		// A track's recording year is not evidence that a verified edition changed.
		if verified && sameTitle && artistID == previousArtist && previousYear > 0 {
			year = previousYear
		}
	}
	return db.UpsertLocalAlbum(ctx, tx, title, artistID, albumArtistName, year, albumDir, hasCover)
}
