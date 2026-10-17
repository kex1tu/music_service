package tageditor

import (
	"context"
	"time"

	"music-manager/internal/id3"
	"music-manager/internal/services/lyrics"
	"music-manager/internal/services/organizer"
	"music-manager/internal/services/resolver"
)

// PathOrganizer определяет контракт для перемещения и канонической раскладки альбомов.
type PathOrganizer interface {
	MoveAlbum(sourceDir string, fallbackAlbum, fallbackAlbumArtist string, tracks []id3.TrackTags, coverBase64 string) (*organizer.OrganizerResult, error)
}

// LyricsService определяет контракт чтения, поиска и сохранения текстов песен (.lrc / .txt).
type LyricsService interface {
	GetLyrics(relPath string) (*lyrics.LyricsResponse, error)
	SearchLyrics(ctx context.Context, artist, title, album string, duration int) (*lyrics.LyricsSearchResult, error)
	SaveLyrics(relPath string, content string, kind string) error
	DeleteLyrics(relPath string) (bool, error)
}

// DeezerCatalog определяет контракт взаимодействия с каталогом Deezer API.
type DeezerCatalog interface {
	SearchAlbums(ctx context.Context, query string) ([]resolver.DeezerAlbum, error)
	GetAlbum(ctx context.Context, albumID string) (*resolver.DeezerAlbum, error)
	SearchTracks(ctx context.Context, query string) ([]resolver.DeezerTrack, error)
	GetTrack(ctx context.Context, trackID string) (*resolver.DeezerTrack, error)
}

// MusicBrainzCatalog определяет контракт взаимодействия с базой MusicBrainz и OAuth 2.0.
type MusicBrainzCatalog interface {
	SearchReleases(ctx context.Context, query string) ([]resolver.CatalogReleaseSummary, error)
	GetRelease(ctx context.Context, mbid string) (*resolver.CatalogReleaseDetails, error)
	GetOAuthStatus() resolver.MusicBrainzOAuthStatus
	GetAuthorizationURL(state string, overrideRedirectURI ...string) string
	ExchangeCode(ctx context.Context, code string, redirectURI string) (*resolver.OAuthTokenResponse, error)
	Disconnect(ctx context.Context) error
}

// ShazamCatalog определяет контракт взаимодействия с поиском Apple/Shazam и songrec.
type ShazamCatalog interface {
	Search(ctx context.Context, query string) ([]resolver.CatalogReleaseSummary, error)
	RecognizeAudioFile(ctx context.Context, fullFilePath string) (*resolver.CatalogReleaseDetails, error)
}

// AcoustIDProvider определяет контракт акустического распознавания дорожек по отпечаткам Chromaprint.
type AcoustIDProvider interface {
	Recognize(ctx context.Context, audioPath string) (*resolver.CatalogReleaseDetails, error)
}

// ArtistEnricher определяет контракт для автоматического парсинга приглашенных исполнителей (feat.).
type ArtistEnricher interface {
	EnrichTrackArtists(ctx context.Context, title string, currentArtists []string) []string
}

// TokenPersistence определяет сохранение токенов авторизации в постоянном хранилище.
type TokenPersistence interface {
	SaveTokens(ctx context.Context, accessToken, refreshToken string, expiresAt time.Time, username string) error
	ClearTokens(ctx context.Context) error
}
