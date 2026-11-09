package handlers

import (
	"bytes"
	"context"
	"fmt"
	"net/http"

	"strings"
	"time"

	"music-manager/internal/core/mediaexec"
	"music-manager/internal/services/resolver"
)

// PreviewAudio выполняет потоковое прослушивание 10-15 секунд аудио без полного скачивания на диск.
func (h *DownloadHandler) PreviewAudio(w http.ResponseWriter, r *http.Request) {
	targetURL := strings.TrimSpace(r.URL.Query().Get("url"))
	if targetURL == "" {
		http.Error(w, `{"error":"url parameter is required"}`, http.StatusBadRequest)
		return
	}

	if !resolver.IsAllowedURL(targetURL) {
		http.Error(w, `{"error":"domain is not in allowed whitelist"}`, http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	// 1. Получение прямого медиапотока через yt-dlp -g
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
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, cleanErr), http.StatusBadGateway)
		return
	}

	streamURL := strings.TrimSpace(strings.Split(outDlp.String(), "\n")[0])
	if streamURL == "" {
		http.Error(w, `{"error":"empty stream url resolved"}`, http.StatusBadGateway)
		return
	}

	// 2. Потоковое кодирование 15 секунд фрагмента через ffmpeg со сдвигом на 30с
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
		// Если -ss 30 не сработал (трек короче 30 секунд), пробуем с начала (-ss 0)
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
}
