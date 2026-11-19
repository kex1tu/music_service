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
	return db.UpsertLocalAlbum(ctx, tx, title, artistID, albumArtistName, year, albumDir, hasCover)
}
