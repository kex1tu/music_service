package db

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSQLite_HighConcurrencyWAL(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "concurrency_stress.db")

	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer database.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Предварительное заполнение базовыми данными для чтения
	for i := 1; i <= 20; i++ {
		artName := fmt.Sprintf("Artist %d", i)
		cleanArt := fmt.Sprintf("artist %d", i)
		if _, err := database.conn.ExecContext(ctx, "INSERT INTO artists (id, name, clean_name) VALUES (?, ?, ?)", i, artName, cleanArt); err != nil {
			t.Fatalf("failed to insert artist: %v", err)
		}
		if _, err := database.conn.ExecContext(ctx, "INSERT INTO tracked_artists (name, clean_name, is_followed) VALUES (?, ?, 1)", artName, cleanArt); err != nil {
			t.Fatalf("failed to insert tracked artist: %v", err)
		}
		if _, err := database.conn.ExecContext(ctx, "INSERT INTO genres (id, name) VALUES (?, ?)", i, fmt.Sprintf("Genre %d", i)); err != nil {
			t.Fatalf("failed to insert genre: %v", err)
		}
		if _, err := database.conn.ExecContext(ctx, "INSERT INTO albums (id, title, clean_title, album_artist, artist_id, folder_path) VALUES (?, ?, ?, ?, ?, ?)",
			i, fmt.Sprintf("Album %d", i), fmt.Sprintf("album %d", i), artName, i, fmt.Sprintf("Folder %d", i)); err != nil {
			t.Fatalf("failed to insert album: %v", err)
		}
		if _, err := database.conn.ExecContext(ctx, `INSERT INTO tracks (id, album_id, artist_id, title, clean_title, file_path, duration, bitrate, file_size, mtime, file_hash)
			VALUES (?, ?, ?, ?, ?, ?, 200, 320, 1000000, CURRENT_TIMESTAMP, ?)`,
			100+i, i, i, fmt.Sprintf("Track %d", i), fmt.Sprintf("track %d", i), fmt.Sprintf("path/%d.mp3", i), fmt.Sprintf("hash%04d", i)); err != nil {
			t.Fatalf("failed to insert track: %v", err)
		}
		if _, err := database.conn.ExecContext(ctx, "INSERT INTO track_genres (track_id, genre_id) VALUES (?, ?)", 100+i, i); err != nil {
			t.Fatalf("failed to insert track_genres: %v", err)
		}
	}

	start := time.Now()
	const numGoroutines = 50
	const opsPerGoroutine = 10 // Всего 500 смешанных операций

	var wg sync.WaitGroup
	errCh := make(chan error, numGoroutines*opsPerGoroutine)

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		goroutineID := g

		go func(gid int) {
			defer wg.Done()

			for op := 0; op < opsPerGoroutine; op++ {
				switch gid % 4 {
				case 0:
					// Группа 1: Запись и чтение очереди загрузки
					jobID := fmt.Sprintf("job-%d-%d", gid, op)
					job := DownloadJobEntity{
						ID:       jobID,
						URL:      "https://soundcloud.com/test",
						Source:   "soundcloud",
						Status:   "pending",
						Progress: float64(op * 10),
					}
					if err := database.SaveJob(ctx, job); err != nil {
						errCh <- fmt.Errorf("gid %d: SaveJob error: %w", gid, err)
						return
					}
					fetchedJob, err := database.GetJob(ctx, jobID)
					if err != nil || fetchedJob == nil {
						errCh <- fmt.Errorf("gid %d: GetJob error: %w", gid, err)
						return
					}
					if err := database.DeleteJob(ctx, jobID); err != nil {
						errCh <- fmt.Errorf("gid %d: DeleteJob error: %w", gid, err)
						return
					}

				case 1:
					// Группа 2: Чтение треков и обновление радар-релизов
					hash := fmt.Sprintf("hash%04d", 1+(op%20))
					tr, err := database.GetTrackByIDOrHash(ctx, hash)
					if err != nil || tr == nil {
						errCh <- fmt.Errorf("gid %d: GetTrackByIDOrHash error: %w", gid, err)
						return
					}
					rel := &RadarReleaseEntity{
						DeezerID:        fmt.Sprintf("dz-%d-%d", gid, op),
						Title:           fmt.Sprintf("Release %d-%d", gid, op),
						CleanTitle:      fmt.Sprintf("release %d %d", gid, op),
						ArtistName:      "Artist 1",
						CleanArtistName: "artist 1",
						ReleaseType:     "single",
						SourceURL:       "https://deezer.com/album/1",
						Status:          "new",
						IsRadar:         true,
					}
					if err := database.UpsertRadarRelease(ctx, rel); err != nil {
						errCh <- fmt.Errorf("gid %d: UpsertRadarRelease error: %w", gid, err)
						return
					}

				case 2:
					// Группа 3: Полнотекстовый поиск FTS и выборки списков
					res, err := database.SearchLibrary(ctx, "Artist", 10)
					if err != nil || res == nil {
						errCh <- fmt.Errorf("gid %d: SearchLibrary error: %w", gid, err)
						return
					}
					artists, err := database.GetTrackedArtists(ctx)
					if err != nil || len(artists) == 0 {
						errCh <- fmt.Errorf("gid %d: GetTrackedArtists error: %w", gid, err)
						return
					}

				case 3:
					// Группа 4: Реконсиляция и выборка ленты радара
					feed, _, err := database.GetRadarFeed(ctx, "all", "", 10, 0)
					if err != nil {
						errCh <- fmt.Errorf("gid %d: GetRadarFeed error: %w", gid, err)
						return
					}
					_ = feed
					_, _ = database.ReconcileAllRadarReleases(ctx)
				}
			}
		}(goroutineID)
	}

	wg.Wait()
	close(errCh)

	duration := time.Since(start)
	t.Logf("Completed 500 mixed operations across 50 concurrent goroutines in %v", duration)

	var errList []error
	for e := range errCh {
		errList = append(errList, e)
	}

	if len(errList) > 0 {
		t.Fatalf("Encountered %d errors during high concurrency test. First error: %v", len(errList), errList[0])
	}

	if duration > 5*time.Second {
		t.Errorf("expected 500 operations to complete within 5 seconds, took %v", duration)
	}
}
