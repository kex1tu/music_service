package radio

import (
	"github.com/go-chi/chi/v5"
)

// Module представляет изолированный вертикальный слайс Radio & Wave.
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
	return "radio"
}

// Service возвращает ссылку на доменный сервис радио.
func (m *Module) Service() *Service {
	return m.service
}

// RegisterRoutes регистрирует маршруты радиовещания и управления станциями в Chi-роутере.
func (m *Module) RegisterRoutes(r chi.Router) {
	// 1. Управление радио и очередями /api/radio
	r.Route("/api/radio", func(r chi.Router) {
		r.Get("/now-playing", m.GetNowPlaying)
		r.Get("/wave/queue", m.GetWaveQueue)
		r.Get("/wave/stats", m.GetWaveStats)
		r.Get("/genres", m.GetGenres)
		r.Post("/m3u/generate", m.GenerateM3U)
		r.Post("/navidrome/sync-radio", m.SyncNavidromeRadio)
	})

	// 2. Непрерывные аудиопотоки ICY /api/radio/stream
	r.Route("/api/radio/stream", func(r chi.Router) {
		r.Options("/*", m.HandleStreamOptions)
		r.Get("/now-playing", m.GetNowPlaying)
		r.Get("/wave", m.StreamWave)
		r.Head("/wave", m.StreamWave)
		r.Get("/wave.mp3", m.StreamWave)
		r.Head("/wave.mp3", m.StreamWave)
		r.Get("/genre/{name}", m.StreamGenre)
		r.Head("/genre/{name}", m.StreamGenre)
		r.Get("/artist/{name}", m.StreamArtist)
		r.Head("/artist/{name}", m.StreamArtist)
	})
}
