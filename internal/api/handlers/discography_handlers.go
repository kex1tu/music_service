package handlers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"strconv"
	"strings"
	"time"

	"music-manager/internal/core/mediaexec"
	"music-manager/internal/services/resolver"
)

type DiscographyResponse struct {
	ArtistName    string               `json:"artist_name"`
	ArtistID      int64                `json:"artist_id"`
	ArtistAvatar  string               `json:"artist_avatar,omitempty"`
	Source        string               `json:"source,omitempty"`
	TotalReleases int                  `json:"total_releases"`
	Releases      []DiscographyRelease `json:"releases"`
}

type DiscographyRelease struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	Type       string `json:"type"`
	Year       int    `json:"year"`
	URL        string `json:"url"`
	CoverURL   string `json:"cover_url"`
	TrackCount int    `json:"track_count"`
}

func isYouTubeArtistURL(rawURL string) bool {
	lower := strings.ToLower(rawURL)
	if !strings.Contains(lower, "youtube.com") && !strings.Contains(lower, "youtu.be") {
		return false
	}
	// Поддерживаем: /channel/, /@handle, /user/, /c/
	return strings.Contains(lower, "/channel/") ||
		strings.Contains(lower, "/@") ||
		strings.Contains(lower, "/user/") ||
		strings.Contains(lower, "/c/")
}

// GetDiscography обрабатывает запрос дискографии для всех поддерживаемых фронтендом сервисов
func (h *DownloadHandler) GetDiscography(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	rawURL := strings.TrimSpace(r.URL.Query().Get("url"))
	if rawURL == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"detail": "Параметр url обязателен"})
		return
	}

	lower := strings.ToLower(rawURL)

	// 1. YouTube & YouTube Music
	if strings.Contains(lower, "youtube.com") || strings.Contains(lower, "youtu.be") {
		h.getYoutubeMusicDiscography(w, r, rawURL)
		return
	}

	// 2. Deezer
	if strings.Contains(lower, "deezer.com") || strings.Contains(lower, "deezer.page.link") {
		h.getDeezerDiscography(w, r, rawURL)
		return
	}

	// 3. Apple Music / iTunes
	if strings.Contains(lower, "music.apple.com") || strings.Contains(lower, "itunes.apple.com") {
		h.getAppleMusicDiscography(w, r, rawURL)
		return
	}

	// 4. Яндекс Музыка
	if strings.Contains(lower, "yandex") {
		h.getYandexMusicDiscography(w, r, rawURL)
		return
	}

	// 5. Spotify: пока не реализован прямой API сборщик дискографии
	if strings.Contains(lower, "spotify.com") {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"detail": "Пакетная дискография для Spotify пока не поддерживается напрямую. Рекомендуется использовать ссылку на артиста в Яндекс Музыке, Deezer, Apple Music или YouTube Music.",
		})
		return
	}

	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"detail": "Нераспознанный URL артиста. Поддерживаются Яндекс Музыка, Deezer, YouTube Music и Apple Music.",
	})
}

// getDeezerDiscography — стандартная дискография Deezer
func (h *DownloadHandler) getDeezerDiscography(w http.ResponseWriter, r *http.Request, rawURL string) {
	if h.deezerClient == nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"detail": "Deezer клиент не инициализирован"})
		return
	}

	resourceType, artistID, err := h.deezerClient.ParseURL(r.Context(), rawURL)
	if err != nil || resourceType != "artist" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"detail": "Некорректная ссылка на артиста Deezer"})
		return
	}

	artist, err := h.deezerClient.GetArtist(r.Context(), artistID)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"detail": fmt.Sprintf("Не удалось получить профиль артиста Deezer: %v", err)})
		return
	}

	albums, err := h.deezerClient.GetArtistAlbums(r.Context(), artistID)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"detail": fmt.Sprintf("Не удалось получить дискографию Deezer: %v", err)})
		return
	}

	releases := make([]DiscographyRelease, 0, len(albums))
	for _, alb := range albums {
		relType := strings.ToLower(alb.RecordType)
		if relType == "" {
			relType = "album"
		}
		releases = append(releases, DiscographyRelease{
			ID:         alb.ID,
			Title:      alb.Title,
			Type:       relType,
			Year:       alb.ReleaseYear(),
			URL:        fmt.Sprintf("https://www.deezer.com/album/%d", alb.ID),
			CoverURL:   alb.BestCoverURL(),
			TrackCount: alb.NbTracks,
		})
	}

	_ = json.NewEncoder(w).Encode(DiscographyResponse{
		ArtistName:    artist.Name,
		ArtistID:      artist.ID,
		ArtistAvatar:  artist.Picture,
		Source:        "deezer",
		TotalReleases: len(releases),
		Releases:      releases,
	})
}

// getYandexMusicDiscography — получение дискографии через открытый API Яндекс Музыки
func (h *DownloadHandler) getYandexMusicDiscography(w http.ResponseWriter, r *http.Request, rawURL string) {
	var yandexClient *resolver.YandexMusicClient
	if h.service != nil && h.service.Resolver() != nil {
		yandexClient = h.service.Resolver().Yandex()
	}
	if yandexClient == nil {
		yandexClient = resolver.NewYandexMusicClient("")
	}

	resType, artistID, err := yandexClient.ParseURL(rawURL)
	if err != nil || resType != "artist" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"detail": "Некорректная ссылка на артиста Яндекс Музыки (ожидается ссылка вида https://music.yandex.ru/artist/{id})",
		})
		return
	}

	artist, err := yandexClient.GetArtistBriefInfo(r.Context(), artistID)
	if err != nil {
		statusCode := http.StatusBadRequest
		if strings.Contains(err.Error(), "451") || strings.Contains(err.Error(), "Geoblock") {
			statusCode = http.StatusUnavailableForLegalReasons
		}
		w.WriteHeader(statusCode)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"detail": fmt.Sprintf("Не удалось получить профиль артиста Яндекс Музыки: %v", err),
		})
		return
	}

	albums, err := yandexClient.GetArtistDirectAlbums(r.Context(), artistID, 0, 100)
	if err != nil {
		statusCode := http.StatusInternalServerError
		if strings.Contains(err.Error(), "451") || strings.Contains(err.Error(), "Geoblock") {
			statusCode = http.StatusUnavailableForLegalReasons
		}
		w.WriteHeader(statusCode)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"detail": fmt.Sprintf("Не удалось получить дискографию Яндекс Музыки: %v", err),
		})
		return
	}

	releases := make([]DiscographyRelease, 0, len(albums))
	for _, alb := range albums {
		releases = append(releases, DiscographyRelease{
			ID:         alb.ID,
			Title:      alb.Title,
			Type:       alb.RecordType(),
			Year:       alb.Year,
			URL:        fmt.Sprintf("https://music.yandex.ru/album/%d", alb.ID),
			CoverURL:   alb.BestCoverURL(),
			TrackCount: alb.TrackCount,
		})
	}

	_ = json.NewEncoder(w).Encode(DiscographyResponse{
		ArtistName:    artist.Name,
		ArtistID:      artist.ID,
		ArtistAvatar:  artist.AvatarURL(),
		Source:        "yandex",
		TotalReleases: len(releases),
		Releases:      releases,
	})
}

// fetchYtReleases выполняет yt-dlp для целевого URL и возвращает список найденных релизов/плейлистов
func fetchYtReleases(ctx context.Context, targetURL, cookiesPath string) ([]DiscographyRelease, string, error) {
	args := []string{
		"--ignore-config",
		"--no-download-archive",
		"--dump-json",
		"--flat-playlist",
		"--no-warnings",
		"--ignore-errors",
	}
	useCookies := resolver.IsValidNetscapeCookiesFile(cookiesPath)
	if useCookies {
		args = append(args, "--cookies", cookiesPath)
	}
	args = append(args, targetURL)

	cmdCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	cmd := mediaexec.CommandContext(cmdCtx, "yt-dlp", args...)
	defer cmd.Close()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if useCookies && strings.Contains(stderr.String(), "does not look like a Netscape format cookies file") {
			argsNoCookies := make([]string, 0, len(args))
			for i := 0; i < len(args); i++ {
				if args[i] == "--cookies" {
					i++
					continue
				}
				argsNoCookies = append(argsNoCookies, args[i])
			}
			cmdRetry := mediaexec.CommandContext(cmdCtx, "yt-dlp", argsNoCookies...)
			defer cmdRetry.Close()
			stdout.Reset()
			stderr.Reset()
			cmdRetry.Stdout = &stdout
			cmdRetry.Stderr = &stderr
			err = cmdRetry.Run()
		}
		if err != nil && stdout.Len() == 0 {
			cleanStderr := resolver.CleanYtDlpStderr(stderr.String())
			return nil, "", fmt.Errorf("yt-dlp error: %w: %s", err, cleanStderr)
		}
	}

	type YtReleaseItem struct {
		ID            string `json:"id"`
		Title         string `json:"title"`
		Uploader      string `json:"uploader"`
		Channel       string `json:"channel"`
		URL           string `json:"url"`
		WebpageURL    string `json:"webpage_url"`
		Thumbnail     string `json:"thumbnail"`
		ReleaseYear   int    `json:"release_year"`
		PlaylistCount int    `json:"playlist_count"`
	}

	var items []DiscographyRelease
	artistName := ""
	scanner := bufio.NewScanner(&stdout)
	buf := make([]byte, 1024*1024)
	scanner.Buffer(buf, 10*1024*1024)

	idx := int64(1)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e YtReleaseItem
		if err := json.Unmarshal(line, &e); err == nil && (e.ID != "" || e.URL != "") {
			if artistName == "" {
				if e.Channel != "" {
					artistName = resolver.CleanChannelName(e.Channel)
				} else if e.Uploader != "" {
					artistName = resolver.CleanChannelName(e.Uploader)
				}
			}

			relURL := e.WebpageURL
			if relURL == "" {
				relURL = e.URL
			}
			if relURL == "" && e.ID != "" {
				relURL = fmt.Sprintf("https://music.youtube.com/playlist?list=%s", e.ID)
			}

			relType := "album"
			if e.PlaylistCount == 1 {
				relType = "single"
			}

			items = append(items, DiscographyRelease{
				ID:         idx,
				Title:      e.Title,
				Type:       relType,
				Year:       e.ReleaseYear,
				URL:        relURL,
				CoverURL:   e.Thumbnail,
				TrackCount: e.PlaylistCount,
			})
			idx++
		}
	}

	return items, artistName, nil
}

// getYoutubeMusicDiscography — извлечение релизов YouTube Music через yt-dlp
func (h *DownloadHandler) getYoutubeMusicDiscography(w http.ResponseWriter, r *http.Request, rawURL string) {
	targetURL := rawURL
	// Убираем параметры запроса (?si=...)
	if idx := strings.Index(targetURL, "?"); idx != -1 {
		targetURL = targetURL[:idx]
	}

	// Нормализуем URL артиста: добавляем /releases если это канал или @handle
	isReleases := strings.Contains(targetURL, "/releases")
	isPlaylists := strings.Contains(targetURL, "/playlists")
	if !isReleases && !isPlaylists {
		targetURL = strings.TrimRight(targetURL, "/") + "/releases"
	}

	var cookiesPath string
	if h.service != nil {
		cookiesPath = h.service.CookiesPath()
	}

	items, artistName, err := fetchYtReleases(r.Context(), targetURL, cookiesPath)

	// Каскадный fallback: если на /releases пусто, пробуем /playlists (для обычных YouTube каналов)
	if (len(items) == 0 || err != nil) && strings.Contains(targetURL, "/releases") {
		fallbackURL := strings.Replace(targetURL, "/releases", "/playlists", 1)
		if fbItems, fbArtist, fbErr := fetchYtReleases(r.Context(), fallbackURL, cookiesPath); fbErr == nil && len(fbItems) > 0 {
			items = fbItems
			if artistName == "" {
				artistName = fbArtist
			}
			err = nil
		}
	}

	if err != nil && len(items) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"detail": fmt.Sprintf("Сбой получения релизов из YouTube: %v", err),
		})
		return
	}

	if len(items) == 0 {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"detail": "Релизы артиста не найдены на YouTube. Рекомендуется использовать ссылку на артиста в Яндекс Музыке, Deezer или Apple Music.",
		})
		return
	}

	if artistName == "" {
		artistName = "Исполнитель YouTube"
	}

	_ = json.NewEncoder(w).Encode(DiscographyResponse{
		ArtistName:    artistName,
		ArtistID:      0,
		Source:        "youtube",
		TotalReleases: len(items),
		Releases:      items,
	})
}

// getAppleMusicDiscography — получение дискографии через открытый iTunes Search API
func (h *DownloadHandler) getAppleMusicDiscography(w http.ResponseWriter, r *http.Request, rawURL string) {
	// Извлекаем Apple Music Artist ID: https://music.apple.com/ru/artist/name/123456
	parts := strings.Split(rawURL, "/")
	lastPart := parts[len(parts)-1]
	if idx := strings.Index(lastPart, "?"); idx != -1 {
		lastPart = lastPart[:idx]
	}

	itunesURL := fmt.Sprintf("https://itunes.apple.com/lookup?id=%s&entity=album&limit=100", url.QueryEscape(lastPart))
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, itunesURL, nil)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"detail": err.Error()})
		return
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"detail": err.Error()})
		return
	}
	defer resp.Body.Close()

	var itunesData struct {
		Results []struct {
			WrapperType       string `json:"wrapperType"`
			ArtistName        string `json:"artistName"`
			ArtistID          int64  `json:"artistId"`
			CollectionID      int64  `json:"collectionId"`
			CollectionName    string `json:"collectionName"`
			ArtworkURL100     string `json:"artworkUrl100"`
			ReleaseDate       string `json:"releaseDate"`
			TrackCount        int    `json:"trackCount"`
			CollectionViewURL string `json:"collectionViewUrl"`
		} `json:"results"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&itunesData); err != nil || len(itunesData.Results) == 0 {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"detail": "Дискография артиста Apple Music не найдена"})
		return
	}

	artistName := itunesData.Results[0].ArtistName
	artistID := itunesData.Results[0].ArtistID
	var releases []DiscographyRelease

	for _, item := range itunesData.Results {
		if item.WrapperType == "collection" {
			year := 0
			if len(item.ReleaseDate) >= 4 {
				year, _ = strconv.Atoi(item.ReleaseDate[:4])
			}
			coverBig := strings.Replace(item.ArtworkURL100, "100x100bb", "600x600bb", 1)
			releases = append(releases, DiscographyRelease{
				ID:         item.CollectionID,
				Title:      item.CollectionName,
				Type:       "album",
				Year:       year,
				URL:        item.CollectionViewURL,
				CoverURL:   coverBig,
				TrackCount: item.TrackCount,
			})
		}
	}

	_ = json.NewEncoder(w).Encode(DiscographyResponse{
		ArtistName:    artistName,
		ArtistID:      artistID,
		Source:        "applemusic",
		TotalReleases: len(releases),
		Releases:      releases,
	})
}
