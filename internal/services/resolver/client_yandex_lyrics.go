package resolver

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var (
	// ErrYandexNoToken возвращается при попытке запроса текстов без указания OAuth токена.
	ErrYandexNoToken         = errors.New("токен Яндекс Музыки не задан или пуст")
	ErrYandexNoLyricsSignKey = errors.New("ключ подписи текстов Яндекс Музыки не задан")
	// ErrYandexLyricsNotFound возвращается, если у трека отсутствует официальный текст в каталоге.
	ErrYandexLyricsNotFound = errors.New("текст для трека Яндекс Музыки не найден")
)

var errYandexLyricsRedirectLimit = errors.New("слишком много перенаправлений хранилища текстов")

var lrcTimestampRegex = regexp.MustCompile(`\[\d{2}:\d{2}(?:\.\d{2,3})?\]`)

func signYandexLyricsRequest(trackID int64, key string) (int64, string) {
	timestamp := time.Now().UTC().Unix()
	msg := fmt.Sprintf("%d%d", trackID, timestamp)
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(msg))
	sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return timestamp, sign
}

// GetTrackLyrics запрашивает официальный текст песни Яндекс Музыки по точному числовому ID трека.
// Сначала запрашивается синхронизированный формат (format=LRC).
// Если LRC отсутствует (404), автоматически пробуется обычный текст (format=TEXT).
// Возвращает текст песни, признак синхронизированности (synced) и ошибку.
func (c *YandexMusicClient) GetTrackLyrics(ctx context.Context, trackID int64) (string, bool, error) {
	if trackID <= 0 {
		return "", false, errors.New("некорректный ID трека Яндекс Музыки")
	}
	if strings.TrimSpace(c.token) == "" {
		return "", false, ErrYandexNoToken
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}

	if c.signKey == "" {
		return "", false, ErrYandexNoLyricsSignKey
	}
	metadataClient := *c.httpClient
	metadataClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	// 1. Вспомогательная функция запроса метаданных текста для указанного формата
	fetchMetadata := func(format string) (string, error) {
		ts, sign := signYandexLyricsRequest(trackID, c.signKey)
		endpoint := fmt.Sprintf("/tracks/%d/lyrics?format=%s&timeStamp=%d&sign=%s",
			trackID, url.QueryEscape(format), ts, url.QueryEscape(sign))

		req, err := c.newRequest(ctx, http.MethodGet, endpoint)
		if err != nil {
			return "", err
		}

		resp, err := metadataClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", fmt.Errorf("сбой запроса метаданных текста Яндекс Музыки: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound {
			return "", ErrYandexLyricsNotFound
		}
		if resp.StatusCode != http.StatusOK {
			return "", yandexStatusError(resp, fmt.Sprintf("текста трека %d (%s)", trackID, format))
		}

		var env struct {
			Result struct {
				DownloadURL string `json:"downloadUrl"`
			} `json:"result"`
		}
		if decErr := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&env); decErr != nil {
			return "", fmt.Errorf("ошибка парсинга JSON метаданных текста: %w", decErr)
		}

		cleanURL := strings.TrimSpace(env.Result.DownloadURL)
		if cleanURL == "" {
			return "", ErrYandexLyricsNotFound
		}
		return cleanURL, nil
	}

	// Сначала запрашиваем синхронизированный LRC
	downloadURL, err := fetchMetadata("LRC")
	if err != nil {
		if ctx.Err() != nil {
			return "", false, ctx.Err()
		}
		if errors.Is(err, ErrYandexLyricsNotFound) {
			// Fallback на обычный текст (TEXT)
			var textErr error
			downloadURL, textErr = fetchMetadata("TEXT")
			if textErr != nil {
				if ctx.Err() != nil {
					return "", false, ctx.Err()
				}
				return "", false, textErr
			}
		} else {
			return "", false, err
		}
	}

	baseURL, err := url.Parse(c.baseURL)
	if err != nil {
		return "", false, err
	}
	u, err := baseURL.Parse(downloadURL)
	if err != nil || !allowedYandexLyricsURL(u, baseURL) {
		return "", false, errors.New("недопустимый адрес хранилища текстов Яндекс Музыки")
	}

	dlReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", false, err
	}
	dlReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	dlReq.Header.Set("Accept", "*/*")
	// ВАЖНО: Заголовок Authorization: OAuth СТРОГО не передается на сервер хранения текста (AWS S3 / Yandex Storage)

	dlClient := &http.Client{
		Timeout:   15 * time.Second,
		Transport: c.httpClient.Transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errYandexLyricsRedirectLimit
			}
			if !allowedYandexLyricsURL(req.URL, baseURL) {
				return errors.New("недопустимый адрес перенаправления хранилища текстов")
			}
			req.Header.Del("Authorization")
			return nil
		},
	}

	dlResp, err := dlClient.Do(dlReq)
	if err != nil {
		if ctx.Err() != nil {
			return "", false, ctx.Err()
		}
		if errors.Is(err, errYandexLyricsRedirectLimit) {
			return "", false, errYandexLyricsRedirectLimit
		}
		return "", false, errors.New("сбой скачивания файла текста Яндекс Музыки")
	}
	defer dlResp.Body.Close()

	if dlResp.StatusCode != http.StatusOK {
		if dlResp.StatusCode == http.StatusNotFound {
			return "", false, ErrYandexLyricsNotFound
		}
		return "", false, fmt.Errorf("сервер хранения текста вернул HTTP %d", dlResp.StatusCode)
	}

	const maxLyricsBytes = 512 * 1024 // 512 KB
	rawBytes, err := io.ReadAll(io.LimitReader(dlResp.Body, maxLyricsBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return "", false, ctx.Err()
		}
		return "", false, fmt.Errorf("ошибка чтения тела текста: %w", err)
	}
	if len(rawBytes) > maxLyricsBytes {
		return "", false, fmt.Errorf("размер файла текста превышает допустимый лимит 512 КБ")
	}

	content := strings.TrimSpace(string(rawBytes))
	if content == "" {
		return "", false, ErrYandexLyricsNotFound
	}

	lower := strings.ToLower(content)
	if strings.HasPrefix(lower, "<!doctype") || strings.HasPrefix(lower, "<html") || (strings.Contains(lower, "<head>") && strings.Contains(lower, "<body>")) {
		return "", false, fmt.Errorf("сервер вернул HTML-страницу вместо текста песни")
	}

	if (strings.HasPrefix(content, "{") && strings.HasSuffix(content, "}")) || (strings.HasPrefix(content, "[") && strings.HasSuffix(content, "]")) {
		var jsTest any
		if json.Unmarshal([]byte(content), &jsTest) == nil {
			return "", false, fmt.Errorf("сервер вернул JSON-ответ вместо текста песни")
		}
	}

	synced := lrcTimestampRegex.MatchString(content)
	return content, synced, nil
}

// allowedYandexLyricsURL allows the API origin and the known Yandex lyrics CDN.
// The injected API origin also permits same-origin requests from local HTTP fixtures.
func allowedYandexLyricsURL(target, api *url.URL) bool {
	if target.User != nil || target.Host == "" {
		return false
	}
	if target.Scheme == api.Scheme && strings.EqualFold(target.Host, api.Host) {
		return true
	}
	return target.Scheme == "https" && strings.EqualFold(target.Host, "music-lyrics.s3-private.mds.yandex.net")
}
