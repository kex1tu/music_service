package downloader

import (
	"path/filepath"
	"testing"

	"music-manager/internal/db"
)

func TestBackfillKeepsSelectedDiscAndCatalogIDAfterReload(t *testing.T) {
	ctx := t.Context()
	database, err := db.Open(filepath.Join(t.TempDir(), "backfill.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	_, err = database.Conn().ExecContext(ctx, `
		INSERT INTO artists (id, name, clean_name) VALUES (1, 'Band', 'band');
		INSERT INTO albums (id, artist_id, title, clean_title, folder_path) VALUES (1, 1, 'Live', 'live', 'Live');
		INSERT INTO missing_tracks (album_id, title, artist, track_number, disc_number, deezer_id)
		VALUES (1, 'Intro', 'Band', 1, 1, '441'), (1, 'Intro', 'Band', 1, 2, '442');
	`)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(database.Conn())
	svc := &Service{repo: repo}
	selection := &BackfillRequest{AlbumID: "44", Positions: []db.BackfillSelection{{
		TrackPosition: db.ReleasePosition(2, 1), DeezerID: "442",
	}}}
	if err := svc.freezeBackfillSelection(ctx, "Live", selection); err != nil {
		t.Fatal(err)
	}
	folder := "Live"
	job := &Job{ID: "multidisc", Status: StatusQueued, IsBackfill: true, TargetAlbumPath: &folder, Backfill: selection}
	if err := repo.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	list, err := repo.GetAllJobs(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("load jobs: %v, %v", list, err)
	}
	restored := list[0]
	if restored.Backfill == nil || len(restored.Backfill.Positions) != 1 || restored.Backfill.Positions[0] != selection.Positions[0] {
		t.Fatalf("selection did not survive persistence: %+v", restored.Backfill)
	}
	_, tracks, err := svc.resolveBackfill(ctx, restored)
	if err != nil || len(tracks) != 1 || tracks[0].DiscNumber != 2 || tracks[0].TrackNumber != 1 || tracks[0].TrackID != "dz_442" {
		t.Fatalf("resolved the wrong catalog position: %+v, %v", tracks, err)
	}

	ambiguous := &BackfillRequest{AlbumID: "44", TrackNumbers: []int{1}}
	if err := svc.freezeBackfillSelection(ctx, "Live", ambiguous); err == nil {
		t.Fatal("ambiguous legacy track number selected more than one disc")
	}
}
