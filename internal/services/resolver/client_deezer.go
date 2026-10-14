package resolver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
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
	Preview        string         `json:"preview"`
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

type browserHeadersTransport struct {
	base http.RoundTripper
}

func (t *browserHeadersTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36")
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json, text/plain, */*")
	}
	if req.Header.Get("Accept-Language") == "" {
		req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en-US;q=0.8,en;q=0.7")
	}
	req.Header.Set("Sec-Ch-Ua", `"Chromium";v="128", "Not;A=Brand";v="24", "Google Chrome";v="128"`)
	req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	req.Header.Set("Sec-Ch-Ua-Platform", `"Windows"`)
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")

	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// DeezerClient handles HTTP requests to api.deezer.com with proxy and fallback support.
type DeezerClient struct {
	httpClient      *http.Client
	baseURL         string
	fallbackBaseURL string
}

// NewDeezerClient creates a new DeezerClient with anti-bot headers and proxy/fallback support.
func NewDeezerClient(customTransport ...http.RoundTripper) *DeezerClient {
	var transport http.RoundTripper = &http.Transport{
		Proxy: http.ProxyFromEnvironment,
	}
	if len(customTransport) > 0 && customTransport[0] != nil {
		transport = customTransport[0]
	} else if proxyURLStr := strings.TrimSpace(os.Getenv("DEEZER_PROXY")); proxyURLStr != "" {
		if pURL, err := url.Parse(proxyURLStr); err == nil {
			transport = &http.Transport{
				Proxy: http.ProxyURL(pURL),
			}
		}
	}

	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("DEEZER_API_BASE")), "/")
	if baseURL == "" {
		baseURL = "https://api.deezer.com"
	}

	fallbackBase := strings.TrimRight(strings.TrimSpace(os.Getenv("DEEZER_FALLBACK_API_BASE")), "/")
	if fallbackBase == "" {
		fallbackBase = "https://musicdownloader.verymad.net/deezer-api"
	}

	return &DeezerClient{
		httpClient: &http.Client{
			Timeout:   15 * time.Second,
			Transport: &browserHeadersTransport{base: transport},
		},
		baseURL:         baseURL,
		fallbackBaseURL: fallbackBase,
	}
}

// SetBaseURL overrides baseURL and clears fallbackBaseURL (primarily for tests).
func (c *DeezerClient) SetBaseURL(u string) {
	c.baseURL = strings.TrimRight(u, "/")
	c.fallbackBaseURL = ""
}

// fetchDeezerJSON performs an HTTP request to Deezer API with automatic fallback mirror upon 403 or network error.
func (c *DeezerClient) fetchDeezerJSON(ctx context.Context, path string, target any) error {
	path = "/" + strings.TrimPrefix(path, "/")
	urls := []string{c.baseURL + path}
	if c.fallbackBaseURL != "" && c.fallbackBaseURL != c.baseURL {
		urls = append(urls, c.fallbackBaseURL+path)
	}

	var lastErr error
	for _, targetURL := range urls {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
		if err != nil {
			lastErr = err
			continue
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}

		var errCheck struct {
			Error *struct {
				Type    string `json:"type"`
				Message string `json:"message"`
				Code    int    `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &errCheck)
		if errCheck.Error != nil {
			lastErr = fmt.Errorf("deezer api error: %s", errCheck.Error.Message)
			continue
		}

		if err := json.Unmarshal(body, target); err != nil {
			lastErr = err
			continue
		}

		return nil
	}

	if lastErr != nil {
		return fmt.Errorf("deezer api error: %w", lastErr)
	}
	return fmt.Errorf("deezer api request failed")
}

// ParseURL parses a Deezer URL into resource type ("track", "album", "artist", "playlist") and ID.
func (c *DeezerClient) ParseURL(ctx context.Context, rawURL string) (resourceType string, id string, err error) {
	cleanURL := strings.TrimSpace(rawURL)
	if !strings.HasPrefix(strings.ToLower(cleanURL), "http://") && !strings.HasPrefix(strings.ToLower(cleanURL), "https://") {
		cleanURL = "https://" + cleanURL
	}

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
	var album DeezerAlbum
	if err := c.fetchDeezerJSON(ctx, fmt.Sprintf("/album/%s", albumID), &album); err == nil && album.ID > 0 {
		if album.Tracks != nil && album.Tracks.Next != "" {
			nextURL := album.Tracks.Next
			for nextURL != "" {
				var nList DeezerTrackList
				if u, pErr := url.Parse(nextURL); pErr == nil {
					if decErr := c.fetchDeezerJSON(ctx, u.RequestURI(), &nList); decErr == nil {
						album.Tracks.Data = append(album.Tracks.Data, nList.Data...)
						nextURL = nList.Next
						continue
					}
				}
				break
			}
		}
		return &album, nil
	}

	// Fallback to iTunes if Deezer is unreachable
	if itunesAlb, itErr := c.getAlbumItunes(ctx, albumID); itErr == nil {
		return itunesAlb, nil
	}
	return nil, fmt.Errorf("deezer api returned status 403 (доступ заблокирован Cloudflare/Akamai; настройте DEEZER_PROXY в .env)")
}

// GetTrack retrieves track metadata by track ID.
func (c *DeezerClient) GetTrack(ctx context.Context, trackID string) (*DeezerTrack, error) {
	var track DeezerTrack
	if err := c.fetchDeezerJSON(ctx, fmt.Sprintf("/track/%s", trackID), &track); err == nil && track.ID > 0 {
		return &track, nil
	}
	return nil, fmt.Errorf("deezer api returned status 403 (доступ заблокирован Cloudflare/Akamai; настройте DEEZER_PROXY в .env)")
}

// GetArtist retrieves artist metadata by artist ID.
func (c *DeezerClient) GetArtist(ctx context.Context, artistID string) (*DeezerArtist, error) {
	var artist DeezerArtist
	if err := c.fetchDeezerJSON(ctx, fmt.Sprintf("/artist/%s", artistID), &artist); err == nil && artist.ID > 0 {
		return &artist, nil
	}
	return nil, fmt.Errorf("deezer api returned status 403 (доступ заблокирован Cloudflare/Akamai; настройте DEEZER_PROXY в .env)")
}

// GetArtistAlbums retrieves all albums for an artist, handling pagination.
func (c *DeezerClient) GetArtistAlbums(ctx context.Context, artistID string) ([]DeezerAlbum, error) {
	var allAlbums []DeezerAlbum
	nextPath := fmt.Sprintf("/artist/%s/albums?limit=100", artistID)

	for nextPath != "" {
		var res struct {
			Data  []DeezerAlbum `json:"data"`
			Next  string        `json:"next"`
			Total int           `json:"total"`
		}
		if err := c.fetchDeezerJSON(ctx, nextPath, &res); err != nil {
			if len(allAlbums) > 0 {
				return allAlbums, nil
			}
			return nil, fmt.Errorf("deezer api returned status 403 (доступ заблокирован Cloudflare/Akamai; настройте DEEZER_PROXY в .env)")
		}

		allAlbums = append(allAlbums, res.Data...)
		if res.Next != "" {
			if u, pErr := url.Parse(res.Next); pErr == nil {
				nextPath = u.RequestURI()
			} else {
				break
			}
		} else {
			break
		}
	}

	return allAlbums, nil
}

// SearchAlbums searches albums by query text with iTunes fallback and artist aliases.
func (c *DeezerClient) SearchAlbums(ctx context.Context, query string) ([]DeezerAlbum, error) {
	var res struct {
		Data []DeezerAlbum `json:"data"`
	}
	if err := c.fetchDeezerJSON(ctx, fmt.Sprintf("/search/album?q=%s&limit=25", url.QueryEscape(query)), &res); err == nil && len(res.Data) > 0 {
		return res.Data, nil
	}

	// Fallback to iTunes Search API if Deezer is blocked (403) or fails
	if itunesAlbums, err := c.searchAlbumsItunes(ctx, query); err == nil && len(itunesAlbums) > 0 {
		return itunesAlbums, nil
	}

	// Fallback to artist aliases (e.g. "ЛСП" <-> "LSP", "Toxi$" <-> "toxis")
	for _, alias := range TransliterateArtist(query) {
		if strings.EqualFold(alias, query) {
			continue
		}
		if err := c.fetchDeezerJSON(ctx, fmt.Sprintf("/search/album?q=%s&limit=25", url.QueryEscape(alias)), &res); err == nil && len(res.Data) > 0 {
			return res.Data, nil
		}
		if itunesRes, err := c.searchAlbumsItunes(ctx, alias); err == nil && len(itunesRes) > 0 {
			return itunesRes, nil
		}
	}

	return nil, fmt.Errorf("no albums found for query %q", query)
}

// SearchTracks searches tracks by query text.
func (c *DeezerClient) SearchTracks(ctx context.Context, query string) ([]DeezerTrack, error) {
	var res struct {
		Data []DeezerTrack `json:"data"`
	}
	if err := c.fetchDeezerJSON(ctx, fmt.Sprintf("/search/track?q=%s&limit=25", url.QueryEscape(query)), &res); err == nil && len(res.Data) > 0 {
		return res.Data, nil
	}

	// Fallback to artist aliases
	for _, alias := range TransliterateArtist(query) {
		if strings.EqualFold(alias, query) {
			continue
		}
		if err := c.fetchDeezerJSON(ctx, fmt.Sprintf("/search/track?q=%s&limit=25", url.QueryEscape(alias)), &res); err == nil && len(res.Data) > 0 {
			return res.Data, nil
		}
	}

	return nil, fmt.Errorf("no tracks found on deezer")
}

// SearchTrackVersions searches Deezer for all versions of a track by artist and title.
func (c *DeezerClient) SearchTrackVersions(ctx context.Context, artist, title string) ([]DeezerTrack, error) {
	q := strings.TrimSpace(fmt.Sprintf("%s %s", artist, title))
	if q == "" {
		return nil, fmt.Errorf("empty query")
	}

	tracks, err := c.SearchTracks(ctx, q)
	if err == nil && len(tracks) > 0 {
		return tracks, nil
	}

	for _, alias := range TransliterateArtist(artist) {
		if strings.EqualFold(alias, artist) {
			continue
		}
		if t, err := c.SearchTracks(ctx, fmt.Sprintf("%s %s", alias, title)); err == nil && len(t) > 0 {
			return t, nil
		}
	}

	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("no versions found on deezer for %s - %s", artist, title)
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

// DeezerRelatedArtist represents a related artist from Deezer API.
type DeezerRelatedArtist struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Picture string `json:"picture_medium"`
}

// SearchArtist searches for an artist by name.
func (c *DeezerClient) SearchArtist(ctx context.Context, name string) (*DeezerArtist, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("artist name cannot be empty")
	}

	searchURL := fmt.Sprintf("%s/search/artist?q=%s&limit=1", c.baseURL, url.QueryEscape(name))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("deezer search artist failed with status %d", resp.StatusCode)
	}

	var result struct {
		Data  []DeezerArtist `json:"data"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if result.Error != nil {
		return nil, fmt.Errorf("deezer API error: %s", result.Error.Message)
	}
	if len(result.Data) == 0 {
		return nil, fmt.Errorf("artist not found: %s", name)
	}

	return &result.Data[0], nil
}

// GetRelatedArtists fetches related artists for a given Deezer artist ID.
func (c *DeezerClient) GetRelatedArtists(ctx context.Context, artistID string) ([]DeezerRelatedArtist, error) {
	artistID = strings.TrimSpace(artistID)
	if artistID == "" {
		return nil, fmt.Errorf("artist ID cannot be empty")
	}

	relURL := fmt.Sprintf("%s/artist/%s/related?limit=20", c.baseURL, url.PathEscape(artistID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, relURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("deezer related artists failed with status %d", resp.StatusCode)
	}

	var result struct {
		Data  []DeezerRelatedArtist `json:"data"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if result.Error != nil {
		return nil, fmt.Errorf("deezer API error: %s", result.Error.Message)
	}

	return result.Data, nil
}


