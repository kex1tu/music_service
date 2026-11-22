package resolver

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

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
	"music.yandex.kz", "music.yandex.by", "music.yandex.uz",
	"music.apple.com",
}

// IsAllowedURL проверяет, принадлежит ли URL белому списку разрешенных доменов.
func IsAllowedURL(rawURL string) bool {
	cleanURL := ExtractURLFromInput(rawURL)
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
	VersionClaim     string                        `json:"version_claim,omitempty"`
	VersionPolicy    VersionPolicy                 `json:"version_policy,omitempty"`
	TrackID          string                        `json:"track_id"`
	CatalogTrackID   string                        `json:"catalog_track_id,omitempty"`
	YandexTrackID    int64                         `json:"yandex_track_id,omitempty"`
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

// GetYandexTrackID возвращает числовой идентификатор Яндекс Музыки, если трек получен из Яндекса.
func (t *ResolvedTrack) GetYandexTrackID() (int64, bool) {
	if t.YandexTrackID > 0 {
		return t.YandexTrackID, true
	}
	return ExtractYandexTrackID(t.CatalogTrackID, t.TrackID)
}

// ExtractYandexTrackID извлекает точный числовой идентификатор трека Яндекс Музыки
// из каталожного ID (CatalogTrackID) или ключа очереди (TrackID).
// Возвращает (id, true) только если идентификатор явно принадлежит каталогу Яндекс Музыки (префикс "ya_").
// Идентификаторы сторонних каталогов (например "dz_123", "sc_456", "yt_789", "12345") строго игнорируются.
func ExtractYandexTrackID(catalogTrackID, trackID string) (int64, bool) {
	candidate := strings.TrimSpace(catalogTrackID)
	if candidate == "" || !strings.HasPrefix(candidate, "ya_") {
		candidate = strings.TrimSpace(trackID)
	}
	if !strings.HasPrefix(candidate, "ya_") {
		return 0, false
	}

	rawID := strings.TrimPrefix(candidate, "ya_")
	// Если это составной ключ слота очереди (например "ya_101_1"), отсекаем суффикс слота
	if idx := strings.Index(rawID, "_"); idx != -1 {
		rawID = rawID[:idx]
	}

	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// ReleaseMetadata holds album-level metadata.
type ReleaseMetadata struct {
	AlbumTitle    string
	AlbumArtist   string
	CoverURL      string
	Year          int
	Genre         string
	TotalTracks   int
	DiscTotal     int
	FetchedTracks int
	SkippedTracks int
	IsPlaylist    bool
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
		deezerClient:       deezer,
		yandexClient:       yandex,
		ytmusicClient:      ytmusic,
		featEnricher:       NewFeatEnricher(yandex, ytmusic),
		audioMatcher:       matcher,
		fullReleaseMatcher: NewFullReleaseMatcher(ytmusic, NewSoundCloudSetSearcher(cookiesPath)),
		cookiesPath:        cookiesPath,
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
	cleanURL := ExtractURLFromInput(rawURL)
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
	cleanURL := ExtractURLFromInput(rawURL)
	source := explicitSource
	if source == "" || source == "auto" || source == "radar" {
		detected, err := r.DetectSource(cleanURL)
		if err != nil {
			return nil, nil, err
		}
		source = detected
	}

	switch source {
	case "deezer":
		return r.resolveDeezer(ctx, cleanURL)
	case "yandex":
		return r.resolveYandex(ctx, cleanURL)
	case "youtube", "soundcloud", "bandcamp":
		return r.resolveYtDlp(ctx, cleanURL)
	case "torrent":
		return r.resolveTorrentOrDeezer(ctx, cleanURL)
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

// AutoSelectRelease searches for a sufficiently evidenced source for a catalog release.
func (r *SourceResolver) AutoSelectRelease(ctx context.Context, artist, album string, tracks []ResolvedTrack) (*ReleaseCandidateItem, error) {
	return r.FullReleaseMatcher().AutoSelectRelease(ctx, artist, album, tracks)
}

// AutoReleaseVersionStatus identifies a source chosen for a catalog release.
const AutoReleaseVersionStatus = "auto_release"

// BindAutomaticRelease assigns links from a single release to unbound catalog tracks.
func (r *SourceResolver) BindAutomaticRelease(ctx context.Context, rawURL string, meta *ReleaseMetadata, tracks []ResolvedTrack) (*ReleaseCandidateItem, error) {
	if r == nil || meta == nil || meta.IsPlaylist || len(tracks) == 0 {
		return nil, nil
	}
	source, err := r.DetectSource(rawURL)
	if err != nil || (source != "deezer" && source != "yandex") {
		return nil, nil
	}
	for _, track := range tracks {
		if track.AudioSourceURL != "" || track.VersionStatus == "manual" || track.VersionStatus == AutoReleaseVersionStatus {
			return nil, nil
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	match, err := r.AutoSelectRelease(ctx, meta.AlbumArtist, meta.AlbumTitle, tracks)
	if match == nil || err != nil {
		return nil, err
	}
	for idx, audioURL := range match.TrackMapping {
		track := &tracks[idx]
		track.AudioSourceURL = audioURL
		track.VersionStatus = AutoReleaseVersionStatus
		track.MatchScore = float64(match.TrackEvaluations[idx].Score)
	}
	return match, nil
}
