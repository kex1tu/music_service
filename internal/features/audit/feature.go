package audit

import (
	"github.com/go-chi/chi/v5"
)

// Module represents the isolated Audit & Library Integrity vertical slice.
type Module struct {
	service *Service
}

// NewModule constructs a new Module instance.
func NewModule(svc *Service) *Module {
	return &Module{
		service: svc,
	}
}

// Name returns the canonical slice name.
func (m *Module) Name() string {
	return "audit"
}

// Service returns the underlying audit service.
func (m *Module) Service() *Service {
	return m.service
}

// RegisterRoutes mounts all 35 audit routes under /files/audit/* as well as modern RESTful aliases.
func (m *Module) RegisterRoutes(r chi.Router) {
	// 1. Canonical /files/audit endpoints (35 routes)
	r.Route("/files/audit", func(r chi.Router) {
		m.registerCensorshipRoutes(r)
		r.Get("/summary", m.Summary)
		r.Post("/refresh", m.Refresh)
		r.Get("/search", m.Search)

		// File paths & reorganization
		r.Get("/invalid-paths", m.InvalidPaths)
		r.Get("/artist-conflicts", m.ArtistConflicts)
		r.Post("/artist-conflicts/resolve", m.ResolveArtistConflict)
		r.Post("/fix-path", m.FixPath)
		r.Post("/fix-all-paths", m.FixAllPaths)
		r.Post("/reorganize", m.Reorganize)
		r.Post("/normalize-navidrome", m.NormalizeNavidrome)

		// Duplicates
		r.Get("/duplicates", m.Duplicates)
		r.Post("/duplicates/fix", m.FixDuplicates)
		r.Post("/duplicates/fix-all", m.FixAllDuplicates)

		// Missing lyrics (.lrc / .txt)
		r.Get("/missing-lyrics", m.MissingLyrics)
		r.Post("/missing-lyrics/fetch-all", m.BatchLyricsFetch)
		r.Get("/missing-lyrics/progress", m.BatchLyricsProgress)

		// Missing covers
		r.Get("/missing-covers", m.MissingCovers)
		r.Post("/missing-covers/backfill", m.BackfillCover)
		r.Post("/missing-covers/backfill-all", m.BatchCoversBackfill)
		r.Get("/missing-covers/progress", m.BatchCoversProgress)
		r.Post("/missing-covers/cancel", m.CancelCoversBatch)

		// Genres
		r.Get("/non-en-genres", m.NonEnGenres)
		r.Post("/retag-genres", m.RetagGenres)

		// Incomplete albums & backfill
		r.Get("/incomplete-albums", m.IncompleteAlbums)
		r.Get("/incomplete-albums/catalog-match", m.CatalogMatch)
		r.Get("/incomplete-albums/editor-data", m.BackfillEditorData)
		r.Get("/incomplete-albums/backfill-editor-data", m.BackfillEditorData)
		r.Post("/incomplete-albums/download-missing", m.DownloadMissing)

		// Tag health audit & fixes
		r.Get("/tag-issues", m.TagIssues)
		r.Post("/tag-issues/fix", m.FixTagIssue)
		r.Post("/tag-issues/fix-safe", m.FixSafeTagIssues)
		r.Post("/tag-issues/fix-selected", m.FixSelectedTagIssues)
		r.Post("/tag-issues/fix-all", m.FixAllTagIssues)
		r.Get("/tag-issues/catalog-match", m.CatalogMatchTrack)
		r.Get("/tag-issues/catalog-match-album", m.CatalogMatchAlbum)
		r.Post("/tag-issues/apply-catalog", m.ApplyCatalogTags)
		r.Post("/tag-issues/apply-catalog-album", m.ApplyCatalogAlbum)
	})

	// 2. Modern RESTful aliases (/api/audit/*)
	r.Route("/api/audit", func(r chi.Router) {
		m.registerCensorshipRoutes(r)
		r.Get("/summary", m.Summary)
		r.Post("/refresh", m.Refresh)
		r.Get("/search", m.Search)
		r.Get("/invalid-paths", m.InvalidPaths)
		r.Post("/fix-path", m.FixPath)
		r.Post("/fix-all-paths", m.FixAllPaths)
		r.Post("/reorganize", m.Reorganize)
		r.Post("/normalize-navidrome", m.NormalizeNavidrome)
		r.Get("/duplicates", m.Duplicates)
		r.Post("/duplicates/fix", m.FixDuplicates)
		r.Post("/duplicates/fix-all", m.FixAllDuplicates)
		r.Post("/duplicates/resolve", m.ResolveDuplicates)
		r.Get("/missing-lyrics", m.MissingLyrics)
		r.Post("/missing-lyrics/fetch-all", m.BatchLyricsFetch)
		r.Get("/missing-lyrics/progress", m.BatchLyricsProgress)
		r.Get("/missing-covers", m.MissingCovers)
		r.Post("/missing-covers/backfill", m.BackfillCover)
		r.Post("/missing-covers/backfill-all", m.BatchCoversBackfill)
		r.Get("/missing-covers/progress", m.BatchCoversProgress)
		r.Post("/missing-covers/cancel", m.CancelCoversBatch)
		r.Get("/non-en-genres", m.NonEnGenres)
		r.Post("/retag-genres", m.RetagGenres)
		r.Get("/incomplete-albums", m.IncompleteAlbums)
		r.Get("/incomplete-albums/catalog-match", m.CatalogMatch)
		r.Get("/incomplete-albums/editor-data", m.BackfillEditorData)
		r.Post("/incomplete-albums/download-missing", m.DownloadMissing)
		r.Get("/tag-issues", m.TagIssues)
		r.Post("/tag-issues/fix", m.FixTagIssue)
		r.Post("/tag-issues/fix-safe", m.FixSafeTagIssues)
		r.Post("/tag-issues/fix-selected", m.FixSelectedTagIssues)
		r.Post("/tag-issues/fix-all", m.FixAllTagIssues)
		r.Get("/tag-issues/catalog-match", m.CatalogMatchTrack)
		r.Get("/tag-issues/catalog-match-album", m.CatalogMatchAlbum)
		r.Post("/tag-issues/apply-catalog", m.ApplyCatalogTags)
		r.Post("/tag-issues/apply-catalog-album", m.ApplyCatalogAlbum)
	})

	// 3. /api/library/audit aliases
	r.Route("/api/library/audit", func(r chi.Router) {
		r.Get("/summary", m.Summary)
		r.Post("/refresh", m.Refresh)
		r.Get("/search", m.Search)
	})

	// 4. /duplicates and /api/duplicates aliases
	r.Route("/duplicates", func(r chi.Router) {
		r.Get("/", m.Duplicates)
		r.Post("/fix", m.FixDuplicates)
		r.Post("/fix-all", m.FixAllDuplicates)
		r.Post("/resolve", m.ResolveDuplicates)
	})
	r.Route("/api/duplicates", func(r chi.Router) {
		r.Get("/", m.Duplicates)
		r.Post("/fix", m.FixDuplicates)
		r.Post("/fix-all", m.FixAllDuplicates)
		r.Post("/resolve", m.ResolveDuplicates)
	})

	// 5. /backfill aliases
	r.Route("/backfill", func(r chi.Router) {
		r.Post("/download-missing", m.DownloadMissing)
		r.Post("/covers", m.BackfillCover)
		r.Post("/covers-all", m.BatchCoversBackfill)
		r.Get("/editor-data", m.BackfillEditorData)
	})

	// 6. /tags/audit aliases
	r.Route("/tags/audit", func(r chi.Router) {
		r.Get("/", m.TagIssues)
		r.Post("/fix", m.FixTagIssue)
		r.Post("/fix-safe", m.FixSafeTagIssues)
		r.Post("/fix-selected", m.FixSelectedTagIssues)
		r.Post("/fix-all", m.FixAllTagIssues)
		r.Get("/catalog-match", m.CatalogMatchTrack)
		r.Get("/catalog-match-album", m.CatalogMatchAlbum)
		r.Post("/apply-catalog", m.ApplyCatalogTags)
		r.Post("/apply-catalog-album", m.ApplyCatalogAlbum)
	})
}
