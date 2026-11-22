package downloader_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"music-manager/internal/core/auth"
	"music-manager/internal/features/downloader"
	"music-manager/internal/services/resolver"
	"music-manager/internal/testkit"
)

// mockResolver имплементирует резолвер для предсказуемых тестов Preflight и Download.
type mockResolver struct {
	meta   *resolver.ReleaseMetadata
	tracks []resolver.ResolvedTrack
	err    error
}

func (m *mockResolver) Resolve(ctx context.Context, rawURL, source string) (*resolver.ReleaseMetadata, []resolver.ResolvedTrack, error) {
	return m.meta, m.tracks, m.err
}

func (m *mockResolver) DetectSource(rawURL string) (string, error) {
	return "mock", nil
}

func (m *mockResolver) Matcher() *resolver.AudioMatcher {
	return nil
}

func TestDownloader_Preflight_DeduplicationAndSelection(t *testing.T) {
	env := testkit.NewEnv(t)

	// Вставляем артиста
	res, err := env.DB.Conn().Exec(`INSERT INTO artists (name, clean_name) VALUES ('Daft Punk', 'daft punk')`)
	if err != nil {
		t.Fatalf("failed to insert artist: %v", err)
	}
	artistID, _ := res.LastInsertId()

	// Вставляем альбом
	res, err = env.DB.Conn().Exec(`INSERT INTO albums (artist_id, title, clean_title, year, folder_path) VALUES (?, 'Discovery', 'discovery', 2001, 'Daft Punk/Discovery')`, artistID)
	if err != nil {
		t.Fatalf("failed to insert album: %v", err)
	}
	albumID, _ := res.LastInsertId()

	// Вставляем трек 1: High quality (320k)
	_, err = env.DB.Conn().Exec(`
		INSERT INTO tracks (artist_id, album_id, title, clean_title, file_path, bitrate, duration, file_size, mtime)
		VALUES (?, ?, 'One More Time', 'one more time', 'Daft Punk/Discovery/01. One More Time.mp3', 320000, 320, 10000000, 1600000000.0)
	`, artistID, albumID)
	if err != nil {
		t.Fatalf("failed to insert track 1: %v", err)
	}

	// Вставляем трек 2: Low quality (128k, подлежит апгрейду)
	_, err = env.DB.Conn().Exec(`
		INSERT INTO tracks (artist_id, album_id, title, clean_title, file_path, bitrate, duration, file_size, mtime)
		VALUES (?, ?, 'Aerodynamic', 'aerodynamic', 'Daft Punk/Discovery/02. Aerodynamic.mp3', 128000, 210, 5000000, 1600000000.0)
	`, artistID, albumID)
	if err != nil {
		t.Fatalf("failed to insert track 2: %v", err)
	}

	repo := downloader.NewRepository(env.DB.Conn())

	// Мок Deezer Album API
	deezerAlbum := resolver.DeezerAlbum{
		ID:          302127,
		Title:       "Discovery",
		ReleaseDate: "2001-03-12",
		Artist: resolver.DeezerArtist{
			Name: "Daft Punk",
		},
		Tracks: &resolver.DeezerTrackList{
			Data: []resolver.DeezerTrack{
				{ID: 101, Title: "One More Time", Duration: 320, TrackPosition: 1, DiskNumber: 1, Artist: resolver.DeezerArtist{Name: "Daft Punk"}},
				{ID: 102, Title: "Aerodynamic", Duration: 210, TrackPosition: 2, DiskNumber: 1, Artist: resolver.DeezerArtist{Name: "Daft Punk"}},
				{ID: 103, Title: "Digital Love", Duration: 301, TrackPosition: 3, DiskNumber: 1, Artist: resolver.DeezerArtist{Name: "Daft Punk"}},
			},
		},
	}
	env.MockHTTP.RegisterJSON("https://api.deezer.com/album/302127", http.StatusOK, deezerAlbum)

	deezerClient := resolver.NewDeezerClient(env.MockHTTP)
	sourceRes := resolver.NewSourceResolver(deezerClient, nil, "")

	svc := downloader.NewService(downloader.ServiceConfig{
		Repo:          repo,
		Resolver:      sourceRes,
		TmpSandbox:    env.TmpSandbox,
		MusicSandbox:  env.MusicSandbox,
		MaxConcurrent: 2,
	})

	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("failed to start service: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	mod := downloader.NewModule(svc)

	r := chi.NewRouter()
	authMiddleware := auth.Middleware(env.SecretKey)
	r.Group(func(r chi.Router) {
		r.Use(authMiddleware)
		mod.RegisterRoutes(r)
	})
	env.Mount(r)
	env.SetAuthUser("admin")

	t.Run("CheckTrackInLibrary directly", func(t *testing.T) {
		info1, err := repo.CheckTrackInLibrary(context.Background(), "One More Time", "Daft Punk")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if info1 == nil || info1.QualityTier != "true_320k" {
			t.Fatalf("expected true_320k, got %+v", info1)
		}

		info2, err := repo.CheckTrackInLibrary(context.Background(), "Aerodynamic", "Daft Punk")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if info2 == nil || info2.QualityTier != "upscaled_128k" {
			t.Fatalf("expected upscaled_128k, got %+v", info2)
		}

		info3, err := repo.CheckTrackInLibrary(context.Background(), "Digital Love", "Daft Punk")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if info3 != nil {
			t.Fatalf("expected nil for missing track, got %+v", info3)
		}
	})

	t.Run("CreateJobWithOptions preserves SelectedTrackIndices", func(t *testing.T) {
		const url = "https://www.deezer.com/album/302127"
		preview, err := svc.PreflightRelease(t.Context(), url, "deezer")
		if err != nil {
			t.Fatal(err)
		}
		job, err := svc.CreateJobWithOptions(t.Context(), url, "deezer", downloader.JobOptions{
			PreflightID:          preview.PreflightID,
			SelectedTrackIndices: []int{0, 2},
			SkipExisting:         true,
		})
		if err != nil {
			t.Fatalf("failed to create job: %v", err)
		}
		snap := job.Snapshot()
		if len(snap.SelectedTrackIndices) != 2 || snap.SelectedTrackIndices[0] != 0 || snap.SelectedTrackIndices[1] != 2 || !snap.SkipExisting {
			t.Fatalf("unexpected download selection: indices=%v, skip existing=%t", snap.SelectedTrackIndices, snap.SkipExisting)
		}
	})

	t.Run("POST /downloads/preflight validation", func(t *testing.T) {
		// Empty body / invalid URL should return error
		resp := env.PostJSON("/downloads/preflight", downloader.PreflightRequest{URL: ""})
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected status 400 for empty url, got %d", resp.StatusCode)
		}

		// Disallowed domain should return 400
		resp = env.PostJSON("/downloads/preflight", downloader.PreflightRequest{URL: "https://evil.com/hack"})
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected status 400 for disallowed domain, got %d", resp.StatusCode)
		}
	})

	t.Run("POST /downloads/preflight full release resolution", func(t *testing.T) {
		resp := env.PostJSON("/downloads/preflight", downloader.PreflightRequest{
			URL:    "https://www.deezer.com/album/302127",
			Source: "deezer",
		})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d: %s", resp.StatusCode, string(env.ReadBody(resp)))
		}

		var preflightResp downloader.PreflightResponse
		env.DecodeJSON(resp, &preflightResp)

		if preflightResp.Release.Title != "Discovery" {
			t.Errorf("expected title 'Discovery', got %q", preflightResp.Release.Title)
		}
		if preflightResp.Summary.TotalTracks != 3 {
			t.Errorf("expected 3 total tracks, got %d", preflightResp.Summary.TotalTracks)
		}
		if preflightResp.Summary.ExistingHighQualityCount != 1 {
			t.Errorf("expected 1 high quality existing, got %d", preflightResp.Summary.ExistingHighQualityCount)
		}
		if preflightResp.Summary.UpgradeAvailableCount != 1 {
			t.Errorf("expected 1 upgrade available, got %d", preflightResp.Summary.UpgradeAvailableCount)
		}
		if preflightResp.Summary.NewTracksCount != 1 {
			t.Errorf("expected 1 new track, got %d", preflightResp.Summary.NewTracksCount)
		}

		if len(preflightResp.Tracks) != 3 {
			t.Fatalf("expected 3 tracks, got %d", len(preflightResp.Tracks))
		}

		// Track 0: One More Time -> EXISTS_HIGH_QUALITY, DefaultSelect false
		tr0 := preflightResp.Tracks[0]
		if tr0.Status != downloader.TrackStatusExistsHighQuality || tr0.DefaultSelect != false {
			t.Errorf("track 0: expected EXISTS_HIGH_QUALITY/false, got %s/%v", tr0.Status, tr0.DefaultSelect)
		}

		// Track 1: Aerodynamic -> EXISTS_UPGRADE_AVAILABLE, DefaultSelect true
		tr1 := preflightResp.Tracks[1]
		if tr1.Status != downloader.TrackStatusExistsUpgrade || tr1.DefaultSelect != true {
			t.Errorf("track 1: expected EXISTS_UPGRADE_AVAILABLE/true, got %s/%v", tr1.Status, tr1.DefaultSelect)
		}

		// Track 2: Digital Love -> NEW, DefaultSelect true
		tr2 := preflightResp.Tracks[2]
		if tr2.Status != downloader.TrackStatusNew || tr2.DefaultSelect != true {
			t.Errorf("track 2: expected NEW/true, got %s/%v", tr2.Status, tr2.DefaultSelect)
		}
	})

	t.Run("POST /downloads with selected_track_indices filters job", func(t *testing.T) {
		const url = "https://www.deezer.com/album/302127"
		previewResp := env.PostJSON("/downloads/preflight", downloader.PreflightRequest{URL: url, Source: "deezer"})
		var preview downloader.PreflightResponse
		env.DecodeJSON(previewResp, &preview)
		createResp := env.PostJSON("/downloads", downloader.CreateJobRequest{
			URL:                  "https://www.deezer.com/album/302127",
			Source:               "deezer",
			PreflightID:          preview.PreflightID,
			SelectedTrackIndices: []int{1, 2},
		})
		if createResp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", createResp.StatusCode)
		}

		var createdJob downloader.Job
		env.DecodeJSON(createResp, &createdJob)
		if len(createdJob.SelectedTrackIndices) != 2 {
			t.Fatalf("expected 2 selected track indices, got %d", len(createdJob.SelectedTrackIndices))
		}
	})
}
