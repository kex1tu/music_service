package resolver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// GetArtistBriefInfo возвращает имя и аватар артиста.
func (c *YandexMusicClient) GetArtistBriefInfo(ctx context.Context, artistID int64) (*YandexArtistMeta, error) {
	req, err := c.newRequest(ctx, http.MethodGet, fmt.Sprintf("/artists/%d/brief-info", artistID))
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("сбой запроса профиля артиста Яндекс Музыки: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, yandexStatusError(resp, fmt.Sprintf("артиста %d", artistID))
	}

	var data struct {
		Result struct {
			Artist YandexArtistMeta `json:"artist"`
		} `json:"result"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("ошибка парсинга JSON артиста Яндекс Музыки: %w", err)
	}

	if data.Result.Artist.ID == 0 {
		data.Result.Artist.ID = artistID
	}
	return &data.Result.Artist, nil
}

// GetArtistDirectAlbums возвращает список официальных альбомов и синглов артиста.
func (c *YandexMusicClient) GetArtistDirectAlbums(ctx context.Context, artistID int64, page, pageSize int) ([]YandexAlbum, error) {
	if pageSize <= 0 {
		pageSize = 100
	}
	req, err := c.newRequest(ctx, http.MethodGet, fmt.Sprintf("/artists/%d/direct-albums?page=%d&pageSize=%d", artistID, page, pageSize))
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("сбой запроса дискографии артиста Яндекс Музыки: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, yandexStatusError(resp, fmt.Sprintf("дискографии артиста %d", artistID))
	}

	var data struct {
		Result struct {
			Albums []YandexAlbum `json:"albums"`
		} `json:"result"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("ошибка парсинга JSON дискографии Яндекс Музыки: %w", err)
	}

	return data.Result.Albums, nil
}

// GetAlbumWithTracks возвращает метаданные альбома и его треклист.
func (c *YandexMusicClient) GetAlbumWithTracks(ctx context.Context, albumID int64) (*YandexAlbumWithTracks, error) {
	req, err := c.newRequest(ctx, http.MethodGet, fmt.Sprintf("/albums/%d/with-tracks", albumID))
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("сбой запроса треклиста альбома Яндекс Музыки: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, yandexStatusError(resp, fmt.Sprintf("альбома %d", albumID))
	}

	var data struct {
		Result struct {
			YandexAlbum
			Volumes [][]YandexTrack `json:"volumes"`
		} `json:"result"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("ошибка парсинга JSON треклиста альбома Яндекс Музыки: %w", err)
	}

	res := &YandexAlbumWithTracks{
		YandexAlbum: data.Result.YandexAlbum,
	}

	pos := 1
	for discIdx, volume := range data.Result.Volumes {
		discNum := discIdx + 1
		for _, tr := range volume {
			tCopy := tr
			tCopy.DiskNumber = discNum
			tCopy.TrackPosition = pos
			pos++
			res.Tracks = append(res.Tracks, tCopy)
		}
	}

	return res, nil
}

// SearchTrack выполняет поиск треков для сверки метаданных и соавторов.
func (c *YandexMusicClient) SearchTrack(ctx context.Context, query string) ([]YandexTrack, error) {
	cleanQ := strings.TrimSpace(query)
	if cleanQ == "" {
		return nil, nil
	}

	req, err := c.newRequest(ctx, http.MethodGet, fmt.Sprintf("/search?text=%s&type=track&page=0", url.QueryEscape(cleanQ)))
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("сбой поиска трека в Яндекс Музыке: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, yandexStatusError(resp, fmt.Sprintf("поиска трека %q", cleanQ))
	}

	var data struct {
		Result struct {
			Tracks struct {
				Results []YandexTrack `json:"results"`
			} `json:"tracks"`
		} `json:"result"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("ошибка парсинга JSON результатов поиска: %w", err)
	}

	return data.Result.Tracks.Results, nil
}

// GetTracks выполняет пакетный запрос метаданных треков через POST /tracks.
func (c *YandexMusicClient) GetTracks(ctx context.Context, trackIDs []string) ([]YandexTrack, error) {
	if len(trackIDs) == 0 {
		return nil, nil
	}

	var allTracks []YandexTrack
	batchSize := 100
	for i := 0; i < len(trackIDs); i += batchSize {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		end := i + batchSize
		if end > len(trackIDs) {
			end = len(trackIDs)
		}
		batch := trackIDs[i:end]

		form := url.Values{}
		form.Set("track-ids", strings.Join(batch, ","))

		req, err := c.newRequest(ctx, http.MethodPost, "/tracks")
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Body = io.NopCloser(strings.NewReader(form.Encode()))

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("сбой пакетного запроса треков Яндекс Музыки: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			err := yandexStatusError(resp, "пакетного запроса треков")
			resp.Body.Close()
			return nil, err
		}

		var data struct {
			Result []YandexTrack `json:"result"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&data)
		resp.Body.Close()
		if decodeErr != nil {
			return nil, fmt.Errorf("ошибка парсинга JSON пакета треков Яндекс Музыки: %w", decodeErr)
		}
		allTracks = append(allTracks, data.Result...)
	}

	return allTracks, nil
}
