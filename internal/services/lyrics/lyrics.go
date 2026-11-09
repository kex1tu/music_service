package lyrics

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"path/filepath"
	"regexp"
	"strings"
	"time"

	"music-manager/internal/core/safefs"
	"music-manager/internal/core/sandbox"
)

var timestampRegex = regexp.MustCompile(`\[\d{2}:\d{2}(?:\.\d{2,3})?\]`)

// LyricsSearchResult represents a lyrics search result from an external provider.
type LyricsSearchResult struct {
	Lyrics     string `json:"lyrics,omitempty"`
	Content    string `json:"content,omitempty"`
	Synced     bool   `json:"synced"`
	Source     string `json:"source"` // lrclib, genius, none
	TrackName  string `json:"track_name,omitempty"`
	ArtistName string `json:"artist_name,omitempty"`
}

// LyricsResponse represents the current lyrics status for a local file.
type LyricsResponse struct {
	TrackPath string  `json:"track_path"`
	Lyrics    *string `json:"lyrics"`
	Content   *string `json:"content"`
	Kind      string  `json:"kind"` // lrc, txt, none
	Synced    bool    `json:"synced"`
}

// LyricsProvider defines the contract for searching lyrics from remote providers.
type LyricsProvider interface {
	Search(ctx context.Context, artist, title, album string, duration int) (*LyricsSearchResult, error)
}

// LrclibClient is an HTTP client for lrclib.net.
type LrclibClient struct {
	client  *http.Client
	baseURL string
}

// NewLrclibClient creates a new LrclibClient.
func NewLrclibClient() *LrclibClient {
	return &LrclibClient{
		client:  &http.Client{Timeout: 10 * time.Second},
		baseURL: "https://lrclib.net/api",
	}
}

// SetBaseURL overrides the API base URL (useful for testing with mock servers).
func (c *LrclibClient) SetBaseURL(baseURL string) {
	c.baseURL = strings.TrimRight(baseURL, "/")
}

// Search searches LRCLIB for track lyrics.
func (c *LrclibClient) Search(ctx context.Context, artist, title, album string, duration int) (*LyricsSearchResult, error) {
	if strings.TrimSpace(artist) == "" || strings.TrimSpace(title) == "" {
		return nil, nil
	}

	// 1. Try exact match /api/get
	exactURL := fmt.Sprintf("%s/get?artist_name=%s&track_name=%s", c.baseURL, url.QueryEscape(artist), url.QueryEscape(title))
	if album != "" {
		exactURL += "&album_name=" + url.QueryEscape(album)
	}
	if duration > 0 {
		exactURL += fmt.Sprintf("&duration=%d", duration)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, exactURL, nil)
	if err == nil {
		req.Header.Set("User-Agent", "MusicManager/1.0")
		resp, rErr := c.client.Do(req)
		if rErr == nil && resp.StatusCode == http.StatusOK {
			var data struct {
				TrackName    string `json:"trackName"`
				ArtistName   string `json:"artistName"`
				PlainLyrics  string `json:"plainLyrics"`
				SyncedLyrics string `json:"syncedLyrics"`
			}
			if decErr := json.NewDecoder(resp.Body).Decode(&data); decErr == nil {
				_ = resp.Body.Close()
				if data.SyncedLyrics != "" {
					return &LyricsSearchResult{
						Lyrics:     data.SyncedLyrics,
						Content:    data.SyncedLyrics,
						Synced:     true,
						Source:     "lrclib",
						TrackName:  data.TrackName,
						ArtistName: data.ArtistName,
					}, nil
				}
				if data.PlainLyrics != "" {
					return &LyricsSearchResult{
						Lyrics:     data.PlainLyrics,
						Content:    data.PlainLyrics,
						Synced:     false,
						Source:     "lrclib",
						TrackName:  data.TrackName,
						ArtistName: data.ArtistName,
					}, nil
				}
			} else {
				_ = resp.Body.Close()
			}
		} else if resp != nil {
			_ = resp.Body.Close()
		}
	}

	// 2. Fallback to /api/search
	searchURL := fmt.Sprintf("%s/search?q=%s", c.baseURL, url.QueryEscape(artist+" "+title))
	sReq, sErr := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if sErr != nil {
		return nil, sErr
	}
	sReq.Header.Set("User-Agent", "MusicManager/1.0")

	sResp, sErr := c.client.Do(sReq)
	if sErr != nil {
		return nil, sErr
	}
	defer sResp.Body.Close()

	if sResp.StatusCode == http.StatusOK {
		var results []struct {
			TrackName    string `json:"trackName"`
			ArtistName   string `json:"artistName"`
			PlainLyrics  string `json:"plainLyrics"`
			SyncedLyrics string `json:"syncedLyrics"`
		}
		if decErr := json.NewDecoder(sResp.Body).Decode(&results); decErr == nil && len(results) > 0 {
			for _, r := range results {
				if r.SyncedLyrics != "" {
					return &LyricsSearchResult{
						Lyrics:     r.SyncedLyrics,
						Content:    r.SyncedLyrics,
						Synced:     true,
						Source:     "lrclib",
						TrackName:  r.TrackName,
						ArtistName: r.ArtistName,
					}, nil
				}
			}
			// Fallback to first with plain lyrics
			for _, r := range results {
				if r.PlainLyrics != "" {
					return &LyricsSearchResult{
						Lyrics:     r.PlainLyrics,
						Content:    r.PlainLyrics,
						Synced:     false,
						Source:     "lrclib",
						TrackName:  r.TrackName,
						ArtistName: r.ArtistName,
					}, nil
				}
			}
		}
	}

	return nil, nil
}

// Service provides local and remote lyrics management.
type Service struct {
	musicSandbox *sandbox.PathSandbox
	provider     LyricsProvider
}

// NewService creates a new LyricsService with an optional custom LyricsProvider.
func NewService(musicSandbox *sandbox.PathSandbox, provider ...LyricsProvider) *Service {
	var p LyricsProvider = NewLrclibClient()
	if len(provider) > 0 && provider[0] != nil {
		p = provider[0]
	}
	return &Service{
		musicSandbox: musicSandbox,
		provider:     p,
	}
}

// GetLyrics reads the local lyric file (.lrc or .txt) adjacent to the MP3 file.
func (s *Service) GetLyrics(relPath string) (*LyricsResponse, error) {
	fullPath, err := s.musicSandbox.Resolve(relPath, false)
	if err != nil {
		return nil, err
	}

	base := strings.TrimSuffix(fullPath, filepath.Ext(fullPath))
	lrcPath := base + ".lrc"
	txtPath := base + ".txt"

	if data, err := safefs.ReadFile(lrcPath); err == nil {
		text := string(data)
		return &LyricsResponse{
			TrackPath: relPath,
			Lyrics:    &text,
			Content:   &text,
			Kind:      "lrc",
			Synced:    timestampRegex.MatchString(text),
		}, nil
	}

	if data, err := safefs.ReadFile(txtPath); err == nil {
		text := string(data)
		return &LyricsResponse{
			TrackPath: relPath,
			Lyrics:    &text,
			Content:   &text,
			Kind:      "txt",
			Synced:    false,
		}, nil
	}

	return &LyricsResponse{
		TrackPath: relPath,
		Lyrics:    nil,
		Content:   nil,
		Kind:      "none",
		Synced:    false,
	}, nil
}

// SearchLyrics searches for lyrics using the configured provider.
func (s *Service) SearchLyrics(ctx context.Context, artist, title, album string, duration int) (*LyricsSearchResult, error) {
	if s.provider == nil {
		return &LyricsSearchResult{Source: "none"}, nil
	}
	res, err := s.provider.Search(ctx, artist, title, album, duration)
	if err != nil {
		return nil, err
	}
	if res == nil {
		return &LyricsSearchResult{Source: "none"}, nil
	}
	return res, nil
}

// SaveLyrics saves lyrics strictly to an external .lrc or .txt file adjacent to the MP3 file.
// Invariant: Never writes lyrics into ID3 tags.
func (s *Service) SaveLyrics(relPath string, content string, kind string) error {
	fullPath, err := s.musicSandbox.Resolve(relPath, false)
	if err != nil {
		return err
	}

	base := strings.TrimSuffix(fullPath, filepath.Ext(fullPath))

	targetExt := ".txt"
	if kind == "lrc" || (kind == "auto" && timestampRegex.MatchString(content)) {
		targetExt = ".lrc"
	}

	targetPath := base + targetExt
	if err := safefs.WriteFile(targetPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write lyrics file: %w", err)
	}

	// Clean up alternate format if it exists
	if targetExt == ".lrc" {
		_ = safefs.Remove(base + ".txt")
	} else {
		_ = safefs.Remove(base + ".lrc")
	}

	return nil
}

// DeleteLyrics removes any existing .lrc or .txt file next to the MP3.
func (s *Service) DeleteLyrics(relPath string) (bool, error) {
	fullPath, err := s.musicSandbox.Resolve(relPath, false)
	if err != nil {
		return false, err
	}

	base := strings.TrimSuffix(fullPath, filepath.Ext(fullPath))
	lrcPath := base + ".lrc"
	txtPath := base + ".txt"

	deleted := false
	if _, err := safefs.Stat(lrcPath); err == nil {
		if err := safefs.Remove(lrcPath); err == nil {
			deleted = true
		}
	}
	if _, err := safefs.Stat(txtPath); err == nil {
		if err := safefs.Remove(txtPath); err == nil {
			deleted = true
		}
	}

	return deleted, nil
}
