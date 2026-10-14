package resolver

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"music-manager/internal/db"
)

var deluxeRemasterRegex = regexp.MustCompile(`(?i)\s*[\(\[](?:deluxe|expanded|remaster(?:ed)?|special|bonus|anniversary|reissue|collectors?|edition|version)[^\)\]]*[\)\]]`)
var cleanupPunctRegex = regexp.MustCompile(`[^\p{L}\p{N}\s]`)

// CleanReleaseTitle нормализует название релиза, отсекая суффиксы Deluxe, Remastered и спецсимволы.
func CleanReleaseTitle(title string) string {
	cleaned := deluxeRemasterRegex.ReplaceAllString(title, "")
	cleaned = cleanupPunctRegex.ReplaceAllString(cleaned, " ")
	return strings.ToLower(strings.Join(strings.Fields(cleaned), " "))
}

// IsDeluxeOrRemaster проверяет, содержит ли название маркеры расширенного издания или ремастера.
func IsDeluxeOrRemaster(title string) bool {
	return deluxeRemasterRegex.MatchString(title)
}

// AggregatedRelease представляет объединенную информацию о релизе из нескольких каталогов.
type AggregatedRelease struct {
	ArtistName    string `json:"artist_name"`
	CleanArtist   string `json:"clean_artist"`
	Title         string `json:"title"`
	CleanTitle    string `json:"clean_title"`
	ReleaseType   string `json:"release_type"` // album, ep, single, compilation
	ReleaseDate   string `json:"release_date"` // YYYY-MM-DD
	Year          int    `json:"year"`
	CoverURL      string `json:"cover_url"`
	TrackCount    int    `json:"track_count"`
	DeezerID      string `json:"deezer_id,omitempty"`
	MusicBrainzID string `json:"musicbrainz_id,omitempty"`
	ShazamID      string `json:"shazam_id,omitempty"`
	SourceURL     string `json:"source_url"`
	Status        string `json:"status"` // new, in_library, version_exists, missing, downloaded, ignored
	IsRadar       bool   `json:"is_radar"`
}

// DiscographyAggregator выполняет кросс-каталожный сбор, склейку дубликатов и сверку с локальной БД.
type DiscographyAggregator struct {
	deezer *DeezerClient
	mb     *MusicBrainzClient
	itunes *ITunesClient
	db     *db.DB
}

// NewDiscographyAggregator создает новый экземпляр агрегатора дискографий.
func NewDiscographyAggregator(deezer *DeezerClient, mb *MusicBrainzClient, itunes *ITunesClient, database *db.DB) *DiscographyAggregator {
	return &DiscographyAggregator{
		deezer: deezer,
		mb:     mb,
		itunes: itunes,
		db:     database,
	}
}

// Aggregate опрашивает каталоги для артиста, склеивает дубликаты и определяет статусы относительно локальной базы.
func (a *DiscographyAggregator) Aggregate(ctx context.Context, artistName, alias string, knownIDs map[string]string) ([]AggregatedRelease, error) {
	artistName = strings.TrimSpace(artistName)
	if artistName == "" {
		return nil, fmt.Errorf("artist name cannot be empty")
	}

	searchName := artistName
	if strings.TrimSpace(alias) != "" {
		searchName = strings.TrimSpace(alias)
	}

	var mu sync.Mutex
	var collected []AggregatedRelease

	var wg sync.WaitGroup

	// 1. Сбор релизов из Deezer API
	if a.deezer != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			deezerID := ""
			if knownIDs != nil {
				deezerID = knownIDs["deezer"]
			}

			if deezerID == "" {
				art, err := a.deezer.SearchArtist(ctx, searchName)
				if err == nil && art != nil {
					deezerID = strconv.FormatInt(art.ID, 10)
				}
			}

			if deezerID != "" {
				albums, err := a.deezer.GetArtistAlbums(ctx, deezerID)
				if err == nil {
					mu.Lock()
					for _, alb := range albums {
						relType := strings.ToLower(alb.RecordType)
						if relType == "compile" {
							relType = "compilation"
						} else if relType == "" {
							relType = "album"
						}
						year := 0
						if len(alb.ReleaseDate) >= 4 {
							year, _ = strconv.Atoi(alb.ReleaseDate[:4])
						}

						cover := alb.CoverXL
						if cover == "" {
							cover = alb.CoverMedium
						}

						collected = append(collected, AggregatedRelease{
							ArtistName:  artistName,
							CleanArtist: CleanArtistKey(artistName),
							Title:       alb.Title,
							CleanTitle:  CleanReleaseTitle(alb.Title),
							ReleaseType: relType,
							ReleaseDate: alb.ReleaseDate,
							Year:        year,
							CoverURL:    cover,
							TrackCount:  alb.NbTracks,
							DeezerID:    strconv.FormatInt(alb.ID, 10),
							SourceURL:   fmt.Sprintf("https://www.deezer.com/album/%d", alb.ID),
						})
					}
					mu.Unlock()
				}
			}
		}()
	}

	// 2. Сбор релизов из MusicBrainz API
	if a.mb != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mbid := ""
			if knownIDs != nil {
				mbid = knownIDs["musicbrainz"]
			}

			if mbid != "" {
				rgs, err := a.mb.GetArtistReleaseGroups(ctx, mbid)
				if err == nil {
					mu.Lock()
					for _, rg := range rgs {
						collected = append(collected, AggregatedRelease{
							ArtistName:    artistName,
							CleanArtist:   CleanArtistKey(artistName),
							Title:         rg.Title,
							CleanTitle:    CleanReleaseTitle(rg.Title),
							ReleaseType:   "album",
							ReleaseDate:   rg.Date,
							Year:          rg.Year,
							MusicBrainzID: rg.ID,
							SourceURL:     fmt.Sprintf("https://musicbrainz.org/release-group/%s", rg.ID),
						})
					}
					mu.Unlock()
				}
			}
		}()
	}

	wg.Wait()

	// 3. Дедупликация и слияние
	deduped := a.deduplicateReleases(collected)

	// 4. Сверка с локальной библиотекой SQLite
	if a.db != nil {
		a.reconcileWithLibrary(ctx, artistName, deduped)
	}

	return deduped, nil
}

// deduplicateReleases склеивает дубликаты одного и того же релиза от разных провайдеров.
func (a *DiscographyAggregator) deduplicateReleases(raw []AggregatedRelease) []AggregatedRelease {
	var deduped []AggregatedRelease

	for _, item := range raw {
		merged := false
		for i := range deduped {
			existing := &deduped[i]

			// Проверка совпадения названия и близости года релиза (разница <= 1 год)
			sameTitle := existing.CleanTitle == item.CleanTitle
			sameYear := existing.Year == 0 || item.Year == 0 || math.Abs(float64(existing.Year-item.Year)) <= 1

			if sameTitle && sameYear {
				// Склеиваем идентификаторы и улучшаем метаданные
				if existing.DeezerID == "" && item.DeezerID != "" {
					existing.DeezerID = item.DeezerID
					existing.SourceURL = item.SourceURL
				}
				if existing.MusicBrainzID == "" && item.MusicBrainzID != "" {
					existing.MusicBrainzID = item.MusicBrainzID
				}
				if existing.CoverURL == "" && item.CoverURL != "" {
					existing.CoverURL = item.CoverURL
				}
				if existing.TrackCount <= 1 && item.TrackCount > 1 {
					existing.TrackCount = item.TrackCount
				}
				if existing.ReleaseDate == "" && item.ReleaseDate != "" {
					existing.ReleaseDate = item.ReleaseDate
					existing.Year = item.Year
				}
				merged = true
				break
			}
		}

		if !merged {
			deduped = append(deduped, item)
		}
	}

	return deduped
}

// reconcileWithLibrary проверяет наличие релизов на диске и классифицирует статусы.
func (a *DiscographyAggregator) reconcileWithLibrary(ctx context.Context, artistName string, releases []AggregatedRelease) {
	cleanArt := CleanArtistKey(artistName)

	// Выбираем все локальные альбомы этого артиста из SQLite
	query := `
	SELECT a.title, COALESCE(a.year, 0)
	FROM albums a
	WHERE LOWER(a.album_artist) = ? OR a.artist_id = (SELECT id FROM artists WHERE LOWER(name) = ?);
	`
	rows, err := a.db.Conn().QueryContext(ctx, query, cleanArt, cleanArt)
	if err != nil {
		return
	}
	defer rows.Close()

	type localAlbum struct {
		cleanTitle string
		rawTitle   string
		year       int
	}
	var localAlbums []localAlbum
	for rows.Next() {
		var title string
		var year int
		if err := rows.Scan(&title, &year); err == nil {
			localAlbums = append(localAlbums, localAlbum{
				cleanTitle: CleanReleaseTitle(title),
				rawTitle:   title,
				year:       year,
			})
		}
	}

	currentYear := time.Now().Year()

	for i := range releases {
		rel := &releases[i]

		// 1. Проверяем точное совпадение с локальным альбомом
		inLibrary := false
		versionExists := false

		for _, local := range localAlbums {
			if local.cleanTitle == rel.CleanTitle {
				inLibrary = true
				break
			}
			// Если базовая версия есть, а текущий релиз — Deluxe/Remaster
			if strings.Contains(rel.CleanTitle, local.cleanTitle) && IsDeluxeOrRemaster(rel.Title) {
				versionExists = true
			}
		}

		if inLibrary {
			rel.Status = "in_library"
			rel.IsRadar = false
		} else if versionExists {
			rel.Status = "version_exists"
			rel.IsRadar = false
		} else {
			// Релиза нет в медиатеке. Проверяем свежесть: релиз за последние 2 года попадает в Радар
			if rel.Year > 0 && currentYear-rel.Year <= 2 {
				rel.Status = "new"
				rel.IsRadar = true
			} else {
				rel.Status = "missing"
				rel.IsRadar = false
			}
		}
	}
}
