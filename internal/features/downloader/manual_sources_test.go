package downloader

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/db"
	"music-manager/internal/services/resolver"
)

func TestManualSourceSelectionsSurviveReload(t *testing.T) {
	database, e := db.Open(filepath.Join(t.TempDir(), "jobs.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer database.Close()
	repo := NewRepository(database.Conn())
	job := &Job{ID: "manual-selection", Status: StatusWaitingSource, URL: "https://www.deezer.com/album/1", Tracks: []resolver.ResolvedTrack{{TrackID: "one", Title: "Song", Artist: "Artist", AudioSourceURL: "https://soundcloud.com/a/b", VersionStatus: "manual", VersionPolicy: resolver.PolicyUncensored}}}
	if e = repo.SaveJob(context.Background(), job); e != nil {
		t.Fatal(e)
	}
	loaded, e := repo.GetJob(context.Background(), job.ID)
	if e != nil || len(loaded.Tracks) != 1 || loaded.Tracks[0].AudioSourceURL != job.Tracks[0].AudioSourceURL || loaded.Tracks[0].VersionPolicy != resolver.PolicyUncensored {
		t.Fatalf("lost manual selection: %+v %v", loaded, e)
	}
	loaded.UpdateTrack(0, func(tr *resolver.ResolvedTrack) { tr.Status = "ready" })
	loaded.reconcileManualSources()
	snap := loaded.Snapshot()
	if snap.Status != StatusReady || snap.CompletedTracks != 1 {
		t.Fatalf("manual completion not reflected: %+v", snap)
	}
}
func TestWaitingSourceDoesNotConsumePinOrCache(t *testing.T) {
	database, e := db.Open(filepath.Join(t.TempDir(), "jobs.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer database.Close()
	repo := NewRepository(database.Conn())
	pin := PinnedTrackSourceDTO{CacheKey: resolver.BuildCacheKey("", "Artist", "Song"), PinnedURL: "https://soundcloud.com/a/b"}
	if e = repo.SavePinnedSource(context.Background(), pin); e != nil {
		t.Fatal(e)
	}
	s := NewService(ServiceConfig{Repo: repo})
	j := &Job{ID: "waiting", Tracks: []resolver.ResolvedTrack{{TrackID: "one", Title: "Song", Artist: "Artist"}}}
	ok := s.processSingleTrack(context.Background(), j, &resolver.ReleaseMetadata{}, 1, j.Snapshot().Tracks[0], "", "", nil, 1)
	if ok || j.Snapshot().Tracks[0].Status != "waiting_source" || j.Snapshot().Tracks[0].AudioSourceURL != "" {
		t.Fatalf("saved source silently consumed: %+v", j.Snapshot())
	}
}

func TestManualReleaseSandboxErrorDoesNotExposeHostPaths(t *testing.T) {
	dir := t.TempDir()
	sb, e := sandbox.NewPathSandbox(dir)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink("/etc", filepath.Join(dir, "escape")); e != nil {
		t.Fatal(e)
	}
	s := NewService(ServiceConfig{MusicSandbox: sb})
	_, e = s.SwitchReleaseSource(context.Background(), SwitchReleaseSourceRequest{SourceURL: "https://music.youtube.com/watch?v=fixture", TargetPath: "escape", TrackMapping: map[int]string{0: "https://music.youtube.com/watch?v=fixture"}, TargetTracks: []resolver.ResolvedTrack{{Title: "Song", Artist: "Artist"}}})
	if e == nil || strings.Contains(e.Error(), dir) || strings.Contains(e.Error(), "/etc") {
		t.Fatalf("unsafe sandbox error: %v", e)
	}
}
