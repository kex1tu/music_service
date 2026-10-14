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

// LastfmSimilarArtist представляет похожего артиста из Last.fm API.
type LastfmSimilarArtist struct {
	Name  string  `json:"name"`
	Match float64 `json:"match"`
	URL   string  `json:"url"`
}

// LastfmClient осуществляет запросы к Last.fm API v2.0.
type LastfmClient struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

// NewLastfmClient создает новый экземпляр клиента Last.fm.
func NewLastfmClient(apiKey string) *LastfmClient {
	return &LastfmClient{
		apiKey:     strings.TrimSpace(apiKey),
		baseURL:    "https://ws.audioscrobbler.com/2.0",
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// SetBaseURL переопределяет базовый URL (для модульного тестирования с httptest.Server).
func (c *LastfmClient) SetBaseURL(u string) {
	c.baseURL = u
}

// IsConfigured проверяет, задан ли API-ключ.
func (c *LastfmClient) IsConfigured() bool {
	return c.apiKey != ""
}

// GetSimilarArtists запрашивает список похожих артистов через метод artist.getsimilar.
func (c *LastfmClient) GetSimilarArtists(ctx context.Context, artistName string, limit int) ([]LastfmSimilarArtist, error) {
	if !c.IsConfigured() {
		return nil, fmt.Errorf("last.fm API key is not configured")
	}
	artistName = strings.TrimSpace(artistName)
	if artistName == "" {
		return nil, fmt.Errorf("artist name cannot be empty")
	}
	if limit <= 0 {
		limit = 10
	}

	endpoint := fmt.Sprintf("%s/?method=artist.getsimilar&artist=%s&api_key=%s&limit=%d&format=json",
		c.baseURL, url.QueryEscape(artistName), url.QueryEscape(c.apiKey), limit)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "MusicManager/1.0 (contact@verymad.net)")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("last.fm request failed with status %d", resp.StatusCode)
	}

	var data struct {
		SimilarArtists struct {
			Artist []struct {
				Name  string `json:"name"`
				Match string `json:"match"`
				URL   string `json:"url"`
			} `json:"artist"`
		} `json:"similarartists"`
		Error   int    `json:"error,omitempty"`
		Message string `json:"message,omitempty"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	if data.Error > 0 {
		return nil, fmt.Errorf("last.fm error %d: %s", data.Error, data.Message)
	}

	var result []LastfmSimilarArtist
	for _, a := range data.SimilarArtists.Artist {
		matchScore, _ := strconv.ParseFloat(a.Match, 64)
		result = append(result, LastfmSimilarArtist{
			Name:  a.Name,
			Match: matchScore,
			URL:   a.URL,
		})
	}
	return result, nil
}
