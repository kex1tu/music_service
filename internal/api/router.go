package api

import (
	"html/template"
	"io/fs"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"music-manager/internal/api/handlers"
	"music-manager/internal/config"
	"music-manager/internal/core/auth"
	"music-manager/web"
)

type RouterParams struct {
	Config          *config.Config
	AuthHandler     *handlers.AuthHandler
	DownloadHandler *handlers.DownloadHandler
	TagHandler      *handlers.TagHandler
	FileHandler     *handlers.FileHandler
	LyricsHandler   *handlers.LyricsHandler
}

// NewRouter constructs and configures the main Chi HTTP router.
func NewRouter(p RouterParams) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Compress(5))

	// Parse HTML templates (supports both modular components/modals and single index.html)
	tmpl, _ := template.ParseFS(web.Assets, "templates/*.html", "templates/components/*.html", "templates/modals/*.html")

	// Health check endpoint
	healthHandler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","app":"music-manager","version":"1.0.0"}`))
	}
	r.Get("/api/health", healthHandler)
	r.Head("/api/health", healthHandler)

	// Static assets from embedded filesystem
	staticFS, err := fs.Sub(web.Assets, "static")
	if err == nil {
		r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	}

	// Main Web UI page
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if tmpl != nil && tmpl.Lookup("index.html") != nil {
			if err := tmpl.ExecuteTemplate(w, "index.html", nil); err == nil {
				return
			}
		}
		// Fallback to reading file directly
		indexHTML, err := web.Assets.ReadFile("templates/index.html")
		if err != nil {
			http.Error(w, "failed to read index.html", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(indexHTML)
	})

	// Public Auth Endpoints & Aliases
	r.Post("/login", p.AuthHandler.Login)
	r.Post("/logout", p.AuthHandler.Logout)
	r.Route("/api/auth", func(r chi.Router) {
		r.Post("/login", p.AuthHandler.Login)
		r.Post("/logout", p.AuthHandler.Logout)
		r.Get("/me", p.AuthHandler.Me)
	})

	// Protected Endpoints
	authMiddleware := auth.Middleware(p.Config.SecretKey)

	// Downloads API
	r.Route("/downloads", func(r chi.Router) {
		r.Use(authMiddleware)
		r.Post("/", p.DownloadHandler.CreateDownload)
		r.Post("/batch", p.DownloadHandler.CreateBatchDownload)
		r.Get("/discography", p.DownloadHandler.GetDiscography)
		r.Post("/torrent", p.DownloadHandler.UploadTorrent)
		r.Get("/", p.DownloadHandler.ListDownloads)
		r.Get("/queue/summary", p.DownloadHandler.GetQueueSummary)
		r.Post("/queue/clear", p.DownloadHandler.ClearQueue)
		r.Delete("/completed", p.DownloadHandler.ClearCompleted)
		r.Get("/{id}", p.DownloadHandler.GetDownload)
		r.Delete("/{id}", p.DownloadHandler.DeleteDownload)
		r.Post("/{id}/retry", p.DownloadHandler.RetryDownload)
		r.Get("/{id}/tracks", p.DownloadHandler.GetDownloadTracks)
	})

	// Tags API
	r.Route("/tags", func(r chi.Router) {
		r.Use(authMiddleware)
		r.Get("/", p.TagHandler.GetTags)
		r.Post("/", p.TagHandler.SaveTags)
		r.Post("/batch", p.TagHandler.BatchSaveTags)
		r.Put("/batch", p.TagHandler.BatchSaveTags)
		r.Post("/recognize-audio", p.TagHandler.RecognizeAudio)
		r.Get("/cover", p.TagHandler.GetCover)
		r.Get("/covers/search", p.TagHandler.SearchCovers)
		r.Get("/search-deezer-albums", p.TagHandler.SearchDeezerAlbums)
		r.Get("/deezer/albums", p.TagHandler.SearchDeezerAlbums)
		r.Get("/deezer-album-details", p.TagHandler.GetDeezerAlbumDetails)
		r.Get("/deezer/albums/{id}", p.TagHandler.GetDeezerAlbumDetails)
	})

	// Lyrics API
	r.Route("/lyrics", func(r chi.Router) {
		r.Use(authMiddleware)
		r.Get("/", p.LyricsHandler.GetLyrics)
		r.Get("/search", p.LyricsHandler.SearchLyrics)
		r.Post("/", p.LyricsHandler.SaveLyrics)
		r.Delete("/", p.LyricsHandler.DeleteLyrics)
	})

	// Files API & Library Audit
	r.Route("/files", func(r chi.Router) {
		r.Use(authMiddleware)
		r.Get("/", p.FileHandler.ListFiles)
		r.Delete("/", p.FileHandler.DeleteFile)
		r.Get("/stream", p.FileHandler.StreamAudio)
		r.Get("/search", p.FileHandler.SearchLibrary)

		r.Route("/audit", func(r chi.Router) {
			r.Get("/search", p.FileHandler.SearchLibrary)
			r.Get("/summary", p.FileHandler.AuditSummary)
			r.Post("/refresh", p.FileHandler.RefreshAudit)
			r.Get("/invalid-paths", p.FileHandler.InvalidPaths)
			r.Post("/fix-path", p.FileHandler.FixPath)
			r.Post("/fix-all-paths", p.FileHandler.FixAllPaths)
			r.Get("/missing-lyrics", p.FileHandler.MissingLyrics)
			r.Post("/missing-lyrics/fetch-all", p.FileHandler.BatchLyricsFetch)
			r.Get("/missing-lyrics/progress", p.FileHandler.BatchLyricsProgress)
			r.Get("/missing-covers", p.FileHandler.MissingCovers)
			r.Get("/duplicates", p.FileHandler.Duplicates)
			r.Post("/duplicates/fix", p.FileHandler.FixDuplicates)
			r.Post("/duplicates/fix-all", p.FileHandler.FixAllDuplicates)
			r.Get("/non-en-genres", p.FileHandler.NonEnGenres)
			r.Post("/retag-genres", p.FileHandler.RetagGenres)
			r.Post("/reorganize", p.FileHandler.ReorganizeLibrary)
			r.Get("/incomplete-albums", p.FileHandler.IncompleteAlbums)
			r.Get("/incomplete-albums/catalog-match", p.FileHandler.CatalogMatch)
			r.Get("/incomplete-albums/editor-data", p.FileHandler.BackfillEditorData)
			r.Get("/incomplete-albums/backfill-editor-data", p.FileHandler.BackfillEditorData)
			r.Post("/incomplete-albums/download-missing", p.FileHandler.DownloadMissing)
			r.Get("/tag-issues", p.FileHandler.TagIssues)
			r.Post("/tag-issues/fix", p.FileHandler.FixTagIssue)
			r.Post("/tag-issues/fix-safe", p.FileHandler.FixSafeTagIssues)
			r.Post("/tag-issues/fix-selected", p.FileHandler.FixSelectedTagIssues)
			r.Post("/tag-issues/fix-all", p.FileHandler.FixAllTagIssues)
			r.Get("/tag-issues/catalog-match", p.FileHandler.CatalogMatchTrack)
			r.Post("/tag-issues/apply-catalog", p.FileHandler.ApplyCatalogTags)
		})
	})

	return r
}
