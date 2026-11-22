package scanner

import (
	"testing"

	"music-manager/internal/testkit"
)

func TestScannerResolvesHomonymByArtistID(t *testing.T) {
	env := testkit.NewEnv(t)
	ctx := t.Context()
	_, err := env.DB.Conn().ExecContext(ctx, `INSERT INTO artists(id, name, clean_name, deezer_id)
		VALUES (11, 'Twin', 'twin', '111'), (22, 'Twin', 'twin', '222')`)
	if err != nil {
		t.Fatal(err)
	}
	const path = "Album/01. Song.mp3"
	env.CreateDummyMP3(path, "Song", "Twin", "Album")
	env.CreateDummyMP3("Unrelated/01.mp3", "Unrelated", "Someone Else", "Unrelated")
	scan := NewScanner(env.MusicSandbox, env.DB, nil)
	if count, err := scan.ScanAll(ctx); err != nil || count != 1 {
		t.Fatalf("other files should still index: count=%d error=%v", count, err)
	}
	conflicts, err := scan.ArtistConflicts(ctx)
	if err != nil || len(conflicts) != 1 || conflicts[0].Path != path || len(conflicts[0].Choices) != 2 {
		t.Fatalf("unexpected conflicts: %+v, %v", conflicts, err)
	}
	if err := scan.ResolveArtistConflict(ctx, path, false, 22); err != nil {
		t.Fatal(err)
	}
	if count, err := scan.ScanAll(ctx); err != nil || count != 2 {
		t.Fatalf("rescan indexed %d: %v", count, err)
	}
	var artistID int64
	if err := env.DB.Conn().QueryRowContext(ctx, "SELECT artist_id FROM tracks WHERE file_path = ?", path).Scan(&artistID); err != nil || artistID != 22 {
		t.Fatalf("artist ID = %d, error = %v", artistID, err)
	}
	conflicts, err = scan.ArtistConflicts(ctx)
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("resolved conflict remains: %+v, %v", conflicts, err)
	}
}
