package resolver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ITunesTrackMatch represents a single track search match from the iTunes Search API.
type ITunesTrackMatch struct {
	Title       string
	Artist      string
	Artists     []string
	Album       string
	Genre       string
	Year        int
	TrackNumber int
	Duration    int
	CoverURL    string
}

// ITunesClient is an HTTP client for querying the iTunes Search API.
type ITunesClient struct {
	httpClient *http.Client
	baseURL    string
}

// NewITunesClient creates a new ITunesClient. If httpClient is nil, a default client with 8s timeout is used.
func NewITunesClient(httpClient *http.Client) *ITunesClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 8 * time.Second}
	}
	return &ITunesClient{
		httpClient: httpClient,
		baseURL:    "https://itunes.apple.com",
	}
}

// SetBaseURL overrides the base URL (primarily for mocking with httptest.Server in unit tests).
func (c *ITunesClient) SetBaseURL(u string) {
	c.baseURL = strings.TrimRight(u, "/")
}

// ItunesTrack is an alias for ITunesTrackMatch for consistency with catalog clients.
type ItunesTrack = ITunesTrackMatch

// SearchTracks searches iTunes for tracks matching the given query string.
func (c *ITunesClient) SearchTracks(ctx context.Context, query string) ([]ITunesTrackMatch, error) {
	return c.SearchTracksWithCountry(ctx, query, "")
}

// SearchTracksWithCountry searches iTunes for tracks matching the given query string in a specific country storefront (e.g. "US").
func (c *ITunesClient) SearchTracksWithCountry(ctx context.Context, query, country string) ([]ITunesTrackMatch, error) {
	u := fmt.Sprintf("%s/search?term=%s&entity=song&limit=25", c.baseURL, url.QueryEscape(query))
	if country != "" {
		u += fmt.Sprintf("&country=%s", url.QueryEscape(country))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("itunes search returned %d", resp.StatusCode)
	}

	var res struct {
		Results []struct {
			TrackName        string `json:"trackName"`
			ArtistName       string `json:"artistName"`
			CollectionName   string `json:"collectionName"`
			PrimaryGenreName string `json:"primaryGenreName"`
			ReleaseDate      string `json:"releaseDate"`
			TrackNumber      int    `json:"trackNumber"`
			TrackTimeMillis  int    `json:"trackTimeMillis"`
			ArtworkURL100    string `json:"artworkUrl100"`
		} `json:"results"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}

	var list []ITunesTrackMatch
	for _, r := range res.Results {
		year := 0
		if len(r.ReleaseDate) >= 4 {
			year, _ = strconv.Atoi(r.ReleaseDate[:4])
		}
		cover := r.ArtworkURL100
		if cover != "" {
			cover = strings.Replace(cover, "100x100bb.jpg", "1000x1000bb.jpg", 1)
		}
		num := r.TrackNumber
		if num <= 0 {
			num = 1
		}
		dur := 0
		if r.TrackTimeMillis > 0 {
			dur = r.TrackTimeMillis / 1000
		}
		cleanTit, feats := ExtractFeatFromText(r.TrackName)
		artists := []string{r.ArtistName}
		artists = append(artists, feats...)
		list = append(list, ITunesTrackMatch{
			Title:       cleanTit,
			Artist:      strings.Join(artists, "\x00"),
			Artists:     artists,
			Album:       r.CollectionName,
			Genre:       strings.TrimSpace(r.PrimaryGenreName),
			Year:        year,
			TrackNumber: num,
			Duration:    dur,
			CoverURL:    cover,
		})
	}
	return list, nil
}
