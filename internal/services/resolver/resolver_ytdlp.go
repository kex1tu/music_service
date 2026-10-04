package resolver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"

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

	cmd := exec.CommandContext(cmdCtx, "yt-dlp", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()

	type YtEntry struct {
		ID          string  `json:"id"`
		Title       string  `json:"title"`
		Uploader    string  `json:"uploader"`
		Channel     string  `json:"channel"`
		Duration    float64 `json:"duration"`
		WebpageURL  string  `json:"webpage_url"`
		URL         string  `json:"url"`
		Thumbnail   string  `json:"thumbnail"`
		TrackNumber any     `json:"track_number"`
		Album       string  `json:"album"`
		Artist      string  `json:"artist"`
		ReleaseYear int     `json:"release_year"`
		Genre       string  `json:"genre"`
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
			cmdRetry := exec.CommandContext(cmdCtx, "yt-dlp", argsNoCookies...)
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

	// Извлекаем артиста и альбом первого элемента для релиза
	firstParsed := ParseYouTubeTitle(entries[0].Title, entries[0].Channel, entries[0].Uploader)
	firstArtists := firstParsed.Artists
	artistName := entries[0].Artist
	if artistName == "" {
		artistName = firstParsed.Artist
	}
	if len(firstArtists) == 0 && artistName != "" {
		firstArtists = []string{artistName}
	}
	if r.featEnricher != nil {
		firstArtists = r.featEnricher.EnrichTrackArtists(ctx, firstParsed.Title, firstArtists)
	}
	if len(firstArtists) > 0 {
		artistName = id3.FormatMultiValue(firstArtists)
	}

	isArtistPage := strings.Contains(rawURL, "/channel/") || strings.Contains(rawURL, "/releases")
	albumTitle := entries[0].Album
	if albumTitle == "" || isArtistPage {
		if isArtistPage && artistName != "" {
			albumTitle = artistName
		} else if firstParsed.Title != "" {
			albumTitle = firstParsed.Title
		} else {
			albumTitle = "Unknown Album"
		}
	}

	coverURL := entries[0].Thumbnail
	year := entries[0].ReleaseYear

	meta := &ReleaseMetadata{
		AlbumTitle:  albumTitle,
		AlbumArtist: artistName,
		CoverURL:    coverURL,
		Year:        year,
		Genre:       entries[0].Genre,
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
		}

		parsed := ParseYouTubeTitle(e.Title, e.Channel, e.Uploader)
		cleanTit := parsed.Title
		cleanArt := parsed.Artist
		if e.Artist != "" {
			cleanArt = e.Artist
		}

		artists := parsed.Artists
		if len(artists) == 0 && cleanArt != "" {
			artists = []string{cleanArt}
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
		tracks = append(tracks, tr)
	}

	return meta, tracks, nil
}

