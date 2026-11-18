package resolver

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// YandexMusicClient инкапсулирует вызовы к открытому HTTPS API Яндекс Музыки.
type YandexMusicClient struct {
	httpClient *http.Client
	baseURL    string
	token      string
	signKey    string
}

// NewYandexMusicClient создает новый экземпляр клиента Яндекс Музыки.
// Поддерживает опциональный proxyURL (http://, https://, socks5://) для обхода региональных ограничений (HTTP 451).
func NewYandexMusicClient(token string, proxyURL ...string) *YandexMusicClient {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
	}

	if len(proxyURL) > 0 && strings.TrimSpace(proxyURL[0]) != "" {
		proxyStr := strings.TrimSpace(proxyURL[0])
		if parsedProxy, err := url.Parse(proxyStr); err == nil {
			transport.Proxy = http.ProxyURL(parsedProxy)
		}
	}

	return &YandexMusicClient{
		httpClient: &http.Client{
			Timeout:   15 * time.Second,
			Transport: transport,
		},
		baseURL: "https://api.music.yandex.net",
		token:   strings.TrimSpace(token),
	}
}

// SetSignKey задаёт ключ подписи запросов текстов до запуска обработчиков.
func (c *YandexMusicClient) SetSignKey(key string) {
	c.signKey = strings.TrimSpace(key)
}

func yandexStatusError(resp *http.Response, contextMsg string) error {
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	if resp.StatusCode == http.StatusUnavailableForLegalReasons || resp.StatusCode == 451 {
		return fmt.Errorf("яндекс музыка заблокировала доступ (HTTP 451 Geoblock): IP-адрес сервера находится вне РФ/СНГ. Укажите YANDEX_MUSIC_PROXY в .env (например, http://ru-proxy:port или socks5://ru-proxy:1080) или используйте ссылки Deezer/YouTube")
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("яндекс музыка вернула HTTP %d (доступ запрещен/требуется авторизация): проверьте корректность YANDEX_MUSIC_TOKEN в .env или убедитесь, что плейлист является публичным", resp.StatusCode)
	}
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("яндекс музыка вернула HTTP 404: запрашиваемый ресурс для %s не найден или удален", contextMsg)
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == 429 {
		return fmt.Errorf("яндекс музыка вернула HTTP 429 (слишком много запросов / rate limit): повторите попытку позже")
	}
	return fmt.Errorf("яндекс музыка вернула HTTP %d для %s", resp.StatusCode, contextMsg)
}

// SetBaseURL позволяет переопределить базовый URL для мок-тестов.
func (c *YandexMusicClient) SetBaseURL(u string) {
	c.baseURL = strings.TrimRight(u, "/")
}

func (c *YandexMusicClient) newRequest(ctx context.Context, method, path string) (*http.Request, error) {
	reqURL := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	if c.token != "" {
		req.Header.Set("Authorization", "OAuth "+c.token)
	}
	return req, nil
}
