package radio

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"music-manager/internal/core/auth"
	"music-manager/internal/db"
	serviceRadio "music-manager/internal/services/radio"
)

// ServiceConfig определяет зависимости для инициализации сервиса Radio.
type ServiceConfig struct {
	SessionPolicy    auth.Policy
	Selector         *serviceRadio.WaveSelector
	Streamer         *serviceRadio.IcyStreamer
	M3U              *serviceRadio.M3UGenerator
	Subsonic         *serviceRadio.NavidromeSubsonicClient
	NavidromeReader  *serviceRadio.NavidromeReader
	Repo             Repository
	RadioStreamToken string
	SecretKey        string
	RadioWaveSize    int
}

// Service инкапсулирует бизнес-логику радиовещания «Моя Волна», генерации плейлистов и синхронизации с внешними серверами.
type Service struct {
	sessionPolicy    auth.Policy
	selector         *serviceRadio.WaveSelector
	streamer         *serviceRadio.IcyStreamer
	m3u              *serviceRadio.M3UGenerator
	subsonic         *serviceRadio.NavidromeSubsonicClient
	navidromeReader  *serviceRadio.NavidromeReader
	repo             Repository
	radioStreamToken string
	secretKey        string
	radioWaveSize    int
}

// NewService создает новый экземпляр Service.
func NewService(cfg ServiceConfig) *Service {
	waveSize := cfg.RadioWaveSize
	if waveSize <= 0 {
		waveSize = 60
	}
	return &Service{
		selector:         cfg.Selector,
		sessionPolicy:    cfg.SessionPolicy,
		streamer:         cfg.Streamer,
		m3u:              cfg.M3U,
		subsonic:         cfg.Subsonic,
		navidromeReader:  cfg.NavidromeReader,
		repo:             cfg.Repo,
		radioStreamToken: cfg.RadioStreamToken,
		secretKey:        cfg.SecretKey,
		radioWaveSize:    waveSize,
	}
}

// CheckStreamAuth проверяет авторизацию для ICY потока (токен в query ?token=..., заголовок X-Stream-Token, Bearer или cookie).
func (s *Service) CheckStreamAuth(r *http.Request) bool {
	if s.radioStreamToken == "" {
		return true
	}
	token := r.URL.Query().Get("token")
	if token == "" {
		token = r.Header.Get("X-Stream-Token")
	}
	if token != "" && token == s.radioStreamToken {
		return true
	}
	authHdr := r.Header.Get("Authorization")
	if strings.HasPrefix(authHdr, "Bearer ") && strings.TrimPrefix(authHdr, "Bearer ") == s.radioStreamToken {
		return true
	}
	if cookie, err := r.Cookie(s.cookieName()); err == nil && cookie.Value != "" {
		if _, valid := auth.ValidateSessionToken(cookie.Value, s.secretKey, s.sessionPolicy); valid {
			return true
		}
	}
	return false
}

// GetWaveQueue возвращает сформированную очередь треков для плеера «Моя Волна».
func (s *Service) GetWaveQueue(ctx context.Context, limit int) ([]*db.TrackEntity, error) {
	if s.selector == nil {
		return []*db.TrackEntity{}, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	return s.selector.GeneratePlaylist(ctx, limit)
}

// GetWaveStats возвращает общую статистику панели Радио.
func (s *Service) GetWaveStats(ctx context.Context) (*WaveStatsResponse, error) {
	var topGenres []string
	if s.repo != nil {
		topGenres, _ = s.repo.GetAvailableGenres(ctx, 10)
	}
	if topGenres == nil {
		topGenres = []string{}
	}

	naviAvailable, naviWarning := s.navidromeReader.ListeningStatus(ctx)
	subsonicConfigured := s.subsonic != nil && s.subsonic.IsConfigured()

	return &WaveStatsResponse{
		TopGenres:          topGenres,
		NavidromeAvailable: naviAvailable,
		NavidromeWarning:   naviWarning,
		SubsonicConfigured: subsonicConfigured,
		WaveSize:           s.radioWaveSize,
		StreamTokenSet:     s.radioStreamToken != "",
		StreamToken:        s.radioStreamToken,
	}, nil
}

// GetNowPlaying возвращает текущий трек и историю эфира.
func (s *Service) GetNowPlaying(stationType, param string) (*serviceRadio.RadioTrackInfo, []*serviceRadio.RadioTrackInfo) {
	if s.streamer == nil {
		return nil, []*serviceRadio.RadioTrackInfo{}
	}
	return s.streamer.GetNowPlaying(stationType, param)
}

// ServeStream обслуживает непрерывный ICY-аудиопоток.
func (s *Service) ServeStream(w http.ResponseWriter, r *http.Request, stationType, param string) {
	if s.streamer == nil {
		http.Error(w, "Streamer unavailable", http.StatusServiceUnavailable)
		return
	}
	s.streamer.ServeHTTP(w, r, stationType, param)
}

// GetGenres возвращает список популярных жанров библиотеки.
func (s *Service) GetGenres(ctx context.Context, limit int) ([]string, error) {
	if s.repo == nil {
		return []string{}, nil
	}
	if limit <= 0 {
		limit = 50
	}
	return s.repo.GetAvailableGenres(ctx, limit)
}

// GenerateM3U генерирует плейлист «Моя Волна.m3u».
func (s *Service) GenerateM3U(ctx context.Context) (string, error) {
	if s.m3u == nil {
		return "", fmt.Errorf("M3U generator not configured")
	}
	return s.m3u.GenerateWaveM3U(ctx)
}

// SyncNavidromeRadio регистрирует поток «Моя Волна» в Navidrome через Subsonic REST API.
func (s *Service) SyncNavidromeRadio(ctx context.Context, req SyncRadioRequest, scheme, host string) (*SyncRadioResponse, error) {
	if s.subsonic == nil || !s.subsonic.IsConfigured() {
		return nil, fmt.Errorf("Navidrome Subsonic credentials not configured in environment")
	}

	stationName := "Моя Волна"
	if strings.TrimSpace(req.Name) != "" {
		stationName = strings.TrimSpace(req.Name)
	}

	streamURL := strings.TrimSpace(req.StreamURL)
	if streamURL == "" {
		streamURL = fmt.Sprintf("%s://%s/api/radio/stream/wave", scheme, host)
		if s.radioStreamToken != "" {
			streamURL = fmt.Sprintf("%s?token=%s", streamURL, s.radioStreamToken)
		}
	}
	homePageURL := fmt.Sprintf("%s://%s", scheme, host)

	if err := s.subsonic.CreateRadioStation(ctx, stationName, streamURL, homePageURL); err != nil {
		return nil, fmt.Errorf("failed to export radio station to Navidrome: %w", err)
	}

	return &SyncRadioResponse{
		Status:    "ok",
		Name:      stationName,
		StreamURL: streamURL,
	}, nil
}

func (s *Service) cookieName() string {
	if s.sessionPolicy.Name != "" {
		return s.sessionPolicy.Name
	}
	return auth.CookieName
}
