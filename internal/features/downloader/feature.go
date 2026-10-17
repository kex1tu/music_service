package downloader

import (
	"context"
	"net/http"
	"sync"

	"github.com/go-chi/chi/v5"

	"music-manager/internal/services/resolver"
)

// PreviewStreamer выполняет потоковый стриминг превью-аудио в ResponseWriter.
type PreviewStreamer func(ctx context.Context, targetURL string, w http.ResponseWriter) error

// YtReleasesFetcher извлекает список релизов дискографии исполнителя с YouTube/YouTube Music.
type YtReleasesFetcher func(ctx context.Context, targetURL, cookiesPath string) ([]DiscographyRelease, string, error)

// Module представляет изолированный вертикальный слайс Downloader.
type Module struct {
	mu                sync.RWMutex
	service           *Service
	deezerClient      *resolver.DeezerClient
	previewStreamer   PreviewStreamer
	ytReleasesFetcher YtReleasesFetcher
}

// NewModule создает новый экземпляр HTTP-модуля Downloader.
func NewModule(svc *Service, deezerClient ...*resolver.DeezerClient) *Module {
	var dz *resolver.DeezerClient
	if len(deezerClient) > 0 && deezerClient[0] != nil {
		dz = deezerClient[0]
	} else if svc != nil && svc.Resolver() != nil {
		dz = svc.Resolver().Deezer()
	}
	return &Module{
		service:      svc,
		deezerClient: dz,
	}
}

// SetPreviewStreamer переопределяет функцию потокового превью (для unit/integration тестов).
func (m *Module) SetPreviewStreamer(streamer PreviewStreamer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.previewStreamer = streamer
}

func (m *Module) getPreviewStreamer() PreviewStreamer {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.previewStreamer
}

// SetYtReleasesFetcher переопределяет функцию извлечения релизов YouTube (для unit/integration тестов).
func (m *Module) SetYtReleasesFetcher(fetcher YtReleasesFetcher) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ytReleasesFetcher = fetcher
}

func (m *Module) getYtReleasesFetcher() YtReleasesFetcher {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.ytReleasesFetcher
}

// Name возвращает канонический идентификатор слайса.
func (m *Module) Name() string {
	return "downloader"
}

// Service возвращает ссылку на сервис загрузок.
func (m *Module) Service() *Service {
	return m.service
}

// RegisterRoutes регистрирует все 26 HTTP-маршрутов модуля в Chi-роутере.
func (m *Module) RegisterRoutes(r chi.Router) {
	// Основная группа маршрутов /downloads (21 маршрут)
	r.Route("/downloads", func(r chi.Router) {
		r.Post("/", m.CreateJob)
		r.Post("/batch", m.CreateBatch)
		r.Get("/discography", m.GetDiscography)
		r.Post("/torrent", m.UploadTorrent)
		r.Get("/", m.ListJobs)
		r.Get("/queue/summary", m.GetQueueSummary)
		r.Post("/queue/clear", m.ClearQueue)
		r.Delete("/completed", m.ClearCompleted)
		r.Post("/clear-completed", m.ClearCompleted)
		r.Post("/retry-failed", m.RetryAllFailed)
		r.Post("/pause", m.PauseQueue)
		r.Post("/resume", m.ResumeQueue)
		r.Get("/{id}", m.GetJob)
		r.Delete("/{id}", m.DeleteJob)
		r.Post("/{id}/retry", m.RetryJob)
		r.Get("/{id}/tracks", m.GetJobTracks)
		r.Post("/tracks/{trackId}/switch-version", m.SwitchTrackVersion)
		r.Post("/tracks/{trackId}/bind-source", m.SwitchTrackVersion)
		r.Get("/candidates", m.GetCandidates)
		r.Get("/preview", m.PreviewAudio)
		r.Post("/backfill/bind-source", m.BindBackfillSource)
	})

	// Совместимые алиасы под префиксом /api/downloads (5 маршрутов)
	r.Route("/api/downloads", func(r chi.Router) {
		r.Post("/tracks/{trackId}/switch-version", m.SwitchTrackVersion)
		r.Post("/tracks/{trackId}/bind-source", m.SwitchTrackVersion)
		r.Get("/candidates", m.GetCandidates)
		r.Get("/preview", m.PreviewAudio)
		r.Post("/backfill/bind-source", m.BindBackfillSource)
	})
}
