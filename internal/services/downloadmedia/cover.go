package downloadmedia

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"music-manager/internal/id3"
	"music-manager/internal/services/resolver"
)

// DownloadCover fetches the original cover and tries the resized Yandex URL if needed.
func DownloadCover(ctx context.Context, client *http.Client, coverURL string) ([]byte, error) {
	if strings.TrimSpace(coverURL) == "" {
		return nil, errors.New("empty cover URL")
	}

	origURL, fallbackURL := resolver.YandexCoverCandidateURLs(coverURL)

	data, err := fetchCoverData(ctx, client, origURL)
	if err == nil && len(data) > 0 {
		return data, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	if fallbackURL != "" && fallbackURL != origURL {
		fallbackData, fallbackErr := fetchCoverData(ctx, client, fallbackURL)
		if fallbackErr == nil && len(fallbackData) > 0 {
			return fallbackData, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("failed to download original (%v) and fallback 1000x1000 (%w)", err, fallbackErr)
	}

	return nil, err
}

func fetchCoverData(ctx context.Context, client *http.Client, urlStr string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	if req.URL.User != nil {
		return nil, fmt.Errorf("cover URL must not contain user credentials")
	}

	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to download cover: status %d", resp.StatusCode)
	}

	if resp.ContentLength > int64(id3.MaxCoverDownloadBytes) {
		return nil, fmt.Errorf("cover Content-Length %d exceeds maximum allowed %d bytes", resp.ContentLength, id3.MaxCoverDownloadBytes)
	}

	limitReader := io.LimitReader(resp.Body, int64(id3.MaxCoverDownloadBytes)+1)
	data, err := io.ReadAll(limitReader)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > int64(id3.MaxCoverDownloadBytes) {
		return nil, fmt.Errorf("cover size exceeds maximum allowed %d bytes", id3.MaxCoverDownloadBytes)
	}
	if len(data) == 0 {
		return nil, errors.New("empty cover response")
	}

	return data, nil
}
