package downloader

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"music-manager/internal/core/httputil"
	"music-manager/internal/core/mediaexec"
	"music-manager/internal/services/resolver"
)

// SwitchTrackVersion переключает аудиоисточник трека на альтернативный SoundCloud URL.
// POST /downloads/tracks/{trackId}/switch-version
// POST /downloads/tracks/{trackId}/bind-source
// POST /api/downloads/tracks/{trackId}/switch-version
// POST /api/downloads/tracks/{trackId}/bind-source
func (m *Module) SwitchTrackVersion(w http.ResponseWriter, r *http.Request) {
	trackID := chi.URLParam(r, "trackId")
	if trackID == "" {
		httputil.RespondError(w, http.StatusBadRequest, "trackId is required")
		return
	}

	var req SwitchVersionRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.RespondError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	req.TargetURL = strings.TrimSpace(req.TargetURL)
	if req.TargetURL == "" {
		httputil.RespondError(w, http.StatusBadRequest, "target_url is required")
		return
	}

	if req.VersionPolicy == "" {
		req.VersionPolicy = resolver.PolicyRelease
	}
	if !resolver.ValidVersionPolicy(req.VersionPolicy) {
		httputil.RespondError(w, 400, "invalid version_policy")
		return
	}
	result, err := m.service.switchJobTrackSource(r.Context(), strings.TrimSpace(req.JobID), trackID, req.TargetURL, req.VersionPolicy)
	if err != nil {
		respondQueueChangeError(w, err)
		return
	}

	httputil.RespondJSON(w, http.StatusOK, result)
}

// GetCandidates ищет альтернативные версии трека на YouTube Music, SoundCloud и YouTube.
// GET /downloads/candidates
// GET /api/downloads/candidates
func (m *Module) GetCandidates(w http.ResponseWriter, r *http.Request) {
	policy := resolver.VersionPolicy(r.URL.Query().Get("version_policy"))
	if policy == "" {
		policy = resolver.PolicyRelease
	}
	if !resolver.ValidVersionPolicy(policy) {
		httputil.RespondError(w, 400, "invalid version_policy")
		return
	}
	artist := strings.TrimSpace(httputil.QueryString(r, "artist", ""))
	title := strings.TrimSpace(httputil.QueryString(r, "title", ""))
	customQ := strings.TrimSpace(httputil.QueryString(r, "q", ""))
	dur := httputil.QueryInt(r, "duration", 0)

	if customQ == "" && (artist == "" && title == "") {
		httputil.RespondError(w, http.StatusBadRequest, "query, or artist and title are required")
		return
	}

	candidates, err := m.service.GetCandidatesWithLimit(r.Context(), artist, title, dur, customQ, r.URL.Query().Get("isrc"), httputil.QueryInt(r, "soundcloud_limit", 30))
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	candidates = resolver.RankCandidateItems(candidates, title, artist, dur, policy)
	httputil.RespondJSON(w, http.StatusOK, CandidatesResponse{
		VersionPolicy: policy, Selection: "manual",
		Artist:     artist,
		Title:      title,
		Query:      customQ,
		Candidates: candidates,
		Total:      len(candidates),
	})
}

// PreviewAudio стримит 15-секундный MP3 фрагмент трека в реальном времени без скачивания на диск.
// GET /downloads/preview
// GET /api/downloads/preview
func (m *Module) PreviewAudio(w http.ResponseWriter, r *http.Request) {
	targetURL := strings.TrimSpace(httputil.QueryString(r, "url", ""))
	if targetURL == "" {
		httputil.RespondError(w, http.StatusBadRequest, "url parameter is required")
		return
	}

	if !resolver.IsAllowedURL(targetURL) {
		httputil.RespondError(w, http.StatusBadRequest, "domain is not in allowed whitelist")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	streamer := m.getPreviewStreamer()
	if streamer == nil {
		streamer = defaultPreviewStreamer
	}

	if err := streamer(ctx, targetURL, w); err != nil {
		httputil.RespondError(w, http.StatusBadGateway, err.Error())
		return
	}
}

// defaultPreviewStreamer реализует реальный стриминг через yt-dlp и ffmpeg
// с автоматической изоляцией подпроцессов в тестовом режиме.
func defaultPreviewStreamer(ctx context.Context, targetURL string, w http.ResponseWriter) error {
	if os.Getenv("MUSIC_MANAGER_TEST_MODE") == "1" {
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Transfer-Encoding", "chunked")

		// Минимальные валидные кадры MPEG-1 Layer 3 (320 kbps, 44.1 kHz, stereo)
		dummyPayload := make([]byte, 1044*4)
		for i := 0; i < 4; i++ {
			offset := i * 1044
			dummyPayload[offset+0] = 0xFF
			dummyPayload[offset+1] = 0xFB
			dummyPayload[offset+2] = 0xE0
			dummyPayload[offset+3] = 0x04
		}
		_, _ = w.Write(dummyPayload)
		return nil
	}

	cmdDlp := mediaexec.CommandContext(ctx, "yt-dlp",
		"--no-warnings",
		"--no-playlist",
		"--format", "bestaudio/best",
		"-g",
		targetURL,
	)
	var outDlp, errDlp bytes.Buffer
	cmdDlp.Stdout = &outDlp
	cmdDlp.Stderr = &errDlp

	if err := cmdDlp.Run(); err != nil || strings.TrimSpace(outDlp.String()) == "" {
		cleanErr := resolver.CleanYtDlpStderr(errDlp.String())
		if cleanErr == "" {
			cleanErr = "failed to resolve audio stream URL"
		}
		return errors.New(cleanErr)
	}

	streamURL := strings.TrimSpace(strings.Split(outDlp.String(), "\n")[0])
	if streamURL == "" {
		return errors.New("empty stream url resolved")
	}

	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Transfer-Encoding", "chunked")

	cmdFfmpeg := mediaexec.CommandContext(ctx, "ffmpeg",
		"-ss", "30",
		"-i", streamURL,
		"-t", "15",
		"-acodec", "libmp3lame",
		"-b:a", "128k",
		"-f", "mp3",
		"-vn",
		"-",
	)
	cmdFfmpeg.Stdout = w
	if err := cmdFfmpeg.Run(); err != nil {
		cmdFallback := mediaexec.CommandContext(ctx, "ffmpeg",
			"-ss", "0",
			"-i", streamURL,
			"-t", "15",
			"-acodec", "libmp3lame",
			"-b:a", "128k",
			"-f", "mp3",
			"-vn",
			"-",
		)
		cmdFallback.Stdout = w
		_ = cmdFallback.Run()
	}
	return nil
}

// BindBackfillSource привязывает выбранный URL к отсутствующему треку релиза и запускает докачку.
// POST /downloads/backfill/bind-source
// POST /api/downloads/backfill/bind-source
func (m *Module) BindBackfillSource(w http.ResponseWriter, r *http.Request) {
	var req BindBackfillSourceRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.RespondError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if req.VersionPolicy == "" {
		req.VersionPolicy = resolver.PolicyRelease
	}
	if !resolver.ValidVersionPolicy(req.VersionPolicy) {
		httputil.RespondError(w, 400, "invalid version_policy")
		return
	}

	req.TargetURL = strings.TrimSpace(req.TargetURL)
	if req.TargetURL == "" {
		httputil.RespondError(w, http.StatusBadRequest, "target_url is required")
		return
	}

	if !resolver.IsAllowedURL(req.TargetURL) {
		httputil.RespondError(w, http.StatusBadRequest, "target_url domain is not allowed")
		return
	}

	title := req.TrackTitle
	if title == "" {
		title = req.AlbumTitle
	}
	selection, err := m.service.SwitchReleaseSource(r.Context(), SwitchReleaseSourceRequest{
		SourceURL: req.TargetURL, TargetPath: req.Path, VersionPolicy: req.VersionPolicy,
		TrackMapping: map[int]string{0: req.TargetURL},
		TargetTracks: []resolver.ResolvedTrack{{
			Title: title, Artist: req.ArtistName, Album: req.AlbumTitle,
			TrackNumber: req.TrackNumber, Duration: req.Duration, ISRC: req.ISRC,
		}},
	})
	if err != nil {
		if errors.Is(err, errQueueStorage) {
			respondQueueChangeError(w, err)
		} else {
			httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	job, _ := m.service.GetJob(selection.JobID)

	httputil.RespondJSON(w, http.StatusOK, job)
}
