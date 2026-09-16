package resolver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"music-manager/internal/id3"
)

var allowedDomains = []string{
	"youtube.com", "youtu.be",
	"music.youtube.com",
	"soundcloud.com",
	"deezer.com", "deezer.page.link", "link.deezer.com",
	"spotify.com", "spotify.link",
	"music.yandex.ru", "music.yandex.com",
	"music.apple.com",
}

// ResolvedTrack defines the standardized track model across all sources.
type ResolvedTrack struct {
	TrackID        string  `json:"track_id"`
	TrackNumber    int     `json:"track_number"`
	TrackTotal     *int    `json:"track_total,omitempty"`
	DiscNumber     int     `json:"disc_number"`
	DiscTotal      *int    `json:"disc_total,omitempty"`
	Title          string  `json:"title"`
	Artist         string  `json:"artist"`
	Album          string  `json:"album"`
	AlbumArtist    string  `json:"album_artist"`
	Year           *int    `json:"year,omitempty"`
	Genre          *string `json:"genre,omitempty"`
	Duration       int     `json:"duration"`
	ISRC           *string `json:"isrc,omitempty"`
	CoverURL       *string `json:"cover_url,omitempty"`
	AudioSourceURL string  `json:"audio_source_url"`
	ConvertedPath  *string `json:"converted_path,omitempty"`
	Status         string  `json:"status"` // pending, downloading, converting, ready, error
	Error          *string `json:"error,omitempty"`
}

// ReleaseMetadata holds album-level metadata.
type ReleaseMetadata struct {
	AlbumTitle  string
	AlbumArtist string
	CoverURL    string
	Year        int
	Genre       string
	TotalTracks int
	DiscTotal   int
}

// SourceResolver coordinates URL detection and metadata extraction.
type SourceResolver struct {
	deezerClient *DeezerClient
	audioMatcher *AudioMatcher
	cookiesPath  string
}

// NewSourceResolver creates a new SourceResolver.
func NewSourceResolver(deezer *DeezerClient, matcher *AudioMatcher, cookiesPath string) *SourceResolver {
	if deezer == nil {
		deezer = NewDeezerClient()
	}
	if matcher == nil {
		matcher = NewAudioMatcher(cookiesPath)
	}
	return &SourceResolver{
		deezerClient: deezer,
		audioMatcher: matcher,
		cookiesPath:  cookiesPath,
	}
}

// Deezer returns the underlying DeezerClient.
func (r *SourceResolver) Deezer() *DeezerClient {
	return r.deezerClient
}

// AudioMatcher returns the underlying AudioMatcher.
func (r *SourceResolver) Matcher() *AudioMatcher {
	return r.audioMatcher
}

// DetectSource identifies the source platform from a URL or torrent string.
func (r *SourceResolver) DetectSource(rawURL string) (string, error) {
	cleanURL := strings.TrimSpace(rawURL)
	lower := strings.ToLower(cleanURL)

	if strings.HasPrefix(lower, "magnet:") || strings.HasSuffix(lower, ".torrent") || strings.Contains(lower, ".torrent?") {
		return "torrent", nil
	}

	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		cleanURL = "https://" + cleanURL
	}

	u, err := url.Parse(cleanURL)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}

	host := strings.ToLower(u.Hostname())
	allowed := false
	for _, domain := range allowedDomains {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			allowed = true
			break
		}
	}

	if !allowed {
		return "", fmt.Errorf("domain %q is not in the whitelist of allowed sources", host)
	}

	if strings.Contains(host, "deezer.com") || strings.Contains(host, "deezer.page.link") {
		return "deezer", nil
	}
	if strings.Contains(host, "soundcloud.com") {
		return "soundcloud", nil
	}
	if strings.Contains(host, "youtube.com") || strings.Contains(host, "youtu.be") {
		return "youtube", nil
	}
	if strings.Contains(host, "spotify.com") || strings.Contains(host, "spotify.link") {
		return "spotify", nil
	}
	if strings.Contains(host, "yandex") {
		return "yandex", nil
	}
	if strings.Contains(host, "apple.com") {
		return "applemusic", nil
	}

	return "", fmt.Errorf("could not determine source type for domain %q", host)
}

// Resolve extracts track metadata and audio streams from a source URL.
func (r *SourceResolver) Resolve(ctx context.Context, rawURL, explicitSource string) (*ReleaseMetadata, []ResolvedTrack, error) {
	source := explicitSource
	if source == "" || source == "auto" {
		detected, err := r.DetectSource(rawURL)
		if err != nil {
			return nil, nil, err
		}
		source = detected
	}

	switch source {
	case "deezer":
		return r.resolveDeezer(ctx, rawURL)
	case "youtube", "soundcloud":
		return r.resolveYtDlp(ctx, rawURL)
	case "torrent":
		return r.resolveTorrentOrDeezer(ctx, rawURL)
	default:
		return nil, nil, fmt.Errorf("source %q is not fully supported for direct download yet", source)
	}
}

func (r *SourceResolver) resolveDeezer(ctx context.Context, rawURL string) (*ReleaseMetadata, []ResolvedTrack, error) {
	resType, id, err := r.deezerClient.ParseURL(ctx, rawURL)
	if err != nil {
		return nil, nil, err
	}

	switch resType {
	case "album":
		album, err := r.deezerClient.GetAlbum(ctx, id)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get deezer album: %w", err)
		}

		genre := album.PrimaryGenre()
		year := album.ReleaseYear()
		coverURL := album.BestCoverURL()

		meta := &ReleaseMetadata{
			AlbumTitle:  album.Title,
			AlbumArtist: album.Artist.Name,
			CoverURL:    coverURL,
			Year:        year,
			Genre:       genre,
			TotalTracks: album.NbTracks,
		}

		var tracks []ResolvedTrack
		if album.Tracks != nil {
			total := len(album.Tracks.Data)
			for i, t := range album.Tracks.Data {
				pos := t.TrackPosition
				if pos == 0 {
					pos = i + 1
				}
				trackTot := total
				tArtist := t.Artist.Name
				if tArtist == "" {
					tArtist = album.Artist.Name
				}

				// Format artist with null-byte separation if contributors exist
				var artists []string
				if len(t.Contributors) > 0 {
					for _, c := range t.Contributors {
						if c.Name != "" {
							artists = append(artists, c.Name)
						}
					}
				}
				if len(artists) == 0 && tArtist != "" {
					artists = []string{tArtist}
				}
				formattedArtist := id3.FormatMultiValue(artists)

				discNum := t.DiskNumber
				if discNum <= 0 {
					discNum = 1
				}

				tr := ResolvedTrack{
					TrackID:     fmt.Sprintf("dz_%d", t.ID),
					TrackNumber: pos,
					TrackTotal:  &trackTot,
					DiscNumber:  discNum,
					Title:       id3.CleanTitle(t.Title, ""),
					Artist:      formattedArtist,
					Album:       album.Title,
					AlbumArtist: album.Artist.Name,
					Duration:    t.Duration,
					Status:      "pending",
				}
				if year > 0 {
					tr.Year = &year
				}
				if genre != "" {
					tr.Genre = &genre
				}
				if coverURL != "" {
					tr.CoverURL = &coverURL
				}
				if t.ISRC != "" {
					isrcCopy := t.ISRC
					tr.ISRC = &isrcCopy
				}

				tracks = append(tracks, tr)
			}
		}

		return meta, tracks, nil

	case "track":
		track, err := r.deezerClient.GetTrack(ctx, id)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get deezer track: %w", err)
		}

		albumTitle := ""
		coverURL := ""
		year := 0
		if track.Album != nil {
			albumTitle = track.Album.Title
			coverURL = track.Album.BestCoverURL()
			year = track.Album.ReleaseYear()
		}

		meta := &ReleaseMetadata{
			AlbumTitle:  albumTitle,
			AlbumArtist: track.Artist.Name,
			CoverURL:    coverURL,
			Year:        year,
			TotalTracks: 1,
		}

		tot := 1
		tr := ResolvedTrack{
			TrackID:     fmt.Sprintf("dz_%d", track.ID),
			TrackNumber: 1,
			TrackTotal:  &tot,
			DiscNumber:  1,
			Title:       id3.CleanTitle(track.Title, ""),
			Artist:      track.Artist.Name,
			Album:       albumTitle,
			AlbumArtist: track.Artist.Name,
			Duration:    track.Duration,
			CoverURL:    &coverURL,
			Status:      "pending",
		}
		if year > 0 {
			tr.Year = &year
		}
		if track.ISRC != "" {
			isrcCopy := track.ISRC
			tr.ISRC = &isrcCopy
		}

		return meta, []ResolvedTrack{tr}, nil

	default:
		return nil, nil, fmt.Errorf("unsupported deezer resource type: %s", resType)
	}
}

func (r *SourceResolver) resolveYtDlp(ctx context.Context, rawURL string) (*ReleaseMetadata, []ResolvedTrack, error) {
	args := []string{
		"--dump-json",
		"--flat-playlist",
		"--no-warnings",
		"--ignore-errors",
		rawURL,
	}
	if r.cookiesPath != "" {
		args = append(args, "--cookies", r.cookiesPath)
	}

	cmdCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "yt-dlp", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil && stdout.Len() == 0 {
		return nil, nil, fmt.Errorf("yt-dlp extraction failed: %w: %s", err, stderr.String())
	}

	type YtEntry struct {
		ID          string  `json:"id"`
		Title       string  `json:"title"`
		Uploader    string  `json:"uploader"`
		Channel     string  `json:"channel"`
		Duration    float64 `json:"duration"`
		WebpageURL  string  `json:"webpage_url"`
		Thumbnail   string  `json:"thumbnail"`
		TrackNumber any     `json:"track_number"`
		Album       string  `json:"album"`
		Artist      string  `json:"artist"`
		ReleaseYear int     `json:"release_year"`
		Genre       string  `json:"genre"`
	}

	var entries []YtEntry
	scanner := bufio.NewScanner(&stdout)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e YtEntry
		if err := json.Unmarshal(line, &e); err == nil && e.ID != "" {
			entries = append(entries, e)
		}
	}

	if len(entries) == 0 {
		return nil, nil, fmt.Errorf("no tracks found at URL %s", rawURL)
	}

	albumTitle := entries[0].Album
	if albumTitle == "" {
		albumTitle = entries[0].Title
	}
	artistName := entries[0].Artist
	if artistName == "" {
		artistName = entries[0].Channel
		if artistName == "" {
			artistName = entries[0].Uploader
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

		title := e.Title
		cleanArt := artistName
		if e.Artist != "" {
			cleanArt = e.Artist
		}

		trURL := e.WebpageURL
		if trURL == "" {
			trURL = fmt.Sprintf("https://www.youtube.com/watch?v=%s", e.ID)
		}

		tr := ResolvedTrack{
			TrackID:        fmt.Sprintf("yt_%s", e.ID),
			TrackNumber:    pos,
			TrackTotal:     &total,
			DiscNumber:     1,
			Title:          id3.CleanTitle(title, filepath.Base(title)),
			Artist:         cleanArt,
			Album:          albumTitle,
			AlbumArtist:    artistName,
			Duration:       int(e.Duration),
			AudioSourceURL: trURL,
			Status:         "pending",
		}
		if e.Thumbnail != "" {
			tCover := e.Thumbnail
			tr.CoverURL = &tCover
		}
		if year > 0 {
			tr.Year = &year
		}
		tracks = append(tracks, tr)
	}

	return meta, tracks, nil
}

var releaseTagRegex = regexp.MustCompile(`(?i)(\[[^\]]*\]|\([^\)]*(?:vinyl|flac|mp3|web|remaster|deluxe|edition|cd|lossless|24bit|16bit)[^\)]*\))`)

func cleanReleaseTitle(s string) string {
	s = releaseTagRegex.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.ReplaceAll(s, "+", " ")
	s = strings.TrimSpace(s)
	return s
}

func (r *SourceResolver) resolveTorrentOrDeezer(ctx context.Context, rawURL string) (*ReleaseMetadata, []ResolvedTrack, error) {
	// 1. If URL is Deezer, resolve directly
	if strings.Contains(strings.ToLower(rawURL), "deezer.com") {
		return r.resolveDeezer(ctx, rawURL)
	}

	// 2. If magnet link, extract dn (display name)
	lower := strings.ToLower(rawURL)
	if strings.HasPrefix(lower, "magnet:") {
		u, err := url.Parse(rawURL)
		if err == nil {
			dn := u.Query().Get("dn")
			if dn != "" {
				cleanQuery := cleanReleaseTitle(dn)
				if cleanQuery != "" && r.deezerClient != nil {
					albums, searchErr := r.deezerClient.SearchAlbums(ctx, cleanQuery)
					if searchErr == nil && len(albums) > 0 {
						dzURL := fmt.Sprintf("https://www.deezer.com/album/%d", albums[0].ID)
						return r.resolveDeezer(ctx, dzURL)
					}
				}
			}
		}
	}

	// 3. Fallback: if user passed query text or release title directly as rawURL
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") && !strings.HasPrefix(lower, "magnet:") {
		cleanQuery := cleanReleaseTitle(rawURL)
		if cleanQuery != "" && r.deezerClient != nil {
			albums, searchErr := r.deezerClient.SearchAlbums(ctx, cleanQuery)
			if searchErr == nil && len(albums) > 0 {
				dzURL := fmt.Sprintf("https://www.deezer.com/album/%d", albums[0].ID)
				return r.resolveDeezer(ctx, dzURL)
			}
		}
	}

	return nil, nil, fmt.Errorf("прямая загрузка BitTorrent (.torrent/magnet) временно недоступна; в каталоге Deezer релиз не найден")
}

