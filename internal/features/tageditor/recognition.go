package tageditor

import (
	"context"
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"music-manager/internal/core/mediaexec"
	"music-manager/internal/core/safefs"
)

// RecognizeAudio выполняет акустическое распознавание файла через Shazam songrec с безопасным фолбэком.
func (s *Service) RecognizeAudio(ctx context.Context, relPath string) (*RecognizeAudioResponse, error) {
	fullPath, _, err := s.ResolvePath(relPath)
	if err != nil {
		return &RecognizeAudioResponse{Recognized: false}, nil
	}

	st, err := safefs.Stat(fullPath)
	if err != nil || st.IsDir() {
		return &RecognizeAudioResponse{Recognized: false}, nil
	}

	// 1. Попытка через внедренный ShazamCatalog (ShazamProvider)
	if s.shazam != nil {
		rec, recErr := s.shazam.RecognizeAudioFile(ctx, fullPath)
		if recErr == nil && rec != nil {
			return &RecognizeAudioResponse{
				Recognized: true,
				Title:      rec.Title,
				Artist:     rec.Artist,
				Album:      rec.Title,
				Year:       rec.Year,
				Genre:      rec.Genre,
				CoverURL:   rec.CoverURL,
			}, nil
		}
	}

	// 2. Фолбэк на прямой запуск songrec CLI при наличии
	songrecBin, err := exec.LookPath("songrec")
	if err != nil {
		return &RecognizeAudioResponse{Recognized: false}, nil
	}

	recCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := mediaexec.CommandContext(recCtx, songrecBin, "audio-file-to-recognized-song", fullPath)
	defer cmd.Close()
	out, err := cmd.Output()
	if err != nil {
		return &RecognizeAudioResponse{Recognized: false}, nil
	}

	var raw struct {
		Track *struct {
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

	if err := json.Unmarshal(out, &raw); err != nil || raw.Track == nil {
		return &RecognizeAudioResponse{Recognized: false}, nil
	}

	title := raw.Track.Title
	artist := raw.Track.Subtitle
	genre := raw.Track.Genres.Primary
	coverURL := raw.Track.Images.CoverArtHQ
	if coverURL == "" {
		coverURL = raw.Track.Images.CoverArt
	}
	if coverURL != "" {
		coverURL = strings.ReplaceAll(coverURL, "/400x400cc.jpg", "/1000x1000cc.jpg")
		coverURL = strings.ReplaceAll(coverURL, "/800x800cc.jpg", "/1000x1000cc.jpg")
	}

	album := ""
	year := 0
	for _, sec := range raw.Track.Sections {
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

	return &RecognizeAudioResponse{
		Recognized: true,
		Title:      title,
		Artist:     artist,
		Album:      album,
		Year:       year,
		Genre:      genre,
		CoverURL:   coverURL,
	}, nil
}
