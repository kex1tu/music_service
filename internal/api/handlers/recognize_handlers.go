package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"music-manager/internal/core/mediaexec"
)

type songrecResult struct {
	Matches []any `json:"matches"`
	Track   *struct {
		Title    string `json:"title"`
		Subtitle string `json:"subtitle"`
		Genres   struct {
			Primary string `json:"primary"`
		} `json:"genres"`
		Images struct {
			CoverArt   string `json:"coverart"`
			CoverArtHQ string `json:"coverarthq"`
		} `json:"images"`
		Sections []struct {
			Type     string `json:"type"`
			Metadata []struct {
				Title string `json:"title"`
				Text  string `json:"text"`
			} `json:"metadata"`
		} `json:"sections"`
	} `json:"track"`
}

type recognizeAudioRequest struct {
	Path string `json:"path"`
}

type recognizeAudioResponse struct {
	Recognized bool   `json:"recognized"`
	Title      string `json:"title,omitempty"`
	Artist     string `json:"artist,omitempty"`
	Album      string `json:"album,omitempty"`
	Year       int    `json:"year,omitempty"`
	Genre      string `json:"genre,omitempty"`
	CoverURL   string `json:"cover_url,omitempty"`
}

// RecognizeAudio performs acoustic music recognition via songrec (Shazam CLI) with safe fallback.
func (h *TagHandler) RecognizeAudio(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var req recognizeAudioRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		_ = json.NewEncoder(w).Encode(recognizeAudioResponse{Recognized: false})
		return
	}

	fullPath, err := h.resolvePath(req.Path)
	if err != nil || !fileExists(fullPath) {
		_ = json.NewEncoder(w).Encode(recognizeAudioResponse{Recognized: false})
		return
	}

	songrecBin, err := exec.LookPath("songrec")
	if err != nil {
		_ = json.NewEncoder(w).Encode(recognizeAudioResponse{Recognized: false})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	cmd := mediaexec.CommandContext(ctx, songrecBin, "audio-file-to-recognized-song", fullPath)
	defer cmd.Close()
	out, err := cmd.Output()
	if err != nil {
		_ = json.NewEncoder(w).Encode(recognizeAudioResponse{Recognized: false})
		return
	}

	var res songrecResult
	if err := json.Unmarshal(out, &res); err != nil || res.Track == nil {
		_ = json.NewEncoder(w).Encode(recognizeAudioResponse{Recognized: false})
		return
	}

	title := res.Track.Title
	artist := res.Track.Subtitle
	genre := res.Track.Genres.Primary
	coverURL := res.Track.Images.CoverArtHQ
	if coverURL == "" {
		coverURL = res.Track.Images.CoverArt
	}
	if coverURL != "" {
		coverURL = strings.ReplaceAll(coverURL, "/400x400cc.jpg", "/1000x1000cc.jpg")
		coverURL = strings.ReplaceAll(coverURL, "/800x800cc.jpg", "/1000x1000cc.jpg")
	}

	album := ""
	year := 0
	for _, sec := range res.Track.Sections {
		for _, m := range sec.Metadata {
			switch strings.ToLower(m.Title) {
			case "album":
				album = m.Text
			case "released":
				if y, yErr := strconv.Atoi(strings.TrimSpace(m.Text)); yErr == nil {
					year = y
				}
			}
		}
	}

	_ = json.NewEncoder(w).Encode(recognizeAudioResponse{
		Recognized: true,
		Title:      title,
		Artist:     artist,
		Album:      album,
		Year:       year,
		Genre:      genre,
		CoverURL:   coverURL,
	})
}
