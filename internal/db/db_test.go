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

func TestSoundCloudCache(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "db_sc_test_*")
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

	// 1. TTL тест: запись старше 30 дней с is_manual = 0 протухает (nil), а с is_manual = 1 возвращается
	_, err = database.conn.ExecContext(ctx, `
		INSERT INTO soundcloud_uncensored_cache (
			cache_key, track_title, artist_name, selected_url, status, duration, is_manual, candidates_json, checked_at
		) VALUES ('key_expired_auto', 'Title', 'Artist', 'https://sc.com/1', 'likely', 180, 0, '[]', datetime('now', '-35 days')),
		         ('key_expired_manual', 'Title', 'Artist', 'https://sc.com/2', 'manual', 180, 1, '[]', datetime('now', '-35 days'));
	`)
	if err != nil {
		t.Fatalf("failed to insert expired entries: %v", err)
	}

	resAuto, err := database.GetSoundCloudCache(ctx, "key_expired_auto", 720)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resAuto != nil {
		t.Errorf("expected expired auto entry to be nil (cache miss), got: %+v", resAuto)
	}

	resManual, err := database.GetSoundCloudCache(ctx, "key_expired_manual", 720)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resManual == nil || resManual.SelectedURL != "https://sc.com/2" {
		t.Errorf("expected expired manual entry to be returned, got: %+v", resManual)
	}

	// 2. link_dead тест: автоматическая запись с status = 'link_dead' или selected_url = '' возвращает nil (MISS)
	_, err = database.conn.ExecContext(ctx, `
		INSERT INTO soundcloud_uncensored_cache (
			cache_key, track_title, artist_name, selected_url, status, duration, is_manual, candidates_json, checked_at
		) VALUES ('key_dead_auto', 'Title', 'Artist', 'https://sc.com/dead', 'link_dead', 180, 0, '[]', datetime('now')),
		         ('key_empty_url', 'Title', 'Artist', '', 'marked', 180, 0, '[]', datetime('now'));
	`)
	if err != nil {
		t.Fatalf("failed to insert dead entries: %v", err)
	}

	deadAuto, err := database.GetSoundCloudCache(ctx, "key_dead_auto", 720)
	if err != nil || deadAuto != nil {
		t.Errorf("expected dead auto entry to return nil, err: %v, entry: %+v", err, deadAuto)
	}
	emptyUrl, err := database.GetSoundCloudCache(ctx, "key_empty_url", 720)
	if err != nil || emptyUrl != nil {
		t.Errorf("expected empty selected_url to return nil, err: %v, entry: %+v", err, emptyUrl)
	}

	// 3. Ротация 404: InvalidateSoundCloudURL удаляет битый URL и переключается на следующий лучший из candidates_json
	cands := []SoundCloudCandidateItem{
		{URL: "https://sc.com/first", Title: "Track 1", Duration: 180, Score: 90, Status: "marked"},
		{URL: "https://sc.com/second", Title: "Track 2", Duration: 182, Score: 85, Status: "likely"},
	}
	saveEntry := SoundCloudCacheEntry{
		CacheKey:    "key_rotation",
		TrackTitle:  "Track",
		ArtistName:  "Artist",
		SelectedURL: "https://sc.com/first",
		Status:      "marked",
		Duration:    180,
		IsManual:    false,
		Candidates:  cands,
	}
	if err := database.SaveSoundCloudCache(ctx, saveEntry); err != nil {
		t.Fatalf("failed to save cache entry: %v", err)
	}

	// Инвалидируем первый URL
	inv1, err := database.InvalidateSoundCloudURL(ctx, "key_rotation", "https://sc.com/first")
	if err != nil {
		t.Fatalf("failed to invalidate URL: %v", err)
	}
	if inv1.SelectedURL != "https://sc.com/second" || inv1.Status != "likely" || inv1.Duration != 182 {
		t.Errorf("unexpected rotated entry: %+v", inv1)
	}
	if len(inv1.Candidates) != 1 {
		t.Errorf("expected 1 remaining candidate, got %d", len(inv1.Candidates))
	}

	// Инвалидируем второй (последний) URL
	inv2, err := database.InvalidateSoundCloudURL(ctx, "key_rotation", "https://sc.com/second")
	if err != nil {
		t.Fatalf("failed to invalidate URL: %v", err)
	}
	if inv2.SelectedURL != "" || inv2.Status != "link_dead" {
		t.Errorf("expected empty url and link_dead status, got: %+v", inv2)
	}

	// 4. Защита ручного пина: автоматический резолвер не перезаписывает строку с is_manual = 1
	if err := database.PinSoundCloudVersion(ctx, "key_pin", "Artist", "Title", "https://sc.com/pinned", 200); err != nil {
		t.Fatalf("failed to pin version: %v", err)
	}

	// Попытка авто-сохранения поверх ручного пина
	autoOverwrite := SoundCloudCacheEntry{
		CacheKey:    "key_pin",
		TrackTitle:  "Overwritten Title",
		ArtistName:  "Artist",
		SelectedURL: "https://sc.com/auto",
		Status:      "likely",
		Duration:    150,
		IsManual:    false,
	}
	if err := database.SaveSoundCloudCache(ctx, autoOverwrite); err != nil {
		t.Fatalf("failed to run SaveSoundCloudCache: %v", err)
	}

	pinnedGet, err := database.GetSoundCloudCache(ctx, "key_pin", 720)
	if err != nil || pinnedGet == nil {
		t.Fatalf("failed to get pinned entry: %v", err)
	}
	if pinnedGet.SelectedURL != "https://sc.com/pinned" || !pinnedGet.IsManual || pinnedGet.Duration != 200 {
		t.Errorf("pinned entry was overwritten! got: %+v", pinnedGet)
	}

	// 5. Безопасный UPSERT: повторное закрепление не затирает ранее сохранённый candidates_json
	upsertEntry := SoundCloudCacheEntry{
		CacheKey:    "key_upsert",
		TrackTitle:  "Track",
		ArtistName:  "Artist",
		SelectedURL: "https://sc.com/orig",
		Status:      "marked",
		Duration:    190,
		IsManual:    false,
		Candidates:  cands,
	}
	if err := database.SaveSoundCloudCache(ctx, upsertEntry); err != nil {
		t.Fatalf("failed to save initial entry: %v", err)
	}

	// Закрепляем вручную
	if err := database.PinSoundCloudVersion(ctx, "key_upsert", "Artist", "Track", "https://sc.com/manual_choice", 195); err != nil {
		t.Fatalf("failed to pin version: %v", err)
	}

	retrievedPinned, err := database.GetSoundCloudCache(ctx, "key_upsert", 720)
	if err != nil || retrievedPinned == nil {
		t.Fatalf("failed to get pinned entry: %v", err)
	}
	if retrievedPinned.SelectedURL != "https://sc.com/manual_choice" || !retrievedPinned.IsManual {
		t.Errorf("pin failed: %+v", retrievedPinned)
	}
	if len(retrievedPinned.Candidates) != 2 {
		t.Errorf("candidates_json was wiped out! expected 2 candidates, got %d", len(retrievedPinned.Candidates))
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

				// 3. Save cache
				cacheEntry := SoundCloudCacheEntry{
					CacheKey:    "key-" + string(rune('a'+workerID)),
					TrackTitle:  "Title",
					ArtistName:  "Artist",
					SelectedURL: "https://soundcloud.com/1",
					Status:      "likely",
					Duration:    180,
				}
				if err := database.SaveSoundCloudCache(ctx, cacheEntry); err != nil {
					errCh <- err
					return
				}

				// 4. Read cache
				if _, err := database.GetSoundCloudCache(ctx, "key-"+string(rune('a'+workerID)), 720); err != nil {
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

func TestDB_PinnedSources(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "db_pinned_test_*")
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

	// 1. Проверяем сохранение и получение pinned source
	pin := PinnedTrackSource{
		CacheKey:   "artist_track_key",
		ArtistName: "The Beatles",
		TrackTitle: "Yesterday",
		PinnedURL:  "https://www.youtube.com/watch?v=pinned123",
		SourceType: "manual",
	}

	if err := database.SavePinnedSource(ctx, pin); err != nil {
		t.Fatalf("failed to save pinned source: %v", err)
	}

	retrieved, err := database.GetPinnedSource(ctx, "artist_track_key")
	if err != nil {
		t.Fatalf("failed to get pinned source: %v", err)
	}
	if retrieved == nil {
		t.Fatal("expected retrieved pinned source, got nil")
	}
	if retrieved.PinnedURL != pin.PinnedURL {
		t.Errorf("expected url %s, got %s", pin.PinnedURL, retrieved.PinnedURL)
	}
	if retrieved.SourceType != "manual" {
		t.Errorf("expected source_type manual, got %s", retrieved.SourceType)
	}

	// Обновление существующего пина (UPSERT)
	pin.PinnedURL = "https://soundcloud.com/thebeatles/yesterday"
	if err := database.SavePinnedSource(ctx, pin); err != nil {
		t.Fatalf("failed to update pinned source: %v", err)
	}

	retrievedUpdated, err := database.GetPinnedSource(ctx, "artist_track_key")
	if err != nil {
		t.Fatalf("failed to get updated pinned source: %v", err)
	}
	if retrievedUpdated.PinnedURL != pin.PinnedURL {
		t.Errorf("expected updated url %s, got %s", pin.PinnedURL, retrievedUpdated.PinnedURL)
	}

	// Удаление пина
	if err := database.DeletePinnedSource(ctx, "artist_track_key"); err != nil {
		t.Fatalf("failed to delete pinned source: %v", err)
	}
	deleted, err := database.GetPinnedSource(ctx, "artist_track_key")
	if err != nil {
		t.Fatalf("unexpected error getting deleted pin: %v", err)
	}
	if deleted != nil {
		t.Fatalf("expected nil for deleted pinned source, got %+v", deleted)
	}

	// 2. Тест обновления pinned_source_url у tracks
	_, err = database.conn.ExecContext(ctx, `
		INSERT INTO artists (id, name, clean_name) VALUES (10, 'The Beatles', 'the beatles');
		INSERT INTO albums (id, artist_id, title, clean_title, album_artist, year, folder_path, has_cover) 
		VALUES (10, 10, 'Help!', 'help', 'The Beatles', 1965, 'The Beatles - Help!', 0);
		INSERT INTO tracks (id, album_id, artist_id, title, clean_title, track_number, duration, bitrate, file_path, file_size, mtime)
		VALUES (100, 10, 10, 'Yesterday', 'yesterday', 1, 125, 320, 'The Beatles/Help!/01. Yesterday.mp3', 1000, 1234567.0);
	`)
	if err != nil {
		t.Fatalf("failed to insert test track: %v", err)
	}

	if err := database.UpdateTrackPinnedURL(ctx, 100, "https://soundcloud.com/thebeatles/yesterday"); err != nil {
		t.Fatalf("UpdateTrackPinnedURL failed: %v", err)
	}

	var trackPinned string
	err = database.conn.QueryRowContext(ctx, "SELECT COALESCE(pinned_source_url, '') FROM tracks WHERE id = 100").Scan(&trackPinned)
	if err != nil {
		t.Fatalf("query track failed: %v", err)
	}
	if trackPinned != "https://soundcloud.com/thebeatles/yesterday" {
		t.Errorf("expected pinned url 'https://soundcloud.com/thebeatles/yesterday', got %q", trackPinned)
	}

	// 3. Тест UpdateMissingTrackPinnedURL
	_, err = database.conn.ExecContext(ctx, `
		INSERT INTO missing_tracks (id, album_id, track_number, title, artist) 
		VALUES (200, 10, 2, 'Act Naturally', 'The Beatles');
	`)
	if err != nil {
		t.Fatalf("failed to insert test missing track: %v", err)
	}

	if err := database.UpdateMissingTrackPinnedURL(ctx, 200, "https://youtube.com/watch?v=123"); err != nil {
		t.Fatalf("UpdateMissingTrackPinnedURL failed: %v", err)
	}

	var missingPinned string
	err = database.conn.QueryRowContext(ctx, "SELECT COALESCE(pinned_source_url, '') FROM missing_tracks WHERE id = 200").Scan(&missingPinned)
	if err != nil {
		t.Fatalf("query missing track failed: %v", err)
	}
	if missingPinned != "https://youtube.com/watch?v=123" {
		t.Errorf("expected missing track pinned url 'https://youtube.com/watch?v=123', got %q", missingPinned)
	}
}

func TestRadarAndRadioQueries(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "db_radar_radio_test_*")
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

	artists, err := database.GetTrackedArtists(ctx, false)
	if err != nil {
		t.Fatalf("GetTrackedArtists failed: %v", err)
	}
	if len(artists) != 2 {
		t.Fatalf("expected 2 tracked artists, got %d", len(artists))
	}

	if err := database.SetArtistAutoDownload(ctx, "Daft Punk", true); err != nil {
		t.Fatalf("SetArtistAutoDownload failed: %v", err)
	}
	if err := database.SetArtistFollowed(ctx, "The Weeknd", false); err != nil {
		t.Fatalf("SetArtistFollowed failed: %v", err)
	}

	followedOnly, err := database.GetTrackedArtists(ctx, true)
	if err != nil {
		t.Fatalf("GetTrackedArtists(followed=true) failed: %v", err)
	}
	if len(followedOnly) != 1 || followedOnly[0].Name != "Daft Punk" || !followedOnly[0].AutoDownload {
		t.Fatalf("unexpected followed artists: %+v", followedOnly)
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

	// 3. Тест Artist Relations
	sims := []ArtistRelationItem{
		{ArtistName: "Daft Punk", RelatedName: "Justice", Score: 0.95, Source: "deezer"},
		{ArtistName: "Daft Punk", RelatedName: "Kavinsky", Score: 0.88, Source: "lastfm"},
	}
	if err := database.SaveArtistRelations(ctx, "Daft Punk", sims); err != nil {
		t.Fatalf("SaveArtistRelations failed: %v", err)
	}
	related, err := database.GetRelatedArtists(ctx, "Daft Punk", 5)
	if err != nil {
		t.Fatalf("GetRelatedArtists failed: %v", err)
	}
	if len(related) != 2 || related[0] != "justice" {
		t.Fatalf("unexpected related artists: %+v", related)
	}

	// 4. Тест Radio History
	// Создаем тестовый трек для foreign key
	_, err = database.conn.ExecContext(ctx, `
		INSERT OR IGNORE INTO artists (id, name, clean_name) VALUES (1, 'Daft Punk', 'daft punk');
		INSERT OR IGNORE INTO albums (id, title, clean_title, artist_id, folder_path) VALUES (1, 'RAM', 'ram', 1, 'RAM');
		INSERT OR IGNORE INTO tracks (id, album_id, artist_id, title, clean_title, file_path, duration, bitrate, file_size, mtime, file_hash)
		VALUES (500, 1, 1, 'Get Lucky', 'get lucky', 'Daft Punk/RAM/01. Get Lucky.mp3', 248, 320, 10000000, CURRENT_TIMESTAMP, 'hash500');
	`)
	if err != nil {
		t.Fatalf("failed to insert test track for radio: %v", err)
	}

	if err := database.RecordRadioPlay(ctx, 500, "wave", ""); err != nil {
		t.Fatalf("RecordRadioPlay failed: %v", err)
	}
	recentTracks, err := database.GetRecentRadioTracks(ctx, "wave", 6)
	if err != nil {
		t.Fatalf("GetRecentRadioTracks failed: %v", err)
	}
	if len(recentTracks) != 1 || recentTracks[0] != 500 {
		t.Fatalf("unexpected recent tracks: %+v", recentTracks)
	}

	recentArtists, err := database.GetRecentRadioArtists(ctx, "wave", 5)
	if err != nil {
		t.Fatalf("GetRecentRadioArtists failed: %v", err)
	}
	if len(recentArtists) != 1 || recentArtists[0] != "daft punk" {
		t.Fatalf("unexpected recent artists: %+v", recentArtists)
	}

	// 5. Тест сохранения персистентных данных при ResetLibraryTables
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
	artistsAfterReset, err := database.GetTrackedArtists(ctx, false)
	if err != nil || len(artistsAfterReset) != 2 {
		t.Fatalf("tracked_artists lost after ResetLibraryTables: err=%v, count=%d", err, len(artistsAfterReset))
	}
	unreadAfterReset, err := database.GetRadarUnreadCount(ctx)
	if err != nil || unreadAfterReset != 1 {
		t.Fatalf("radar_releases lost after ResetLibraryTables: err=%v, unread=%d", err, unreadAfterReset)
	}
}
