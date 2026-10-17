package tageditor

import (
	"github.com/go-chi/chi/v5"
)

// Module представляет изолированный вертикальный слайс TagEditor & Metadata.
type Module struct {
	service *Service
}

// NewModule создает новый экземпляр Module.
func NewModule(svc *Service) *Module {
	return &Module{
		service: svc,
	}
}

// Name возвращает канонический идентификатор слайса.
func (m *Module) Name() string {
	return "tageditor"
}

// Service возвращает ссылку на доменный сервис редактора тегов.
func (m *Module) Service() *Service {
	return m.service
}

// RegisterRoutes регистрирует все 39 HTTP-маршрутов модуля в Chi-роутере.
func (m *Module) RegisterRoutes(r chi.Router) {
	// 1. Канонические маршруты /tags (17 эндпоинтов)
	r.Route("/tags", func(r chi.Router) {
		r.Get("/", m.GetTags)
		r.Post("/", m.SaveTags)
		r.Post("/batch", m.BatchSaveTags)
		r.Put("/batch", m.BatchSaveTags)
		r.Post("/recognize-audio", m.RecognizeAudio)
		r.Get("/cover", m.GetCover)
		r.Get("/covers/search", m.SearchCovers)

		// Deezer алиасы
		r.Get("/search-deezer-albums", m.SearchDeezerAlbums)
		r.Get("/deezer/albums", m.SearchDeezerAlbums)
		r.Get("/deezer-album-details", m.GetDeezerAlbumDetails)
		r.Get("/deezer/albums/{id}", m.GetDeezerAlbumDetails)
		r.Get("/search-deezer-tracks", m.SearchDeezerTracks)
		r.Get("/deezer/tracks", m.SearchDeezerTracks)
		r.Get("/deezer-track-details", m.GetDeezerTrackDetails)
		r.Get("/deezer/tracks/{id}", m.GetDeezerTrackDetails)
	})

	// 2. Современные RESTful алиасы под префиксом /api/tags (12 эндпоинтов)
	r.Route("/api/tags", func(r chi.Router) {
		r.Get("/", m.GetTags)
		r.Post("/", m.SaveTags)
		r.Post("/batch", m.BatchSaveTags)
		r.Put("/batch", m.BatchSaveTags)
		r.Post("/recognize-audio", m.RecognizeAudio)
		r.Get("/cover", m.GetCover)
		r.Get("/covers/search", m.SearchCovers)
		r.Get("/deezer/albums", m.SearchDeezerAlbums)
		r.Get("/deezer/albums/{id}", m.GetDeezerAlbumDetails)
		r.Get("/deezer/tracks", m.SearchDeezerTracks)
		r.Get("/deezer/tracks/{id}", m.GetDeezerTrackDetails)
		r.Post("/rename-pattern", m.RenamePattern)
	})

	// 3. Единый API поиска по внешним каталогам /api/catalog (4 эндпоинта)
	r.Route("/api/catalog", func(r chi.Router) {
		r.Get("/search", m.SearchCatalog)
		r.Get("/deezer/album/{id}", m.GetCatalogDeezerAlbum)
		r.Get("/musicbrainz/release/{id}", m.GetCatalogMusicBrainzRelease)
		r.Get("/shazam/track/{id}", m.GetCatalogShazamTrack)
	})

	// 4. MusicBrainz OAuth API /api/auth/musicbrainz (4 эндпоинта)
	r.Route("/api/auth/musicbrainz", func(r chi.Router) {
		r.Get("/callback", m.MusicBrainzCallback) // Публичный редирект от musicbrainz.org
		r.Get("/login", m.MusicBrainzLogin)
		r.Get("/status", m.MusicBrainzStatus)
		r.Post("/disconnect", m.MusicBrainzDisconnect)
	})

	// 5. Puddletag Workstation и переименование файлов по шаблону (5 эндпоинтов)
	r.Post("/files/rename-pattern", m.RenamePattern)
	r.Route("/puddletag", func(r chi.Router) {
		r.Post("/rename-pattern", m.RenamePattern)
		r.Post("/batch", m.BatchSaveTags)
	})
	r.Route("/api/puddletag", func(r chi.Router) {
		r.Post("/rename-pattern", m.RenamePattern)
		r.Post("/batch", m.BatchSaveTags)
	})
}
