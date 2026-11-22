package resolver

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

func TestSoundCloudReleaseUsesTrackOwnerAndPermalink(t *testing.T) {
	searcher := NewSoundCloudSetSearcher("")
	const foreignURL = "https://soundcloud.com/orion/home"
	release := searcher.convertAlbumToCandidate(scAPIAlbumItem{
		Title: "Night", User: scAPIUser{Username: "Vega"},
		Tracks: []scAPITrackItem{
			{ID: 1, Title: "Home", User: scAPIUser{Username: "Orion"}, Duration: 180000, PermalinkURL: foreignURL},
			{ID: 2, Title: "Home", User: scAPIUser{Username: "Vega"}, Duration: 180000},
		},
	}, nil)
	if release.Tracks[0].Artist != "Orion" || releaseTrackURL(release.Tracks[1]) != "" {
		t.Fatalf("bad track provenance: %+v", release.Tracks)
	}
	targets := []ResolvedTrack{{Title: "Home", Artist: "Vega", Duration: 180}}
	if _, n := assignTracklists(targets, release.Tracks); n != 0 {
		t.Fatal("matched a foreign track or fabricated an audio URL")
	}
	parsed, err := searcher.ParseOutputBytes(
		[]byte(`{"id":"12345","title":"Home","uploader":"Vega","duration":180}`),
		"https://soundcloud.com/vega/sets/night",
	)
	if err != nil || releaseTrackURL(parsed.Tracks[0]) != "" {
		t.Fatalf("fabricated URL from flat SoundCloud item: %v, %v", parsed, err)
	}
}

func TestReleaseAssignmentsUseVersionAndStrongTrackEvidence(t *testing.T) {
	targets := []ResolvedTrack{{Title: "Home (Explicit)", Artist: "Vega", Duration: 180}}
	tracks := []AlbumTrackCandidate{
		{Title: "Home — Clean Version", Artist: "Vega", Duration: 180, VideoID: "clean"},
		{Title: "Home", VersionClaim: "uncensored", Artist: "Vega", Duration: 180, VideoID: "explicit"},
		{Title: "Home (Explicit)", Artist: "", Duration: 180, VideoID: "no_artist"},
		{Title: "Home (Explicit)", Artist: "Vega", VideoID: "no_duration"},
	}
	for _, tc := range []struct {
		policy VersionPolicy
		want   string
	}{
		{PolicyRelease, "explicit"}, {PolicyUncensored, "explicit"}, {PolicyClean, "clean"},
	} {
		items := RankReleaseItems([]ReleaseCandidateItem{{Title: "Night", Artist: "Vega", Tracks: tracks}}, "Vega", "Night", targets, tc.policy)
		c := items[0]
		if c.MatchedCount != 1 || c.VersionMatches != 1 || c.TrackMapping[0] != "https://music.youtube.com/watch?v="+tc.want {
			t.Fatalf("%s: wrong mapping %+v", tc.policy, c)
		}
	}
	items := RankReleaseItems([]ReleaseCandidateItem{{Title: "Night", Artist: "Vega", Tracks: tracks[2:], MatchedCount: 1, TrackMapping: map[int]string{0: "stale"}}},
		"Vega", "Night", targets, PolicyRelease)
	if c := items[0]; c.MatchedCount != 0 || c.MissingTracks != 1 || c.Score != 0 || len(c.TrackMapping) != 0 {
		t.Fatalf("weak evidence entered the coverage: %+v", c)
	}
	if e := EvaluateTrack("Overture (Part I)", "Vega", 180, "Overture (Part II)", "Vega", 180); e.Compatible {
		t.Fatalf("matched different parts: %+v", e)
	}
}

func TestReleaseDirectLinkUsesTheSameEvaluation(t *testing.T) {
	searcher := NewSoundCloudSetSearcher("")
	const rawURL = "https://soundcloud.com/orion/sets/elsewhere"
	candidate := AlbumReleaseCandidate{Title: "Elsewhere", Artist: "Orion", PlaylistID: rawURL, Tracks: []AlbumTrackCandidate{
		{Title: "Home (Clean)", Artist: "Vega", Duration: 180, DirectURL: "https://soundcloud.com/vega/home"},
		{Title: "Intro", Artist: "Orion", Duration: 30, DirectURL: "https://soundcloud.com/orion/intro"},
	}}
	searcher.SetCustomSearchFunc(func(context.Context, string, string, int) ([]AlbumReleaseCandidate, error) {
		return []AlbumReleaseCandidate{candidate}, nil
	})
	searcher.SetCustomParseURLFunc(func(context.Context, string) (*AlbumReleaseCandidate, error) { return &candidate, nil })
	matcher := &FullReleaseMatcher{scSearcher: searcher}
	targets := []ResolvedTrack{{Title: "Home", Artist: "Vega", Duration: 180}}
	search, err := matcher.SearchReleaseCandidatesPaged(t.Context(), "Vega", "Night", targets, 0, 0, "", "", 30, PolicyClean)
	if err != nil || len(search.Candidates) != 1 {
		t.Fatalf("search: %v %v", search, err)
	}
	direct, err := matcher.ParseAndMatchReleaseURL(t.Context(), rawURL, "Vega", "Night", targets, PolicyClean)
	if err != nil {
		t.Fatal(err)
	}
	found := search.Candidates[0]
	found.ID = direct.ID
	if !reflect.DeepEqual(found, *direct) || direct.Score != 39 || direct.ExtraTracks != 1 {
		t.Fatalf("search and direct URL differ: %+v %+v", found, *direct)
	}
}

func TestAutoSelectReleaseCoverageAndAmbiguity(t *testing.T) {
	targets := make([]ResolvedTrack, 20)
	candidates := make([]AlbumTrackCandidate, 20)
	for i := range targets {
		title := fmt.Sprintf("Song %d", i+100)
		// Separate by enough time that adjacent song names cannot provide another edge.
		duration := 120 + i*40
		targets[i] = ResolvedTrack{Title: title, Artist: "Vega", Duration: duration}
		candidates[i] = AlbumTrackCandidate{Title: title, Artist: "Vega", Duration: duration, VideoID: fmt.Sprintf("video%d", i)}
	}
	searcher := NewSoundCloudSetSearcher("")
	matcher := &FullReleaseMatcher{scSearcher: searcher}
	for _, tc := range []struct {
		name  string
		count int
		mode  string
		want  bool
	}{
		{"17 of 20", 17, "", true},
		{"16 of 20", 16, "", false},
		{"foreign album", 20, "foreign", false},
		{"two equivalent editions", 20, "tie", false},
		{"ambiguous versions in one release", 20, "versions", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			searcher.SetCustomSearchFunc(func(context.Context, string, string, int) ([]AlbumReleaseCandidate, error) {
				candidate := AlbumReleaseCandidate{Title: "Night", Artist: "Vega", PlaylistID: "https://soundcloud.com/vega/sets/night", Tracks: candidates[:tc.count]}
				switch tc.mode {
				case "foreign":
					candidate.Artist = "Orion"
				case "tie":
					other := candidate
					other.PlaylistID += "-other"
					other.Tracks = append([]AlbumTrackCandidate(nil), candidate.Tracks...)
					other.Tracks[0].VideoID = "other"
					return []AlbumReleaseCandidate{candidate, other}, nil
				case "versions":
					other := candidate.Tracks[0]
					other.VideoID = "other"
					other.Title += " (Clean)"
					candidate.Tracks = append(append([]AlbumTrackCandidate(nil), candidate.Tracks...), other)
				}
				return []AlbumReleaseCandidate{candidate}, nil
			})
			match, err := matcher.AutoSelectRelease(t.Context(), "Vega", "Night", targets)
			if err != nil || (match != nil) != tc.want {
				t.Fatalf("auto choice: %+v %v", match, err)
			}
			if match != nil && len(match.TrackMapping) != tc.count {
				t.Fatalf("unexpected coverage: %+v", match.TrackMapping)
			}
		})
	}
}
