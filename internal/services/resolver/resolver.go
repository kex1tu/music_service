package resolver

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"music-manager/internal/db"
	"music-manager/internal/services/censorcheck"
)

var allowedDomains = []string{
	"youtube.com", "youtu.be",
	"music.youtube.com",
	"soundcloud.com",
	"bandcamp.com",
	"deezer.com", "deezer.page.link", "link.deezer.com",
	"spotify.com", "spotify.link",
	"music.yandex.ru", "music.yandex.com",
	"music.apple.com",
}

// IsAllowedURL проверяет, принадлежит ли URL белому списку разрешенных доменов.
func IsAllowedURL(rawURL string) bool {
	cleanURL := strings.TrimSpace(rawURL)
	if !strings.HasPrefix(cleanURL, "http://") && !strings.HasPrefix(cleanURL, "https://") {
		cleanURL = "https://" + cleanURL
	}
	u, err := url.Parse(cleanURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, d := range allowedDomains {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

// AudioQualityTelemetry содержит спектральные и кодековые метрики исходного аудиопотока.
type AudioQualityTelemetry struct {
	SourceCodec   string `json:"source_codec"`   // "opus", "aac", "mp3", "flac"
	SourceBitrate int    `json:"source_bitrate"` // в bps: e.g. 128000, 160000, 320000
	CutoffHz      int    `json:"cutoff_hz"`      // e.g. 15400, 18500, 20500
	QualityTier   string `json:"quality_tier"`   // "true_320k", "hq_opus", "upscaled_128k", "lossless"
	Description   string `json:"description"`    // e.g. "YouTube AAC 128k (Срез ~15.4 kHz)"
}

// ResolvedTrack defines the standardized track model across all sources.
type ResolvedTrack struct {
	VersionPolicy    VersionPolicy                 `json:"version_policy,omitempty"`
	TrackID          string                        `json:"track_id"`
	TrackNumber      int                           `json:"track_number"`
	TrackTotal       *int                          `json:"track_total,omitempty"`
	DiscNumber       int                           `json:"disc_number"`
	DiscTotal        *int                          `json:"disc_total,omitempty"`
	Title            string                        `json:"title"`
	Artist           string                        `json:"artist"`
	Album            string                        `json:"album"`
	AlbumArtist      string                        `json:"album_artist"`
	Year             *int                          `json:"year,omitempty"`
	Genre            *string                       `json:"genre,omitempty"`
	Duration         int                           `json:"duration"`
	ISRC             *string                       `json:"isrc,omitempty"`
	CoverURL         *string                       `json:"cover_url,omitempty"`
	AudioSourceURL   string                        `json:"audio_source_url"`
	ConvertedPath    *string                       `json:"converted_path,omitempty"`
	Status           string                        `json:"status"` // pending, downloading, converting, ready, error
	Error            *string                       `json:"error,omitempty"`
	VersionStatus    string                        `json:"version_status,omitempty"`
	Candidates       []db.SoundCloudCandidateItem  `json:"candidates,omitempty"`
	AudioQuality     *AudioQualityTelemetry        `json:"audio_quality,omitempty"`
	QuickListenSpots []censorcheck.QuickListenSpot `json:"quick_listen_spots,omitempty"`
	MatchScore       float64                       `json:"match_score,omitempty"`
	IsLowMatch       bool                          `json:"is_low_match,omitempty"`
}

// ReleaseMetadata holds album-level metadata.
type ReleaseMetadata struct {
	AlbumTitle  string
	AlbumArtist string
	CoverURL    string
	Year        int
	Genre       string
	TotalTracks int
	DiscTotal   int
}

// SourceResolver coordinates URL detection and metadata extraction.
type SourceResolver struct {
	deezerClient       *DeezerClient
	yandexClient       *YandexMusicClient
	ytmusicClient      *YouTubeMusicClient
	featEnricher       *FeatEnricher
	audioMatcher       *AudioMatcher
	fullReleaseMatcher *FullReleaseMatcher
	cookiesPath        string
}

// NewSourceResolver creates a new SourceResolver.
// yandexOpts optionally takes [token, proxyURL].
func NewSourceResolver(deezer *DeezerClient, matcher *AudioMatcher, cookiesPath string, yandexOpts ...string) *SourceResolver {
	if deezer == nil {
		deezer = NewDeezerClient()
	}
	if matcher == nil {
		matcher = NewAudioMatcher(cookiesPath)
	}
	var yaToken, yaProxy string
	if len(yandexOpts) > 0 {
		yaToken = yandexOpts[0]
	}
	if len(yandexOpts) > 1 {
		yaProxy = yandexOpts[1]
	}
	yandex := NewYandexMusicClient(yaToken, yaProxy)
	ytmusic := NewYouTubeMusicClient("")
	if matcher != nil {
		matcher.SetYtMusicClient(ytmusic)
	}
	return &SourceResolver{
		deezerClient:  deezer,
		yandexClient:  yandex,
		ytmusicClient: ytmusic,
		featEnricher:  NewFeatEnricher(yandex, ytmusic),
		audioMatcher:  matcher,
		cookiesPath:   cookiesPath,
	}
}

// SetYandexClient configures a custom YandexMusicClient (e.g. for testing).
func (r *SourceResolver) SetYandexClient(client *YandexMusicClient) {
	if client != nil {
		r.yandexClient = client
		if r.featEnricher != nil {
			r.featEnricher.yandexClient = client
		}
	}
}

// Deezer returns the underlying DeezerClient.
func (r *SourceResolver) Deezer() *DeezerClient {
	return r.deezerClient
}

// Matcher returns the underlying AudioMatcher.
func (r *SourceResolver) Matcher() *AudioMatcher {
	return r.audioMatcher
}

// Enricher returns the underlying FeatEnricher.
func (r *SourceResolver) Enricher() *FeatEnricher {
	return r.featEnricher
}

// YandexClient returns the underlying YandexMusicClient.
func (r *SourceResolver) YandexClient() *YandexMusicClient {
	return r.yandexClient
}

// YtMusicClient returns the underlying YouTubeMusicClient.
func (r *SourceResolver) YtMusicClient() *YouTubeMusicClient {
	return r.ytmusicClient
}

// DetectSource identifies the source platform from a URL or torrent string.
func (r *SourceResolver) DetectSource(rawURL string) (string, error) {
	cleanURL := strings.TrimSpace(rawURL)
	lower := strings.ToLower(cleanURL)

	if strings.HasPrefix(lower, "magnet:") || strings.HasSuffix(lower, ".torrent") || strings.Contains(lower, ".torrent?") {
		return "torrent", nil
	}

	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		cleanURL = "https://" + cleanURL
	}

	u, err := url.Parse(cleanURL)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}

	host := strings.ToLower(u.Hostname())
	allowed := false
	for _, domain := range allowedDomains {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			allowed = true
			break
		}
	}

	if !allowed {
		return "", fmt.Errorf("domain %q is not in the whitelist of allowed sources", host)
	}

	if strings.Contains(host, "deezer.com") || strings.Contains(host, "deezer.page.link") {
		return "deezer", nil
	}
	if strings.Contains(host, "soundcloud.com") {
		return "soundcloud", nil
	}
	if strings.Contains(host, "bandcamp.com") {
		return "bandcamp", nil
	}
	if strings.Contains(host, "youtube.com") || strings.Contains(host, "youtu.be") {
		return "youtube", nil
	}
	if strings.Contains(host, "spotify.com") || strings.Contains(host, "spotify.link") {
		return "spotify", nil
	}
	if strings.Contains(host, "yandex") {
		return "yandex", nil
	}
	if strings.Contains(host, "apple.com") {
		return "applemusic", nil
	}

	return "", fmt.Errorf("could not determine source type for domain %q", host)
}

// Resolve extracts track metadata and audio streams from a source URL.
func (r *SourceResolver) Resolve(ctx context.Context, rawURL, explicitSource string) (*ReleaseMetadata, []ResolvedTrack, error) {
	source := explicitSource
	if source == "" || source == "auto" || source == "radar" {
		detected, err := r.DetectSource(rawURL)
		if err != nil {
			return nil, nil, err
		}
		source = detected
	}

	switch source {
	case "deezer":
		return r.resolveDeezer(ctx, rawURL)
	case "yandex":
		return r.resolveYandex(ctx, rawURL)
	case "youtube", "soundcloud", "bandcamp":
		return r.resolveYtDlp(ctx, rawURL)
	case "torrent":
		return r.resolveTorrentOrDeezer(ctx, rawURL)
	default:
		return nil, nil, fmt.Errorf("source %q is not fully supported for direct download yet", source)
	}
}

// SetFullReleaseMatcher переопределяет FullReleaseMatcher (для мок-тестов).
func (r *SourceResolver) SetFullReleaseMatcher(m *FullReleaseMatcher) {
	r.fullReleaseMatcher = m
}

// FullReleaseMatcher возвращает настроенный экземпляр FullReleaseMatcher.
func (r *SourceResolver) FullReleaseMatcher() *FullReleaseMatcher {
	if r.fullReleaseMatcher != nil {
		return r.fullReleaseMatcher
	}
	return NewFullReleaseMatcher(r.ytmusicClient, NewSoundCloudSetSearcher(r.cookiesPath))
}

// MatchFullRelease ищет цельный официальный релиз на YouTube Music и сопоставляет треклист.
func (r *SourceResolver) MatchFullRelease(ctx context.Context, targetArtist, targetAlbum string, targetTracks []ResolvedTrack) (*FullReleaseMatchResult, error) {
	return r.FullReleaseMatcher().MatchFullRelease(ctx, targetArtist, targetAlbum, targetTracks)
}
