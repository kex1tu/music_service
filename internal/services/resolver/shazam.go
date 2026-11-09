package resolver

import (
	"context"
	"encoding/json"
	"fmt"

	"strconv"
	"strings"
	"time"

	"music-manager/internal/core/mediaexec"
)

// CommandExecutor определяет интерфейс для запуска внешних утилит (songrec).
type CommandExecutor func(ctx context.Context, name string, args ...string) ([]byte, error)

// DefaultCommandExecutor запускает команду через exec.CommandContext.
func DefaultCommandExecutor(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := mediaexec.CommandContext(ctx, name, args...)
	defer cmd.Close()
	return cmd.Output()
}

// ShazamProvider реализует гибридную интеграцию с Shazam (акустический фингерпринтинг + каталог Apple/Shazam).
type ShazamProvider struct {
	itunesClient *ITunesClient
	songrecBin   string
	execFn       CommandExecutor
}

// NewShazamProvider создает новый экземпляр ShazamProvider.
func NewShazamProvider(itunes *ITunesClient) *ShazamProvider {
	if itunes == nil {
		itunes = NewITunesClient(nil)
	}
	return &ShazamProvider{
		itunesClient: itunes,
		songrecBin:   "songrec",
		execFn:       DefaultCommandExecutor,
	}
}

// SetCommandExecutor переопределяет функцию выполнения команд (для unit-тестов).
func (p *ShazamProvider) SetCommandExecutor(fn CommandExecutor) {
	p.execFn = fn
}

// SetITunesClient переопределяет iTunes клиент (для unit-тестов).
func (p *ShazamProvider) SetITunesClient(client *ITunesClient) {
	p.itunesClient = client
}

// Search выполняет поиск треков и альбомов по каталогу Shazam / Apple Music.
func (p *ShazamProvider) Search(ctx context.Context, query string) ([]CatalogReleaseSummary, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}

	matches, err := p.itunesClient.SearchTracks(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("shazam search: %w", err)
	}

	summaries := make([]CatalogReleaseSummary, 0, len(matches))
	seen := make(map[string]bool)

	for i, m := range matches {
		cover := m.CoverURL
		if cover != "" {
			cover = strings.ReplaceAll(cover, "100x100bb.jpg", "1000x1000bb.jpg")
		}

		albumTitle := m.Album
		if albumTitle == "" {
			albumTitle = m.Title
		}

		key := fmt.Sprintf("%s:%s", strings.ToLower(m.Artist), strings.ToLower(albumTitle))
		if seen[key] && len(summaries) >= 15 {
			continue
		}
		seen[key] = true

		summaries = append(summaries, CatalogReleaseSummary{
			ID:         fmt.Sprintf("shazam-%d-%d", m.Year, i+1),
			Source:     "shazam",
			Title:      albumTitle,
			Artist:     m.Artist,
			CoverURL:   cover,
			Year:       m.Year,
			Date:       strconv.Itoa(m.Year),
			TrackCount: 1,
			Label:      m.Genre,
			Score:      90 - i,
		})

		if len(summaries) >= 15 {
			break
		}
	}

	return summaries, nil
}

type songrecRawResult struct {
	Matches []any `json:"matches"`
	Track   *struct {
		Title    string `json:"title"`
		Subtitle string `json:"subtitle"`
		Key      string `json:"key"`
		Genres   struct {
			Primary string `json:"primary"`
		} `json:"genres"`
		Images struct {
			CoverArt   string `json:"coverart"`
			CoverArtHQ string `json:"coverarthq"`
		} `json:"images"`
		Sections []struct {
			Type     string `json:"type"`
			Metadata []struct {
				Title string `json:"title"`
				Text  string `json:"text"`
			} `json:"metadata"`
		} `json:"sections"`
	} `json:"track"`
}

// RecognizeAudioFile выполняет акустическое распознавание реального аудиофайла через songrec.
func (p *ShazamProvider) RecognizeAudioFile(ctx context.Context, fullFilePath string) (*CatalogReleaseDetails, error) {
	if fullFilePath == "" {
		return nil, fmt.Errorf("empty audio file path")
	}

	recCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	out, err := p.execFn(recCtx, p.songrecBin, "audio-file-to-recognized-song", fullFilePath)
	if err != nil {
		return nil, fmt.Errorf("songrec execution failed: %w", err)
	}

	var raw songrecRawResult
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("songrec json unmarshal: %w", err)
	}

	if raw.Track == nil {
		return nil, fmt.Errorf("song not recognized by shazam")
	}

	title := raw.Track.Title
	artist := raw.Track.Subtitle
	genre := raw.Track.Genres.Primary

	coverURL := raw.Track.Images.CoverArtHQ
	if coverURL == "" {
		coverURL = raw.Track.Images.CoverArt
	}
	if coverURL != "" {
		coverURL = strings.ReplaceAll(coverURL, "/400x400cc.jpg", "/1000x1000cc.jpg")
		coverURL = strings.ReplaceAll(coverURL, "/800x800cc.jpg", "/1000x1000cc.jpg")
	}

	album := ""
	year := 0
	label := ""

	for _, sec := range raw.Track.Sections {
		for _, m := range sec.Metadata {
			switch strings.ToLower(m.Title) {
			case "album":
				album = m.Text
			case "released":
				if y, yErr := strconv.Atoi(strings.TrimSpace(m.Text)); yErr == nil {
					year = y
				}
			case "label":
				label = m.Text
			}
		}
	}

	if album == "" {
		album = title
	}

	trackDetail := CatalogTrackDetail{
		Position:     1,
		TrackNumber:  1,
		DiscNumber:   1,
		Title:        title,
		Artist:       artist,
		Artists:      []string{artist},
		Duration:     0,
		ReleaseTitle: album,
	}

	return &CatalogReleaseDetails{
		ID:         raw.Track.Key,
		Source:     "shazam",
		Title:      album,
		Artist:     artist,
		CoverURL:   coverURL,
		Year:       year,
		Date:       strconv.Itoa(year),
		Genre:      genre,
		Genres:     []string{genre},
		Label:      label,
		TrackCount: 1,
		Tracks:     []CatalogTrackDetail{trackDetail},
	}, nil
}
