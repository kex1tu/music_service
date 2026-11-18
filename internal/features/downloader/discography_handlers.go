package downloader

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"music-manager/internal/core/httputil"
	"music-manager/internal/core/mediaexec"
	"music-manager/internal/services/resolver"
)

// isHostMatchingDomain проверяет точное совпадение хоста или принадлежность поддомену.
func isHostMatchingDomain(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}

// GetDiscography возвращает дискографию артиста по внешней ссылке.
// GET /downloads/discography
func (m *Module) GetDiscography(w http.ResponseWriter, r *http.Request) {
	rawURL := strings.TrimSpace(httputil.QueryString(r, "url", ""))
	if rawURL == "" {
		httputil.RespondError(w, http.StatusBadRequest, "Параметр url обязателен")
		return
	}

	// 1. Валидация схемы: разрешены только HTTP и HTTPS
	if strings.Contains(rawURL, "://") && !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		httputil.RespondError(w, http.StatusBadRequest, "Поддерживаются только схемы HTTP и HTTPS")
		return
	}

	// 2. Строгая проверка через белый список доверенных источников (SSRF Guard)
	if !resolver.IsAllowedURL(rawURL) {
		httputil.RespondError(w, http.StatusBadRequest, "Домен не входит в список разрешенных источников")
		return
	}

	// 3. Синтаксический разбор URL и извлечение канонического хоста
	cleanURL := rawURL
	if !strings.HasPrefix(cleanURL, "http://") && !strings.HasPrefix(cleanURL, "https://") {
		cleanURL = "https://" + cleanURL
	}
	parsedURL, err := url.Parse(cleanURL)
	if err != nil || parsedURL.Hostname() == "" {
		httputil.RespondError(w, http.StatusBadRequest, "Некорректный URL")
		return
	}
	host := strings.ToLower(parsedURL.Hostname())

	// 4. Диспетчеризация строго по валидированному хосту

	// YouTube & YouTube Music
	if isHostMatchingDomain(host, "youtube.com") || isHostMatchingDomain(host, "youtu.be") {
		m.getYoutubeMusicDiscography(w, r, rawURL)
		return
	}

	// Deezer
	if isHostMatchingDomain(host, "deezer.com") || isHostMatchingDomain(host, "deezer.page.link") || host == "link.deezer.com" {
		m.getDeezerDiscography(w, r, rawURL)
		return
	}

	// Apple Music / iTunes
	if host == "music.apple.com" || host == "itunes.apple.com" || isHostMatchingDomain(host, "apple.com") {
		m.getAppleMusicDiscography(w, r, rawURL)
		return
	}

	// Яндекс Музыка
	if isHostMatchingDomain(host, "music.yandex.ru") || isHostMatchingDomain(host, "music.yandex.com") || host == "yandex.ru" || host == "yandex.com" {
		m.getYandexMusicDiscography(w, r, rawURL)
		return
	}

	// Spotify (информативный ответ об отсутствии прямой пакетной дискографии)
	if isHostMatchingDomain(host, "spotify.com") || isHostMatchingDomain(host, "spotify.link") {
		httputil.RespondError(w, http.StatusBadRequest, "Пакетная дискография для Spotify пока не поддерживается напрямую. Рекомендуется использовать ссылку на артиста в Яндекс Музыке, Deezer, Apple Music или YouTube Music.")
		return
	}

	httputil.RespondError(w, http.StatusBadRequest, "Нераспознанный URL артиста. Поддерживаются Яндекс Музыка, Deezer, YouTube Music и Apple Music.")
}

func (m *Module) getDeezerDiscography(w http.ResponseWriter, r *http.Request, rawURL string) {
	var deezer *resolver.DeezerClient
	if m.deezerClient != nil {
		deezer = m.deezerClient
	} else if m.service != nil && m.service.Resolver() != nil {
		deezer = m.service.Resolver().Deezer()
	}
	if deezer == nil {
		deezer = resolver.NewDeezerClient()
	}

	resourceType, artistID, err := deezer.ParseURL(r.Context(), rawURL)
	if err != nil || resourceType != "artist" {
		httputil.RespondError(w, http.StatusBadRequest, "Некорректная ссылка на артиста Deezer")
		return
	}

	artist, err := deezer.GetArtist(r.Context(), artistID)
	if err != nil {
		httputil.RespondError(w, http.StatusBadRequest, fmt.Sprintf("Не удалось получить профиль артиста Deezer: %v", err))
		return
	}

	albums, err := deezer.GetArtistAlbums(r.Context(), artistID)
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, fmt.Sprintf("Не удалось получить дискографию Deezer: %v", err))
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

	httputil.RespondJSON(w, http.StatusOK, DiscographyResponse{
		ArtistName:    artist.Name,
		ArtistID:      artist.ID,
		ArtistAvatar:  artist.Picture,
		Source:        "deezer",
		TotalReleases: len(releases),
		Releases:      releases,
	})
}

func (m *Module) getYandexMusicDiscography(w http.ResponseWriter, r *http.Request, rawURL string) {
	var yandexClient *resolver.YandexMusicClient
	if m.service != nil && m.service.Resolver() != nil {
		yandexClient = m.service.Resolver().YandexClient()
	}
	if yandexClient == nil {
		yandexClient = resolver.NewYandexMusicClient("")
	}

	resType, artistID, err := yandexClient.ParseURL(rawURL)
	if err != nil || resType != "artist" {
		httputil.RespondError(w, http.StatusBadRequest, "Некорректная ссылка на артиста Яндекс Музыки (ожидается ссылка вида https://music.yandex.ru/artist/{id})")
		return
	}

	artist, err := yandexClient.GetArtistBriefInfo(r.Context(), artistID)
	if err != nil {
		statusCode := http.StatusBadRequest
		if strings.Contains(err.Error(), "451") || strings.Contains(err.Error(), "Geoblock") {
			statusCode = http.StatusUnavailableForLegalReasons
		}
		httputil.RespondError(w, statusCode, fmt.Sprintf("Не удалось получить профиль артиста Яндекс Музыки: %v", err))
		return
	}

	albums, err := yandexClient.GetArtistDirectAlbums(r.Context(), artistID, 0, 100)
	if err != nil {
		statusCode := http.StatusInternalServerError
		if strings.Contains(err.Error(), "451") || strings.Contains(err.Error(), "Geoblock") {
			statusCode = http.StatusUnavailableForLegalReasons
		}
		httputil.RespondError(w, statusCode, fmt.Sprintf("Не удалось получить дискографию Яндекс Музыки: %v", err))
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

	httputil.RespondJSON(w, http.StatusOK, DiscographyResponse{
		ArtistName:    artist.Name,
		ArtistID:      artist.ID,
		ArtistAvatar:  artist.AvatarURL(),
		Source:        "yandex",
		TotalReleases: len(releases),
		Releases:      releases,
	})
}

func (m *Module) getYoutubeMusicDiscography(w http.ResponseWriter, r *http.Request, rawURL string) {
	targetURL := rawURL
	if idx := strings.Index(targetURL, "?"); idx != -1 {
		targetURL = targetURL[:idx]
	}

	isReleases := strings.Contains(targetURL, "/releases")
	isPlaylists := strings.Contains(targetURL, "/playlists")
	if !isReleases && !isPlaylists {
		targetURL = strings.TrimRight(targetURL, "/") + "/releases"
	}

	var cookiesPath string
	if m.service != nil {
		cookiesPath = m.service.CookiesPath()
	}

	fetcher := m.getYtReleasesFetcher()
	if fetcher == nil {
		fetcher = defaultYtReleasesFetcher
	}

	items, artistName, err := fetcher(r.Context(), targetURL, cookiesPath)
	if (len(items) == 0 || err != nil) && strings.Contains(targetURL, "/releases") {
		fallbackURL := strings.Replace(targetURL, "/releases", "/playlists", 1)
		if fbItems, fbArtist, fbErr := fetcher(r.Context(), fallbackURL, cookiesPath); fbErr == nil && len(fbItems) > 0 {
			items = fbItems
			if artistName == "" {
				artistName = fbArtist
			}
			err = nil
		}
	}

	if err != nil && len(items) == 0 {
		httputil.RespondError(w, http.StatusBadRequest, fmt.Sprintf("Сбой получения релизов из YouTube: %v", err))
		return
	}

	if len(items) == 0 {
		httputil.RespondError(w, http.StatusNotFound, "Релизы артиста не найдены на YouTube. Рекомендуется использовать ссылку на артиста в Яндекс Музыке, Deezer или Apple Music.")
		return
	}

	if artistName == "" {
		artistName = "Исполнитель YouTube"
	}

	httputil.RespondJSON(w, http.StatusOK, DiscographyResponse{
		ArtistName:    artistName,
		ArtistID:      0,
		Source:        "youtube",
		TotalReleases: len(items),
		Releases:      items,
	})
}

// defaultYtReleasesFetcher выполняет yt-dlp для целевого URL с автоматической
// защитой от запуска реальных внешних процессов в тестовом окружении.
func defaultYtReleasesFetcher(ctx context.Context, targetURL, cookiesPath string) ([]DiscographyRelease, string, error) {
	if os.Getenv("MUSIC_MANAGER_TEST_MODE") == "1" {
		mockReleases := []DiscographyRelease{
			{
				ID:         1,
				Title:      "Mock Test Album",
				Type:       "album",
				Year:       2026,
				URL:        targetURL,
				CoverURL:   "https://lh3.googleusercontent.com/mock-cover",
				TrackCount: 10,
			},
		}
		return mockReleases, "Mock YouTube Artist", nil
	}

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

func (m *Module) getAppleMusicDiscography(w http.ResponseWriter, r *http.Request, rawURL string) {
	parts := strings.Split(rawURL, "/")
	lastPart := parts[len(parts)-1]
	if idx := strings.Index(lastPart, "?"); idx != -1 {
		lastPart = lastPart[:idx]
	}

	itunesURL := fmt.Sprintf("https://itunes.apple.com/lookup?id=%s&entity=album&limit=100", url.QueryEscape(lastPart))
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, itunesURL, nil)
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		httputil.RespondError(w, http.StatusInternalServerError, err.Error())
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
		httputil.RespondError(w, http.StatusNotFound, "Дискография артиста Apple Music не найдена")
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
			cover := strings.Replace(item.ArtworkURL100, "100x100bb.jpg", "600x600bb.jpg", 1)
			relType := "album"
			if item.TrackCount <= 3 {
				relType = "single"
			}
			releases = append(releases, DiscographyRelease{
				ID:         item.CollectionID,
				Title:      item.CollectionName,
				Type:       relType,
				Year:       year,
				URL:        item.CollectionViewURL,
				CoverURL:   cover,
				TrackCount: item.TrackCount,
			})
		}
	}

	httputil.RespondJSON(w, http.StatusOK, DiscographyResponse{
		ArtistName:    artistName,
		ArtistID:      artistID,
		Source:        "apple",
		TotalReleases: len(releases),
		Releases:      releases,
	})
}
