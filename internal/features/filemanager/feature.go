package filemanager

import (
	"github.com/go-chi/chi/v5"
)

// Module представляет изолированный вертикальный слайс FileManager & Streaming.
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
	return "filemanager"
}

// Service возвращает ссылку на доменный сервис файлового менеджера.
func (m *Module) Service() *Service {
	return m.service
}

// RegisterRoutes регистрирует маршруты файлового менеджера и стриминга в Chi-роутере.
func (m *Module) RegisterRoutes(r chi.Router) {
	// 1. Традиционные маршруты /files
	r.Route("/files", func(r chi.Router) {
		r.Get("/", m.ListFiles)
		r.Delete("/", m.DeleteFile)
		r.Get("/stream", m.StreamAudio)
		r.Get("/search", m.SearchLibrary)
		r.Post("/rename", m.RenameFile)
		r.Post("/reorganize", m.ReorganizeLibrary)
	})

	// 2. Современные RESTful маршруты /api/files
	r.Route("/api/files", func(r chi.Router) {
		r.Get("/", m.ListFiles)
		r.Delete("/", m.DeleteFile)
		r.Post("/rename", m.RenameFile)
		r.Get("/search", m.SearchLibrary)
	})

	// 3. Стриминг и метаданные треков /api/tracks
	r.Route("/api/tracks", func(r chi.Router) {
		r.Get("/{id}/stream", m.StreamTrack)
		r.Get("/{id}/metadata", m.GetTrackMetadata)
		r.Get("/{id}/cover", m.GetTrackCover)
	})

	// 4. Каталог библиотеки и VPN-обфускация (xHTTP chunks) /api/library
	r.Route("/api/library", func(r chi.Router) {
		r.Get("/tracks", m.ListLibraryTracks)
		r.Get("/stream/{id}/chunks", m.StreamTrack)
		r.Get("/stream/{id}/chunks/*", m.StreamTrack)
	})
}
