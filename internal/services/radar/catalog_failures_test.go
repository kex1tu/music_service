package radar

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"music-manager/internal/db"
	"music-manager/internal/services/resolver"
	"music-manager/internal/testkit"
)

const radarFixtureArtist = "Fixture Band"

func radarCatalogFixture(t *testing.T, handle http.HandlerFunc) (*Service, *db.DB) {
	t.Helper()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	addRadarLibraryAlbum(t, database, radarFixtureArtist)
	if err := database.UpdateArtistCustomLinks(t.Context(), radarFixtureArtist, "", "51", "fixture-artist", ""); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handle)
	t.Cleanup(server.Close)
	deezer := resolver.NewDeezerClient(server.Client().Transport)
	deezer.SetBaseURL(server.URL)
	mb := resolver.NewMusicBrainzClient("")
	mb.SetBaseURL(server.URL)
	return NewService(database, resolver.NewDiscographyAggregator(deezer, mb, nil, database), nil), database
}

func writeRadarFixtureCatalog(w http.ResponseWriter, r *http.Request, date string) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/artist/51/albums":
		fmt.Fprintf(w, `{"data":[{"id":55,"title":"Catalog release","release_date":%q,"nb_tracks":3,
			"record_type":"album","artist":{"name":"Fixture Band"},"contributors":[{"name":"Fixture Band"}]}]}`, date)
	case "/release-group":
		fmt.Fprintf(w, `{"release-groups":[{"id":"linked-group","title":"Catalog release","primary-type":"Album",
			"first-release-date":%q,"artist-credit":[{"name":"Fixture Band"}],
			"relations":[{"url":{"resource":"https://www.deezer.com/album/55"}}]}]}`, date)
	default:
		http.NotFound(w, r)
	}
}

func TestRadarPartialCatalogFailureKeepsHealthySource(t *testing.T) {
	for _, tc := range []struct {
		source   string
		failed   string
		external string
	}{
		{"deezer", "/release-group", "55"},
		{"musicbrainz", "/artist/51/albums", "linked-group"},
	} {
		t.Run(tc.source, func(t *testing.T) {
			date := time.Now().UTC().Format(time.DateOnly)
			s, database := radarCatalogFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == tc.failed {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				writeRadarFixtureCatalog(w, r, date)
			})
			result, err := s.ScanArtist(t.Context(), radarFixtureArtist)
			if err != nil || result.TotalDiscovered != 1 || result.NewReleases != 1 {
				t.Fatalf("partial scan = %+v, %v", result, err)
			}
			var provider, external string
			if err := database.Conn().QueryRow("SELECT provider, external_id FROM radar_release_sources").Scan(&provider, &external); err != nil {
				t.Fatal(err)
			}
			if provider != tc.source || external != tc.external {
				t.Fatalf("stored catalog source = %s/%s, want %s/%s", provider, external, tc.source, tc.external)
			}
			feed, total, err := database.GetRadarFeed(t.Context(), "all", "", 10, 0)
			if err != nil || total != 1 || len(feed) != 1 || feed[0].Title != "Catalog release" {
				t.Fatalf("feed after partial scan = %+v, total=%d, error=%v", feed, total, err)
			}
		})
	}
}

func TestRadarTotalCatalogFailureAndRecoveryPreserveIgnoredCard(t *testing.T) {
	date := time.Now().UTC().Format(time.DateOnly)
	var fail atomic.Bool
	s, database := radarCatalogFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writeRadarFixtureCatalog(w, r, date)
	})
	ctx := t.Context()
	initial, err := s.ScanArtist(ctx, radarFixtureArtist)
	if err != nil || initial.TotalDiscovered != 1 {
		t.Fatalf("initial scan = %+v, %v", initial, err)
	}
	var id int64
	if err := database.Conn().QueryRow("SELECT id FROM radar_releases").Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := database.SetRadarReleaseStatus(ctx, id, "ignored"); err != nil {
		t.Fatal(err)
	}
	// Give the failure a deterministic marker for detecting writes to last_checked_at.
	if _, err := database.Conn().Exec("UPDATE tracked_artists SET last_checked_at = '2000-01-01 00:00:00'"); err != nil {
		t.Fatal(err)
	}
	checkCard := func() {
		t.Helper()
		var cards, sources int
		var status string
		if err := database.Conn().QueryRow(`SELECT
			(SELECT COUNT(*) FROM radar_releases),
			(SELECT COUNT(*) FROM radar_release_sources WHERE release_id = ?1),
			(SELECT status FROM radar_releases WHERE id = ?1)
		`, id).Scan(&cards, &sources, &status); err != nil || cards != 1 || sources != 2 || status != "ignored" {
			t.Fatalf("stored card: total=%d sources=%d status=%q error=%v", cards, sources, status, err)
		}
		feed, total, err := database.GetRadarFeed(ctx, "all", "", 10, 0)
		if err != nil || total != 0 || len(feed) != 0 {
			t.Fatalf("ignored card returned to feed: %+v, %d, %v", feed, total, err)
		}
	}

	fail.Store(true)
	failed, err := s.ScanArtist(ctx, radarFixtureArtist)
	if failed != nil || err == nil || !strings.Contains(strings.ToLower(err.Error()), "deezer") ||
		!strings.Contains(strings.ToLower(err.Error()), "musicbrainz") {
		t.Fatalf("total outage looked successful: %+v, %v", failed, err)
	}
	checkCard()
	var checked string
	if err := database.Conn().QueryRow("SELECT CAST(last_checked_at AS TEXT) FROM tracked_artists").Scan(&checked); err != nil || checked != "2000-01-01 00:00:00" {
		t.Fatalf("outage changed the last successful check: %q, %v", checked, err)
	}

	fail.Store(false)
	recovered, err := s.ScanArtist(ctx, radarFixtureArtist)
	if err != nil || recovered.TotalDiscovered != 1 || recovered.NewReleases != 0 ||
		len(recovered.Releases) != 1 || recovered.Releases[0].Status != "ignored" || recovered.Releases[0].IsRadar {
		t.Fatalf("recovered scan = %+v, %v", recovered, err)
	}
	checkCard()
	if err := database.Conn().QueryRow("SELECT CAST(last_checked_at AS TEXT) FROM tracked_artists").Scan(&checked); err != nil || checked == "2000-01-01 00:00:00" {
		t.Fatalf("successful retry did not update last check: %q, %v", checked, err)
	}
}

func TestRadarCancelledArtistScanDoesNotPublishPartialResults(t *testing.T) {
	date := time.Now().UTC().Format(time.DateOnly)
	entered := make(chan string, 2)
	s, database := radarCatalogFixture(t, func(w http.ResponseWriter, r *http.Request) {
		entered <- r.URL.Path
		if r.URL.Path == "/release-group" {
			<-r.Context().Done()
			return
		}
		writeRadarFixtureCatalog(w, r, date)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	type outcome struct {
		result *ScanArtistResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := s.ScanArtist(ctx, radarFixtureArtist)
		done <- outcome{result, err}
	}()
	for range 2 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	cancel()
	select {
	case got := <-done:
		if got.result != nil || !errors.Is(got.err, context.Canceled) {
			t.Fatalf("cancelled scan = %+v, %v", got.result, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled scan did not return")
	}
	var cards int
	if err := database.Conn().QueryRow("SELECT COUNT(*) FROM radar_releases").Scan(&cards); err != nil || cards != 0 {
		t.Fatalf("cancelled scan published %d cards: %v", cards, err)
	}
}

func TestRadarEmptyCatalogScanSucceeds(t *testing.T) {
	s, database := radarCatalogFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/artist/51/albums":
			fmt.Fprint(w, `{"data":[]}`)
		case "/release-group":
			fmt.Fprint(w, `{"release-groups":[]}`)
		default:
			http.NotFound(w, r)
		}
	})
	result, err := s.ScanArtist(t.Context(), radarFixtureArtist)
	if err != nil || result.TotalDiscovered != 0 || len(result.Releases) != 0 {
		t.Fatalf("empty catalog result = %+v, %v", result, err)
	}
	artist, err := database.GetTrackedArtistByName(t.Context(), radarFixtureArtist)
	if err != nil || artist.LastCheckedAt == nil {
		t.Fatalf("successful empty scan was not recorded: %+v, %v", artist, err)
	}
}

func radarCurrentRunDone(s *Service) <-chan struct{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.scan.done
}

func awaitRadarSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(10 * time.Second):
		t.Fatal(message)
	}
}

func TestRadarBackgroundScanContinuesAfterCatalogFailure(t *testing.T) {
	date := time.Now().UTC().Format(time.DateOnly)
	s, database := radarCatalogFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/artist/51/albums", "/release-group":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "/artist/59/albums":
			fmt.Fprintf(w, `{"data":[{"id":59,"title":"Healthy release","release_date":%q,"record_type":"album",
				"artist":{"name":"Working Band"},"contributors":[{"name":"Working Band"}]}]}`, date)
		default:
			http.NotFound(w, r)
		}
	})
	addRadarLibraryAlbum(t, database, "Working Band")
	if err := database.UpdateArtistCustomLinks(t.Context(), "Working Band", "", "59", "", ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.CancelScan)
	if err := s.ScanAll(t.Context(), "failure-fixture"); err != nil {
		t.Fatal(err)
	}
	awaitRadarSignal(t, radarCurrentRunDone(s), "background scan did not complete")

	progress := s.GetProgress()
	if progress.IsRunning || progress.CurrentIndex != 2 || progress.TotalArtists != 2 ||
		progress.CurrentArtist != "Working Band" || progress.NewReleasesFound != 1 ||
		!strings.Contains(progress.LastError, radarFixtureArtist) || !strings.Contains(progress.LastError, "503") {
		t.Fatalf("unexpected progress after a partial failure: %+v", progress)
	}
	feed, total, err := database.GetRadarFeed(t.Context(), "all", "", 10, 0)
	if err != nil || total != 1 || len(feed) != 1 || feed[0].Title != "Healthy release" {
		t.Fatalf("successful artist was not scanned: %+v, %d, %v", feed, total, err)
	}
}

func TestRadarCancelledBackgroundScanCannotFinishItsReplacement(t *testing.T) {
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	addRadarLibraryAlbum(t, database, radarFixtureArtist)
	if err := database.UpdateArtistCustomLinks(t.Context(), radarFixtureArtist, "", "51", "", ""); err != nil {
		t.Fatal(err)
	}
	date := time.Now().UTC().Format(time.DateOnly)
	firstEntered, secondEntered, secondCancelled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	releaseFirst := sync.OnceFunc(func() { close(release) })
	defer releaseFirst()
	var calls atomic.Int32
	transport := testkit.NewMockTransport()
	transport.RegisterHandler("https://radar-fixture.invalid/artist/51/albums", func(req *http.Request) (*http.Response, error) {
		switch calls.Add(1) {
		case 1:
			close(firstEntered)
			// Let the cancelled provider deliver a late result even after cancellation.
			<-release
			reply := httptest.NewRecorder()
			writeRadarFixtureCatalog(reply, req, date)
			return reply.Result(), nil
		case 2:
			close(secondEntered)
			<-req.Context().Done()
			close(secondCancelled)
			return nil, req.Context().Err()
		default:
			return nil, fmt.Errorf("unexpected additional catalog request")
		}
	})
	deezer := resolver.NewDeezerClient(transport)
	deezer.SetBaseURL("https://radar-fixture.invalid")
	s := NewService(database, resolver.NewDiscographyAggregator(deezer, nil, nil, database), nil)
	t.Cleanup(s.CancelScan)

	if err := s.ScanAll(t.Context(), "first"); err != nil {
		t.Fatal(err)
	}
	firstDone := radarCurrentRunDone(s)
	awaitRadarSignal(t, firstEntered, "first scan did not start its provider request")
	s.CancelScan()

	requestCtx, finishRequest := context.WithCancel(t.Context())
	if err := s.ScanAll(requestCtx, "restart"); err != nil {
		finishRequest()
		t.Fatal(err)
	}
	finishRequest() // The HTTP request ends while the second scan is still active.
	secondDone := radarCurrentRunDone(s)
	awaitRadarSignal(t, secondEntered, "replacement scan did not start its provider request")
	want := s.GetProgress()
	if !want.IsRunning || want.CurrentIndex != 1 {
		t.Fatalf("replacement scan never became active: %+v", want)
	}

	releaseFirst()
	awaitRadarSignal(t, firstDone, "cancelled scan did not finish after its provider returned")
	if got := s.GetProgress(); got != want {
		t.Fatalf("old run changed the replacement's progress: got %+v, want %+v", got, want)
	}

	s.CancelScan()
	awaitRadarSignal(t, secondCancelled, "CancelScan no longer cancels the replacement request")
	awaitRadarSignal(t, secondDone, "replacement scan did not finish after cancellation")
	var count int
	if err := database.Conn().QueryRow("SELECT COUNT(*) FROM radar_releases").Scan(&count); err != nil || count != 0 {
		t.Fatalf("cancelled runs published %d cards: %v", count, err)
	}
}
