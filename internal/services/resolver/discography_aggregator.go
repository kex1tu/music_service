package resolver

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"music-manager/internal/db"
	"music-manager/internal/id3"
)

// CleanReleaseTitle нормализует название релиза, отсекая суффиксы Deluxe, Remastered и спецсимволы.
func CleanReleaseTitle(title string) string {
	return id3.CleanReleaseTitle(title)
}

// IsDeluxeOrRemaster проверяет, содержит ли название маркеры расширенного издания или ремастера.
func IsDeluxeOrRemaster(title string) bool {
	return id3.IsDeluxeOrRemaster(title)
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
						relDate := alb.ReleaseDate
						if len(relDate) == 4 {
							relDate = relDate + "-01-01"
						} else if relDate == "" && year > 0 {
							relDate = fmt.Sprintf("%04d-01-01", year)
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
							ReleaseDate: relDate,
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

						rgDate := rg.Date
						if len(rgDate) == 4 {
							rgDate = rgDate + "-01-01"
						} else if rgDate == "" && rg.Year > 0 {
							rgDate = fmt.Sprintf("%04d-01-01", rg.Year)
						}

						collected = append(collected, AggregatedRelease{
							ArtistName:    formattedArtist,
							CleanArtist:   CleanArtistKey(artistName),
							Title:         rg.Title,
							CleanTitle:    CleanReleaseTitle(rg.Title),
							ReleaseType:   "album",
							ReleaseDate:   rgDate,
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
		cleanTitle  string
		rawTitle    string
		artist      string
		albumArtist string
		folderPath  string
		year        int
	}

	cleanArt := CleanArtistKey(artistName)
	cleanTitles := make([]string, 0, len(releases))
	titleSet := make(map[string]bool)
	for _, rel := range releases {
		if rel.CleanTitle != "" && !titleSet[rel.CleanTitle] {
			titleSet[rel.CleanTitle] = true
			cleanTitles = append(cleanTitles, rel.CleanTitle)
		}
	}

	// 1. Извлекаем локальные альбомы, связанные с артистом или названиями релизов
	var localAlbums []localEntry
	albumArgs := []any{cleanArt, "%" + artistName + "%"}
	albumPlaceholders := make([]string, len(cleanTitles))
	for i, ct := range cleanTitles {
		albumPlaceholders[i] = "?"
		albumArgs = append(albumArgs, ct)
	}

	inClause := "1=0"
	if len(albumPlaceholders) > 0 {
		inClause = fmt.Sprintf("a.clean_title IN (%s)", strings.Join(albumPlaceholders, ","))
	}

	albumQuery := fmt.Sprintf(`
	SELECT a.title, a.clean_title, COALESCE(NULLIF(a.album_artist, ''), ar.name), ar.name, a.folder_path, COALESCE(a.year, 0)
	FROM albums a
	JOIN artists ar ON ar.id = a.artist_id
	WHERE ar.clean_name = ? 
	   OR a.folder_path LIKE ?
	   OR %s;
	`, inClause)

	if rows, err := a.db.Conn().QueryContext(ctx, albumQuery, albumArgs...); err == nil {
		for rows.Next() {
			var t, ct, albArt, art, fp string
			var yr int
			if err := rows.Scan(&t, &ct, &albArt, &art, &fp, &yr); err == nil {
				localAlbums = append(localAlbums, localEntry{
					cleanTitle:  ct,
					rawTitle:    t,
					albumArtist: albArt,
					artist:      art,
					folderPath:  fp,
					year:        yr,
				})
			}
		}
		rows.Close()
	}

	// 2. Извлекаем локальные треки, связанные с артистом или названиями релизов (для синглов)
	var localTracks []localEntry
	trackArgs := []any{cleanArt, "%" + artistName + "%"}
	trackPlaceholders := make([]string, len(cleanTitles))
	for i, ct := range cleanTitles {
		trackPlaceholders[i] = "?"
		trackArgs = append(trackArgs, ct)
	}

	trackInClause := "1=0"
	if len(trackPlaceholders) > 0 {
		trackInClause = fmt.Sprintf("t.clean_title IN (%s)", strings.Join(trackPlaceholders, ","))
	}

	trackQuery := fmt.Sprintf(`
	SELECT t.title, t.clean_title, COALESCE(NULLIF(al.album_artist, ''), ar.name), ar.name, al.folder_path, COALESCE(al.year, 0)
	FROM tracks t
	JOIN artists ar ON ar.id = t.artist_id
	JOIN albums al ON al.id = t.album_id
	WHERE ar.clean_name = ? 
	   OR al.folder_path LIKE ?
	   OR %s;
	`, trackInClause)

	if rows, err := a.db.Conn().QueryContext(ctx, trackQuery, trackArgs...); err == nil {
		for rows.Next() {
			var t, ct, albArt, art, fp string
			var yr int
			if err := rows.Scan(&t, &ct, &albArt, &art, &fp, &yr); err == nil {
				localTracks = append(localTracks, localEntry{
					cleanTitle:  ct,
					rawTitle:    t,
					albumArtist: albArt,
					artist:      art,
					folderPath:  fp,
					year:        yr,
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
			artistMatches := matchesAnyArtist(relArtists, local.artist) ||
				matchesAnyArtist(relArtists, local.albumArtist) ||
				containsAnyToken(local.folderPath, relArtists) ||
				containsAnyToken(local.folderPath, []string{artistName})

			cleanLocal := CleanReleaseTitle(local.rawTitle)
			if cleanLocal == "" {
				cleanLocal = local.cleanTitle
			}
			cleanRel := CleanReleaseTitle(rel.Title)
			if cleanRel == "" {
				cleanRel = rel.CleanTitle
			}

			exactMatch := (local.cleanTitle == rel.CleanTitle) || (cleanLocal == cleanRel)
			substrMatch := (len(cleanLocal) >= 4 && strings.Contains(cleanRel, cleanLocal)) ||
				(len(cleanRel) >= 4 && strings.Contains(cleanLocal, cleanRel))

			if (exactMatch || substrMatch) && artistMatches {
				inLibrary = true
				break
			}
			if strings.Contains(rel.CleanTitle, local.cleanTitle) && IsDeluxeOrRemaster(rel.Title) && artistMatches {
				versionExists = true
			}
		}

		// 2) Сверка со списком локальных треков артиста (если в альбомах не найдено)
		if !inLibrary && !versionExists {
			for _, tr := range localTracks {
				artistMatches := matchesAnyArtist(relArtists, tr.artist) ||
					matchesAnyArtist(relArtists, tr.albumArtist) ||
					containsAnyToken(tr.folderPath, relArtists) ||
					containsAnyToken(tr.folderPath, []string{artistName})

				cleanTr := CleanReleaseTitle(tr.rawTitle)
				if cleanTr == "" {
					cleanTr = tr.cleanTitle
				}
				cleanRel := CleanReleaseTitle(rel.Title)
				if cleanRel == "" {
					cleanRel = rel.CleanTitle
				}

				exactMatch := (tr.cleanTitle == rel.CleanTitle) || (cleanTr == cleanRel)
				substrMatch := (len(cleanTr) >= 4 && strings.Contains(cleanRel, cleanTr)) ||
					(len(cleanRel) >= 4 && strings.Contains(cleanTr, cleanRel))

				if (exactMatch || substrMatch) && artistMatches {
					inLibrary = true
					break
				}
			}
		}

		// 3) Дополнительный кросс-поиск в БД по точному clean_title
		if !inLibrary && !versionExists {
			var albTitle, albArtist, albFp string
			if err := a.db.Conn().QueryRowContext(ctx, `
				SELECT a.title, COALESCE(NULLIF(a.album_artist, ''), ar.name), a.folder_path
				FROM albums a
				JOIN artists ar ON ar.id = a.artist_id
				WHERE a.clean_title = ? OR a.clean_title = ? LIMIT 1
			`, rel.CleanTitle, CleanReleaseTitle(rel.Title)).Scan(&albTitle, &albArtist, &albFp); err == nil {
				if matchesAnyArtist(relArtists, albArtist) || containsAnyToken(albFp, relArtists) || containsAnyToken(albFp, []string{artistName}) {
					inLibrary = true
				}
			}

			if !inLibrary {
				var trTitle, trArtist, trFp string
				if err := a.db.Conn().QueryRowContext(ctx, `
					SELECT t.title, COALESCE(NULLIF(al.album_artist, ''), ar.name), al.folder_path
					FROM tracks t
					JOIN artists ar ON ar.id = t.artist_id
					JOIN albums al ON al.id = t.album_id
					WHERE t.clean_title = ? OR t.clean_title = ? LIMIT 1
				`, rel.CleanTitle, CleanReleaseTitle(rel.Title)).Scan(&trTitle, &trArtist, &trFp); err == nil {
					if matchesAnyArtist(relArtists, trArtist) || containsAnyToken(trFp, relArtists) || containsAnyToken(trFp, []string{artistName}) {
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
			// Релиза нет в медиатеке. Проверяем свежесть:
			// В раздел «Новые релизы» попадают только релизы за последний разумный период:
			// Релизы, вышедшие за последние 365 дней или текущий/прошлый год.
			// Старый дискографический бэк-каталог помечается как 'missing' (не засоряя радар новинок).
			isRecent := false
			if rel.ReleaseDate != "" {
				if parsedDate, pErr := time.Parse("2006-01-02", rel.ReleaseDate); pErr == nil {
					if time.Since(parsedDate) <= 365*24*time.Hour && time.Since(parsedDate) >= -7*24*time.Hour {
						isRecent = true
					}
				}
			}
			if !isRecent && rel.Year > 0 {
				if currentYear-rel.Year <= 1 {
					isRecent = true
				}
			}

			if isRecent {
				rel.Status = "new"
				rel.IsRadar = true
			} else {
				rel.Status = "missing"
				rel.IsRadar = false
			}
		}
	}
}

func containsAnyToken(str string, tokens []string) bool {
	lower := strings.ToLower(str)
	for _, t := range tokens {
		tLower := strings.ToLower(CleanArtistKey(t))
		if tLower != "" && strings.Contains(lower, tLower) {
			return true
		}
	}
	return false
}
