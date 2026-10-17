package radar

import (
	"github.com/go-chi/chi/v5"
)

// Module представляет изолированный вертикальный слайс Release Radar.
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
	return "radar"
}

// Service возвращает ссылку на доменный сервис радара.
func (m *Module) Service() *Service {
	return m.service
}

// RegisterRoutes регистрирует маршруты Release Radar в Chi-роутере.
func (m *Module) RegisterRoutes(r chi.Router) {
	r.Route("/api/radar", func(r chi.Router) {
		r.Get("/feed", m.GetFeed)
		r.Get("/unread-count", m.GetUnreadCount)
		r.Get("/artists", m.GetTrackedArtists)
		r.Post("/artists", m.AddTrackedArtist)
		r.Post("/artists/{name}/follow", m.FollowArtist)
		r.Post("/artists/{name}/auto-download", m.SetAutoDownload)
		r.Post("/artists/{name}/custom-links", m.UpdateCustomLinks)
		r.Get("/artists/{name}/discography", m.GetArtistDiscography)
		r.Post("/scan", m.TriggerScan)
		r.Get("/progress", m.GetScanProgress)
		r.Post("/cancel", m.CancelScan)
		r.Post("/releases/{id}/status", m.SetReleaseStatus)
	})
}
