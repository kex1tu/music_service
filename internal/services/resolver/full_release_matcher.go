package resolver

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"
	"unicode"
)

// ContainsCyrillic проверяет наличие кириллических символов в строке.
func ContainsCyrillic(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}

// ReleaseCandidateItem представляет отдельного кандидата цельного релиза (альбом/сет/плейлист)
// со статусом цензуры, соответствием треклиста и детализированным маппингом.
type ReleaseCandidateItem struct {
	ResultType        string                  `json:"result_type,omitempty"`
	MappingLimited    bool                    `json:"mapping_limited"`
	Policy            VersionPolicy           `json:"version_policy"`
	Evaluation        MatchEvaluation         `json:"evaluation"`
	TrackEvaluations  map[int]MatchEvaluation `json:"track_evaluations"`
	AmbiguousTracks   int                     `json:"ambiguous_tracks,omitempty"`
	VersionMatches    int                     `json:"version_matches"`
	MeanTrackScore    int                     `json:"mean_track_score"`
	WeakestTrackScore int                     `json:"weakest_track_score"`
	MissingTracks     int                     `json:"missing_tracks"`
	ExtraTracks       int                     `json:"extra_tracks"`
	ID                string                  `json:"id"`
	Source            string                  `json:"source"` // "ytmusic_album", "soundcloud_set", "direct_url"
	Title             string                  `json:"title"`
	Artist            string                  `json:"artist"`
	URL               string                  `json:"url"`
	TrackCount        int                     `json:"track_count"`
	CoverURL          string                  `json:"cover_url"`
	Tracks            []AlbumTrackCandidate   `json:"tracks"`
	MatchedCount      int                     `json:"matched_count"`
	TotalCount        int                     `json:"total_count"`
	MatchedRatio      float64                 `json:"matched_ratio"` // 0.0 - 1.0
	TrackMapping      map[int]string          `json:"track_mapping"` // targetIdx -> candidateURL
	IsCyrillic        bool                    `json:"is_cyrillic"`
	CensorStatus      string                  `json:"censor_status"` // "red_flag" | "recommended" | "clean" | "neutral"
	CensorBadge       string                  `json:"censor_badge"`  // e.g. "🚩 Офиц. релиз (возможна цензура)" | "⭐ SoundCloud (оригинал)" | "⭐ Официальный релиз"
	Score             int                     `json:"score"`
}

// ScoreReleaseCandidate вычисляет оценку кандидата с учетом контекста языка (RU vs Зарубежный) и совпадения треклиста.
func ScoreReleaseCandidate(source string, isCyrillic bool, matchedRatio float64) (int, string, string) {
	if matchedRatio < 0 {
		matchedRatio = 0
	}
	if matchedRatio > 1 {
		matchedRatio = 1
	}
	return int(matchedRatio * 100), "unknown", "Цензура не проверена"
}

// FullReleaseMatchResult представляет результаты сопоставления цельного релиза.
type FullReleaseMatchResult struct {
	SourceURL    string         `json:"source_url"`
	SourceType   string         `json:"source_type"` // "ytmusic_album", "soundcloud_set"
	ReleaseTitle string         `json:"release_title"`
	MatchedCount int            `json:"matched_count"`
	TotalCount   int            `json:"total_count"`
	TrackURLs    map[int]string `json:"track_urls"` // индекс исходного трека (0..N-1) -> прямой AudioSourceURL
}

// FullReleaseMatcher координирует поиск и сопоставление цельных релизов (альбомов, EP, плейлистов).
type FullReleaseMatcher struct {
	ytMusic    *YouTubeMusicClient
	scSearcher *SoundCloudSetSearcher
}

// NewFullReleaseMatcher создает новый экземпляр FullReleaseMatcher.
func NewFullReleaseMatcher(ytMusic *YouTubeMusicClient, scSearcher ...*SoundCloudSetSearcher) *FullReleaseMatcher {
	if ytMusic == nil {
		ytMusic = NewYouTubeMusicClient("")
	}
	var sc *SoundCloudSetSearcher
	if len(scSearcher) > 0 {
		sc = scSearcher[0]
	} else {
		sc = NewSoundCloudSetSearcher("")
	}
	return &FullReleaseMatcher{
		ytMusic:    ytMusic,
		scSearcher: sc,
	}
}

// MatchTracklists сопоставляет треклист целевого альбома с треклистом кандидата.
// Возвращает маппинг индексов целевых треков на прямые URL и количество успешно сопоставленных треков.
func (m *FullReleaseMatcher) MatchTracklists(targetTracks []ResolvedTrack, candTracks []AlbumTrackCandidate) (map[int]string, int) {
	return assignTracklists(targetTracks, candTracks)
}

// MatchFullRelease is the YouTube Music edition of automatic release selection.
func (m *FullReleaseMatcher) MatchFullRelease(ctx context.Context, targetArtist, targetAlbum string, targetTracks []ResolvedTrack) (*FullReleaseMatchResult, error) {
	youtube := &FullReleaseMatcher{ytMusic: m.ytMusic}
	c, err := youtube.AutoSelectRelease(ctx, targetArtist, targetAlbum, targetTracks)
	if err != nil || c == nil {
		return nil, err
	}
	return &FullReleaseMatchResult{
		SourceURL: c.URL, SourceType: c.Source, ReleaseTitle: c.Title,
		MatchedCount: c.MatchedCount, TotalCount: c.TotalCount, TrackURLs: c.TrackMapping,
	}, nil
}

// AutoSelectRelease selects a single release with strong identity and at least
// 85% verified metadata coverage. Equal evidence for distinct audio is ambiguous.
func (m *FullReleaseMatcher) AutoSelectRelease(ctx context.Context, artist, album string, tracks []ResolvedTrack) (*ReleaseCandidateItem, error) {
	if strings.TrimSpace(artist) == "" || strings.TrimSpace(album) == "" || len(tracks) == 0 || len(tracks) > maxReleaseTargets {
		return nil, nil
	}
	page, err := m.SearchReleaseCandidatesPaged(ctx, artist, album, tracks, 0, 0, "", "", 30)
	if err != nil {
		return nil, err
	}
	return selectRankedRelease(page.Candidates, tracks), nil
}

func selectRankedRelease(items []ReleaseCandidateItem, tracks []ResolvedTrack) *ReleaseCandidateItem {
	var best *ReleaseCandidateItem
	for i := range items {
		c := &items[i]
		if 100*c.MatchedCount < 85*c.TotalCount || c.MappingLimited || c.AmbiguousTracks > 0 || !c.Evaluation.Compatible ||
			c.Evaluation.TitleSimilarity != 1 || c.Evaluation.ArtistSimilarity < 0.95 ||
			!IsAllowedURL(c.URL) || c.ResultType == "playlist" {
			continue
		}
		versionMissing := false
		for idx, evaluation := range c.TrackEvaluations {
			if desired := desiredReleaseTrackClaim(tracks[idx], c.Policy); desired != "unknown" && evaluation.VersionClaim != desired {
				versionMissing = true
				break
			}
		}
		if versionMissing {
			continue
		}
		if best == nil {
			best = c
			continue
		}
		if best.MatchedCount == c.MatchedCount && best.VersionMatches == c.VersionMatches &&
			best.Score-c.Score <= 2 && !maps.EqualFunc(best.TrackMapping, c.TrackMapping, SameAudioSource) {
			return nil
		}
	}
	return best
}

// ReleaseCandidatesPageResult содержит список кандидатов цельного релиза и состояние постраничной пагинации.
type ReleaseCandidatesPageResult struct {
	Candidates        []ReleaseCandidateItem `json:"candidates"`
	IsCyrillic        bool                   `json:"is_cyrillic"`
	HasMoreSoundCloud bool                   `json:"has_more_soundcloud"`
	AlbumOffset       int                    `json:"album_offset"`
	PlaylistOffset    int                    `json:"playlist_offset"`
	AlbumToken        string                 `json:"album_token,omitempty"`
	PlaylistToken     string                 `json:"playlist_token,omitempty"`
}

// SearchReleaseCandidates выполняет мульти-поиск кандидатов релиза (YouTube Music и SoundCloud)
// и возвращает отранжированный список кандидатов с маппингом треков и контекстными бейджами цензуры.
func (m *FullReleaseMatcher) SearchReleaseCandidates(ctx context.Context, targetArtist, targetAlbum string, targetTracks []ResolvedTrack, soundCloudLimit ...int) ([]ReleaseCandidateItem, bool, error) {
	limit := 30
	if len(soundCloudLimit) > 0 {
		limit = soundCloudLimit[0]
	}
	res, err := m.SearchReleaseCandidatesPaged(ctx, targetArtist, targetAlbum, targetTracks, 0, 0, "", "", limit)
	return res.Candidates, res.IsCyrillic, err
}

// SearchReleaseCandidatesPaged выполняет постраничный мульти-поиск кандидатов релиза.
// При запросе продолжения (albumOffset > 0, playlistOffset > 0 или передаче токенов)
// опрос YouTube Music пропускается, чтобы не дублировать и не перезатирать уже найденные YT Music релизы.
func (m *FullReleaseMatcher) SearchReleaseCandidatesPaged(
	ctx context.Context,
	targetArtist, targetAlbum string,
	targetTracks []ResolvedTrack,
	albumOffset, playlistOffset int,
	albumToken, playlistToken string,
	soundCloudLimit int, policies ...VersionPolicy,
) (ReleaseCandidatesPageResult, error) {
	if soundCloudLimit <= 0 {
		soundCloudLimit = 30
	}
	soundCloudLimit = SoundCloudCandidateLimit(soundCloudLimit)

	isCyrillic := releaseUsesCyrillic(targetArtist, targetAlbum, targetTracks)

	policy := PolicyRelease
	if len(policies) > 0 {
		policy = policies[0]
	}
	_, censorStatus, censorBadge := ScoreReleaseCandidate("", false, 0)
	isContinuation := albumOffset != 0 || playlistOffset != 0 || albumToken != "" || playlistToken != ""
	var ytCandidates []AlbumReleaseCandidate
	var scRes SoundCloudSetsPageResult
	var ytErr, scErr error
	var searches sync.WaitGroup
	if !isContinuation && m.ytMusic != nil {
		searches.Go(func() {
			providerCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			ytCandidates, ytErr = m.ytMusic.SearchAlbumReleaseCandidates(providerCtx, targetArtist, targetAlbum, 4)
		})
	}
	if m.scSearcher != nil {
		searches.Go(func() {
			scRes, scErr = m.scSearcher.SearchSoundCloudSetsPaged(ctx, targetArtist, targetAlbum, albumOffset, playlistOffset, albumToken, playlistToken, soundCloudLimit)
		})
	}
	searches.Wait()
	if scErr != nil && isContinuation {
		return ReleaseCandidatesPageResult{}, scErr
	}

	var items []ReleaseCandidateItem
	for idx, cand := range ytCandidates {
		candURL := ""
		if cand.PlaylistID != "" {
			candURL = fmt.Sprintf("https://music.youtube.com/playlist?list=%s", cand.PlaylistID)
		} else if cand.BrowseID != "" {
			candURL = fmt.Sprintf("https://music.youtube.com/browse/%s", cand.BrowseID)
		}
		items = append(items, ReleaseCandidateItem{
			ID: fmt.Sprintf("ytm_%d_%s", idx, cand.BrowseID), Source: "ytmusic_album",
			Title: cand.Title, Artist: cand.Artist, URL: candURL,
			TrackCount: len(cand.Tracks), CoverURL: cand.CoverURL, Tracks: cand.Tracks,
			IsCyrillic: isCyrillic, CensorStatus: censorStatus, CensorBadge: censorBadge,
		})
	}
	for idx, cand := range scRes.Candidates {
		items = append(items, ReleaseCandidateItem{
			ID: fmt.Sprintf("sc_%d_%s", idx, cand.BrowseID), Source: "soundcloud_set", ResultType: cand.ResultType,
			Title: cand.Title, Artist: cand.Artist, URL: cand.PlaylistID,
			TrackCount: len(cand.Tracks), CoverURL: cand.CoverURL, Tracks: cand.Tracks,
			IsCyrillic: isCyrillic, CensorStatus: censorStatus, CensorBadge: censorBadge,
		})
	}
	if len(items) == 0 && (ytErr != nil || scErr != nil) {
		return ReleaseCandidatesPageResult{}, errors.Join(ytErr, scErr)
	}
	if err := ctx.Err(); err != nil {
		return ReleaseCandidatesPageResult{}, err
	}
	return ReleaseCandidatesPageResult{
		Candidates:        RankReleaseItems(items, targetArtist, targetAlbum, targetTracks, policy),
		IsCyrillic:        isCyrillic,
		HasMoreSoundCloud: scRes.HasMoreSoundCloud,
		AlbumOffset:       scRes.AlbumOffset,
		PlaylistOffset:    scRes.PlaylistOffset,
		AlbumToken:        scRes.AlbumToken,
		PlaylistToken:     scRes.PlaylistToken,
	}, nil
}

// ParseAndMatchReleaseURL разбирает произвольную прямую ссылку на сет/альбом и сопоставляет её с целевыми треками.
func (m *FullReleaseMatcher) ParseAndMatchReleaseURL(ctx context.Context, rawURL, targetArtist, targetAlbum string, targetTracks []ResolvedTrack, policies ...VersionPolicy) (*ReleaseCandidateItem, error) {
	searcher := m.scSearcher
	if searcher == nil {
		searcher = NewSoundCloudSetSearcher("")
	}
	cand, err := searcher.ParseSetURL(ctx, rawURL)
	if err != nil {
		return nil, err
	}

	policy := PolicyRelease
	if len(policies) > 0 {
		policy = policies[0]
	}
	sourceType := "direct_url"
	switch source, _ := (&SourceResolver{}).DetectSource(rawURL); source {
	case "soundcloud":
		sourceType = "soundcloud_set"
	case "youtube":
		sourceType = "ytmusic_album"
	}

	_, censorStatus, censorBadge := ScoreReleaseCandidate("", false, 0)
	items := []ReleaseCandidateItem{{
		ID: "direct_custom", Source: sourceType, ResultType: cand.ResultType,
		Title: cand.Title, Artist: cand.Artist, URL: rawURL,
		TrackCount: len(cand.Tracks), CoverURL: cand.CoverURL, Tracks: cand.Tracks,
		IsCyrillic:   releaseUsesCyrillic(targetArtist, targetAlbum, targetTracks),
		CensorStatus: censorStatus, CensorBadge: censorBadge,
	}}
	return &RankReleaseItems(items, targetArtist, targetAlbum, targetTracks, policy)[0], nil
}

func releaseUsesCyrillic(artist, album string, tracks []ResolvedTrack) bool {
	if ContainsCyrillic(artist) || ContainsCyrillic(album) {
		return true
	}
	for _, track := range tracks {
		if ContainsCyrillic(track.Title) || ContainsCyrillic(track.Artist) {
			return true
		}
	}
	return false
}
