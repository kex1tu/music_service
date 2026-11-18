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
	"music-manager/internal/testkit"
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

func TestManualSourcesUseRecordingIdentityAcrossJobs(t *testing.T) {
	env := testkit.NewEnv(t)
	repo := NewRepository(env.DB.Conn())
	service := NewService(ServiceConfig{Repo: repo, Matcher: resolver.NewAudioMatcher("")})

	isrc := "TEST2400001"
	tracks := []resolver.ResolvedTrack{
		{Artist: "Artist", Title: "Song"},
		{Artist: "Artist", Title: "Song (Club Remix)"},
		{Artist: "Artist", Title: "Known recording", ISRC: &isrc},
	}
	sources := map[int]string{
		0: "https://soundcloud.com/artist/original",
		1: "https://soundcloud.com/artist/club-remix",
		2: "https://soundcloud.com/artist/known-recording",
	}
	selection, err := service.SwitchReleaseSource(t.Context(), SwitchReleaseSourceRequest{
		SourceURL: "https://www.deezer.com/album/123", TrackMapping: sources, TargetTracks: tracks,
	})
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewService(ServiceConfig{Repo: repo, Matcher: resolver.NewAudioMatcher("")})
	job, ok := restarted.GetJob(selection.JobID)
	if !ok {
		t.Fatal("release choice missing after reload")
	}
	for index, track := range job.Snapshot().Tracks {
		if track.AudioSourceURL != sources[index] || track.VersionStatus != "manual" {
			t.Fatalf("track %d lost its job selection: %+v", index, track)
		}
		if pin, err := repo.GetPinnedSource(t.Context(), track.TrackID); err != nil || pin != nil {
			t.Fatalf("job track ID used as global pin: %+v, %v", pin, err)
		}
	}

	newTask := &Job{ID: "next-task", Status: StatusWaitingSource, Tracks: tracks}
	if err := repo.CreateJob(t.Context(), newTask); err != nil {
		t.Fatal(err)
	}
	for index, track := range newTask.Tracks {
		var code string
		if track.ISRC != nil {
			// The title can be different in another catalog when ISRC is known.
			track.Title = "Another catalog title"
			code = strings.ToLower(*track.ISRC)
		}
		candidates, err := restarted.GetCandidates(t.Context(), track.Artist, track.Title, 0, "", code)
		if err != nil {
			t.Fatal(err)
		}
		var saved []string
		for _, candidate := range candidates {
			if candidate.SavedSelection {
				saved = append(saved, candidate.URL)
			}
		}
		if len(saved) != 1 || saved[0] != sources[index] {
			t.Fatalf("%s saved selection = %v, want %s", track.Title, saved, sources[index])
		}
	}
}

func TestManualTrackSourceIsScopedToJob(t *testing.T) {
	env := testkit.NewEnv(t)
	repo := NewRepository(env.DB.Conn())
	for _, id := range []string{"first-task", "selected-task"} {
		job := &Job{ID: id, Status: StatusWaitingSource, Tracks: []resolver.ResolvedTrack{
			{TrackID: "catalog-id", Artist: "Artist", Title: "Song"},
		}}
		if err := repo.CreateJob(t.Context(), job); err != nil {
			t.Fatal(err)
		}
	}
	service := NewService(ServiceConfig{Repo: repo})
	const selected = "https://soundcloud.com/artist/chosen"
	if _, err := service.switchJobTrackSource(t.Context(), "selected-task", "catalog-id", selected, resolver.PolicyRelease); err != nil {
		t.Fatal(err)
	}

	job, err := repo.GetJob(t.Context(), "selected-task")
	if err != nil || job == nil || job.Status != StatusQueued || job.manualTrackIndex == nil ||
		*job.manualTrackIndex != 0 || job.Tracks[0].AudioSourceURL != selected {
		t.Fatalf("selection was not durably queued: %+v, %v", job, err)
	}
	other, err := repo.GetJob(t.Context(), "first-task")
	if err != nil || other == nil || other.Status != StatusWaitingSource || other.Tracks[0].AudioSourceURL != "" {
		t.Fatalf("another task was affected: %+v, %v", other, err)
	}
}
