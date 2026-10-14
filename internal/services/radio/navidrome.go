package radio

import (
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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

func normalizePath(p string) string {
	clean := strings.TrimSpace(p)
	clean = strings.ReplaceAll(clean, "\\", "/")
	return clean
}

// IsStarred проверяет, помечен ли трек как избранный в Navidrome (с нормализацией путей).
func (s *NavidromeStats) IsStarred(relPath string) bool {
	if s == nil || len(s.StarredPaths) == 0 {
		return false
	}
	cleanRel := normalizePath(relPath)
	if s.StarredPaths[cleanRel] {
		return true
	}
	baseRel := filepath.Base(cleanRel)
	for p := range s.StarredPaths {
		cleanP := normalizePath(p)
		if cleanP == cleanRel || strings.HasSuffix(cleanP, "/"+cleanRel) || strings.HasSuffix(cleanP, cleanRel) || (filepath.Base(cleanP) == baseRel && baseRel != ".") {
			return true
		}
	}
	return false
}

// GetPlayCount возвращает количество прослушиваний трека в Navidrome.
func (s *NavidromeStats) GetPlayCount(relPath string) int {
	if s == nil || len(s.PlayCounts) == 0 {
		return 0
	}
	cleanRel := normalizePath(relPath)
	if count, ok := s.PlayCounts[cleanRel]; ok {
		return count
	}
	for p, count := range s.PlayCounts {
		cleanP := normalizePath(p)
		if cleanP == cleanRel || strings.HasSuffix(cleanP, "/"+cleanRel) || strings.HasSuffix(cleanP, cleanRel) {
			return count
		}
	}
	return 0
}

// GetLastPlayed возвращает дату последнего прослушивания трека в Navidrome.
func (s *NavidromeStats) GetLastPlayed(relPath string) time.Time {
	if s == nil || len(s.LastPlayed) == 0 {
		return time.Time{}
	}
	cleanRel := normalizePath(relPath)
	if lp, ok := s.LastPlayed[cleanRel]; ok {
		return lp
	}
	for p, lp := range s.LastPlayed {
		cleanP := normalizePath(p)
		if cleanP == cleanRel || strings.HasSuffix(cleanP, "/"+cleanRel) || strings.HasSuffix(cleanP, cleanRel) {
			return lp
		}
	}
	return time.Time{}
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
			stats.StarredPaths[normalizePath(p)] = true
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
			normP := normalizePath(p)
			stats.PlayCounts[normP] = count
			if playDate.Valid {
				stats.LastPlayed[normP] = playDate.Time
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
