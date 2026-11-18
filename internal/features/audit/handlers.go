package audit

import (
	"errors"
	"net/http"
	"strings"

	"music-manager/internal/core/httputil"
	"music-manager/internal/core/sandbox"
)

// Summary handles GET /files/audit/summary, GET /api/audit/summary
func (m *Module) Summary(w http.ResponseWriter, r *http.Request) {
	summary, err := m.service.GetSummary(r.Context())
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httputil.RespondJSON(w, http.StatusOK, summary)
}

// AuditSummary is an alias for backward compatibility.
func (m *Module) AuditSummary(w http.ResponseWriter, r *http.Request) {
	m.Summary(w, r)
}

// Refresh handles POST /files/audit/refresh, POST /api/audit/refresh
func (m *Module) Refresh(w http.ResponseWriter, r *http.Request) {
	summary, err := m.service.Refresh(r.Context())
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httputil.RespondJSON(w, http.StatusOK, summary)
}

// RefreshAudit is an alias for backward compatibility.
func (m *Module) RefreshAudit(w http.ResponseWriter, r *http.Request) {
	m.Refresh(w, r)
}

// Search handles GET /files/audit/search, GET /api/audit/search
func (m *Module) Search(w http.ResponseWriter, r *http.Request) {
	query := httputil.QueryString(r, "q", "")
	limit := httputil.QueryInt(r, "limit", 50)

	result, err := m.service.Search(r.Context(), query, limit)
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httputil.RespondJSON(w, http.StatusOK, result)
}

// SearchLibrary is an alias for backward compatibility.
func (m *Module) SearchLibrary(w http.ResponseWriter, r *http.Request) {
	m.Search(w, r)
}

// InvalidPaths handles GET /files/audit/invalid-paths, GET /api/audit/invalid-paths
func (m *Module) InvalidPaths(w http.ResponseWriter, r *http.Request) {
	paths, err := m.service.GetInvalidPaths(r.Context())
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if paths == nil {
		paths = []InvalidPathRecord{}
	}
	httputil.RespondJSON(w, http.StatusOK, paths)
}

// FixPath handles POST /files/audit/fix-path, POST /api/audit/fix-path
func (m *Module) FixPath(w http.ResponseWriter, r *http.Request) {
	var req FixPathRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.RespondError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		httputil.RespondError(w, http.StatusBadRequest, "path required")
		return
	}

	res, err := m.service.FixPath(r.Context(), req.Path, req.Overwrite)
	if err != nil {
		if errors.Is(err, sandbox.ErrPathTraversal) || errors.Is(err, ErrSandboxViolation) {
			httputil.RespondError(w, http.StatusForbidden, "access denied")
		} else if errors.Is(err, ErrFileNotFound) {
			httputil.RespondError(w, http.StatusNotFound, "file not found")
		} else {
			httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	if !res.Success {
		httputil.RespondJSON(w, http.StatusConflict, struct {
			*FixPathResponse
			Detail string `json:"detail"`
		}{res, res.Message})
		return
	}
	httputil.RespondJSON(w, http.StatusOK, res)
}

// FixAllPaths handles POST /files/audit/fix-all-paths, POST /api/audit/fix-all-paths
func (m *Module) FixAllPaths(w http.ResponseWriter, r *http.Request) {
	res, err := m.service.FixAllPaths(r.Context())
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httputil.RespondJSON(w, http.StatusOK, res)
}

// Reorganize handles POST /files/audit/reorganize, POST /api/audit/reorganize
func (m *Module) Reorganize(w http.ResponseWriter, r *http.Request) {
	res, err := m.service.Reorganize(r.Context())
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httputil.RespondJSON(w, http.StatusOK, res)
}

// ReorganizeLibrary is an alias for backward compatibility.
func (m *Module) ReorganizeLibrary(w http.ResponseWriter, r *http.Request) {
	m.Reorganize(w, r)
}

// NormalizeNavidrome handles POST /files/audit/normalize-navidrome, POST /api/audit/normalize-navidrome
func (m *Module) NormalizeNavidrome(w http.ResponseWriter, r *http.Request) {
	rep, err := m.service.NormalizeNavidrome(r.Context())
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httputil.RespondJSON(w, http.StatusOK, NormalizeNavidromeResponse{
		Success: true,
		Report:  *rep,
	})
}

// Duplicates handles GET /files/audit/duplicates, GET /duplicates, GET /api/duplicates
func (m *Module) Duplicates(w http.ResponseWriter, r *http.Request) {
	res, err := m.service.GetDuplicates(r.Context())
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if res.Groups == nil {
		res.Groups = []DuplicateGroup{}
	}
	httputil.RespondJSON(w, http.StatusOK, res)
}

// FixDuplicates handles POST /files/audit/duplicates/fix, POST /duplicates/fix, POST /api/duplicates/fix
func (m *Module) FixDuplicates(w http.ResponseWriter, r *http.Request) {
	var req FixDuplicatesRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.RespondError(w, http.StatusBadRequest, "invalid json")
		return
	}

	var res *FixDuplicatesResponse
	var err error
	if len(req.PathsToDelete) == 0 {
		res, err = m.service.FixDuplicateGroup(r.Context(), req.GroupKey)
	} else {
		res, err = m.service.FixDuplicates(r.Context(), req.PathsToDelete)
	}
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httputil.RespondJSON(w, http.StatusOK, res)
}

// FixAllDuplicates handles POST /files/audit/duplicates/fix-all, POST /duplicates/fix-all
func (m *Module) FixAllDuplicates(w http.ResponseWriter, r *http.Request) {
	res, err := m.service.FixAllDuplicates(r.Context())
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httputil.RespondJSON(w, http.StatusOK, res)
}

// ResolveDuplicates handles POST /duplicates/resolve, POST /api/duplicates/resolve
func (m *Module) ResolveDuplicates(w http.ResponseWriter, r *http.Request) {
	var req ResolveDuplicatesRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.RespondError(w, http.StatusBadRequest, "invalid json")
		return
	}

	res, err := m.service.ResolveDuplicates(r.Context(), req.GroupKey, req.KeepPath, req.PathsToDelete)
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httputil.RespondJSON(w, http.StatusOK, res)
}

// MissingLyrics handles GET /files/audit/missing-lyrics, GET /api/audit/missing-lyrics
func (m *Module) MissingLyrics(w http.ResponseWriter, r *http.Request) {
	limit := httputil.QueryInt(r, "limit", 100)
	items, err := m.service.GetMissingLyrics(r.Context(), limit)
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if items == nil {
		items = []MissingLyricsItem{}
	}
	httputil.RespondJSON(w, http.StatusOK, items)
}

// BatchLyricsFetch handles POST /files/audit/missing-lyrics/fetch-all, POST /api/audit/missing-lyrics/fetch-all
func (m *Module) BatchLyricsFetch(w http.ResponseWriter, r *http.Request) {
	progress, err := m.service.StartBatchLyrics(r.Context())
	if err != nil && !errors.Is(err, ErrBatchAlreadyRunning) {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httputil.RespondJSON(w, http.StatusOK, progress)
}

// BatchLyricsProgress handles GET /files/audit/missing-lyrics/progress, GET /api/audit/missing-lyrics/progress
func (m *Module) BatchLyricsProgress(w http.ResponseWriter, r *http.Request) {
	progress := m.service.GetBatchLyricsProgress()
	httputil.RespondJSON(w, http.StatusOK, progress)
}

// MissingCovers handles GET /files/audit/missing-covers, GET /api/audit/missing-covers
func (m *Module) MissingCovers(w http.ResponseWriter, r *http.Request) {
	limit := httputil.QueryInt(r, "limit", 100)
	items, err := m.service.GetMissingCovers(r.Context(), limit)
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if items == nil {
		items = []MissingCoverItem{}
	}
	httputil.RespondJSON(w, http.StatusOK, items)
}

// BackfillCover handles POST /files/audit/missing-covers/backfill, POST /backfill/covers
func (m *Module) BackfillCover(w http.ResponseWriter, r *http.Request) {
	var req BackfillCoverRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.RespondError(w, http.StatusBadRequest, "invalid json request")
		return
	}
	if req.FolderPath == "" && req.AlbumID <= 0 {
		httputil.RespondError(w, http.StatusBadRequest, "missing folder_path or album_id")
		return
	}

	resp, err := m.service.BackfillAlbumCover(r.Context(), req.FolderPath, req.AlbumID)
	if err != nil {
		if errors.Is(err, ErrFolderNotFound) {
			httputil.RespondError(w, http.StatusNotFound, err.Error())
		} else {
			httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	httputil.RespondJSON(w, http.StatusOK, resp)
}

// BackfillAlbumCover is an alias for backward compatibility.
func (m *Module) BackfillAlbumCover(w http.ResponseWriter, r *http.Request) {
	m.BackfillCover(w, r)
}

// BatchCoversBackfill handles POST /files/audit/missing-covers/backfill-all, POST /backfill/covers-all
func (m *Module) BatchCoversBackfill(w http.ResponseWriter, r *http.Request) {
	progress, err := m.service.StartBatchCovers(r.Context())
	if err != nil && !errors.Is(err, ErrBatchAlreadyRunning) {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httputil.RespondJSON(w, http.StatusOK, progress)
}

// BatchCoversProgress handles GET /files/audit/missing-covers/progress, GET /api/audit/missing-covers/progress
func (m *Module) BatchCoversProgress(w http.ResponseWriter, r *http.Request) {
	progress := m.service.GetBatchCoversProgress()
	httputil.RespondJSON(w, http.StatusOK, progress)
}

// CancelCoversBatch handles POST /files/audit/missing-covers/cancel, POST /api/audit/missing-covers/cancel
func (m *Module) CancelCoversBatch(w http.ResponseWriter, r *http.Request) {
	m.service.CancelBatchCovers()
	httputil.RespondJSON(w, http.StatusOK, map[string]bool{"success": true, "cancelled": true})
}

// NonEnGenres handles GET /files/audit/non-en-genres, GET /api/audit/non-en-genres
func (m *Module) NonEnGenres(w http.ResponseWriter, r *http.Request) {
	items, err := m.service.GetNonEnGenres(r.Context())
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if items == nil {
		items = []NonEnGenreItem{}
	}
	httputil.RespondJSON(w, http.StatusOK, items)
}

// RetagGenres handles POST /files/audit/retag-genres, POST /api/audit/retag-genres
func (m *Module) RetagGenres(w http.ResponseWriter, r *http.Request) {
	resp, err := m.service.RetagGenres(r.Context())
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httputil.RespondJSON(w, http.StatusOK, resp)
}

// IncompleteAlbums handles GET /files/audit/incomplete-albums, GET /api/audit/incomplete-albums
func (m *Module) IncompleteAlbums(w http.ResponseWriter, r *http.Request) {
	resp, err := m.service.GetIncompleteAlbums(r.Context())
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if resp.Albums == nil {
		resp.Albums = []IncompleteAlbum{}
	}
	httputil.RespondJSON(w, http.StatusOK, resp)
}

// CatalogMatch handles GET /files/audit/incomplete-albums/catalog-match, GET /api/audit/incomplete-albums/catalog-match
func (m *Module) CatalogMatch(w http.ResponseWriter, r *http.Request) {
	albumPath := httputil.QueryString(r, "path", "")
	deezerID := httputil.QueryString(r, "deezer_album_id", "")
	if albumPath == "" {
		httputil.RespondError(w, http.StatusBadRequest, "missing path query param")
		return
	}

	match, err := m.service.CatalogMatchAlbum(r.Context(), albumPath, deezerID)
	if err != nil {
		if errors.Is(err, ErrAlbumNotFound) {
			httputil.RespondError(w, http.StatusNotFound, err.Error())
		} else {
			httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	httputil.RespondJSON(w, http.StatusOK, match)
}

// BackfillEditorData handles GET /files/audit/incomplete-albums/editor-data, GET /files/audit/incomplete-albums/backfill-editor-data
func (m *Module) BackfillEditorData(w http.ResponseWriter, r *http.Request) {
	albumPath := httputil.QueryString(r, "path", "")
	jobID := httputil.QueryString(r, "job_id", "")

	data, err := m.service.GetBackfillEditorData(r.Context(), albumPath, jobID)
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httputil.RespondJSON(w, http.StatusOK, data)
}

// DownloadMissing handles POST /files/audit/incomplete-albums/download-missing, POST /backfill/download-missing
func (m *Module) DownloadMissing(w http.ResponseWriter, r *http.Request) {
	var req DownloadMissingRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.RespondError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Path == "" && (req.DeezerAlbumID == nil || *req.DeezerAlbumID == "") {
		httputil.RespondError(w, http.StatusBadRequest, "path or deezer_album_id required")
		return
	}

	jobs, err := m.service.DownloadMissing(r.Context(), req)
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httputil.RespondJSON(w, http.StatusOK, jobs)
}

// TagIssues handles GET /files/audit/tag-issues, GET /tags/audit, GET /api/audit/tag-issues
func (m *Module) TagIssues(w http.ResponseWriter, r *http.Request) {
	filter := httputil.QueryString(r, "filter", "all")
	limit := httputil.QueryInt(r, "limit", 200)

	resp, err := m.service.GetTagIssues(r.Context(), filter, limit)
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if resp.Issues == nil {
		resp.Issues = []TagIssue{}
	}
	httputil.RespondJSON(w, http.StatusOK, resp)
}

// FixTagIssue handles POST /files/audit/tag-issues/fix, POST /tags/audit/fix
func (m *Module) FixTagIssue(w http.ResponseWriter, r *http.Request) {
	var req FixTagIssueRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.RespondError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Path == "" {
		httputil.RespondError(w, http.StatusBadRequest, "missing path")
		return
	}

	tags, err := m.service.FixTagIssue(r.Context(), req)
	if err != nil {
		if errors.Is(err, sandbox.ErrPathTraversal) || errors.Is(err, ErrSandboxViolation) {
			httputil.RespondError(w, http.StatusForbidden, "access denied")
		} else if errors.Is(err, ErrFileNotFound) {
			httputil.RespondError(w, http.StatusNotFound, "file not found in sandbox")
		} else {
			httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	httputil.RespondJSON(w, http.StatusOK, FixTagIssueResponse{
		Success: true,
		Path:    req.Path,
		Tags:    tags,
	})
}

// FixSafeTagIssues handles POST /files/audit/tag-issues/fix-safe, POST /tags/audit/fix-safe
func (m *Module) FixSafeTagIssues(w http.ResponseWriter, r *http.Request) {
	resp, err := m.service.FixSafeTagIssues(r.Context())
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httputil.RespondJSON(w, http.StatusOK, resp)
}

// FixSelectedTagIssues handles POST /files/audit/tag-issues/fix-selected, POST /tags/audit/fix-selected
func (m *Module) FixSelectedTagIssues(w http.ResponseWriter, r *http.Request) {
	var req FixSelectedTagsRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.RespondError(w, http.StatusBadRequest, "invalid json")
		return
	}

	resp, err := m.service.FixSelectedTagIssues(r.Context(), req.Items)
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httputil.RespondJSON(w, http.StatusOK, resp)
}

// FixAllTagIssues handles POST /files/audit/tag-issues/fix-all, POST /tags/audit/fix-all
func (m *Module) FixAllTagIssues(w http.ResponseWriter, r *http.Request) {
	resp, err := m.service.FixAllTagIssues(r.Context())
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httputil.RespondJSON(w, http.StatusOK, resp)
}

// CatalogMatchTrack handles GET /files/audit/tag-issues/catalog-match, GET /tags/audit/catalog-match
func (m *Module) CatalogMatchTrack(w http.ResponseWriter, r *http.Request) {
	path := httputil.QueryString(r, "path", "")
	title := httputil.QueryString(r, "title", "")
	artist := httputil.QueryString(r, "artist", "")

	result, err := m.service.CatalogMatchTrack(r.Context(), path, title, artist)
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httputil.RespondJSON(w, http.StatusOK, result)
}

// CatalogMatchAlbum handles GET /files/audit/tag-issues/catalog-match-album, GET /tags/audit/catalog-match-album
func (m *Module) CatalogMatchAlbum(w http.ResponseWriter, r *http.Request) {
	folderPath := httputil.QueryString(r, "folder_path", "")
	if folderPath == "" {
		httputil.RespondError(w, http.StatusBadRequest, "missing folder_path parameter")
		return
	}

	resp, err := m.service.CatalogMatchAlbumDetails(r.Context(), folderPath)
	if err != nil {
		if errors.Is(err, ErrFolderNotFound) || errors.Is(err, ErrFileNotFound) {
			httputil.RespondError(w, http.StatusNotFound, err.Error())
		} else {
			httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	httputil.RespondJSON(w, http.StatusOK, resp)
}

// ApplyCatalogTags handles POST /files/audit/tag-issues/apply-catalog, POST /tags/audit/apply-catalog
func (m *Module) ApplyCatalogTags(w http.ResponseWriter, r *http.Request) {
	var req ApplyCatalogTagsRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.RespondError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Path == "" {
		httputil.RespondError(w, http.StatusBadRequest, "missing path")
		return
	}

	if err := m.service.ApplyCatalogTags(r.Context(), req); err != nil {
		if errors.Is(err, sandbox.ErrPathTraversal) || errors.Is(err, ErrSandboxViolation) {
			httputil.RespondError(w, http.StatusForbidden, "access denied")
		} else if errors.Is(err, ErrFileNotFound) {
			httputil.RespondError(w, http.StatusNotFound, "file not found")
		} else {
			httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	httputil.RespondJSON(w, http.StatusOK, map[string]bool{"success": true})
}

// ApplyCatalogAlbum handles POST /files/audit/tag-issues/apply-catalog-album, POST /tags/audit/apply-catalog-album
func (m *Module) ApplyCatalogAlbum(w http.ResponseWriter, r *http.Request) {
	var req ApplyCatalogAlbumRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.RespondError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.FolderPath == "" {
		httputil.RespondError(w, http.StatusBadRequest, "missing folder_path")
		return
	}

	resp, err := m.service.ApplyCatalogAlbum(r.Context(), req)
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httputil.RespondJSON(w, http.StatusOK, resp)
}
