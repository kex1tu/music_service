package resolver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	deezerURLPattern       = regexp.MustCompile(`(?i)(?:https?://)?(?:www\.)?deezer\.com/(?:[a-zA-Z]{2}(?:-[a-zA-Z]{2})?/)?(track|album|playlist|artist)/(\d+)`)
	deezerShortLinkPattern = regexp.MustCompile(`(?i)(?:https?://)?(?:www\.)?(?:deezer\.page\.link/[a-zA-Z0-9_\-]+|link\.deezer\.com/(?:s/)?[a-zA-Z0-9_\-]+)`)
)

// DeezerTrack represents track metadata from Deezer API.
type DeezerTrack struct {
	ID             int64          `json:"id"`
	Title          string         `json:"title"`
	TitleShort     string         `json:"title_short"`
	Duration       int            `json:"duration"`
	TrackPosition  int            `json:"track_position"`
	DiskNumber     int            `json:"disk_number"`
	ISRC           string         `json:"isrc"`
	Link           string         `json:"link"`
	ExplicitLyrics bool           `json:"explicit_lyrics"`
	Artist         DeezerArtist   `json:"artist"`
	Album          *DeezerAlbum   `json:"album,omitempty"`
	Contributors   []DeezerArtist `json:"contributors,omitempty"`
}

// DeezerArtist represents artist data in Deezer API.
type DeezerArtist struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Picture string `json:"picture_xl"`
}

// DeezerAlbum represents album data in Deezer API.
type DeezerAlbum struct {
	ID          int64            `json:"id"`
	Title       string           `json:"title"`
	CoverXL     string           `json:"cover_xl"`
	CoverBig    string           `json:"cover_big"`
	CoverMedium string           `json:"cover_medium"`
	ReleaseDate string           `json:"release_date"`
	RecordType  string           `json:"record_type"`
	NbTracks    int              `json:"nb_tracks"`
	Artist      DeezerArtist     `json:"artist"`
	Genres      *DeezerGenres    `json:"genres,omitempty"`
	Tracks      *DeezerTrackList `json:"tracks,omitempty"`
}

type DeezerGenres struct {
	Data []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"data"`
}

type DeezerTrackList struct {
	Data []DeezerTrack `json:"data"`
	Next string        `json:"next,omitempty"`
}

// DeezerClient handles HTTP requests to api.deezer.com.
type DeezerClient struct {
	httpClient *http.Client
	baseURL    string
}

// NewDeezerClient creates a new DeezerClient.
func NewDeezerClient() *DeezerClient {
	return &DeezerClient{
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
		baseURL: "https://api.deezer.com",
	}
}

// ParseURL parses a Deezer URL into resource type ("track", "album", "artist", "playlist") and ID.
func (c *DeezerClient) ParseURL(ctx context.Context, rawURL string) (resourceType string, id string, err error) {
	cleanURL := strings.TrimSpace(rawURL)
	if !strings.HasPrefix(strings.ToLower(cleanURL), "http://") && !strings.HasPrefix(strings.ToLower(cleanURL), "https://") {
		cleanURL = "https://" + cleanURL
	}

	// Resolve short links if applicable
	if deezerShortLinkPattern.MatchString(cleanURL) {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodHead, cleanURL, nil)
		if reqErr == nil {
			resp, respErr := c.httpClient.Do(req)
			if respErr == nil {
				_ = resp.Body.Close()
				candidate := resp.Request.URL.String()
				if deezerURLPattern.MatchString(candidate) {
					cleanURL = candidate
				}
			}
		}
	}

	match := deezerURLPattern.FindStringSubmatch(cleanURL)
	if len(match) < 3 {
		return "", "", fmt.Errorf("URL %q is not a recognized Deezer URL", rawURL)
	}

	return strings.ToLower(match[1]), match[2], nil
}

// GetAlbum retrieves album metadata and its tracks.
func (c *DeezerClient) GetAlbum(ctx context.Context, albumID string) (*DeezerAlbum, error) {
	reqURL := fmt.Sprintf("%s/album/%s", c.baseURL, albumID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "MusicManager/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("deezer request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("deezer api returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var album DeezerAlbum
	if err := json.Unmarshal(body, &album); err != nil {
		return nil, err
	}

	// Fetch all tracks if paginated
	if album.Tracks != nil && album.Tracks.Next != "" {
		nextURL := album.Tracks.Next
		for nextURL != "" {
			nReq, nErr := http.NewRequestWithContext(ctx, http.MethodGet, nextURL, nil)
			if nErr != nil {
				break
			}
			nResp, nErr := c.httpClient.Do(nReq)
			if nErr != nil {
				break
			}
			var nList DeezerTrackList
			decErr := json.NewDecoder(nResp.Body).Decode(&nList)
			_ = nResp.Body.Close()
			if decErr != nil {
				break
			}
			album.Tracks.Data = append(album.Tracks.Data, nList.Data...)
			nextURL = nList.Next
		}
	}

	return &album, nil
}

// GetTrack retrieves track metadata by track ID.
func (c *DeezerClient) GetTrack(ctx context.Context, trackID string) (*DeezerTrack, error) {
	reqURL := fmt.Sprintf("%s/track/%s", c.baseURL, trackID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("deezer api returned status %d", resp.StatusCode)
	}

	var track DeezerTrack
	if err := json.NewDecoder(resp.Body).Decode(&track); err != nil {
		return nil, err
	}
	return &track, nil
}

// SearchAlbums searches albums by query text.
func (c *DeezerClient) SearchAlbums(ctx context.Context, query string) ([]DeezerAlbum, error) {
	reqURL := fmt.Sprintf("%s/search/album?q=%s&limit=25", c.baseURL, url.QueryEscape(query))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var res struct {
		Data []DeezerAlbum `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}
	return res.Data, nil
}

// DownloadCoverBytes downloads an image from coverURL.
func (c *DeezerClient) DownloadCoverBytes(ctx context.Context, coverURL string) ([]byte, error) {
	if coverURL == "" {
		return nil, fmt.Errorf("empty cover URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, coverURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cover download failed with status %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}

// BestCoverURL returns the highest resolution cover available.
func (a *DeezerAlbum) BestCoverURL() string {
	if a.CoverXL != "" {
		return a.CoverXL
	}
	if a.CoverBig != "" {
		return a.CoverBig
	}
	return a.CoverMedium
}

// ReleaseYear parses the release year from ReleaseDate (YYYY-MM-DD).
func (a *DeezerAlbum) ReleaseYear() int {
	if len(a.ReleaseDate) >= 4 {
		year, _ := strconv.Atoi(a.ReleaseDate[:4])
		return year
	}
	return 0
}

// PrimaryGenre returns the first genre name if available.
func (a *DeezerAlbum) PrimaryGenre() string {
	if a.Genres != nil && len(a.Genres.Data) > 0 {
		return a.Genres.Data[0].Name
	}
	return ""
}
