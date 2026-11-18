package radio

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// NavidromeStats содержит статистику прослушиваний из Navidrome.
type NavidromeStats struct {
	StarredPaths map[string]bool
	PlayCounts   map[string]int
	LastPlayed   map[string]time.Time
}

// NavidromeSubsonicClient выполняет экспорт радиостанций в Navidrome Subsonic REST API.
type NavidromeSubsonicClient struct {
	baseURL  string
	user     string
	password string
	client   *http.Client
}

// NewNavidromeSubsonicClient создает новый клиент Subsonic REST API.
func NewNavidromeSubsonicClient(baseURL, user, password string) *NavidromeSubsonicClient {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	return &NavidromeSubsonicClient{
		baseURL:  baseURL,
		user:     strings.TrimSpace(user),
		password: strings.TrimSpace(password),
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

// IsConfigured проверяет, заданы ли реквизиты для подключения к Subsonic API.
func (c *NavidromeSubsonicClient) IsConfigured() bool {
	return c.baseURL != "" && c.user != "" && c.password != ""
}

// authParams формирует стандартную строку авторизации Subsonic REST API (v1.16.1),
// передавая как безопасный MD5 токен с солью (t+s), так и пароль (p) для максимальной совместимости.
func (c *NavidromeSubsonicClient) authParams() string {
	salt := fmt.Sprintf("%d", time.Now().UnixNano())
	hash := md5.Sum([]byte(c.password + salt))
	token := hex.EncodeToString(hash[:])
	return fmt.Sprintf("u=%s&t=%s&s=%s&p=%s&v=1.16.1&c=music-manager&f=json",
		url.QueryEscape(c.user),
		token,
		salt,
		url.QueryEscape(c.password),
	)
}

// CreateRadioStation экспортирует радиостанцию в Navidrome.
// Выполняет предварительный поиск станции: если станция с таким именем уже существует,
// вызывает updateInternetRadioStation, иначе createInternetRadioStation.
func (c *NavidromeSubsonicClient) CreateRadioStation(ctx context.Context, name, streamURL, homeURL string) error {
	if !c.IsConfigured() {
		return fmt.Errorf("navidrome subsonic credentials are not configured")
	}

	auth := c.authParams()

	// 1. Проверяем, существует ли уже такая радиостанция (для предотвращения дублей)
	existingID := c.findExistingStationID(ctx, auth, name)

	var endpoint string
	if existingID != "" {
		endpoint = fmt.Sprintf("%s/rest/updateInternetRadioStation?%s&id=%s&name=%s&streamUrl=%s&homepageUrl=%s&homePageUrl=%s",
			c.baseURL,
			auth,
			url.QueryEscape(existingID),
			url.QueryEscape(name),
			url.QueryEscape(streamURL),
			url.QueryEscape(homeURL),
			url.QueryEscape(homeURL),
		)
	} else {
		endpoint = fmt.Sprintf("%s/rest/createInternetRadioStation?%s&name=%s&streamUrl=%s&homepageUrl=%s&homePageUrl=%s",
			c.baseURL,
			auth,
			url.QueryEscape(name),
			url.QueryEscape(streamURL),
			url.QueryEscape(homeURL),
			url.QueryEscape(homeURL),
		)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("subsonic connection failed to %s: %w", c.baseURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("subsonic request returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var res struct {
		SubsonicResponse struct {
			Status string `json:"status"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error,omitempty"`
		} `json:"subsonic-response"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return err
	}
	if res.SubsonicResponse.Status == "failed" && res.SubsonicResponse.Error != nil {
		// Если обновление завершилось ошибкой, пробуем создать заново
		if existingID != "" {
			return c.callCreateEndpoint(ctx, auth, name, streamURL, homeURL)
		}
		return fmt.Errorf("subsonic error %d: %s", res.SubsonicResponse.Error.Code, res.SubsonicResponse.Error.Message)
	}

	return nil
}

func (c *NavidromeSubsonicClient) findExistingStationID(ctx context.Context, auth, name string) string {
	endpoint := fmt.Sprintf("%s/rest/getInternetRadioStations?%s", c.baseURL, auth)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ""
	}
	resp, err := c.client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return ""
	}
	defer resp.Body.Close()

	var res struct {
		SubsonicResponse struct {
			InternetRadioStations *struct {
				Station []struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"internetRadioStation"`
			} `json:"internetRadioStations,omitempty"`
		} `json:"subsonic-response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil || res.SubsonicResponse.InternetRadioStations == nil {
		return ""
	}
	for _, s := range res.SubsonicResponse.InternetRadioStations.Station {
		if strings.EqualFold(s.Name, name) {
			return s.ID
		}
	}
	return ""
}

func (c *NavidromeSubsonicClient) callCreateEndpoint(ctx context.Context, auth, name, streamURL, homeURL string) error {
	endpoint := fmt.Sprintf("%s/rest/createInternetRadioStation?%s&name=%s&streamUrl=%s&homepageUrl=%s&homePageUrl=%s",
		c.baseURL,
		auth,
		url.QueryEscape(name),
		url.QueryEscape(streamURL),
		url.QueryEscape(homeURL),
		url.QueryEscape(homeURL),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("subsonic createInternetRadioStation returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
