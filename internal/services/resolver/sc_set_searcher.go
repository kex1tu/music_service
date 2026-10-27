package resolver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"strings"
	"time"
)

// SoundCloudSetSearcher осуществляет поиск и разбор плейлистов/сетов SoundCloud и других внешних ссылок.
type SoundCloudSetSearcher struct {
	cookiesPath  string
	searchFunc   func(ctx context.Context, artist, album string, limit int) ([]AlbumReleaseCandidate, error)
	parseURLFunc func(ctx context.Context, setURL string) (*AlbumReleaseCandidate, error)
}

// NewSoundCloudSetSearcher создает новый экземпляр SoundCloudSetSearcher.
func NewSoundCloudSetSearcher(cookiesPath string) *SoundCloudSetSearcher {
	return &SoundCloudSetSearcher{
		cookiesPath: cookiesPath,
	}
}

// SetCustomSearchFunc позволяет переопределить функцию поиска сетов для тестов.
func (s *SoundCloudSetSearcher) SetCustomSearchFunc(fn func(ctx context.Context, artist, album string, limit int) ([]AlbumReleaseCandidate, error)) {
	s.searchFunc = fn
}

// SetCustomParseURLFunc позволяет переопределить функцию разбора URL для тестов.
func (s *SoundCloudSetSearcher) SetCustomParseURLFunc(fn func(ctx context.Context, setURL string) (*AlbumReleaseCandidate, error)) {
	s.parseURLFunc = fn
}

// rawEntry представляет промежуточный JSON-объект, возвращаемый yt-dlp --dump-json.
type rawSetEntry struct {
	ID            string  `json:"id"`
	Title         string  `json:"title"`
	Uploader      string  `json:"uploader"`
	Channel       string  `json:"channel"`
	Duration      float64 `json:"duration"`
	WebpageURL    string  `json:"webpage_url"`
	URL           string  `json:"url"`
	PlaylistTitle string  `json:"playlist_title"`
	Playlist      string  `json:"playlist"`
	Thumbnails    []struct {
		URL string `json:"url"`
	} `json:"thumbnails"`
}

// ParseOutputBytes парсит вывод yt-dlp и конструирует AlbumReleaseCandidate.
func (s *SoundCloudSetSearcher) ParseOutputBytes(data []byte, sourceURL string) (*AlbumReleaseCandidate, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	buf := make([]byte, 1024*1024)
	scanner.Buffer(buf, 10*1024*1024)

	var entries []rawSetEntry
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var e rawSetEntry
		if err := json.Unmarshal(line, &e); err == nil && (e.ID != "" || e.URL != "" || e.WebpageURL != "") {
			entries = append(entries, e)
		}
	}

	if len(entries) == 0 {
		return nil, fmt.Errorf("не найдено треков в выводе yt-dlp для %s", sourceURL)
	}

	isSoundCloud := strings.Contains(sourceURL, "soundcloud.com")
	playlistTitle := entries[0].PlaylistTitle
	if playlistTitle == "" {
		playlistTitle = entries[0].Playlist
	}

	firstArtist := ""
	coverURL := ""
	if len(entries[0].Thumbnails) > 0 {
		coverURL = entries[0].Thumbnails[len(entries[0].Thumbnails)-1].URL
	}

	var tracks []AlbumTrackCandidate
	for idx, e := range entries {
		var art, tit string
		if isSoundCloud {
			art, tit = ParseSoundCloudTitle(e.Title, e.Uploader)
		} else {
			parsed := ParseYouTubeTitle(e.Title, e.Channel, e.Uploader)
			art = parsed.Artist
			tit = parsed.Title
		}

		if firstArtist == "" && art != "" && art != "Unknown Artist" {
			firstArtist = art
		}

		durSec := int(math.Round(e.Duration))
		directURL := e.WebpageURL
		if directURL == "" {
			directURL = e.URL
		}
		if directURL == "" && e.ID != "" && strings.Contains(sourceURL, "youtube") {
			directURL = fmt.Sprintf("https://music.youtube.com/watch?v=%s", e.ID)
		}

		tracks = append(tracks, AlbumTrackCandidate{
			VideoID:   e.ID,
			DirectURL: directURL,
			Title:     tit,
			Artist:    art,
			Duration:  durSec,
			Index:     idx + 1,
		})
	}

	if playlistTitle == "" {
		if len(tracks) > 0 {
			playlistTitle = tracks[0].Title
		} else {
			playlistTitle = "SoundCloud Set"
		}
	}

	return &AlbumReleaseCandidate{
		BrowseID:    sourceURL,
		PlaylistID:  sourceURL,
		Title:       playlistTitle,
		Artist:      firstArtist,
		CoverURL:    coverURL,
		Tracks:      tracks,
		TotalTracks: len(tracks),
	}, nil
}

// ParseSetURL извлекает треклист и метаданные сета/плейлиста по прямой ссылке.
func (s *SoundCloudSetSearcher) ParseSetURL(ctx context.Context, setURL string) (*AlbumReleaseCandidate, error) {
	if s.parseURLFunc != nil {
		return s.parseURLFunc(ctx, setURL)
	}

	cleanURL := strings.TrimSpace(setURL)
	if cleanURL == "" {
		return nil, fmt.Errorf("пустой URL")
	}

	args := []string{
		"--ignore-config",
		"--no-download-archive",
		"--dump-json",
		"--flat-playlist",
		"--no-warnings",
		"--ignore-errors",
	}
	if IsValidNetscapeCookiesFile(s.cookiesPath) {
		args = append(args, "--cookies", s.cookiesPath)
	}
	args = append(args, cleanURL)

	timeout := 45 * time.Second
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "yt-dlp", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil && stdout.Len() == 0 {
		cleanErr := CleanYtDlpStderr(stderr.String())
		return nil, fmt.Errorf("ошибка парсинга сета yt-dlp: %w: %s", err, cleanErr)
	}

	return s.ParseOutputBytes(stdout.Bytes(), cleanURL)
}

// SearchSoundCloudSets выполняет поиск сетов/плейлистов на SoundCloud по артисту и названию альбома.
func (s *SoundCloudSetSearcher) SearchSoundCloudSets(ctx context.Context, artist, album string, limit int) ([]AlbumReleaseCandidate, error) {
	if s.searchFunc != nil {
		return s.searchFunc(ctx, artist, album, limit)
	}

	if limit <= 0 {
		limit = 3
	}

	cleanArt := strings.TrimSpace(artist)
	cleanAlb := strings.TrimSpace(album)
	if cleanArt == "" || cleanAlb == "" {
		return nil, nil
	}

	// Формируем поисковый запрос для SoundCloud
	query := fmt.Sprintf("scsearch15:%s %s", cleanArt, cleanAlb)
	args := []string{
		"--ignore-config",
		"--no-download-archive",
		"--dump-json",
		"--flat-playlist",
		"--no-warnings",
		"--ignore-errors",
		query,
	}

	timeout := 20 * time.Second
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "yt-dlp", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil && stdout.Len() == 0 {
		return nil, nil
	}

	scanner := bufio.NewScanner(&stdout)
	buf := make([]byte, 1024*1024)
	scanner.Buffer(buf, 10*1024*1024)

	var allTracks []AlbumTrackCandidate
	var coverURL string

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var e rawSetEntry
		if err := json.Unmarshal(line, &e); err == nil && (e.WebpageURL != "" || e.URL != "") {
			art, tit := ParseSoundCloudTitle(e.Title, e.Uploader)
			durSec := int(math.Round(e.Duration))
			directURL := e.WebpageURL
			if directURL == "" {
				directURL = e.URL
			}

			if coverURL == "" && len(e.Thumbnails) > 0 {
				coverURL = e.Thumbnails[len(e.Thumbnails)-1].URL
			}

			allTracks = append(allTracks, AlbumTrackCandidate{
				VideoID:   e.ID,
				DirectURL: directURL,
				Title:     tit,
				Artist:    art,
				Duration:  durSec,
				Index:     len(allTracks) + 1,
			})
		}
	}

	if len(allTracks) == 0 {
		return nil, nil
	}

	cand := AlbumReleaseCandidate{
		BrowseID:    fmt.Sprintf("sc_%s_%s", cleanArt, cleanAlb),
		PlaylistID:  fmt.Sprintf("https://soundcloud.com/search?q=%s", cleanArt),
		Title:       cleanAlb,
		Artist:      cleanArt,
		CoverURL:    coverURL,
		Tracks:      allTracks,
		TotalTracks: len(allTracks),
	}

	return []AlbumReleaseCandidate{cand}, nil
}
