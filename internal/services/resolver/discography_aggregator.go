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
	"music-manager/internal/id3"
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

// ExtractAllReleaseArtists объединяет имя основного исполнителя, contributors из каталога и фиты из названия.
func ExtractAllReleaseArtists(primaryArtist string, contributors []DeezerArtist, title string) []string {
	var artists []string
	seen := make(map[string]bool)

	addArtist := func(name string) {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			return
		}
		for _, part := range id3.ParseMultiValue(trimmed) {
			p := strings.TrimSpace(part)
			if p == "" {
				continue
			}
			clean, feats := ExtractFeatFromText(p)
			for _, item := range append([]string{clean}, feats...) {
				item = strings.TrimSpace(item)
				if item == "" {
					continue
				}
				for _, sub := range strings.FieldsFunc(item, func(r rune) bool {
					return r == ',' || r == '&' || r == '/' || r == ';'
				}) {
					subTrim := strings.TrimSpace(sub)
					if subTrim != "" {
						lower := strings.ToLower(CleanQueryText(subTrim))
						if lower != "" && !seen[lower] {
							seen[lower] = true
							artists = append(artists, subTrim)
						}
					}
				}
			}
		}
	}

	if primaryArtist != "" {
		addArtist(primaryArtist)
	}
	for _, c := range contributors {
		addArtist(c.Name)
	}

	// Извлекаем соавторов из названия релиза (например, "Stick Out (feat. Big Baby Tape)")
	_, titleFeats := ExtractFeatFromText(title)
	for _, f := range titleFeats {
		addArtist(f)
	}

	if len(artists) == 0 && primaryArtist != "" {
		return []string{primaryArtist}
	}
	return artists
}

// extractArtistTokens разбивает строку исполнителей на отдельные имена (по запятым, амперсандам, \x00, \\, feat)
func extractArtistTokens(artistStr string) []string {
	var tokens []string
	seen := make(map[string]bool)

	addToken := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		for _, part := range id3.ParseMultiValue(s) {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			clean, feats := ExtractFeatFromText(part)
			candidates := append([]string{clean}, feats...)
			for _, item := range candidates {
				item = strings.TrimSpace(item)
				if item == "" {
					continue
				}
				for _, sub := range strings.FieldsFunc(item, func(r rune) bool {
					return r == ',' || r == '&' || r == '/' || r == ';'
				}) {
					subTrim := strings.TrimSpace(sub)
					if subTrim != "" {
						lower := strings.ToLower(CleanQueryText(subTrim))
						if lower != "" && !seen[lower] {
							seen[lower] = true
							tokens = append(tokens, subTrim)
						}
					}
				}
			}
		}
	}

	addToken(artistStr)
	return tokens
}

// matchesAnyArtist проверяет, пересекается ли хотя бы один артист из releaseArtists с localArtist
func matchesAnyArtist(releaseArtists []string, localArtist string) bool {
	if len(releaseArtists) == 0 {
		return false
	}
	localTokens := extractArtistTokens(localArtist)
	for _, ra := range releaseArtists {
		for _, la := range localTokens {
			if AreArtistsEquivalent(ra, la) {
				return true
			}
		}
	}
	return false
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
					// Обогащаем альбомы официальными соавторами из Deezer API
					albums = a.deezer.EnrichAlbumsContributors(ctx, albums)

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

						primaryArt := alb.Artist.Name
						if primaryArt == "" {
							primaryArt = artistName
						}
						allArtists := ExtractAllReleaseArtists(primaryArt, alb.Contributors, alb.Title)
						formattedArtist := strings.Join(allArtists, ", ")
						if formattedArtist == "" {
							formattedArtist = artistName
						}

						collected = append(collected, AggregatedRelease{
							ArtistName:  formattedArtist,
							CleanArtist: CleanArtistKey(primaryArt),
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
						allArtists := ExtractAllReleaseArtists(artistName, nil, rg.Title)
						formattedArtist := strings.Join(allArtists, ", ")
						if formattedArtist == "" {
							formattedArtist = artistName
						}

						collected = append(collected, AggregatedRelease{
							ArtistName:    formattedArtist,
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
				// Склеиваем соавторов: выбираем вариант с наибольшим числом соавторов
				if len(item.ArtistName) > len(existing.ArtistName) || (strings.Contains(item.ArtistName, ",") && !strings.Contains(existing.ArtistName, ",")) {
					existing.ArtistName = item.ArtistName
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

// reconcileWithLibrary проверяет наличие релизов на диске по соавторам и двум таблицам: albums и tracks.
func (a *DiscographyAggregator) reconcileWithLibrary(ctx context.Context, artistName string, releases []AggregatedRelease) {
	if a.db == nil || len(releases) == 0 {
		return
	}

	type localEntry struct {
		cleanTitle string
		rawTitle   string
		artist     string
		year       int
	}

	// 1. Извлекаем локальные альбомы, связанные с артистом
	cleanArt := CleanArtistKey(artistName)
	var localAlbums []localEntry

	albumQuery := `
	SELECT a.title, COALESCE(NULLIF(a.album_artist, ''), ar.name), COALESCE(a.year, 0)
	FROM albums a
	JOIN artists ar ON ar.id = a.artist_id
	WHERE LOWER(a.album_artist) LIKE ? OR LOWER(ar.name) LIKE ?;
	`
	pattern := "%" + cleanArt + "%"
	if rows, err := a.db.Conn().QueryContext(ctx, albumQuery, pattern, pattern); err == nil {
		for rows.Next() {
			var t, art string
			var yr int
			if err := rows.Scan(&t, &art, &yr); err == nil {
				localAlbums = append(localAlbums, localEntry{
					cleanTitle: CleanReleaseTitle(t),
					rawTitle:   t,
					artist:     art,
					year:       yr,
				})
			}
		}
		rows.Close()
	}

	// 2. Извлекаем локальные треки, связанные с артистом (для обнаружения синглов и треков в сборниках)
	var localTracks []localEntry
	trackQuery := `
	SELECT t.title, COALESCE(NULLIF(al.album_artist, ''), ar.name), COALESCE(al.year, 0)
	FROM tracks t
	JOIN artists ar ON ar.id = t.artist_id
	JOIN albums al ON al.id = t.album_id
	WHERE LOWER(al.album_artist) LIKE ? OR LOWER(ar.name) LIKE ?;
	`
	if rows, err := a.db.Conn().QueryContext(ctx, trackQuery, pattern, pattern); err == nil {
		for rows.Next() {
			var t, art string
			var yr int
			if err := rows.Scan(&t, &art, &yr); err == nil {
				localTracks = append(localTracks, localEntry{
					cleanTitle: CleanReleaseTitle(t),
					rawTitle:   t,
					artist:     art,
					year:       yr,
				})
			}
		}
		rows.Close()
	}

	currentYear := time.Now().Year()

	for i := range releases {
		rel := &releases[i]
		relArtists := extractArtistTokens(rel.ArtistName)
		if len(relArtists) == 0 {
			relArtists = []string{artistName}
		}

		inLibrary := false
		versionExists := false

		// 1) Сверка со списком локальных альбомов артиста
		for _, local := range localAlbums {
			if local.cleanTitle == rel.CleanTitle && matchesAnyArtist(relArtists, local.artist) {
				inLibrary = true
				break
			}
			if strings.Contains(rel.CleanTitle, local.cleanTitle) && IsDeluxeOrRemaster(rel.Title) && matchesAnyArtist(relArtists, local.artist) {
				versionExists = true
			}
		}

		// 2) Сверка со списком локальных треков артиста (если в альбомах не найдено)
		if !inLibrary && !versionExists {
			for _, tr := range localTracks {
				if tr.cleanTitle == rel.CleanTitle && matchesAnyArtist(relArtists, tr.artist) {
					inLibrary = true
					break
				}
			}
		}

		// 3) Дополнительный кросс-поиск в БД по точному clean_title (на случай если в локальной базе артист записан под другим соавтором)
		if !inLibrary && !versionExists {
			// Проверяем таблицу albums по точному clean_title
			var albTitle, albArtist string
			if err := a.db.Conn().QueryRowContext(ctx, `
				SELECT a.title, COALESCE(NULLIF(a.album_artist, ''), ar.name)
				FROM albums a
				JOIN artists ar ON ar.id = a.artist_id
				WHERE a.clean_title = ? LIMIT 1
			`, rel.CleanTitle).Scan(&albTitle, &albArtist); err == nil {
				if matchesAnyArtist(relArtists, albArtist) {
					inLibrary = true
				}
			}

			// Проверяем таблицу tracks по точному clean_title
			if !inLibrary {
				var trTitle, trArtist string
				if err := a.db.Conn().QueryRowContext(ctx, `
					SELECT t.title, COALESCE(NULLIF(al.album_artist, ''), ar.name)
					FROM tracks t
					JOIN artists ar ON ar.id = t.artist_id
					JOIN albums al ON al.id = t.album_id
					WHERE t.clean_title = ? LIMIT 1
				`, rel.CleanTitle).Scan(&trTitle, &trArtist); err == nil {
					if matchesAnyArtist(relArtists, trArtist) {
						inLibrary = true
					}
				}
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
