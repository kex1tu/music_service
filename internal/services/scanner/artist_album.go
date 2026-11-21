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
	if albumArtistName == "" {
		// A track without TPE2 supplies no evidence about the album's artists.
		err := tx.QueryRowContext(ctx, `SELECT artist_id, album_artist FROM albums
			WHERE folder_path = ? AND NULLIF(TRIM(album_artist), '') IS NOT NULL`, db.AlbumRoot(albumDir)).Scan(&artistID, &albumArtistName)
		if err != nil && err != sql.ErrNoRows {
			return 0, err
		}
	}
	return db.UpsertLocalAlbum(ctx, tx, title, artistID, albumArtistName, year, albumDir, hasCover)
}
