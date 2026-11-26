package resolver

import (
	"math"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// VersionPolicy affects ordering, never authorizes source selection.
type VersionPolicy string

const (
	PolicyRelease    VersionPolicy = "release"
	PolicyUncensored VersionPolicy = "uncensored"
	PolicyClean      VersionPolicy = "clean"
)

func ValidVersionPolicy(p VersionPolicy) bool {
	return p == PolicyRelease || p == PolicyUncensored || p == PolicyClean
}

type TrackIdentity struct {
	Raw        string   `json:"raw"`
	Title      string   `json:"title"`
	Versions   []string `json:"versions"`
	Censorship string   `json:"censorship"` // title claim only, never audio verification
	Details    []string `json:"version_details"`
}

type MatchEvaluation struct {
	Score            int      `json:"score"`
	Compatible       bool     `json:"compatible"`
	TitleSimilarity  float64  `json:"title_similarity"`
	ArtistSimilarity float64  `json:"artist_similarity"`
	DurationKnown    bool     `json:"duration_known"`
	Conflicts        []string `json:"conflicts"`
	Unknown          []string `json:"unknown"`
	VersionClaim     string   `json:"version_claim"`
	Evidence         string   `json:"evidence"`
}

var versionPhrases = map[string][]string{
	"live":         {"live", "concert", "концерт", "концертная"},
	"cover":        {"cover", "кавер", "трибьют"},
	"remix":        {"remix", "ремикс"},
	"instrumental": {"instrumental", "караоке", "karaoke", "инструментал", "минусовка"},
	"acoustic":     {"acoustic", "акустика", "акустическая"},
	"slowed":       {"slowed", "reverb", "замедленная"},
	"sped_up":      {"sped up", "speed up", "nightcore"},
	"edit":         {"radio edit", "radio version", "tv edit"},
}

// Parts and movements carry recording identity even if only one character differs.
var numberedPart = regexp.MustCompile(`(?:^|\s)(part|pt|movement|chapter|vol|volume|часть|глава|том)\s+([0-9]+|[ivxlcdm]+)(?:\s|$)`)

var censorshipSuffix = regexp.MustCompile(`(?i)\s*(?:[-–—|:]\s*|\s+)(?:(?:explicit|uncensored|uncut|clean|censored)\s+(?:version|edition)|без цензуры|до цензуры|с цензурой)\s*$|\s*[-–—|:]\s*(?:explicit|uncensored|uncut|clean|censored)\s*$`)
var brackets = regexp.MustCompile(`[\(\[]([^\)\]]*)[\)\]]`)
var artistSep = regexp.MustCompile(`(?i)\s+(?:feat\.?|ft\.?|featuring|with|при уч\.?)\s+|\x00|\\+|\s*[,&;]\s*|\s+/\s+`)

func matchText(s string) string { return strings.ToLower(CleanQueryTextFull(s)) }

// Both arguments are already normalized. Avoid repeating regexp normalization
// for every keyword and every edge of the release assignment matrix.
func phrase(s, p string) bool { return strings.Contains(" "+s+" ", " "+p+" ") }

// ParseTrackIdentity removes only known presentation/version annotations. Unknown
// parentheses (Part II, movement names, etc.) remain part of the identity.
func ParseTrackIdentity(raw string) TrackIdentity {
	r := TrackIdentity{Raw: raw, Censorship: "unknown", Versions: []string{}, Details: []string{}}
	normalized := matchText(raw)
	annotations := []string{}
	for _, b := range brackets.FindAllString(raw, -1) {
		annotations = append(annotations, matchText(b))
	}
	marker := func(p string) bool {
		for _, b := range annotations {
			if phrase(b, p) {
				return true
			}
		}
		return strings.HasSuffix(normalized, " "+p) || phrase(normalized, p+" version") || phrase(normalized, p+" edition") || phrase(normalized, p+" at") || phrase(normalized, p+" concert")
	}
	t := CleanQueryTextFull(NormalizeTrackTitleForMatching(raw))
	// Detect version annotations before any destructive normalization.
	for kind, phrases := range versionPhrases {
		for _, p := range phrases {
			if marker(p) {
				r.Versions = append(r.Versions, kind)
				break
			}
		}
	}
	sort.Strings(r.Versions)
	for _, b := range annotations {
		for _, kind := range r.Versions {
			found := false
			for _, p := range versionPhrases[kind] {
				if phrase(b, p) {
					found = true
					break
				}
			}
			if found {
				r.Details = append(r.Details, b)
				break
			}
		}
	}
	sort.Strings(r.Details)
	if marker("uncensored") || marker("uncut") || marker("explicit") || phrase(normalized, "без цензуры") || phrase(normalized, "до цензуры") {
		r.Censorship = "uncensored"
	} else if marker("clean") || marker("censored") || phrase(normalized, "с цензурой") {
		r.Censorship = "clean"
	}
	// NormalizeTrackTitleForMatching strips slowed/clean; remove the remaining
	// recognized parenthetical versions from the original without losing specifics.
	base := brackets.ReplaceAllStringFunc(raw, func(b string) string {
		norm := matchText(b)
		for _, ps := range versionPhrases {
			for _, p := range ps {
				if phrase(norm, p) {
					return " "
				}
			}
		}
		if phrase(norm, "explicit") || phrase(norm, "uncensored") || phrase(norm, "uncut") || phrase(norm, "clean") || phrase(norm, "censored") || phrase(norm, "без цензуры") {
			return " "
		}
		return b
	})
	if r.Censorship != "unknown" {
		base = censorshipSuffix.ReplaceAllString(base, "")
	}
	if base != raw {
		t = CleanQueryTextFull(NormalizeTrackTitleForMatching(base))
	}
	r.Title = matchText(t)
	return r
}

func textSimilarity(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 1
	}
	r := LevenshteinRatio(a, b)
	aa, bb := strings.Fields(a), strings.Fields(b)
	set := map[string]bool{}
	for _, w := range bb {
		set[w] = true
	}
	n := 0
	seen := map[string]bool{}
	for _, w := range aa {
		if !seen[w] && set[w] {
			n++
		}
		seen[w] = true
	}
	f := 2 * float64(n) / float64(len(aa)+len(bb))
	return math.Max(r, f)
}

func artistSimilarity(a, b string) float64 {
	aa, bb := artistSep.Split(a, -1), artistSep.Split(CleanChannelName(b), -1)
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return 0
	}
	// Main artist is required; matching only a guest cannot establish identity.
	best := 0.0
	for _, v := range bb[:1] {
		if AreArtistsEquivalent(aa[0], v) {
			best = 1
			break
		}
		best = math.Max(best, textSimilarity(matchText(aa[0]), matchText(v)))
	}
	if len(aa) > 1 {
		matched := 0
		for _, guest := range aa[1:] {
			for _, credit := range bb[1:] {
				if AreArtistsEquivalent(guest, credit) {
					matched++
					break
				}
			}
		}
		best *= 0.8 + 0.2*float64(matched)/float64(len(aa)-1)
	}
	return best
}

func desiredVersionClaim(title string, policy VersionPolicy) string {
	if policy == PolicyClean || policy == PolicyUncensored {
		return string(policy)
	}
	return ParseTrackIdentity(title).Censorship
}

func (e MatchEvaluation) strongIdentity() bool {
	return e.Compatible && e.DurationKnown && len(e.Unknown) == 0 &&
		e.TitleSimilarity >= 0.85 && e.ArtistSimilarity >= 0.85 && e.Score >= 85
}

func EvaluateTrack(targetTitle, targetArtist string, targetDuration int, candidateTitle, candidateArtist string, candidateDuration int, uploaderOnly ...bool) MatchEvaluation {
	for _, s := range []string{targetTitle, targetArtist, candidateTitle, candidateArtist} {
		if len(s) > 4096 {
			return MatchEvaluation{Conflicts: []string{"metadata_too_long"}, Unknown: []string{}, Evidence: "metadata_only", VersionClaim: "unknown"}
		}
	}
	t, c := ParseTrackIdentity(targetTitle), ParseTrackIdentity(candidateTitle)
	e := MatchEvaluation{Compatible: true, Conflicts: []string{}, Unknown: []string{}, VersionClaim: c.Censorship, Evidence: "metadata_only"}
	parsed := ParseYouTubeTitle(candidateTitle, candidateArtist, candidateArtist)
	e.TitleSimilarity = textSimilarity(t.Title, c.Title)
	if parsed.Title != "" {
		e.TitleSimilarity = math.Max(e.TitleSimilarity, textSimilarity(t.Title, ParseTrackIdentity(parsed.Title).Title))
	}
	e.ArtistSimilarity = artistSimilarity(targetArtist, candidateArtist)
	mainRatio := artistSimilarity(artistSep.Split(targetArtist, -1)[0], artistSep.Split(CleanChannelName(candidateArtist), -1)[0])
	isUploader := len(uploaderOnly) > 0 && uploaderOnly[0]
	if parsed.Artist != "" && (isUploader || strings.TrimSpace(candidateArtist) == "") {
		e.ArtistSimilarity = math.Max(e.ArtistSimilarity, artistSimilarity(targetArtist, parsed.Artist))
		mainRatio = math.Max(mainRatio, artistSimilarity(artistSep.Split(targetArtist, -1)[0], artistSep.Split(parsed.Artist, -1)[0]))
	}
	artistUnknown := strings.TrimSpace(targetArtist) == "" || strings.TrimSpace(candidateArtist) == ""
	// Uploader identities are not catalog artist credits. An unrelated channel
	// with no explicit artist/title split is missing evidence, not a contradiction.
	if isUploader && mainRatio < 0.85 {
		hasCredit := false
		for _, sep := range []string{" - ", " — ", " – ", " : "} {
			if strings.Contains(candidateTitle, sep) {
				hasCredit = true
			}
		}
		if !hasCredit {
			artistUnknown = true
		}
	}
	if IsLabelChannel(candidateArtist) && mainRatio < 0.85 {
		artistUnknown = true
	}
	if t.Title == "" || c.Title == "" {
		e.Unknown = append(e.Unknown, "title")
	}
	if artistUnknown {
		e.Unknown = append(e.Unknown, "artist")
	}
	targetPart := numberedPart.FindStringSubmatch(t.Title)
	candidatePart := numberedPart.FindStringSubmatch(c.Title)
	if (targetPart != nil || candidatePart != nil) &&
		(targetPart == nil || candidatePart == nil || targetPart[2] != candidatePart[2]) {
		e.Conflicts = append(e.Conflicts, "numbered_part_mismatch")
	}
	if e.TitleSimilarity < 0.65 {
		e.Conflicts = append(e.Conflicts, "title_mismatch")
	}
	if len(strings.Fields(t.Title)) == 1 && len([]rune(t.Title)) <= 3 && t.Title != c.Title && t.Title != ParseTrackIdentity(parsed.Title).Title {
		e.Conflicts = append(e.Conflicts, "short_title_mismatch")
	}
	if !artistUnknown && mainRatio < 0.85 {
		e.Conflicts = append(e.Conflicts, "artist_mismatch")
	}
	if strings.Join(t.Versions, ",") != strings.Join(c.Versions, ",") {
		e.Conflicts = append(e.Conflicts, "version_mismatch")
	}
	if len(t.Details) > 0 && len(c.Details) > 0 && strings.Join(t.Details, "|") != strings.Join(c.Details, "|") {
		e.Conflicts = append(e.Conflicts, "version_detail_mismatch")
	}
	if len(t.Details) > 0 && len(c.Details) == 0 {
		e.Unknown = append(e.Unknown, "version_details")
	}
	e.DurationKnown = targetDuration > 0 && candidateDuration > 0
	d := 0.0
	if e.DurationKnown {
		delta := math.Abs(float64(targetDuration - candidateDuration))
		scale := math.Max(3, float64(targetDuration)*0.015)
		d = math.Exp(-delta / scale)
		if delta > math.Max(12, float64(targetDuration)*0.08) {
			e.Conflicts = append(e.Conflicts, "duration_mismatch")
		}
	} else {
		e.Unknown = append(e.Unknown, "duration")
	}
	e.Score = int(math.Round(50*e.TitleSimilarity + 35*e.ArtistSimilarity + 15*d))
	e.Compatible = len(e.Conflicts) == 0
	if !e.Compatible {
		e.Score = min(e.Score, 39)
		if e.TitleSimilarity < 0.65 || (!artistUnknown && mainRatio < 0.85) {
			e.Score = min(e.Score, 15)
		}
	}
	if len(e.Unknown) > 0 {
		e.Score = min(e.Score, 74)
	}
	return e
}

func candidateKey(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if u.Hostname() == "youtu.be" {
		return "youtube:" + strings.Trim(u.Path, "/")
	}
	if u.Hostname() == "www.youtube.com" || u.Hostname() == "youtube.com" || u.Hostname() == "music.youtube.com" {
		if id := u.Query().Get("v"); id != "" {
			return "youtube:" + id
		}
	}
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/")
}

func SameAudioSource(a, b string) bool { return candidateKey(a) == candidateKey(b) }

// RankCandidateItems is deterministic and never applies a source.
func RankCandidateItems(items []CandidateTrackItem, title, artist string, duration int, policy VersionPolicy) []CandidateTrackItem {
	if !ValidVersionPolicy(policy) {
		policy = PolicyRelease
	}
	seen := map[string]int{}
	out := make([]CandidateTrackItem, 0, len(items))
	// Prefer catalog metadata for the same recording; arrival order of provider
	// goroutines must not change the identity/version evidence retained.
	representative := func(a, b CandidateTrackItem) bool {
		if (a.Source == "ytmusic") != (b.Source == "ytmusic") {
			return a.Source == "ytmusic"
		}
		av, bv := len(ParseTrackIdentity(a.Title).Versions), len(ParseTrackIdentity(b.Title).Versions)
		if av != bv {
			return av > bv
		}
		if len(a.Evaluation.Unknown) != len(b.Evaluation.Unknown) {
			return len(a.Evaluation.Unknown) < len(b.Evaluation.Unknown)
		}
		return a.Title+"\x00"+a.Uploader+"\x00"+a.URL < b.Title+"\x00"+b.Uploader+"\x00"+b.URL
	}
	for _, item := range items {
		if item.URL == "" {
			continue
		}
		key := candidateKey(item.URL)
		item.Evaluation = EvaluateTrack(title, artist, duration, item.Title, item.Uploader, item.Duration, item.Source != "ytmusic")
		item.Score = item.Evaluation.Score
		item.Policy = policy
		item.BitrateInfo = "Не проверено"
		if index, ok := seen[key]; ok {
			old := out[index]
			incomplete := item.SearchIncomplete || old.SearchIncomplete
			if representative(item, old) {
				out[index] = item
			}
			out[index].SearchIncomplete = incomplete
			continue
		}
		seen[key] = len(out)
		out = append(out, item)
	}
	preference := func(i CandidateTrackItem) int {
		if !i.Evaluation.Compatible {
			return -1
		}
		if policy != PolicyRelease && i.Evaluation.VersionClaim == string(policy) {
			return 2
		}
		if policy == PolicyRelease {
			t := ParseTrackIdentity(title)
			if t.Censorship != "unknown" {
				if i.Evaluation.VersionClaim == t.Censorship {
					return 2
				}
				if i.Evaluation.VersionClaim != "unknown" {
					return 0
				}
			}
		}
		return 1
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Evaluation.Compatible != b.Evaluation.Compatible {
			return a.Evaluation.Compatible
		}
		// A preference for a version cannot compensate for missing identity
		// evidence. This is an evidence tier for ordering, not an auto-pick rule.
		strong := func(e MatchEvaluation) bool {
			return len(e.Unknown) == 0 && e.TitleSimilarity >= 0.85 && e.ArtistSimilarity >= 0.85
		}
		if strong(a.Evaluation) != strong(b.Evaluation) {
			return strong(a.Evaluation)
		}
		if preference(a) != preference(b) {
			return preference(a) > preference(b)
		}
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		return candidateKey(a.URL) < candidateKey(b.URL)
	})
	return out
}
