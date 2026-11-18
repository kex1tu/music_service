package downloader

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"music-manager/internal/core/httputil"
	"music-manager/internal/core/safefs"
	"music-manager/internal/db"
	"music-manager/internal/id3"
	"music-manager/internal/services/resolver"
)

// GetReleaseCandidates возвращает список кандидатов цельных релизов (альбомов/сетов)
// для активной задачи, неполного релиза или папки библиотеки.
// GET /downloads/release-candidates
// GET /api/downloads/release-candidates
func (m *Module) GetReleaseCandidates(w http.ResponseWriter, r *http.Request) {
	artist := strings.TrimSpace(httputil.QueryString(r, "artist", ""))
	album := strings.TrimSpace(httputil.QueryString(r, "album", ""))
	jobID := strings.TrimSpace(httputil.QueryString(r, "job_id", ""))
	path := strings.TrimSpace(httputil.QueryString(r, "path", ""))
	missingOnly := httputil.QueryBool(r, "missing_only", false)

	var targetTracks []resolver.ResolvedTrack

	// 1. Если указан job_id — загружаем треки из задачи
	if jobID != "" {
		if job, ok := m.service.GetJob(jobID); ok {
			targetTracks = job.Tracks
			if artist == "" && job.ArtistName != nil {
				artist = *job.ArtistName
			}
			if album == "" && job.AlbumTitle != nil {
				album = *job.AlbumTitle
			}
		}
	}

	var present map[db.TrackPosition]bool
	if missingOnly {
		if path == "" || m.service.MusicSandbox() == nil || m.service.Repo() == nil {
			httputil.RespondError(w, http.StatusBadRequest, "укажите папку целевого релиза")
			return
		}
		full, err := m.service.MusicSandbox().Resolve(path, true)
		if err != nil {
			httputil.RespondError(w, http.StatusBadRequest, "недопустимый путь целевого альбома")
			return
		}
		path, err = m.service.MusicSandbox().ToRelative(full)
		if err != nil {
			httputil.RespondError(w, http.StatusBadRequest, "недопустимый путь целевого альбома")
			return
		}
		missing, err := m.service.Repo().GetMissingTracksForAlbum(r.Context(), path)
		if err != nil {
			httputil.RespondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, gap := range missing {
			track := resolvedBackfillTrack(gap.CatalogTrack, "")
			track.Album, track.AlbumArtist = album, artist
			targetTracks = append(targetTracks, track)
		}
		if len(targetTracks) == 0 {
			present, err = m.service.Repo().GetAlbumTrackPositions(r.Context(), path)
			if err != nil {
				httputil.RespondError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
	}

	// 2. Если указан path папки в библиотеке — считываем MP3 через песочницу
	if !missingOnly && len(targetTracks) == 0 && path != "" && m.service.MusicSandbox() != nil {
		if realPath, err := m.service.MusicSandbox().Resolve(path, false); err == nil {
			if entries, err := safefs.ReadDir(realPath); err == nil {
				for _, e := range entries {
					if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".mp3") {
						itemFull := filepath.Join(realPath, e.Name())
						relTrack := filepath.Join(path, e.Name())
						if t, err := id3.ReadTags(itemFull, relTrack); err == nil && t != nil {
							if artist == "" && t.Artist != "" {
								artist = t.Artist
							}
							if album == "" && t.Album != "" {
								album = t.Album
							}
							targetTracks = append(targetTracks, resolver.ResolvedTrack{
								TrackID:     fmt.Sprintf("local_%d", len(targetTracks)+1),
								TrackNumber: t.TrackNumber,
								Title:       t.Title,
								Artist:      t.Artist,
								Album:       t.Album,
								Duration:    int(t.Duration),
							})
						}
					}
				}
			}
		}
	}

	// 3. Если треков все еще нет, но есть artist и album — пробуем найти эталонный треклист через Deezer
	if len(targetTracks) == 0 && artist != "" && album != "" && m.deezerClient != nil {
		if albums, err := m.deezerClient.SearchAlbums(r.Context(), fmt.Sprintf("%s %s", artist, album)); err == nil && len(albums) > 0 {
			if dAlbum, err := m.deezerClient.GetAlbum(r.Context(), strconv.FormatInt(albums[0].ID, 10)); err == nil && dAlbum != nil {
				catalog, err := resolver.DeezerCatalogTracks(dAlbum)
				if err == nil {
					for _, item := range catalog {
						if present[item.TrackPosition] {
							continue
						}
						track := resolvedBackfillTrack(item, "")
						track.Album, track.AlbumArtist = dAlbum.Title, dAlbum.Artist.Name
						targetTracks = append(targetTracks, track)
					}
				}
			}
		}
	}

	if missingOnly && len(targetTracks) == 0 {
		httputil.RespondError(w, http.StatusNotFound, "не найдены пропуски для докачки")
		return
	}

	if artist == "" && album == "" && len(targetTracks) == 0 {
		httputil.RespondError(w, http.StatusBadRequest, "artist and album or job_id are required")
		return
	}

	matcher := m.service.Resolver().FullReleaseMatcher()
	albumOffset := httputil.QueryInt(r, "album_offset", 0)
	playlistOffset := httputil.QueryInt(r, "playlist_offset", 0)
	albumToken := strings.TrimSpace(r.URL.Query().Get("album_token"))
	playlistToken := strings.TrimSpace(r.URL.Query().Get("playlist_token"))
	limit := httputil.QueryInt(r, "soundcloud_limit", 30)

	pageRes, err := matcher.SearchReleaseCandidatesPaged(r.Context(), artist, album, targetTracks, albumOffset, playlistOffset, albumToken, playlistToken, limit)
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	policy := resolver.VersionPolicy(r.URL.Query().Get("version_policy"))
	if policy == "" {
		policy = resolver.PolicyRelease
	}
	if !resolver.ValidVersionPolicy(policy) {
		httputil.RespondError(w, 400, "invalid version_policy")
		return
	}
	cands := resolver.RankReleaseItems(pageRes.Candidates, artist, album, targetTracks, policy)

	httputil.RespondJSON(w, http.StatusOK, ReleaseCandidatesResponse{
		VersionPolicy:     policy,
		Selection:         "manual",
		Artist:            artist,
		Album:             album,
		IsCyrillic:        pageRes.IsCyrillic,
		TargetTracks:      targetTracks,
		Candidates:        cands,
		HasMoreSoundCloud: pageRes.HasMoreSoundCloud,
		AlbumOffset:       pageRes.AlbumOffset,
		PlaylistOffset:    pageRes.PlaylistOffset,
		AlbumToken:        pageRes.AlbumToken,
		PlaylistToken:     pageRes.PlaylistToken,
	})
}

// ParseReleaseURL парсит произвольную ссылку на сет/альбом и сопоставляет треклист с целевыми треками.
// POST /downloads/release-candidates/parse-url
// POST /api/downloads/release-candidates/parse-url
func (m *Module) ParseReleaseURL(w http.ResponseWriter, r *http.Request) {
	var req ParseReleaseURLRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.RespondError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	req.URL = strings.TrimSpace(req.URL)
	if req.URL == "" {
		httputil.RespondError(w, http.StatusBadRequest, "url is required")
		return
	}

	if !resolver.IsAllowedURL(req.URL) {
		httputil.RespondError(w, http.StatusBadRequest, "domain is not in allowed whitelist")
		return
	}

	matcher := m.service.Resolver().FullReleaseMatcher()
	cand, err := matcher.ParseAndMatchReleaseURL(r.Context(), req.URL, req.Artist, req.Album, req.TargetTracks)
	if err != nil {
		httputil.RespondError(w, http.StatusBadRequest, fmt.Sprintf("ошибка парсинга ссылки: %v", err))
		return
	}

	httputil.RespondJSON(w, http.StatusOK, cand)
}

// SwitchReleaseSource применяет выбранный источник цельного релиза к задаче и запускает скачивание.
// POST /downloads/releases/switch-source
// POST /api/downloads/releases/switch-source
func (m *Module) SwitchReleaseSource(w http.ResponseWriter, r *http.Request) {
	var req SwitchReleaseSourceRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.RespondError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	result, err := m.service.SwitchReleaseSource(r.Context(), req)
	if err != nil {
		httputil.RespondError(w, http.StatusBadRequest, err.Error())
		return
	}

	httputil.RespondJSON(w, http.StatusOK, result)
}
