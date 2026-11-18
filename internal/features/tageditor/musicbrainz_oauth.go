package tageditor

import (
	"context"
	"time"
)

// GetMusicBrainzStatus возвращает статус OAuth подключения к MusicBrainz.
func (s *Service) GetMusicBrainzStatus() MusicBrainzOAuthStatus {
	if s.musicbrainz == nil {
		return MusicBrainzOAuthStatus{
			Configured: false,
			Connected:  false,
		}
	}
	return s.musicbrainz.GetOAuthStatus()
}

// GetMusicBrainzAuthURL возвращает URL страницы авторизации MusicBrainz OAuth 2.0.
func (s *Service) GetMusicBrainzAuthURL(state, redirectURI string) (string, error) {
	if s.musicbrainz == nil {
		return "", ErrClientNotInitialized
	}
	if redirectURI == "" && s.mbConfig.RedirectURI != "" {
		redirectURI = s.mbConfig.RedirectURI
	}
	return s.musicbrainz.GetAuthorizationURL(state, redirectURI), nil
}

// HandleMusicBrainzCallback обрабатывает обмен кода на токен и сохраняет его в базе данных.
func (s *Service) HandleMusicBrainzCallback(ctx context.Context, code, redirectURI string) (string, error) {
	if s.musicbrainz == nil {
		return "", ErrClientNotInitialized
	}
	if redirectURI == "" && s.mbConfig.RedirectURI != "" {
		redirectURI = s.mbConfig.RedirectURI
	}

	tokenResp, err := s.musicbrainz.ExchangeCode(ctx, code, redirectURI)
	if err != nil {
		return "", err
	}

	status := s.musicbrainz.GetOAuthStatus()
	username := status.Username

	// Сохраняем в персистентное хранилище
	if s.tokenStore != nil && tokenResp != nil {
		exp := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
		_ = s.tokenStore.SaveTokens(ctx, tokenResp.AccessToken, tokenResp.RefreshToken, exp, username)
	}

	return username, nil
}

// DisconnectMusicBrainz сбрасывает токены OAuth и возвращает клиент в анонимный режим.
func (s *Service) DisconnectMusicBrainz(ctx context.Context) error {
	if s.musicbrainz != nil {
		_ = s.musicbrainz.Disconnect(ctx)
	}
	if s.tokenStore != nil {
		_ = s.tokenStore.ClearTokens(ctx)
	}
	return nil
}

// ConfiguredRedirectURI возвращает предварительно настроенный RedirectURI для MusicBrainz.
func (s *Service) ConfiguredRedirectURI() string {
	return s.mbConfig.RedirectURI
}
