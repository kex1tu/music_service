package resolver

import (
	"context"
	"fmt"
	"log"
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
	Tracklist     []db.CatalogTrack `json:"-"`
	ArtistName    string            `json:"artist_name"`
	CleanArtist   string            `json:"clean_artist"`
	Title         string            `json:"title"`
	CleanTitle    string            `json:"clean_title"`
	ReleaseType   string            `json:"release_type"` // album, ep, single, compilation
	ReleaseDate   string            `json:"release_date"` // YYYY-MM-DD
	Year          int               `json:"year"`
	CoverURL      string            `json:"cover_url"`
	TrackCount    int               `json:"track_count"`
	DeezerID      string            `json:"deezer_id,omitempty"`
	MusicBrainzID string            `json:"musicbrainz_id,omitempty"`
	ShazamID      string            `json:"shazam_id,omitempty"`
	SourceURL     string            `json:"source_url"`
	Status        string            `json:"status"` // new, in_library, version_exists, missing, downloaded, ignored
	IsRadar       bool              `json:"is_radar"`
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
							ReleaseType:   rg.ReleaseType,
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
	a.reconcileWithLibrary(ctx, artistName, deduped)

	return deduped, nil
}

// deduplicateReleases groups releases by catalog identity. Name-based matching
// is reserved for releases without an external catalog ID.
func (a *DiscographyAggregator) deduplicateReleases(raw []AggregatedRelease) []AggregatedRelease {
	var deduped []AggregatedRelease
	index := make(map[string]int)
	for _, item := range raw {
		if item.ReleaseType == "" {
			item.ReleaseType = "album"
		}
		key := db.RadarIdentityKey(item.ArtistName, item.Title, item.ReleaseType, item.DeezerID, item.MusicBrainzID, item.ShazamID)
		i, exists := index[key]
		if !exists {
			index[key] = len(deduped)
			deduped = append(deduped, item)
			continue
		}
		existing := &deduped[i]
		if len(existing.ArtistName) < len(item.ArtistName) {
			existing.ArtistName = item.ArtistName
		}
		if existing.MusicBrainzID == "" {
			existing.MusicBrainzID = item.MusicBrainzID
		}
		if existing.ShazamID == "" {
			existing.ShazamID = item.ShazamID
		}
		if existing.CoverURL == "" {
			existing.CoverURL = item.CoverURL
		}
		if existing.TrackCount == 0 {
			existing.TrackCount = item.TrackCount
		}
		if existing.ReleaseDate == "" {
			existing.ReleaseDate, existing.Year = item.ReleaseDate, item.Year
		}
	}
	return deduped
}

func (a *DiscographyAggregator) reconcileWithLibrary(ctx context.Context, _ string, releases []AggregatedRelease) {
	now := time.Now()
	for i := range releases {
		rel := &releases[i]
		info := db.RadarReleaseEntity{
			ArtistName: rel.ArtistName, Title: rel.Title, ReleaseType: rel.ReleaseType,
			Year: rel.Year, DeezerID: rel.DeezerID, TrackCount: rel.TrackCount, Tracklist: rel.Tracklist,
		}
		recent := db.RadarReleaseRecent(rel.ReleaseDate, rel.Year, now)
		rel.Status, rel.IsRadar = "missing", recent
		if recent {
			rel.Status = "new"
		}
		if a.db == nil {
			continue
		}

		if rel.DeezerID != "" && a.deezer != nil && len(info.Tracklist) == 0 {
			candidate, err := a.db.HasRadarAlbumCandidate(ctx, info)
			if err == nil && candidate {
				album, err := a.deezer.GetAlbum(ctx, rel.DeezerID)
				if err == nil {
					info.Tracklist, err = DeezerCatalogTracks(album)
				}
				if err != nil {
					log.Printf("[Radar] Could not verify tracklist for %q: %v", rel.Title, err)
					continue
				}
				rel.Tracklist = info.Tracklist
			}
		}
		matched, err := a.db.RadarReleaseInLibrary(ctx, info)
		if err != nil {
			log.Printf("[Radar] Could not reconcile %q: %v", rel.Title, err)
		} else if matched {
			rel.Status, rel.IsRadar = "in_library", false
		}
	}
}
