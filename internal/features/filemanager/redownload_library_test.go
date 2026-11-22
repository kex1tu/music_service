package filemanager

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"music-manager/internal/core/safefs"
	"music-manager/internal/db"
	"music-manager/internal/features/downloader"
	"music-manager/internal/id3"
	"music-manager/internal/services/organizer"
	"music-manager/internal/services/resolver"
	"music-manager/internal/services/scanner"
	"music-manager/internal/testkit"
)

func TestRedownloadRejectsStaleMissingTrackAfterReload(t *testing.T) {
	env := testkit.NewEnv(t)
	ctx := t.Context()
	scan := scanner.NewScanner(env.MusicSandbox, env.DB, nil, env.TmpSandbox)
	org := organizer.NewService(env.MusicSandbox, env.TmpSandbox)
	org.SetTrackChanger(scan)
	queueConfig := downloader.ServiceConfig{Repo: downloader.NewRepository(env.DB.Conn()),
		MusicSandbox: env.MusicSandbox, TmpSandbox: env.TmpSandbox}
	queue := downloader.NewService(queueConfig)
	t.Cleanup(func() { _ = queue.Close() })
	cfg := ServiceConfig{MusicSandbox: env.MusicSandbox, Repo: NewRepository(env.DB.Conn()),
		RedownloadQueue: queue, RedownloadOrganizer: org}
	svc := NewService(cfg)

	const existing = "Album/01. Existing.mp3"
	tags := id3.TrackTags{Title: "Existing", Artist: "Band", AlbumArtist: "Band", Album: "Old edition", Year: 2000, TrackNumber: 1}
	path := env.CreateDummyMP3WithTags(existing, tags, nil)
	if err := scan.IndexFile(path, existing); err != nil {
		t.Fatal(err)
	}
	var albumID int64
	if err := env.DB.Conn().QueryRow(`SELECT id FROM albums WHERE folder_path = 'Album'`).Scan(&albumID); err != nil {
		t.Fatal(err)
	}
	catalog := []db.CatalogTrack{
		{BackfillSelection: db.BackfillSelection{TrackPosition: db.ReleasePosition(1, 1)}, Title: "Existing", Artist: "Band"},
		{BackfillSelection: db.BackfillSelection{TrackPosition: db.ReleasePosition(1, 2)}, Title: "Missing", Artist: "Band"},
	}
	if _, err := db.VerifyCatalog(ctx, env.DB.Conn(), albumID, "42", catalog); err != nil {
		t.Fatal(err)
	}
	response, err := svc.Redownload(ctx, RedownloadRequest{Path: "Album", Type: "album"})
	if err != nil {
		t.Fatal(err)
	}
	job, ok := queue.GetJob(response.JobID)
	if !ok {
		t.Fatal("redownload job missing")
	}
	staged := filepath.Join(job.ID, "converted", "02. Missing.mp3")
	stagedTags := tags
	stagedTags.Title, stagedTags.TrackNumber = "Missing", 2
	if _, err := testkit.CreateDummyMP3WithTags(env.TmpSandbox, staged, stagedTags, nil); err != nil {
		t.Fatal(err)
	}
	job.UpdateTrack(1, func(t *resolver.ResolvedTrack) { t.Status, t.ConvertedPath = "ready", &staged })
	job.SetPartialReady(1, 2)

	tags.Album, tags.Year = "New edition", 2020
	if err := id3.WriteTags(path, tags, nil); err != nil {
		t.Fatal(err)
	}
	if err := scan.IndexFile(path, existing); err != nil {
		t.Fatal(err)
	}
	catalog[1].Title = "Different missing track"
	if _, err := db.VerifyCatalog(ctx, env.DB.Conn(), albumID, "43", catalog); err != nil {
		t.Fatal(err)
	}
	reloaded := downloader.NewService(queueConfig)
	t.Cleanup(func() { _ = reloaded.Close() })
	cfg.RedownloadQueue = reloaded
	result, err := NewService(cfg).confirmRedownload(ctx, job.ID, []string{job.Snapshot().Tracks[1].TrackID})
	if err != nil || len(result) != 1 || result[0].Error != "каталог релиза изменился, создайте новую задачу для недостающей дорожки" {
		t.Fatalf("confirmation did not reject the stale edition: %+v, %v", result, err)
	}
	dest, err := env.MusicSandbox.Resolve("Album/02. Missing.mp3", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := safefs.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale task changed the library: %v", err)
	}
}

func TestRedownloadPartialConfirmAndRebuild(t *testing.T) {
	env := testkit.NewEnv(t)
	ctx := t.Context()
	scan := scanner.NewScanner(env.MusicSandbox, env.DB, nil, env.TmpSandbox)
	org := organizer.NewService(env.MusicSandbox, env.TmpSandbox)
	org.SetTrackChanger(scan)
	queue := downloader.NewService(downloader.ServiceConfig{Repo: downloader.NewRepository(env.DB.Conn()),
		MusicSandbox: env.MusicSandbox, TmpSandbox: env.TmpSandbox})
	t.Cleanup(func() { _ = queue.Close() })
	svc := NewService(ServiceConfig{MusicSandbox: env.MusicSandbox, Repo: NewRepository(env.DB.Conn()),
		RedownloadQueue: queue, RedownloadOrganizer: org})

	const first = "Album/CD1/01. First.mp3"
	const second = "Album/CD2/01. Second.mp3"
	const oldURL = "https://youtu.be/original"
	const newURL = "https://soundcloud.com/artist/first"
	mp3 := env.CreateDummyMP3WithTags(first, id3.TrackTags{
		Title: "First", Artist: "Artist", AlbumArtist: "Artist", Album: "Album",
		Year: 1999, TrackNumber: 1, DiscNumber: 1, DiscTotal: 2, SourceURL: oldURL,
	}, nil)
	env.CreateDummyMP3WithTags(second, id3.TrackTags{Title: "Second", Artist: "Artist", Album: "Album",
		AlbumArtist: "Artist", Year: 1999, TrackNumber: 1, DiscNumber: 2, DiscTotal: 2, SourceURL: oldURL}, nil)
	lyricsPath := filepath.Join(filepath.Dir(mp3), "01. First.lrc")
	if err := safefs.WriteFile(lyricsPath, []byte("[00:01.00]original"), 0600); err != nil {
		t.Fatal(err)
	}
	if count, err := scan.ScanAll(ctx); err != nil || count != 2 {
		t.Fatalf("scan indexed %d tracks: %v", count, err)
	}
	var albumID int64
	if err := env.DB.Conn().QueryRow("SELECT id FROM albums WHERE folder_path = 'Album'").Scan(&albumID); err != nil {
		t.Fatal(err)
	}
	catalog := []db.CatalogTrack{
		{BackfillSelection: db.BackfillSelection{TrackPosition: db.ReleasePosition(1, 1)}, Title: "First", Artist: "Artist"},
		{BackfillSelection: db.BackfillSelection{TrackPosition: db.ReleasePosition(2, 1)}, Title: "Second", Artist: "Artist"},
		{BackfillSelection: db.BackfillSelection{TrackPosition: db.ReleasePosition(2, 2)}, Title: "Missing", Artist: "Artist"},
	}
	if _, err := db.VerifyCatalog(ctx, env.DB.Conn(), albumID, "88", catalog); err != nil {
		t.Fatal(err)
	}
	response, err := svc.Redownload(ctx, RedownloadRequest{Path: "Album", Type: "album"})
	if err != nil {
		t.Fatal(err)
	}
	job, ok := queue.GetJob(response.JobID)
	if !ok || len(job.Snapshot().Tracks) != 3 || job.Snapshot().Status != downloader.StatusWaitingSource {
		t.Fatalf("unexpected release queue: %+v", job)
	}
	missingYear := job.Snapshot().Tracks[2].Year
	if missingYear == nil || *missingYear != 1999 {
		t.Fatalf("missing track has wrong edition year: %v", missingYear)
	}
	staged := filepath.Join(job.ID, "converted", "First.mp3")
	if _, err := testkit.CreateDummyMP3WithTags(env.TmpSandbox, staged,
		id3.TrackTags{Title: "Wrong title", Artist: "Artist", SourceURL: newURL}, nil); err != nil {
		t.Fatal(err)
	}
	job.UpdateTrack(0, func(t *resolver.ResolvedTrack) {
		t.Status, t.ConvertedPath = "ready", &staged
	})
	job.SetPartialReady(1, 3)
	if tags, err := id3.ReadTags(mp3, first); err != nil || tags.SourceURL != oldURL {
		t.Fatalf("staging changed the original: %+v, %v", tags, err)
	}
	ids := []string{job.Snapshot().Tracks[0].TrackID, job.Snapshot().Tracks[1].TrackID}
	for attempt := 0; attempt < 2; attempt++ {
		results, err := svc.confirmRedownload(ctx, job.ID, ids)
		if err != nil || len(results) != 2 || results[0].Path != first || results[0].Error != "" || results[1].Error == "" {
			t.Fatalf("partial confirm: %+v, %v", results, err)
		}
	}
	tags, err := id3.ReadTags(mp3, first)
	if err != nil || tags.SourceURL != newURL || tags.Title != "First" {
		t.Fatalf("replacement lost metadata or source: %+v, %v", tags, err)
	}
	if lyrics, err := safefs.ReadFile(lyricsPath); err != nil || string(lyrics) != "[00:01.00]original" {
		t.Fatalf("replacement changed lyrics: %q, %v", lyrics, err)
	}
	secondPath, err := env.MusicSandbox.Resolve(second, false)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := safefs.OpenFile(secondPath, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := changed.WriteString("externally changed"); err != nil {
		changed.Close()
		t.Fatal(err)
	}
	if err := changed.Close(); err != nil {
		t.Fatal(err)
	}
	for i, title := range []string{"Second", "Missing"} {
		staged := filepath.Join(job.ID, "converted", title+".mp3")
		if _, err := testkit.CreateDummyMP3WithTags(env.TmpSandbox, staged, id3.TrackTags{
			Title: title, Artist: "Artist", AlbumArtist: "Artist", Album: "Album",
			TrackNumber: i + 1, DiscNumber: 2, DiscTotal: 2, SourceURL: newURL, Year: *missingYear,
		}, nil); err != nil {
			t.Fatal(err)
		}
		job.UpdateTrack(i+1, func(t *resolver.ResolvedTrack) {
			t.Status, t.ConvertedPath = "ready", &staged
		})
	}
	job.SetReady(3, 3)
	results, err := svc.confirmRedownload(ctx, job.ID, []string{ids[1], job.Snapshot().Tracks[2].TrackID})
	const missing = "Album/CD2/02. Missing.mp3"
	if err != nil || len(results) != 2 || results[0].Error == "" || results[1].Path != missing || results[1].Error != "" {
		t.Fatalf("expected a conflict for edited original and successful missing track: %+v, %v", results, err)
	}
	var year int
	if err := env.DB.Conn().QueryRow("SELECT year FROM albums WHERE id = ?", albumID).Scan(&year); err != nil || year != 1999 {
		t.Fatalf("import changed release year: %d, %v", year, err)
	}
	var presentCount int
	if err := env.DB.Conn().QueryRow("SELECT present_tracks_count FROM albums WHERE id = ?", albumID).Scan(&presentCount); err != nil || presentCount != 3 {
		t.Fatalf("verified completeness after import: %d, %v", presentCount, err)
	}
	if err := env.DB.ResetLibraryTables(ctx); err != nil {
		t.Fatal(err)
	}
	if count, err := scan.ScanAll(ctx); err != nil || count != 3 {
		t.Fatalf("rebuild indexed %d tracks: %v", count, err)
	}
	for path, want := range map[string]string{first: newURL, second: oldURL, missing: newURL} {
		var got string
		if err := env.DB.Conn().QueryRow("SELECT source_url FROM tracks WHERE file_path = ?", path).Scan(&got); err != nil || got != want {
			t.Fatalf("rebuilt source for %s = %q, want %q: %v", path, got, want, err)
		}
	}
	single, err := svc.Redownload(ctx, RedownloadRequest{Path: first, Type: "track"})
	if err != nil || single.SourceURL != newURL {
		t.Fatalf("single-file repeat did not use the confirmed source: %+v, %v", single, err)
	}
}

func TestRedownloadIncludesUnnumberedTracksAndProtectsOccupiedCatalogSlots(t *testing.T) {
	env := testkit.NewEnv(t)
	ctx := t.Context()
	scan := scanner.NewScanner(env.MusicSandbox, env.DB, nil, env.TmpSandbox)
	for i, title := range []string{"Intro", "Unnumbered", "Also unnumbered"} {
		tags := id3.TrackTags{Title: title, Artist: "Band", Album: "Live", AlbumArtist: "Band"}
		if i == 0 {
			tags.TrackNumber = 1
		}
		env.CreateDummyMP3WithTags("Live/"+title+".mp3", tags, nil)
	}
	if count, err := scan.ScanAll(ctx); err != nil || count != 3 {
		t.Fatalf("scan indexed %d tracks: %v", count, err)
	}
	repo := NewRepository(env.DB.Conn())
	tracks, targets, err := repo.albumRedownloadTracks(ctx, "Live")
	if err != nil || len(tracks) != 3 || len(targets) != 3 {
		t.Fatalf("unverified album only partially queued: %+v, %+v, %v", tracks, targets, err)
	}
	if tracks[1].TrackID == tracks[2].TrackID || targets[1].LibraryPath == "" || targets[2].LibraryPath == "" {
		t.Fatalf("unnumbered songs were conflated: %+v", targets)
	}
	var albumID int64
	if err := env.DB.Conn().QueryRow("SELECT id FROM albums WHERE folder_path = 'Live'").Scan(&albumID); err != nil {
		t.Fatal(err)
	}
	_, err = db.VerifyCatalog(ctx, env.DB.Conn(), albumID, "42", []db.CatalogTrack{{
		BackfillSelection: db.BackfillSelection{TrackPosition: db.ReleasePosition(1, 1)},
		Title:             "Studio intro", Artist: "Band",
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, targets, err = repo.albumRedownloadTracks(ctx, "Live")
	if err != nil || len(targets) != 1 || !targets[0].Conflict || targets[0].LibraryPath != "" {
		t.Fatalf("catalog gap would overwrite a different track at the same position: %+v, %v", targets, err)
	}
}
