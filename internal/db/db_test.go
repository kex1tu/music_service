package db

import (
	"context"
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

