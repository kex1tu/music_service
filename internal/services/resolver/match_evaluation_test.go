package resolver

import "testing"

func TestUnifiedMatchRegressions(t *testing.T) {
	exact := EvaluateTrack("abcdef", "uvwxyz", 180, "abcdef", "uvwxyz - Topic", 180)
	for _, title := range []string{"abcdef (Uncensored)", "abcdef (Explicit)"} {
		e := EvaluateTrack("abcdef", "uvwxyz", 180, title, "uvwxyz - Topic", 180)
		if !e.Compatible || e.Score != exact.Score {
			t.Fatalf("version claim must not cause substring penalty: %+v", e)
		}
	}
	oliver := EvaluateTrack("abcdef", "Oliver", 180, "abcdef", "Oliver - Topic", 180)
	if !oliver.Compatible || oliver.Score != 100 {
		t.Fatalf("Oliver/live collision: %+v", oliver)
	}
	unrelated := calculateCandidateScore("zzzzzz", "qqqqqq", "abcdef", "uvwxyz", 180, 180, true, "ytmusic")
	if unrelated >= 25 {
		t.Fatalf("unrelated Topic passed: %d", unrelated)
	}
	for _, version := range []string{"Remix", "Live", "Slowed", "Instrumental"} {
		e := EvaluateTrack("abcdef", "uvwxyz", 180, "abcdef ("+version+")", "uvwxyz", 180)
		if e.Compatible || e.Score >= 70 {
			t.Fatalf("lost version %s: %+v", version, e)
		}
	}
	e := EvaluateTrack("abcdef", "", 180, "abcdef", "Other", 180)
	if e.Score >= 90 || e.ArtistSimilarity != 0 {
		t.Fatalf("missing artist bonus: %+v", e)
	}
	if ParseTrackIdentity("Song (Part II)").Title == ParseTrackIdentity("Song").Title {
		t.Fatal("lost meaningful parentheses")
	}
	if EvaluateTrack("Love", "ABBA", 180, "Glove", "ABBA", 180).TitleSimilarity >= 0.95 {
		t.Fatal("substring shortcut")
	}
}

func TestCandidateDeduplicationAndPolicies(t *testing.T) {
	items := []CandidateTrackItem{{URL: "https://www.youtube.com/watch?v=x", Title: "Song", Uploader: "Artist", Duration: 180}, {URL: "https://music.youtube.com/watch?v=x", Title: "Song", Uploader: "Artist", Duration: 180}, {URL: "https://soundcloud.com/a/b", Title: "Song (Clean)", Uploader: "Artist", Duration: 180}, {URL: "https://soundcloud.com/a/c", Title: "Another (Clean)", Uploader: "Other", Duration: 180}}
	got := RankCandidateItems(items, "Song", "Artist", 180, PolicyClean)
	if len(got) != 3 || got[0].URL != "https://soundcloud.com/a/b" || got[2].Evaluation.Compatible {
		t.Fatalf("policy/dedup: %+v", got)
	}
}

func TestAssignmentSkipsMissingURLAndWrongVersion(t *testing.T) {
	m := &FullReleaseMatcher{}
	for _, c := range []AlbumTrackCandidate{{Title: "Song", Duration: 180}, {Title: "Song (Slowed)", Duration: 180, VideoID: "x"}} {
		_, n := m.MatchTracklists([]ResolvedTrack{{Title: "Song", Duration: 180}}, []AlbumTrackCandidate{c})
		if n != 0 {
			t.Fatalf("invalid candidate counted: %+v", c)
		}
	}
	_, n := m.MatchTracklists([]ResolvedTrack{{Title: "Song", Duration: 180}, {Title: "Song", Duration: 180}}, []AlbumTrackCandidate{{Title: "Song", Duration: 180, VideoID: "x"}, {Title: "Song", Duration: 180, VideoID: "x"}})
	if n != 1 {
		t.Fatalf("duplicate source counted twice: %d", n)
	}
	sc, _, _ := ScoreReleaseCandidate("soundcloud_set", true, 0)
	yt, st, _ := ScoreReleaseCandidate("ytmusic_album", true, 1)
	if sc >= yt || st != "unknown" {
		t.Fatalf("platform overrides identity %d/%d %s", sc, yt, st)
	}
}

func TestGlobalAssignmentAvoidsGreedyTrap(t *testing.T) {
	// First target can use either candidate; the second has just one compatible edge.
	targets := []ResolvedTrack{{Title: "Song One", Duration: 180}, {Title: "Song One", Duration: 190}}
	candidates := []AlbumTrackCandidate{{Title: "Song One", Duration: 181, VideoID: "both"}, {Title: "Song One", Duration: 170, VideoID: "first_only"}}
	mapping, n := assignTracklists(targets, candidates)
	if n != 2 || mapping[0] != "https://music.youtube.com/watch?v=first_only" {
		t.Fatalf("not a global assignment: %v %d", mapping, n)
	}
}

func TestUploaderIsNotAnArtistCredit(t *testing.T) {
	e := EvaluateTrack("Song", "Artist", 180, "Song", "Anonymous Uploads", 180, true)
	if !e.Compatible || len(e.Unknown) == 0 || e.Score > 74 {
		t.Fatalf("uploader treated as catalog artist: %+v", e)
	}
	e = EvaluateTrack("Song", "Artist", 180, "Song", "Different Artist", 180)
	if e.Compatible {
		t.Fatal("explicit catalog artist contradiction ignored")
	}
	e = EvaluateTrack("Song", "Artist", 180, "Different Artist - Song", "Anonymous Uploads", 180, true)
	if e.Compatible {
		t.Fatal("explicit title credit contradiction ignored")
	}
}

func TestDuplicateRepresentationDoesNotDependOnArrival(t *testing.T) {
	ytm := CandidateTrackItem{URL: "https://music.youtube.com/watch?v=same", Source: "ytmusic", Title: "Song (Live)", Uploader: "Artist", Duration: 180}
	yt := CandidateTrackItem{URL: "https://www.youtube.com/watch?v=same", Source: "youtube", Title: "Artist - Song", Uploader: "Artist - Topic", Duration: 180}
	for _, items := range [][]CandidateTrackItem{{ytm, yt}, {yt, ytm}} {
		got := RankCandidateItems(items, "Song", "Artist", 180, PolicyRelease)
		if len(got) != 1 || got[0].Source != "ytmusic" || got[0].Evaluation.Compatible {
			t.Fatalf("lost authoritative version annotation: %+v", got)
		}
	}
}

func TestVersionPreferenceCannotHideMissingIdentityEvidence(t *testing.T) {
	got := RankCandidateItems([]CandidateTrackItem{{URL: "https://soundcloud.com/a/unknown", Source: "soundcloud", Title: "Song (Uncensored)", Uploader: "Anonymous Uploads", Duration: 180}, {URL: "https://music.youtube.com/watch?v=known", Source: "ytmusic", Title: "Song (Clean)", Uploader: "Artist", Duration: 180}}, "Song", "Artist", 180, PolicyUncensored)
	if got[0].URL != "https://music.youtube.com/watch?v=known" {
		t.Fatalf("version claim compensated for unknown artist: %+v", got)
	}
}
