package radio

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// NavidromeStats содержит статистику прослушиваний из Navidrome.
type NavidromeStats struct {
	StarredPaths map[string]bool
	PlayCounts   map[string]int
	LastPlayed   map[string]time.Time
}

// NavidromeReader осуществляет безопасное чтение navidrome.db в режиме strictly read-only.
type NavidromeReader struct {
	dbPath string
}

// NewNavidromeReader создает новый экземпляр NavidromeReader.
func NewNavidromeReader(dbPath string) *NavidromeReader {
	return &NavidromeReader{dbPath: strings.TrimSpace(dbPath)}
}

// IsAvailable проверяет, существует ли файл базы Navidrome.
func (r *NavidromeReader) IsAvailable() bool {
	if r.dbPath == "" {
		return false
	}
	info, err := os.Stat(r.dbPath)
	return err == nil && !info.IsDir()
}

// GetUserListeningStats читает избранные треки и количество прослушиваний из navidrome.db.
func (r *NavidromeReader) GetUserListeningStats(ctx context.Context) (*NavidromeStats, error) {
	if !r.IsAvailable() {
		return nil, fmt.Errorf("navidrome database not available at %q", r.dbPath)
	}

	// Открываем базу строго в режиме read-only с таймаутом занятости
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(5000)", r.dbPath)
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open navidrome db: %w", err)
	}
	defer conn.Close()

	stats := &NavidromeStats{
		StarredPaths: make(map[string]bool),
		PlayCounts:   make(map[string]int),
		LastPlayed:   make(map[string]time.Time),
	}

	// 1. Извлечение избранного (starred = 1)
	starredQuery := `
	SELECT mf.path
	FROM annotation a
	JOIN media_file mf ON a.item_id = mf.id
	WHERE a.starred = 1;
	`
	rows, err := conn.QueryContext(ctx, starredQuery)
	if err != nil {
		return nil, fmt.Errorf("navidrome query starred failed: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err == nil && p != "" {
			stats.StarredPaths[p] = true
		}
	}

	// 2. Извлечение истории прослушиваний (play_count > 0)
	historyQuery := `
	SELECT mf.path, COALESCE(a.play_count, 0), a.play_date
	FROM annotation a
	JOIN media_file mf ON a.item_id = mf.id
	WHERE a.play_count > 0;
	`
	hRows, err := conn.QueryContext(ctx, historyQuery)
	if err != nil {
		return nil, fmt.Errorf("navidrome query history failed: %w", err)
	}
	defer hRows.Close()
	for hRows.Next() {
		var p string
		var count int
		var playDate sql.NullTime
		if err := hRows.Scan(&p, &count, &playDate); err == nil && p != "" {
			stats.PlayCounts[p] = count
			if playDate.Valid {
				stats.LastPlayed[p] = playDate.Time
			}
		}
	}

	return stats, nil
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

// CreateRadioStation экспортирует радиостанцию в Navidrome.
func (c *NavidromeSubsonicClient) CreateRadioStation(ctx context.Context, name, streamURL, homeURL string) error {
	if !c.IsConfigured() {
		return fmt.Errorf("navidrome subsonic credentials are not configured")
	}

	// Subsonic API endpoint: /rest/createInternetRadioStation
	endpoint := fmt.Sprintf("%s/rest/createInternetRadioStation?u=%s&p=%s&v=1.16.1&c=music-manager&name=%s&streamUrl=%s&homePageUrl=%s&f=json",
		c.baseURL,
		url.QueryEscape(c.user),
		url.QueryEscape(c.password),
		url.QueryEscape(name),
		url.QueryEscape(streamURL),
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
		return fmt.Errorf("subsonic createInternetRadioStation returned status %d", resp.StatusCode)
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
		return fmt.Errorf("subsonic error %d: %s", res.SubsonicResponse.Error.Code, res.SubsonicResponse.Error.Message)
	}

	return nil
}
