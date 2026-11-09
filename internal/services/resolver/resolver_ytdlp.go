package resolver

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
	"music-manager/internal/id3"
)

// fetchYouTubeOEmbed запрашивает метаданные видео через открытый YouTube oEmbed API
// (работает публично, без авторизации и без возрастных ограничений).
func fetchYouTubeOEmbed(ctx context.Context, rawURL string) (*ParsedYouTubeTrack, string, error) {
	oembedURL := fmt.Sprintf("https://www.youtube.com/oembed?url=%s&format=json", url.QueryEscape(rawURL))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, oembedURL, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("youtube oembed status %d", resp.StatusCode)
	}

	var data struct {
		Title        string `json:"title"`
		AuthorName   string `json:"author_name"`
		ThumbnailURL string `json:"thumbnail_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, "", err
	}

	parsed := ParseYouTubeTitle(data.Title, data.AuthorName, data.AuthorName)
	return &parsed, data.ThumbnailURL, nil
}

// resolveYtDlp разрешает ссылки YouTube, YouTube Music и SoundCloud через yt-dlp.
func (r *SourceResolver) resolveYtDlp(ctx context.Context, rawURL string) (*ReleaseMetadata, []ResolvedTrack, error) {
	targetURL := rawURL

	// Нормализация ссылок на артиста в YouTube Music:
	// https://music.youtube.com/channel/UC... -> автоматически переходим в раздел /releases
	if strings.Contains(targetURL, "music.youtube.com/channel/") {
		parsedU, pErr := url.Parse(targetURL)
		if pErr == nil {
			path := strings.TrimRight(parsedU.Path, "/")
			if strings.Count(path, "/") == 2 { // /channel/UCxxxx
				parsedU.Path = path + "/releases"
				targetURL = parsedU.String()
			}
		}
	}

	useCookies := IsValidNetscapeCookiesFile(r.cookiesPath)
	args := []string{
		"--ignore-config",
		"--no-download-archive",
		"--dump-json",
		"--flat-playlist",
		"--no-warnings",
		"--ignore-errors",
		"--extractor-args", "youtube:player_client=tv_embedded,web_embedded,android,ios",
	}
	if useCookies {
		args = append(args, "--cookies", r.cookiesPath)
	}
	args = append(args, targetURL)

	// Увеличиваем таймаут для дискографий исполнителей (может быть много релизов)
	timeout := 60 * time.Second
	if strings.Contains(targetURL, "/channel/") || strings.Contains(targetURL, "/releases") {
		timeout = 3 * time.Minute
	}

	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := mediaexec.CommandContext(cmdCtx, "yt-dlp", args...)
	defer cmd.Close()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()

	type YtEntry struct {
		ID            string  `json:"id"`
		Title         string  `json:"title"`
		Uploader      string  `json:"uploader"`
		Channel       string  `json:"channel"`
		Duration      float64 `json:"duration"`
		WebpageURL    string  `json:"webpage_url"`
		URL           string  `json:"url"`
		Thumbnail     string  `json:"thumbnail"`
		TrackNumber   any     `json:"track_number"`
		PlaylistIndex any     `json:"playlist_index"`
		Album         string  `json:"album"`
		Artist        string  `json:"artist"`
		Track         string  `json:"track"`
		Playlist      string  `json:"playlist"`
		PlaylistTitle string  `json:"playlist_title"`
		ReleaseYear   int     `json:"release_year"`
		Genre         string  `json:"genre"`
	}

	var entries []YtEntry
	if stdout.Len() > 0 {
		scanner := bufio.NewScanner(&stdout)
		buf := make([]byte, 1024*1024)
		scanner.Buffer(buf, 10*1024*1024)

		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			var e YtEntry
			if err := json.Unmarshal(line, &e); err == nil && (e.ID != "" || e.URL != "") {
				entries = append(entries, e)
			}
		}
	}

	// Обработка возрастных ограничений (Age Gate) и сбоев прямого извлечения
	if (runErr != nil && len(entries) == 0) || len(entries) == 0 {
		stderrStr := stderr.String()

		// Fallback: если yt-dlp отклонил куки, повторяем попытку в гостевом режиме без флага --cookies
		if useCookies && strings.Contains(stderrStr, "does not look like a Netscape format cookies file") {
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
			if retryErr := cmdRetry.Run(); retryErr == nil && stdout.Len() > 0 {
				scannerRetry := bufio.NewScanner(&stdout)
				buf := make([]byte, 1024*1024)
				scannerRetry.Buffer(buf, 10*1024*1024)
				for scannerRetry.Scan() {
					line := scannerRetry.Bytes()
					if len(line) == 0 {
						continue
					}
					var e YtEntry
					if err := json.Unmarshal(line, &e); err == nil && (e.ID != "" || e.URL != "") {
						entries = append(entries, e)
					}
				}
				if len(entries) > 0 {
					runErr = nil
				}
			}
		}

		if runErr != nil || len(entries) == 0 {
			stderrStr = stderr.String()
			isAgeOrVideo := strings.Contains(stderrStr, "Sign in to confirm your age") ||
				strings.Contains(stderrStr, "inappropriate") ||
				strings.Contains(stderrStr, "age gate") ||
				strings.Contains(rawURL, "watch?v=") || strings.Contains(rawURL, "youtu.be/")

			if isAgeOrVideo {
				if parsed, thumb, oErr := fetchYouTubeOEmbed(ctx, rawURL); oErr == nil && parsed != nil && parsed.Title != "" {
					tot := 1
					artists := parsed.Artists
					if len(artists) == 0 && parsed.Artist != "" {
						artists = []string{parsed.Artist}
					}
					if r.featEnricher != nil {
						artists = r.featEnricher.EnrichTrackArtists(ctx, parsed.Title, artists)
					}
					fmtArt := parsed.Artist
					albArt := parsed.Artist
					if len(artists) > 0 {
						fmtArt = id3.FormatMultiValue(artists)
						albArt = artists[0]
					}
					meta := &ReleaseMetadata{
						AlbumTitle:  parsed.Title,
						AlbumArtist: fmtArt,
						CoverURL:    thumb,
						TotalTracks: 1,
					}
					tr := ResolvedTrack{
						TrackID:        "yt_oembed",
						TrackNumber:    1,
						TrackTotal:     &tot,
						DiscNumber:     1,
						Title:          parsed.Title,
						Artist:         fmtArt,
						Album:          parsed.Title,
						AlbumArtist:    albArt,
						AudioSourceURL: "", // пустое поле triggers AudioMatcher fallback to SoundCloud
						Status:         "pending",
					}
					if thumb != "" {
						tr.CoverURL = &thumb
					}
					return meta, []ResolvedTrack{tr}, nil
				}
			}

			if len(entries) == 0 && runErr == nil {
				return nil, nil, fmt.Errorf("не найдено треков по ссылке %s", rawURL)
			}
			cleanStderr := CleanYtDlpStderr(stderr.String())
			return nil, nil, fmt.Errorf("yt-dlp extraction failed: %w: %s", runErr, cleanStderr)
		}
	}

	isSoundCloud := strings.Contains(rawURL, "soundcloud.com")
	if !isSoundCloud && len(entries) > 0 {
		isSoundCloud = strings.Contains(entries[0].WebpageURL, "soundcloud.com") || strings.Contains(entries[0].URL, "soundcloud.com")
	}

	// Извлекаем артиста и альбом первого элемента для релиза
	var firstArtists []string
	var artistName string
	var firstTitle string

	if isSoundCloud {
		scArt, scTit := ParseSoundCloudTitle(entries[0].Title, entries[0].Uploader)
		if scArt != "" && scArt != "Unknown Artist" {
			artistName = scArt
		} else if entries[0].Artist != "" && !strings.EqualFold(entries[0].Artist, entries[0].Uploader) {
			artistName = entries[0].Artist
		} else {
			artistName = CleanChannelName(entries[0].Uploader)
		}
		firstTitle = scTit
		cleanArt, feats := ExtractFeatFromText(artistName)
		firstArtists = []string{cleanArt}
		firstArtists = append(firstArtists, feats...)
	} else {
		firstParsed := ParseYouTubeTitle(entries[0].Title, entries[0].Channel, entries[0].Uploader)
		firstArtists = firstParsed.Artists
		firstTitle = firstParsed.Title
		artistName = entries[0].Artist
		if artistName == "" || (entries[0].Uploader != "" && strings.EqualFold(artistName, entries[0].Uploader) && firstParsed.Artist != "" && firstParsed.Artist != "Unknown Artist") {
			artistName = firstParsed.Artist
		}
		if len(firstArtists) == 0 && artistName != "" {
			firstArtists = []string{artistName}
		}
	}

	genre := entries[0].Genre
	year := entries[0].ReleaseYear

	if r.featEnricher != nil {
		enriched := r.featEnricher.EnrichTrackDetails(ctx, firstTitle, firstArtists)
		firstArtists = enriched.Artists
		if genre == "" && enriched.Genre != "" {
			genre = enriched.Genre
		}
		if year == 0 && enriched.Year > 0 {
			year = enriched.Year
		}
	}
	if len(firstArtists) > 0 {
		artistName = id3.FormatMultiValue(firstArtists)
	}

	isArtistPage := strings.Contains(rawURL, "/channel/") || strings.Contains(rawURL, "/releases")
	albumTitle := entries[0].Album
	if isSoundCloud {
		if entries[0].PlaylistTitle != "" {
			albumTitle = strings.TrimSpace(entries[0].PlaylistTitle)
		} else if entries[0].Playlist != "" {
			albumTitle = strings.TrimSpace(entries[0].Playlist)
		}
	}
	if albumTitle == "" || isArtistPage || (len(entries) == 1 && (strings.EqualFold(albumTitle, "single") || strings.EqualFold(albumTitle, "singles"))) {
		if isArtistPage && artistName != "" {
			albumTitle = artistName
		} else if isSoundCloud && (entries[0].PlaylistTitle != "" || entries[0].Playlist != "") {
			if entries[0].PlaylistTitle != "" {
				albumTitle = strings.TrimSpace(entries[0].PlaylistTitle)
			} else {
				albumTitle = strings.TrimSpace(entries[0].Playlist)
			}
		} else if firstTitle != "" {
			albumTitle = firstTitle
		} else {
			albumTitle = "Unknown Album"
		}
	}

	coverURL := entries[0].Thumbnail

	meta := &ReleaseMetadata{
		AlbumTitle:  albumTitle,
		AlbumArtist: artistName,
		CoverURL:    coverURL,
		Year:        year,
		Genre:       genre,
		TotalTracks: len(entries),
	}

	var tracks []ResolvedTrack
	total := len(entries)
	for i, e := range entries {
		pos := i + 1
		if tn, ok := e.TrackNumber.(float64); ok && tn > 0 {
			pos = int(tn)
		} else if tnStr, ok := e.TrackNumber.(string); ok {
			if parsed, err := strconv.Atoi(tnStr); err == nil && parsed > 0 {
				pos = parsed
			}
		} else if pi, ok := e.PlaylistIndex.(float64); ok && pi > 0 {
			pos = int(pi)
		} else if piStr, ok := e.PlaylistIndex.(string); ok {
			if parsed, err := strconv.Atoi(piStr); err == nil && parsed > 0 {
				pos = parsed
			}
		}

		var cleanTit, cleanArt string
		var artists []string

		if isSoundCloud {
			scArt, scTit := ParseSoundCloudTitle(e.Title, e.Uploader)
			if scTit != "" {
				cleanTit = scTit
			} else {
				cleanTit = e.Title
			}
			if scArt != "" && scArt != "Unknown Artist" {
				cleanArt = scArt
			} else if e.Artist != "" && !strings.EqualFold(e.Artist, e.Uploader) {
				cleanArt = e.Artist
			} else {
				cleanArt = CleanChannelName(e.Uploader)
			}
			artClean, feats := ExtractFeatFromText(cleanArt)
			artists = []string{artClean}
			artists = append(artists, feats...)
		} else {
			parsed := ParseYouTubeTitle(e.Title, e.Channel, e.Uploader)
			cleanTit = parsed.Title
			cleanArt = parsed.Artist
			if e.Artist != "" && (e.Uploader == "" || !strings.EqualFold(e.Artist, e.Uploader) || parsed.Artist == "Unknown Artist") {
				cleanArt = e.Artist
			}
			artists = parsed.Artists
			if len(artists) == 0 && cleanArt != "" {
				artists = []string{cleanArt}
			}
		}

		if r.featEnricher != nil {
			artists = r.featEnricher.EnrichTrackArtists(ctx, cleanTit, artists)
		}
		if len(artists) > 0 {
			cleanArt = id3.FormatMultiValue(artists)
		}

		trURL := e.WebpageURL
		if trURL == "" && e.URL != "" {
			trURL = e.URL
		}
		if trURL == "" && e.ID != "" {
			trURL = fmt.Sprintf("https://music.youtube.com/watch?v=%s", e.ID)
		}

		trAlbum := strings.TrimSpace(e.Album)
		if isSoundCloud {
			if e.PlaylistTitle != "" {
				trAlbum = strings.TrimSpace(e.PlaylistTitle)
			} else if e.Playlist != "" {
				trAlbum = strings.TrimSpace(e.Playlist)
			}
		}
		if trAlbum == "" {
			if !isArtistPage {
				trAlbum = albumTitle
			} else {
				trAlbum = cleanTit
			}
		}

		trID := e.ID
		if trID == "" {
			trID = fmt.Sprintf("yt_%d", i+1)
		}

		albArt := artistName
		if len(artists) > 0 {
			albArt = artists[0]
		}
		if total == 1 {
			albArt = cleanArt
		}

		tr := ResolvedTrack{
			TrackID:        fmt.Sprintf("yt_%s", trID),
			TrackNumber:    pos,
			TrackTotal:     &total,
			DiscNumber:     1,
			Title:          cleanTit,
			Artist:         cleanArt,
			Album:          trAlbum,
			AlbumArtist:    albArt,
			Duration:       int(e.Duration),
			AudioSourceURL: trURL,
			Status:         "pending",
		}
		if e.Thumbnail != "" {
			tCover := e.Thumbnail
			tr.CoverURL = &tCover
		}
		if e.ReleaseYear > 0 {
			y := e.ReleaseYear
			tr.Year = &y
		} else if year > 0 {
			tr.Year = &year
		}
		if genre != "" {
			g := genre
			tr.Genre = &g
		}
		tracks = append(tracks, tr)
	}

	return meta, tracks, nil
}
