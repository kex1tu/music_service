package resolver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// GetPlaylist возвращает публичный плейлист по owner и kind (например, /users/yamusic-top/playlists/1076).
func (c *YandexMusicClient) GetPlaylist(ctx context.Context, owner string, kind int64) (*YandexPlaylist, error) {
	cleanOwner := strings.TrimSpace(owner)
	if cleanOwner == "" || kind <= 0 {
		return nil, errors.New("owner and positive kind are required to fetch playlist")
	}

	path := fmt.Sprintf("/users/%s/playlists/%d?rich-tracks=true", url.PathEscape(cleanOwner), kind)
	req, err := c.newRequest(ctx, http.MethodGet, path)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("сбой запроса плейлиста Яндекс Музыки: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, yandexStatusError(resp, fmt.Sprintf("плейлиста %s/%d", cleanOwner, kind))
	}

	var data struct {
		Result YandexPlaylist `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("ошибка парсинга JSON плейлиста Яндекс Музыки: %w", err)
	}

	playlist := &data.Result
	if playlist.Kind == 0 {
		playlist.Kind = kind
	}
	if playlist.Owner.Login == "" && playlist.Owner.UID == 0 {
		playlist.Owner.Login = cleanOwner
	}

	// Обработка пагинации
	if playlist.Pager != nil && playlist.Pager.PerPage > 0 && playlist.Pager.Total > len(playlist.Tracks) {
		c.paginatePlaylist(ctx, playlist, func(page int) string {
			return fmt.Sprintf("/users/%s/playlists/%d?rich-tracks=true&page=%d", url.PathEscape(cleanOwner), kind, page)
		})
	}

	return playlist, nil
}

// GetPlaylistByUUID возвращает публичный плейлист по его глобальному UUID.
func (c *YandexMusicClient) GetPlaylistByUUID(ctx context.Context, playlistUUID string) (*YandexPlaylist, error) {
	cleanUUID := strings.TrimSpace(playlistUUID)
	if cleanUUID == "" {
		return nil, errors.New("playlist UUID cannot be empty")
	}

	path := fmt.Sprintf("/playlist/%s?rich-tracks=true", url.PathEscape(cleanUUID))
	req, err := c.newRequest(ctx, http.MethodGet, path)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("сбой запроса плейлиста по UUID Яндекс Музыки: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// Резервный эндпоинт во множественном числе /playlists/{uuid}
		altPath := fmt.Sprintf("/playlists/%s?rich-tracks=true", url.PathEscape(cleanUUID))
		if altReq, altErr := c.newRequest(ctx, http.MethodGet, altPath); altErr == nil {
			if altResp, doErr := c.httpClient.Do(altReq); doErr == nil {
				defer altResp.Body.Close()
				if altResp.StatusCode == http.StatusOK {
					var altData struct {
						Result YandexPlaylist `json:"result"`
					}
					if json.NewDecoder(altResp.Body).Decode(&altData) == nil {
						playlist := &altData.Result
						if playlist.PlaylistUUID == "" {
							playlist.PlaylistUUID = cleanUUID
						}
						return playlist, nil
					}
				}
			}
		}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, yandexStatusError(resp, fmt.Sprintf("плейлиста UUID %s", cleanUUID))
	}

	var data struct {
		Result YandexPlaylist `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("ошибка парсинга JSON плейлиста по UUID Яндекс Музыки: %w", err)
	}

	playlist := &data.Result
	if playlist.PlaylistUUID == "" {
		playlist.PlaylistUUID = cleanUUID
	}

	if playlist.Pager != nil && playlist.Pager.PerPage > 0 && playlist.Pager.Total > len(playlist.Tracks) {
		c.paginatePlaylist(ctx, playlist, func(page int) string {
			return fmt.Sprintf("/playlist/%s?rich-tracks=true&page=%d", url.PathEscape(cleanUUID), page)
		})
	}

	return playlist, nil
}

func (c *YandexMusicClient) paginatePlaylist(ctx context.Context, playlist *YandexPlaylist, pageURLFunc func(int) string) {
	if playlist.Pager == nil || playlist.Pager.PerPage <= 0 {
		return
	}
	total := playlist.Pager.Total
	perPage := playlist.Pager.PerPage
	currentPage := playlist.Pager.Page + 1
	maxPages := (total + perPage - 1) / perPage
	if maxPages > 50 {
		maxPages = 50
	}

	for page := currentPage; page < maxPages; page++ {
		if ctx.Err() != nil {
			break
		}
		req, err := c.newRequest(ctx, http.MethodGet, pageURLFunc(page))
		if err != nil {
			break
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			break
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			break
		}
		var pageData struct {
			Result struct {
				Tracks []YandexPlaylistTrackItem `json:"tracks"`
			} `json:"result"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&pageData)
		resp.Body.Close()
		if decodeErr != nil || len(pageData.Result.Tracks) == 0 {
			break
		}
		playlist.Tracks = append(playlist.Tracks, pageData.Result.Tracks...)
		if len(playlist.Tracks) >= total {
			break
		}
	}
}
