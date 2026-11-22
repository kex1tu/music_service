package downloader

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"music-manager/internal/testkit"

	"music-manager/internal/services/resolver"
)

func TestQueueDownloadsAndPersistsAutoSelectedRelease(t *testing.T) {
	setupFeatureMockMediaBinaries(t)
	fixture := httptest.NewServer(http.NotFoundHandler())
	defer fixture.Close()
	searcher := resolver.NewSoundCloudSetSearcher("")
	const audioURL = "https://soundcloud.com/vega/home"
	lookups := 0
	searcher.SetCustomSearchFunc(func(_ context.Context, artist, album string, _ int) ([]resolver.AlbumReleaseCandidate, error) {
		lookups++
		return []resolver.AlbumReleaseCandidate{{
			Title: album, Artist: artist, PlaylistID: "https://soundcloud.com/vega/sets/night",
			Tracks: []resolver.AlbumTrackCandidate{{Title: "Home", Artist: artist, Duration: 180, DirectURL: audioURL}},
		}}, nil
	})
	sourceResolver := resolver.NewSourceResolver(nil, nil, "")
	sourceResolver.SetFullReleaseMatcher(resolver.NewFullReleaseMatcher(resolver.NewYouTubeMusicClient(fixture.URL), searcher))

	env := testkit.NewEnv(t)
	repo := NewRepository(env.DB.Conn())
	service := NewService(ServiceConfig{Resolver: sourceResolver, TmpSandbox: env.TmpSandbox, MusicSandbox: env.MusicSandbox, Repo: repo})
	job := &Job{ID: "auto-release", Source: "deezer", URL: "https://www.deezer.com/album/123",
		AlbumMetadata:        resolver.ReleaseMetadata{AlbumTitle: "Night", AlbumArtist: "Vega", TotalTracks: 1},
		SelectedTrackIndices: []int{0},
		Tracks:               []resolver.ResolvedTrack{{TrackID: "dz_1", TrackNumber: 1, Title: "Home", Artist: "Vega", Duration: 180}},
	}
	service.executeRegular(t.Context(), job)
	snap := job.Snapshot()
	if lookups != 1 || snap.Status != StatusReady || snap.Tracks[0].AudioSourceURL != audioURL ||
		snap.Tracks[0].VersionStatus != resolver.AutoReleaseVersionStatus || snap.Tracks[0].MatchScore != 100 {
		t.Fatalf("automatic download: %+v, tracks: %+v, lookup count: %d", snap, snap.Tracks, lookups)
	}
	if err := repo.SaveJob(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	loaded, err := repo.GetJob(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	service.executeRegular(t.Context(), loaded)
	retry := loaded.Snapshot()
	if lookups != 1 || retry.Status != StatusReady || retry.Tracks[0].AudioSourceURL != audioURL ||
		retry.Tracks[0].VersionStatus != resolver.AutoReleaseVersionStatus {
		t.Fatalf("saved selection lost on retry: %+v, tracks: %+v, lookup count: %d", retry, retry.Tracks, lookups)
	}

	manual := job.Snapshot()
	manual.ID = "manual-release"
	const manualURL = "https://soundcloud.com/vega/another"
	selectManualSource(&manual.Tracks[0], manualURL, resolver.PolicyRelease)
	service.executeRegular(t.Context(), manual)
	selected := manual.Snapshot().Tracks[0]
	if lookups != 1 || selected.AudioSourceURL != manualURL || selected.VersionStatus != "manual" {
		t.Fatalf("manual source replaced: %+v, lookup count: %d", selected, lookups)
	}
}
