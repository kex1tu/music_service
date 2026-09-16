package resolver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"music-manager/internal/id3"
)

// resolveYtDlp разрешает ссылки YouTube, YouTube Music и SoundCloud через yt-dlp.
func (r *SourceResolver) resolveYtDlp(ctx context.Context, rawURL string) (*ReleaseMetadata, []ResolvedTrack, error) {
	targetURL := rawURL

	// Нормализация ссылок на артиста в YouTube Music:
	// https://music.youtube.com/channel/UC... -> автоматически переходим в раздел /releases
	if strings.Contains(targetURL, "music.youtube.com/channel/") {
		parsedU, pErr := url.Parse(targetURL)
		if pErr == nil {
			path := strings.TrimRight(parsedU.Path, "/")
			if strings.Count(path, "/") == 2 { // /channel/UCxxxx
				parsedU.Path = path + "/releases"
				targetURL = parsedU.String()
			}
		}
	}

	args := []string{
		"--ignore-config",
		"--no-download-archive",
		"--dump-json",
		"--flat-playlist",
		"--no-warnings",
		"--ignore-errors",
		targetURL,
	}
	if r.cookiesPath != "" {
		args = append(args, "--cookies", r.cookiesPath)
	}

	// Увеличиваем таймаут для дискографий исполнителей (может быть много релизов)
	timeout := 60 * time.Second
	if strings.Contains(targetURL, "/channel/") || strings.Contains(targetURL, "/releases") {
		timeout = 3 * time.Minute
	}

	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "yt-dlp", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil && stdout.Len() == 0 {
		return nil, nil, fmt.Errorf("yt-dlp extraction failed: %w: %s", err, stderr.String())
	}

	type YtEntry struct {
		ID          string  `json:"id"`
		Title       string  `json:"title"`
		Uploader    string  `json:"uploader"`
		Channel     string  `json:"channel"`
		Duration    float64 `json:"duration"`
		WebpageURL  string  `json:"webpage_url"`
		URL         string  `json:"url"`
		Thumbnail   string  `json:"thumbnail"`
		TrackNumber any     `json:"track_number"`
		Album       string  `json:"album"`
		Artist      string  `json:"artist"`
		ReleaseYear int     `json:"release_year"`
		Genre       string  `json:"genre"`
	}

	var entries []YtEntry
	scanner := bufio.NewScanner(&stdout)
	buf := make([]byte, 1024*1024)
	scanner.Buffer(buf, 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e YtEntry
		if err := json.Unmarshal(line, &e); err == nil && (e.ID != "" || e.URL != "") {
			entries = append(entries, e)
		}
	}

	if len(entries) == 0 {
		return nil, nil, fmt.Errorf("не найдено треков по ссылке %s", rawURL)
	}

	artistName := entries[0].Artist
	if artistName == "" {
		artistName = entries[0].Channel
		if artistName == "" {
			artistName = entries[0].Uploader
		}
	}

	isArtistPage := strings.Contains(rawURL, "/channel/")
	albumTitle := entries[0].Album
	if albumTitle == "" || isArtistPage {
		if isArtistPage && artistName != "" {
			albumTitle = artistName
		} else if entries[0].Title != "" {
			albumTitle = entries[0].Title
		} else {
			albumTitle = "Unknown Album"
		}
	}

	coverURL := entries[0].Thumbnail
	year := entries[0].ReleaseYear

	meta := &ReleaseMetadata{
		AlbumTitle:  albumTitle,
		AlbumArtist: artistName,
		CoverURL:    coverURL,
		Year:        year,
		Genre:       entries[0].Genre,
		TotalTracks: len(entries),
	}

	var tracks []ResolvedTrack
	total := len(entries)
	for i, e := range entries {
		pos := i + 1
		if tn, ok := e.TrackNumber.(float64); ok && tn > 0 {
			pos = int(tn)
		} else if tnStr, ok := e.TrackNumber.(string); ok {
			if parsed, err := strconv.Atoi(tnStr); err == nil && parsed > 0 {
				pos = parsed
			}
		}

		title := e.Title
		cleanArt := artistName
		if e.Artist != "" {
			cleanArt = e.Artist
		}

		trURL := e.WebpageURL
		if trURL == "" && e.URL != "" {
			trURL = e.URL
		}
		if trURL == "" && e.ID != "" {
			trURL = fmt.Sprintf("https://music.youtube.com/watch?v=%s", e.ID)
		}

		trAlbum := strings.TrimSpace(e.Album)
		if trAlbum == "" {
			if !isArtistPage {
				trAlbum = albumTitle
			} else {
				trAlbum = title
			}
		}

		trID := e.ID
		if trID == "" {
			trID = fmt.Sprintf("yt_%d", i+1)
		}

		tr := ResolvedTrack{
			TrackID:        fmt.Sprintf("yt_%s", trID),
			TrackNumber:    pos,
			TrackTotal:     &total,
			DiscNumber:     1,
			Title:          id3.CleanTitle(title, filepath.Base(title)),
			Artist:         cleanArt,
			Album:          trAlbum,
			AlbumArtist:    artistName,
			Duration:       int(e.Duration),
			AudioSourceURL: trURL,
			Status:         "pending",
		}
		if e.Thumbnail != "" {
			tCover := e.Thumbnail
			tr.CoverURL = &tCover
		}
		if e.ReleaseYear > 0 {
			y := e.ReleaseYear
			tr.Year = &y
		} else if year > 0 {
			tr.Year = &year
		}
		tracks = append(tracks, tr)
	}

	return meta, tracks, nil
}
