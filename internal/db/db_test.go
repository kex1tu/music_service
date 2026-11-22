package db

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestDB_OpenAndQueries(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "db_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer database.Close()

	ctx := context.Background()

	// 1. Тест создания и сохранения задачи
	job := DownloadJobEntity{
		ID:          "job-123",
		Status:      "QUEUED",
		Source:      "deezer",
		URL:         "https://www.deezer.com/album/123",
		Progress:    0.0,
		CurrentStep: "В очереди",
	}

	if err := database.SaveJob(ctx, job); err != nil {
		t.Fatalf("failed to save job: %v", err)
	}

	retrieved, err := database.GetJob(ctx, "job-123")
	if err != nil {
		t.Fatalf("failed to get job: %v", err)
	}
	if retrieved == nil || retrieved.ID != "job-123" || retrieved.Status != "QUEUED" {
		t.Errorf("unexpected retrieved job: %+v", retrieved)
	}

	// 2. Тест обновления задачи
	job.Status = "DOWNLOADING"
	job.Progress = 45.5
	if err := database.SaveJob(ctx, job); err != nil {
		t.Fatalf("failed to update job: %v", err)
	}

	allJobs, err := database.GetAllJobs(ctx)
	if err != nil {
		t.Fatalf("failed to get all jobs: %v", err)
	}
	if len(allJobs) != 1 || allJobs[0].Progress != 45.5 {
		t.Errorf("unexpected jobs list: %+v", allJobs)
	}
}

func TestAppSettings(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "db_settings_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer database.Close()

	ctx := context.Background()

	// Initial get returns empty
	val, err := database.GetSetting(ctx, "mb_token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != "" {
		t.Errorf("expected empty string, got %s", val)
	}

	// Set and get
	if err := database.SetSetting(ctx, "mb_token", "secret_tok_123"); err != nil {
		t.Fatalf("failed to set setting: %v", err)
	}
	val, err = database.GetSetting(ctx, "mb_token")
	if err != nil || val != "secret_tok_123" {
		t.Errorf("expected secret_tok_123, got %s (err: %v)", val, err)
	}

	// Update setting
	if err := database.SetSetting(ctx, "mb_token", "updated_tok_456"); err != nil {
		t.Fatalf("failed to update setting: %v", err)
	}
	val, err = database.GetSetting(ctx, "mb_token")
	if err != nil || val != "updated_tok_456" {
		t.Errorf("expected updated_tok_456, got %s (err: %v)", val, err)
	}

	// Delete setting
	if err := database.DeleteSetting(ctx, "mb_token"); err != nil {
		t.Fatalf("failed to delete setting: %v", err)
	}
	val, err = database.GetSetting(ctx, "mb_token")
	if err != nil || val != "" {
		t.Errorf("expected empty string after deletion, got %s", val)
	}
}

func TestResetLibraryTablesRollsBackOnFailure(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	ctx := context.Background()
	if _, err := database.conn.ExecContext(ctx, `
		INSERT INTO audit_issues (issue_type, entity_type) VALUES ('test', 'track');
		INSERT INTO artists (name, clean_name) VALUES ('Test Artist', 'test artist');
		CREATE TRIGGER fail_artist_delete BEFORE DELETE ON artists
		BEGIN SELECT RAISE(ABORT, 'test delete failure'); END;
	`); err != nil {
		t.Fatal(err)
	}
	if err := database.ResetLibraryTables(ctx); err == nil {
		t.Fatal("expected cache reset to fail")
	}

	var issueCount int
	if err := database.conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_issues").Scan(&issueCount); err != nil {
		t.Fatal(err)
	}
	if issueCount != 1 {
		t.Fatalf("expected audit issue to survive failed cache reset, got %d", issueCount)
	}
}

func TestDB_SearchLibrary(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "db_search_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer database.Close()

	ctx := context.Background()

	// Вставляем артиста
	resArt, err := database.Conn().ExecContext(ctx,
		"INSERT INTO artists (name, clean_name) VALUES ('Black Eyed Peas', 'black eyed peas')",
	)
	if err != nil {
		t.Fatalf("failed to insert artist: %v", err)
	}
	artID, _ := resArt.LastInsertId()

	// Вставляем альбом
	resAlb, err := database.Conn().ExecContext(ctx,
		"INSERT INTO albums (artist_id, title, clean_title, album_artist, year, folder_path, has_cover) VALUES (?, 'Elephunk', 'elephunk', 'Black Eyed Peas', 2003, 'Black Eyed Peas/Elephunk', 1)",
		artID,
	)
	if err != nil {
		t.Fatalf("failed to insert album: %v", err)
	}
	albID, _ := resAlb.LastInsertId()

	// Вставляем трек
	_, err = database.Conn().ExecContext(ctx,
		"INSERT INTO tracks (album_id, artist_id, title, clean_title, track_number, duration, file_path, file_size, mtime, lyrics_status) VALUES (?, ?, 'Where Is The Love?', 'where is the love?', 1, 272.0, 'Black Eyed Peas/Elephunk/01. Where Is The Love.mp3', 1000, 0, 'lrc')",
		albID, artID,
	)
	if err != nil {
		t.Fatalf("failed to insert track: %v", err)
	}

	// 1. Поиск по артисту
	res, err := database.SearchLibrary(ctx, "black", 10)
	if err != nil {
		t.Fatalf("SearchLibrary failed: %v", err)
	}
	if len(res.Artists) != 1 {
		t.Fatalf("expected 1 artist, got %d", len(res.Artists))
	}
	art := res.Artists[0]
	if art.Name != "Black Eyed Peas" || art.Title != "Black Eyed Peas" {
		t.Errorf("unexpected artist name: %+v", art)
	}
	if art.TrackCount != 1 {
		t.Errorf("expected track_count 1, got %d", art.TrackCount)
	}
	if art.FolderPath != "Black Eyed Peas" {
		t.Errorf("expected folder_path 'Black Eyed Peas', got %q", art.FolderPath)
	}

	// 2. Поиск по альбому
	albRes, err := database.SearchLibrary(ctx, "elephunk", 10)
	if err != nil {
		t.Fatalf("SearchLibrary failed: %v", err)
	}
	if len(albRes.Albums) != 1 {
		t.Fatalf("expected 1 album, got %d", len(albRes.Albums))
	}
	alb := albRes.Albums[0]
	if alb.Album != "Elephunk" || alb.Title != "Elephunk" {
		t.Errorf("unexpected album title: %+v", alb)
	}
	if alb.AlbumArtist != "Black Eyed Peas" || alb.Artist != "Black Eyed Peas" {
		t.Errorf("unexpected album artist: %+v", alb)
	}
	if alb.Year != 2003 {
		t.Errorf("expected year 2003, got %d", alb.Year)
	}
	if alb.Path != "Black Eyed Peas/Elephunk" {
		t.Errorf("unexpected album path: %q", alb.Path)
	}
	if alb.TrackCount != 1 {
		t.Errorf("expected track_count 1, got %d", alb.TrackCount)
	}

	// 3. Поиск по треку
	trkRes, err := database.SearchLibrary(ctx, "love", 10)
	if err != nil {
		t.Fatalf("SearchLibrary failed: %v", err)
	}
	if len(trkRes.Tracks) != 1 {
		t.Fatalf("expected 1 track, got %d", len(trkRes.Tracks))
	}
	trk := trkRes.Tracks[0]
	if trk.Title != "Where Is The Love?" {
		t.Errorf("unexpected track title: %+v", trk)
	}
	if trk.Artist != "Black Eyed Peas" {
		t.Errorf("unexpected track artist: %+v", trk)
	}
	if trk.Path != "Black Eyed Peas/Elephunk/01. Where Is The Love.mp3" {
		t.Errorf("unexpected track path: %q", trk.Path)
	}
}

func TestDB_ConcurrentWALStress(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "stress.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	concurrency := 20
	opsPerWorker := 25

	errCh := make(chan error, concurrency)

	for w := 0; w < concurrency; w++ {
		workerID := w
		go func() {
			for i := 0; i < opsPerWorker; i++ {
				// 1. Write job
				jobID := filepath.Join(tempDir, "job") // unique format
				job := DownloadJobEntity{
					ID:          "job-" + string(rune('a'+workerID)) + "-" + string(rune('0'+(i%10))),
					Status:      "DOWNLOADING",
					Source:      "soundcloud",
					URL:         "https://soundcloud.com/test",
					Progress:    float64(i * 4),
					CurrentStep: "Transcoding",
				}
				if err := database.SaveJob(ctx, job); err != nil {
					errCh <- err
					return
				}

				// 2. Read jobs
				if _, err := database.GetAllJobs(ctx); err != nil {
					errCh <- err
					return
				}

				_ = jobID
			}
			errCh <- nil
		}()
	}

	for w := 0; w < concurrency; w++ {
		if err := <-errCh; err != nil {
			t.Fatalf("concurrent worker failed with error: %v", err)
		}
	}
}

func TestDB_GetTracksMetadataCache(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "db_cache_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer database.Close()

	ctx := context.Background()

	// Вставляем тестового артиста, альбом, жанр и трек
	_, err = database.conn.ExecContext(ctx, `
		INSERT INTO artists (id, name, clean_name) VALUES (1, 'Test Artist', 'test artist');
		INSERT INTO albums (id, artist_id, title, clean_title, album_artist, year, folder_path, has_cover) 
		VALUES (1, 1, 'Test Album', 'test album', 'Test Artist', 2024, 'Test Artist - Test Album', 1);
		INSERT INTO genres (id, name) VALUES (1, 'Rock'), (2, 'Metal');
		INSERT INTO tracks (id, album_id, artist_id, title, clean_title, track_number, track_total, disc_number, disc_total, duration, file_path, file_size, mtime, has_embedded_cover, lyrics_status)
		VALUES (1, 1, 1, 'Test Song', 'test song', 3, 10, 1, 1, 215.5, 'Test Artist - Test Album/03. Test Song.mp3', 5000000, 1234567.0, 1, 'lrc');
		INSERT INTO track_genres (track_id, genre_id) VALUES (1, 1), (1, 2);
	`)
	if err != nil {
		t.Fatalf("failed to seed test data: %v", err)
	}

	cache, err := database.GetTracksMetadataCache(ctx)
	if err != nil {
		t.Fatalf("GetTracksMetadataCache failed: %v", err)
	}

	if len(cache) != 1 {
		t.Fatalf("expected 1 cached track, got %d", len(cache))
	}

	meta, found := cache["Test Artist - Test Album/03. Test Song.mp3"]
	if !found {
		t.Fatalf("expected key 'Test Artist - Test Album/03. Test Song.mp3' in cache")
	}

	if meta.Title != "Test Song" || meta.Artist != "Test Artist" || meta.Album != "Test Album" {
		t.Errorf("unexpected track meta: %+v", meta)
	}
	if meta.TrackNumber != 3 || meta.TrackTotal != 10 {
		t.Errorf("unexpected track numbers: %d/%d", meta.TrackNumber, meta.TrackTotal)
	}
	if meta.Year != 2024 {
		t.Errorf("expected year 2024, got %d", meta.Year)
	}
	if !meta.HasCover {
		t.Errorf("expected HasCover to be true")
	}
	if meta.LyricsStatus != "lrc" {
		t.Errorf("expected lyrics_status 'lrc', got %q", meta.LyricsStatus)
	}
	if meta.Genre != "Rock\\\\Metal" {
		t.Errorf("expected genre 'Rock\\\\Metal', got %q", meta.Genre)
	}
}

func TestRadarQueries(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "db_radar_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer database.Close()

	ctx := context.Background()

	// 1. Тест Tracked Artists
	if err := database.UpsertTrackedArtist(ctx, "Daft Punk"); err != nil {
		t.Fatalf("UpsertTrackedArtist failed: %v", err)
	}
	if err := database.UpsertTrackedArtist(ctx, "The Weeknd"); err != nil {
		t.Fatalf("UpsertTrackedArtist failed: %v", err)
	}

	artists, err := database.GetTrackedArtists(ctx)
	if err != nil {
		t.Fatalf("GetTrackedArtists failed: %v", err)
	}
	if len(artists) != 2 {
		t.Fatalf("expected 2 tracked artists, got %d", len(artists))
	}

	if err := database.UpdateArtistCustomLinks(ctx, "Daft Punk", "DaftPunkAlias", "dz-123", "mb-456", "ya-789"); err != nil {
		t.Fatalf("UpdateArtistCustomLinks failed: %v", err)
	}
	if err := database.UpdateArtistLastChecked(ctx, "Daft Punk"); err != nil {
		t.Fatalf("UpdateArtistLastChecked failed: %v", err)
	}

	artistDP, err := database.GetTrackedArtistByName(ctx, "daft punk")
	if err != nil {
		t.Fatalf("GetTrackedArtistByName failed: %v", err)
	}
	if artistDP.CustomSearchAlias != "DaftPunkAlias" || artistDP.DeezerID != "dz-123" || artistDP.LastCheckedAt == nil {
		t.Fatalf("unexpected artist custom links: %+v", artistDP)
	}

	// 2. Тест Radar Releases
	rel1 := &RadarReleaseEntity{
		ArtistName:  "Daft Punk",
		Title:       "Random Access Memories (10th Anniversary)",
		ReleaseType: "album",
		ReleaseDate: "2023-05-12",
		Year:        2023,
		CoverURL:    "https://example.com/ram.jpg",
		TrackCount:  13,
		DeezerID:    "dz-ram",
		SourceURL:   "https://deezer.com/album/ram",
		Status:      "new",
		IsRadar:     true,
	}
	if err := database.UpsertRadarRelease(ctx, rel1); err != nil {
		t.Fatalf("UpsertRadarRelease failed: %v", err)
	}

	rel2 := &RadarReleaseEntity{
		ArtistName:  "Daft Punk",
		Title:       "Homework",
		ReleaseType: "album",
		ReleaseDate: "1997-01-20",
		Year:        1997,
		CoverURL:    "https://example.com/hw.jpg",
		TrackCount:  16,
		SourceURL:   "https://deezer.com/album/hw",
		Status:      "missing",
		IsRadar:     false,
	}
	if err := database.UpsertRadarRelease(ctx, rel2); err != nil {
		t.Fatalf("UpsertRadarRelease 2 failed: %v", err)
	}

	unreadCount, err := database.GetRadarUnreadCount(ctx)
	if err != nil {
		t.Fatalf("GetRadarUnreadCount failed: %v", err)
	}
	if unreadCount != 1 {
		t.Fatalf("expected 1 unread radar release, got %d", unreadCount)
	}

	feed, total, err := database.GetRadarFeed(ctx, "all", "", 10, 0)
	if err != nil {
		t.Fatalf("GetRadarFeed failed: %v", err)
	}
	if total != 1 || len(feed) != 1 || feed[0].Title != rel1.Title {
		t.Fatalf("unexpected feed: total=%d, len=%d", total, len(feed))
	}

	disco, err := database.GetArtistDiscography(ctx, "daft punk")
	if err != nil {
		t.Fatalf("GetArtistDiscography failed: %v", err)
	}
	if len(disco) != 2 {
		t.Fatalf("expected 2 disco releases, got %d", len(disco))
	}

	// Проверяем что при установке статуса 'downloaded' релиз уходит из фида
	if err := database.SetRadarReleaseStatus(ctx, disco[0].ID, "downloaded"); err != nil {
		t.Fatalf("SetRadarReleaseStatus failed: %v", err)
	}
	feedAfterDl, totalAfterDl, err := database.GetRadarFeed(ctx, "all", "", 10, 0)
	if err != nil || totalAfterDl != 0 || len(feedAfterDl) != 0 {
		t.Fatalf("expected 0 releases in feed after downloaded, got total=%d len=%d", totalAfterDl, len(feedAfterDl))
	}

	// Тест ReconcileAllRadarReleases
	// Вставляем артиста и альбом в albums с clean_title = 'random access memories' (соответствует rel1, который со статусом downloaded)
	_, _ = database.Conn().ExecContext(ctx, "INSERT OR IGNORE INTO artists (id, name, clean_name) VALUES (1, 'Daft Punk', 'daft punk');")
	_, err = database.Conn().ExecContext(ctx, "INSERT INTO albums (title, clean_title, album_artist, artist_id, folder_path, deezer_id, expected_tracks_count) VALUES ('Random Access Memories (10th Anniversary)', 'random access memories', 'Daft Punk', 1, 'path', 'dz-ram', 13);")
	if err != nil {
		t.Fatalf("failed to insert album for reconcile test: %v", err)
	}
	for number := 1; number <= rel1.TrackCount; number++ {
		if _, err := database.Conn().ExecContext(ctx, `
			INSERT INTO tracks (album_id, artist_id, title, clean_title, track_number, file_path, file_size, mtime)
			SELECT id, artist_id, 'Track', 'track', ?, ?, 1000, 1 FROM albums WHERE folder_path = 'path'`,
			number, fmt.Sprintf("path/%02d.mp3", number)); err != nil {
			t.Fatal(err)
		}
	}
	var editionID int64
	if err := database.Conn().QueryRowContext(ctx, "SELECT id FROM albums WHERE folder_path = 'path'").Scan(&editionID); err != nil {
		t.Fatal(err)
	}
	catalog := make([]CatalogTrack, rel1.TrackCount)
	for i := range catalog {
		catalog[i] = CatalogTrack{BackfillSelection: BackfillSelection{TrackPosition: ReleasePosition(1, i+1)}, Title: "Track", Artist: "Daft Punk"}
	}
	if _, err := VerifyCatalog(ctx, database.Conn(), editionID, "dz-ram", catalog); err != nil {
		t.Fatal(err)
	}
	fixedCount, err := database.ReconcileAllRadarReleases(ctx)
	if err != nil {
		t.Fatalf("ReconcileAllRadarReleases failed: %v", err)
	}
	if fixedCount != 1 {
		t.Fatalf("expected 1 release reconciled, got %d", fixedCount)
	}

	// Добавляем свежий непрочитанный релиз для последующей проверки в ResetLibraryTables
	_ = database.UpsertRadarRelease(ctx, &RadarReleaseEntity{
		ArtistName:  "Daft Punk",
		Title:       "Fresh Track 2026",
		ReleaseType: "single",
		ReleaseDate: "2026-01-01",
		Year:        2026,
		SourceURL:   "https://example.com/fresh",
		Status:      "new",
		IsRadar:     true,
	})

	// Добавляем трек для проверки очистки индекса
	_, err = database.conn.ExecContext(ctx, `
		INSERT OR IGNORE INTO artists (id, name, clean_name) VALUES (1, 'Daft Punk', 'daft punk');
		INSERT OR IGNORE INTO albums (id, title, clean_title, artist_id, folder_path) VALUES (1, 'RAM', 'ram', 1, 'RAM');
		INSERT OR IGNORE INTO tracks (id, album_id, artist_id, title, clean_title, file_path, duration, bitrate, file_size, mtime, file_hash)
		VALUES (500, 1, 1, 'Get Lucky', 'get lucky', 'Daft Punk/RAM/01. Get Lucky.mp3', 248, 320, 10000000, CURRENT_TIMESTAMP, 'hash500');
	`)
	if err != nil {
		t.Fatalf("failed to insert test track: %v", err)
	}

	// 3. Тест сохранения персистентных данных при ResetLibraryTables
	if err := database.ResetLibraryTables(ctx); err != nil {
		t.Fatalf("ResetLibraryTables failed: %v", err)
	}

	// Проверяем, что кэш tracks/albums очищен
	var trackCount int
	_ = database.conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM tracks").Scan(&trackCount)
	if trackCount != 0 {
		t.Fatalf("expected tracks to be cleared, got %d", trackCount)
	}

	// Проверяем, что tracked_artists и radar_releases сохранены!
	artistsAfterReset, err := database.GetTrackedArtists(ctx)
	if err != nil || len(artistsAfterReset) != 2 {
		t.Fatalf("tracked_artists lost after ResetLibraryTables: err=%v, count=%d", err, len(artistsAfterReset))
	}
	unreadAfterReset, err := database.GetRadarUnreadCount(ctx)
	if err != nil || unreadAfterReset != 1 {
		t.Fatalf("radar_releases lost after ResetLibraryTables: err=%v, unread=%d", err, unreadAfterReset)
	}
}
