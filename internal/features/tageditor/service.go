package tageditor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"music-manager/internal/core/mediaexec"
	"music-manager/internal/core/safefs"
	"music-manager/internal/core/sandbox"
	"music-manager/internal/id3"
	"music-manager/internal/services/resolver"
)

var (
	// ErrFileNotFound возвращается, если файл не найден в медиатеке.
	ErrFileNotFound = errors.New("file not found in library")
	// ErrDirectoryNotFound возвращается, если директория не найдена в медиатеке.
	ErrDirectoryNotFound = errors.New("directory not found in library")
	// ErrInvalidPath возвращается, если передан пустой или некорректный путь.
	ErrInvalidPath = errors.New("invalid file or directory path")
	// ErrSandboxViolation возвращается при попытке выхода за пределы песочницы.
	ErrSandboxViolation = errors.New("path traversal attempt outside sandbox root")
	// ErrCoverNotFound возвращается, если обложка не найдена.
	ErrCoverNotFound = errors.New("cover art not found")
	// ErrEmbeddedLyricsProhibited фиксирует запрет на вшивание текстов в MP3.
	ErrEmbeddedLyricsProhibited = errors.New("embedded lyrics in MP3 are prohibited by project invariant")
	// ErrClientNotInitialized возвращается, если требуемый клиент не настроен.
	ErrClientNotInitialized = errors.New("requested external catalog provider is not initialized")
)

// ServiceConfig определяет конфигурацию и зависимости сервисного слоя TagEditor.
type ServiceConfig struct {
	MusicSandbox    *sandbox.PathSandbox
	TmpSandbox      *sandbox.PathSandbox
	Repo            TagRepository
	Organizer       PathOrganizer
	Deezer          DeezerCatalog
	MusicBrainz     MusicBrainzCatalog
	Shazam          ShazamCatalog
	AcoustID        AcoustIDProvider
	Lyrics          LyricsService
	Enricher        ArtistEnricher
	TokenStore      TokenPersistence
	HTTPClient      *http.Client
	MusicBrainzConf MusicBrainzConfig
	RedirectURI     string
}

// Service реализует всю доменную логику управления тегами, обложками, каталогами и переименованиями.
type Service struct {
	musicSandbox *sandbox.PathSandbox
	tmpSandbox   *sandbox.PathSandbox
	repo         TagRepository
	organizer    PathOrganizer
	deezer       DeezerCatalog
	musicbrainz  MusicBrainzCatalog
	shazam       ShazamCatalog
	acoustid     AcoustIDProvider
	lyrics       LyricsService
	enricher     ArtistEnricher
	tokenStore   TokenPersistence
	httpClient   *http.Client
	mbConfig     MusicBrainzConfig

	writeMu  sync.Mutex
	dirLocks map[string]*sync.Mutex
}

// NewService создает полностью инициализированный и иммутабельный экземпляр Service.
func NewService(cfg ServiceConfig) *Service {
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}

	mbConf := cfg.MusicBrainzConf
	if mbConf.RedirectURI == "" && cfg.RedirectURI != "" {
		mbConf.RedirectURI = cfg.RedirectURI
	}

	tokStore := cfg.TokenStore
	if tokStore == nil && cfg.Repo != nil {
		if ts, ok := cfg.Repo.(TokenPersistence); ok {
			tokStore = ts
		}
	}

	return &Service{
		musicSandbox: cfg.MusicSandbox,
		tmpSandbox:   cfg.TmpSandbox,
		repo:         cfg.Repo,
		organizer:    cfg.Organizer,
		deezer:       cfg.Deezer,
		musicbrainz:  cfg.MusicBrainz,
		shazam:       cfg.Shazam,
		acoustid:     cfg.AcoustID,
		lyrics:       cfg.Lyrics,
		enricher:     cfg.Enricher,
		tokenStore:   tokStore,
		httpClient:   client,
		mbConfig:     mbConf,
		dirLocks:     make(map[string]*sync.Mutex),
	}
}

// getDirLock возвращает мьютекс для синхронизации записи в конкретную папку.
func (s *Service) getDirLock(dir string) *sync.Mutex {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	mu, exists := s.dirLocks[dir]
	if !exists {
		mu = &sync.Mutex{}
		s.dirLocks[dir] = mu
	}
	return mu
}

// ResolvePath безопасно разрешает путь сначала в tmpSandbox (если файл там есть), затем в musicSandbox.
func (s *Service) ResolvePath(relPath string) (string, bool, error) {
	trimmed := strings.TrimSpace(relPath)
	if trimmed == "" {
		return "", false, ErrInvalidPath
	}

	cleanRel := strings.TrimPrefix(trimmed, "tmp_downloads/")
	cleanRel = strings.TrimPrefix(cleanRel, "tmp/")

	// 1. Попытка разрешить во временной песочнице
	if s.tmpSandbox != nil {
		if full, err := s.tmpSandbox.Resolve(cleanRel, true); err == nil {
			if st, statErr := safefs.Stat(full); statErr == nil && !st.IsDir() {
				return full, true, nil
			}
		}
	}

	// 2. Попытка разрешить в основной песочнице библиотеки
	if s.musicSandbox == nil {
		return "", false, ErrSandboxViolation
	}
	full, err := s.musicSandbox.Resolve(trimmed, true)
	if err != nil {
		return "", false, fmt.Errorf("%w: %v", ErrSandboxViolation, err)
	}
	return full, false, nil
}

// GetTags считывает теги для одного аудиофайла либо собирает сводку по всей директории (альбому).
func (s *Service) GetTags(ctx context.Context, relPath string, asAlbum ...bool) (*GetTagsResponse, error) {
	fullPath, isTmp, err := s.ResolvePath(relPath)
	if err != nil {
		return nil, err
	}

	st, err := safefs.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrFileNotFound
		}
		return nil, err
	}

	forceAlbum := len(asAlbum) > 0 && asAlbum[0]

	// Режим директории: сбор треков и метаданных релиза
	if st.IsDir() || forceAlbum {
		return s.readDirectoryTags(ctx, fullPath, relPath, isTmp)
	}

	// Режим одиночного файла
	return s.readSingleFileTags(ctx, fullPath, relPath)
}

func (s *Service) readSingleFileTags(ctx context.Context, fullPath, relPath string) (*GetTagsResponse, error) {
	tags, err := id3.ReadTags(fullPath, relPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read id3 tags: %w", err)
	}

	var year *int
	if tags.Year > 0 {
		y := tags.Year
		year = &y
	}

	var coverPath *string
	trackBase := strings.TrimSuffix(fullPath, filepath.Ext(fullPath))
	trackRelBase := strings.TrimSuffix(relPath, filepath.Ext(relPath))

	for _, candExt := range []string{".jpg", ".png", ".jpeg", ".webp"} {
		if _, err := safefs.Stat(trackBase + candExt); err == nil {
			cRel := trackRelBase + candExt
			coverPath = &cRel
			break
		}
	}
	if coverPath == nil {
		dir := filepath.Dir(fullPath)
		for _, cand := range []string{"cover.jpg", "cover.png", "folder.jpg", "folder.png", "cover.jpeg"} {
			if _, err := safefs.Stat(filepath.Join(dir, cand)); err == nil {
				cRel := filepath.Join(filepath.Dir(relPath), cand)
				coverPath = &cRel
				break
			}
		}
	}
	hasCover := tags.HasCover || coverPath != nil

	albumTitle := tags.Album
	if albumTitle == "" {
		albumTitle = tags.Title
	}
	albumArtist := tags.AlbumArtist
	if albumArtist == "" {
		albumArtist = tags.Artist
	}

	return &GetTagsResponse{
		Album: AlbumSummaryDTO{
			AlbumArtist: albumArtist,
			Album:       albumTitle,
			Year:        year,
			Genre:       tags.Genre,
			DiscTotal:   tags.DiscTotal,
			TrackTotal:  tags.TrackTotal,
		},
		Tracks:       []TrackTags{*tags},
		SourceDir:    filepath.Dir(relPath),
		CoverPath:    coverPath,
		HasCover:     hasCover,
		Title:        tags.Title,
		Artist:       tags.Artist,
		AlbumArtist:  tags.AlbumArtist,
		Path:         tags.Path,
		TrackNumber:  tags.TrackNumber,
		TrackTotal:   tags.TrackTotal,
		DiscNumber:   tags.DiscNumber,
		DiscTotal:    tags.DiscTotal,
		Year:         tags.Year,
		Genre:        tags.Genre,
		Duration:     tags.Duration,
		Bitrate:      tags.Bitrate,
		LyricsStatus: tags.LyricsStatus,
	}, nil
}

func (s *Service) readDirectoryTags(ctx context.Context, fullPath, relPath string, isTmp bool) (*GetTagsResponse, error) {
	var tracks []TrackTags
	var coverPath *string
	hasCover := false

	// Проверка наличия файла обложки в каталоге
	for _, cand := range []string{"cover.jpg", "cover.png", "folder.jpg", "folder.png", "cover.jpeg"} {
		imgPath := filepath.Join(fullPath, cand)
		if _, err := safefs.Stat(imgPath); err == nil {
			cRel := filepath.Join(relPath, cand)
			coverPath = &cRel
			hasCover = true
			break
		}
	}

	err := safefs.WalkDir(fullPath, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d == nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".") || name == "tmp" || name == "tmp_downloads" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(path), ".mp3") {
			return nil
		}

		var fileRel string
		if isTmp && s.tmpSandbox != nil {
			if r, err := filepath.Rel(s.tmpSandbox.Root(), path); err == nil {
				fileRel = "tmp_downloads/" + filepath.ToSlash(r)
			}
		} else if s.musicSandbox != nil {
			fileRel, _ = s.musicSandbox.ToRelative(path)
		}
		if fileRel == "" {
			fileRel = path
		}

		t, err := id3.ReadTags(path, fileRel)
		if err == nil && t != nil {
			if t.HasCover {
				hasCover = true
			}
			tracks = append(tracks, *t)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to walk directory tracks: %w", err)
	}

	// Сортировка треков: DiscNumber -> TrackNumber -> Path
	sort.SliceStable(tracks, func(i, j int) bool {
		if tracks[i].DiscNumber != tracks[j].DiscNumber {
			return tracks[i].DiscNumber < tracks[j].DiscNumber
		}
		if tracks[i].TrackNumber != tracks[j].TrackNumber {
			return tracks[i].TrackNumber < tracks[j].TrackNumber
		}
		return tracks[i].Path < tracks[j].Path
	})

	albumArtist := ""
	albumTitle := filepath.Base(relPath)
	if albumTitle == "." || albumTitle == "/" {
		albumTitle = ""
	}
	var year *int
	genre := ""
	discTotal := 1
	trackTotal := len(tracks)

	for _, t := range tracks {
		if albumTitle == "" || albumTitle == filepath.Base(relPath) {
			if t.Album != "" {
				albumTitle = t.Album
			}
		}
		if albumArtist == "" {
			if t.AlbumArtist != "" {
				albumArtist = t.AlbumArtist
			} else if t.Artist != "" {
				albumArtist = t.Artist
			}
		}
		if year == nil && t.Year > 0 {
			y := t.Year
			year = &y
		}
		if genre == "" && t.Genre != "" {
			genre = t.Genre
		}
		if t.DiscNumber > discTotal {
			discTotal = t.DiscNumber
		}
		if t.DiscTotal > discTotal {
			discTotal = t.DiscTotal
		}
		if t.TrackTotal > trackTotal {
			trackTotal = t.TrackTotal
		}
	}

	return &GetTagsResponse{
		Album: AlbumSummaryDTO{
			AlbumArtist: albumArtist,
			Album:       albumTitle,
			Year:        year,
			Genre:       genre,
			DiscTotal:   discTotal,
			TrackTotal:  trackTotal,
		},
		Tracks:    tracks,
		SourceDir: relPath,
		CoverPath: coverPath,
		HasCover:  hasCover,
	}, nil
}

// SaveTags записывает теги в один файл с соблюдением стандарта ID3v2.4 и null-byte разделителей.
func (s *Service) SaveTags(ctx context.Context, tags TrackTags) error {
	fullPath, _, err := s.ResolvePath(tags.Path)
	if err != nil {
		return err
	}

	dirLock := s.getDirLock(filepath.Dir(fullPath))
	dirLock.Lock()
	defer dirLock.Unlock()

	// 1. Запись тегов в MP3 файл (id3.WriteTags автоматически исключает USLT/SYLT)
	if err := id3.WriteTags(fullPath, tags, nil); err != nil {
		return fmt.Errorf("failed to write tags: %w", err)
	}

	// 2. Синхронизация с базой данных
	if s.repo != nil {
		_ = s.repo.UpdateTrackTags(ctx, tags.Path, tags)
	}

	return nil
}

// BatchSaveTags выполняет согласованное сохранение метаданных и раскладку дорожек альбома.
func (s *Service) BatchSaveTags(ctx context.Context, req BatchSaveRequest) (*BatchSaveResponse, error) {
	if s.organizer == nil {
		return nil, errors.New("organizer service not configured")
	}

	// Нормализация одиночного трека (сингла): подтягивание полей альбома из трека
	if len(req.Tracks) == 1 {
		t := &req.Tracks[0]
		if strings.TrimSpace(req.Album.AlbumArtist) == "" && strings.TrimSpace(t.Artist) != "" {
			req.Album.AlbumArtist = strings.TrimSpace(t.Artist)
		}
		if strings.TrimSpace(req.Album.Album) == "" || strings.EqualFold(req.Album.Album, "single") || strings.EqualFold(req.Album.Album, "singles") {
			if strings.TrimSpace(t.Title) != "" {
				req.Album.Album = strings.TrimSpace(t.Title)
			}
		}
		if (req.Album.Year == nil || *req.Album.Year == 0) && t.Year > 0 {
			yr := t.Year
			req.Album.Year = &yr
		}
		if strings.TrimSpace(req.Album.Genre) == "" && strings.TrimSpace(t.Genre) != "" {
			req.Album.Genre = strings.TrimSpace(t.Genre)
		}
	}

	// Каскадное распространение полей альбома на треки, если они пустые
	for i := range req.Tracks {
		if strings.TrimSpace(req.Tracks[i].Album) == "" {
			if strings.TrimSpace(req.Album.Album) != "" && !strings.EqualFold(req.Album.Album, "singles") && !strings.EqualFold(req.Album.Album, "single") {
				req.Tracks[i].Album = strings.TrimSpace(req.Album.Album)
			} else if strings.TrimSpace(req.Tracks[i].Title) != "" {
				req.Tracks[i].Album = strings.TrimSpace(req.Tracks[i].Title)
			}
		} else if strings.EqualFold(req.Tracks[i].Album, "single") || strings.EqualFold(req.Tracks[i].Album, "singles") {
			if strings.TrimSpace(req.Tracks[i].Title) != "" {
				req.Tracks[i].Album = strings.TrimSpace(req.Tracks[i].Title)
			}
		}
		if strings.TrimSpace(req.Tracks[i].AlbumArtist) == "" {
			req.Tracks[i].AlbumArtist = strings.TrimSpace(req.Album.AlbumArtist)
		}
		if req.Album.Year != nil && *req.Album.Year > 0 && req.Tracks[i].Year == 0 {
			req.Tracks[i].Year = *req.Album.Year
		}
		if strings.TrimSpace(req.Album.Genre) != "" && strings.TrimSpace(req.Tracks[i].Genre) == "" {
			req.Tracks[i].Genre = strings.TrimSpace(req.Album.Genre)
		}
	}

	// Если CoverBase64 пустой, но передан CoverURL (онлайн-обложка из мультисервисной галереи), скачиваем на сервере
	coverB64 := req.CoverBase64
	if strings.TrimSpace(coverB64) == "" && strings.TrimSpace(req.CoverURL) != "" && strings.HasPrefix(strings.ToLower(req.CoverURL), "http") {
		if data, err := s.downloadImage(ctx, req.CoverURL); err == nil && len(data) > 0 {
			coverB64 = base64.StdEncoding.EncodeToString(data)
		}
	}

	res, err := s.organizer.MoveAlbum(req.SourceDir, req.Album.Album, req.Album.AlbumArtist, req.Tracks, coverB64)
	if err != nil {
		return nil, fmt.Errorf("organizer move album failed: %w", err)
	}

	// Синхронизация с БД
	if s.repo != nil && len(res.Tracks) > 0 {
		_ = s.repo.BatchUpdateTrackTags(ctx, res.Tracks)
	}

	return &BatchSaveResponse{
		Success:        true,
		DestinationDir: res.DestinationDir,
		Tracks:         res.Tracks,
	}, nil
}

// GetCover извлекает фронтальную обложку (APIC type=3) из MP3 либо находит cover.jpg в каталоге.
func (s *Service) GetCover(ctx context.Context, relPath string) (*CoverInfo, error) {
	fullPath, isTmp, err := s.ResolvePath(relPath)
	if err != nil {
		return nil, err
	}

	st, err := safefs.Stat(fullPath)
	if err != nil {
		return nil, ErrFileNotFound
	}

	// 1. Попытка прочесть встроенный APIC фрейм из MP3
	if strings.EqualFold(filepath.Ext(fullPath), ".mp3") {
		cover, err := id3.ReadCover(fullPath)
		if err == nil && cover != nil && len(cover.Data) > 0 {
			return cover, nil
		}
	}

	// 2. Поиск внешнего графического файла рядом с треком или в папке
	var candidates []string
	if !st.IsDir() {
		trackBase := strings.TrimSuffix(fullPath, filepath.Ext(fullPath))
		candidates = append(candidates,
			trackBase+".jpg", trackBase+".png", trackBase+".jpeg", trackBase+".webp",
		)
	}
	dir := fullPath
	if !st.IsDir() {
		dir = filepath.Dir(fullPath)
	}
	for _, cand := range []string{"cover.jpg", "cover.png", "folder.jpg", "folder.png", "cover.jpeg"} {
		candidates = append(candidates, filepath.Join(dir, cand))
	}

	var sb *sandbox.PathSandbox = s.musicSandbox
	if isTmp {
		sb = s.tmpSandbox
	}

	for _, candPath := range candidates {
		if sb != nil {
			relCand, err := sb.ToRelative(candPath)
			if err != nil {
				continue
			}
			resolvedPath, err := sb.Resolve(relCand, false)
			if err != nil {
				continue
			}
			candPath = resolvedPath
		}
		if data, err := safefs.ReadFile(candPath); err == nil {
			mime := "image/jpeg"
			if strings.HasSuffix(candPath, ".png") {
				mime = "image/png"
			} else if strings.HasSuffix(candPath, ".webp") {
				mime = "image/webp"
			}
			return &CoverInfo{
				MimeType: mime,
				Data:     data,
			}, nil
		}
	}

	return nil, ErrCoverNotFound
}

// SearchCovers выполняет параллельный мультисервисный поиск обложек высокого разрешения:
// Apple Music (1400x1400), Deezer (1400x1400), Яндекс Музыка (1000x1000), MusicBrainz (1000x1000).
func (s *Service) SearchCovers(ctx context.Context, query string, limit int) ([]CoverSearchResultItem, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return []CoverSearchResultItem{}, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 24
	}

	client := s.httpClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}

	perSourceLimit := limit / 2
	if perSourceLimit < 6 {
		perSourceLimit = 6
	}

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		results = make(map[string][]CoverSearchResultItem)
	)

	addResults := func(src string, items []CoverSearchResultItem) {
		if len(items) == 0 {
			return
		}
		mu.Lock()
		results[src] = items
		mu.Unlock()
	}

	// 1. Apple Music / iTunes Search API
	wg.Add(1)
	go func() {
		defer wg.Done()
		items := s.searchAppleCovers(ctx, client, q, perSourceLimit)
		addResults("apple", items)
	}()

	// 2. Deezer API
	wg.Add(1)
	go func() {
		defer wg.Done()
		items := s.searchDeezerCovers(ctx, client, q, perSourceLimit)
		addResults("deezer", items)
	}()

	// 3. Яндекс Музыка API
	wg.Add(1)
	go func() {
		defer wg.Done()
		items := s.searchYandexCovers(ctx, client, q, perSourceLimit)
		addResults("yandex", items)
	}()

	// 4. MusicBrainz / Cover Art Archive
	wg.Add(1)
	go func() {
		defer wg.Done()
		items := s.searchMusicBrainzCovers(ctx, client, q, perSourceLimit)
		addResults("musicbrainz", items)
	}()

	wg.Wait()

	// Перемежение (round-robin) результатов для максимального разнообразия первых карточек
	sourceOrder := []string{"apple", "deezer", "yandex", "musicbrainz"}
	maxLen := 0
	for _, src := range sourceOrder {
		if l := len(results[src]); l > maxLen {
			maxLen = l
		}
	}

	merged := make([]CoverSearchResultItem, 0, limit)
	seen := make(map[string]bool)

	for i := 0; i < maxLen; i++ {
		for _, src := range sourceOrder {
			list := results[src]
			if i < len(list) {
				item := list[i]
				cleanURL := strings.TrimSpace(item.CoverURL)
				if cleanURL != "" && !seen[cleanURL] {
					seen[cleanURL] = true
					merged = append(merged, item)
					if len(merged) >= limit {
						break
					}
				}
			}
		}
		if len(merged) >= limit {
			break
		}
	}

	return merged, nil
}

func (s *Service) searchAppleCovers(ctx context.Context, client *http.Client, query string, limit int) []CoverSearchResultItem {
	reqCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	u := fmt.Sprintf("https://itunes.apple.com/search?term=%s&entity=album&limit=%d", url.QueryEscape(query), limit)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var data struct {
		Results []struct {
			CollectionName string `json:"collectionName"`
			TrackName      string `json:"trackName"`
			ArtistName     string `json:"artistName"`
			ArtworkURL100  string `json:"artworkUrl100"`
			ReleaseDate    string `json:"releaseDate"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil
	}

	reArt := regexp.MustCompile(`\d+x\d+bb(?:\.jpg|\.png)?`)
	items := make([]CoverSearchResultItem, 0, len(data.Results))
	for _, it := range data.Results {
		if it.ArtworkURL100 == "" {
			continue
		}
		coverURL := reArt.ReplaceAllString(it.ArtworkURL100, "1400x1400bb.jpg")
		if !strings.HasSuffix(coverURL, ".jpg") && !strings.HasSuffix(coverURL, ".png") && !strings.HasSuffix(coverURL, ".jpeg") {
			coverURL += ".jpg"
		}

		title := it.CollectionName
		if title == "" {
			title = it.TrackName
		}
		var pYear *int
		if len(it.ReleaseDate) >= 4 {
			if yr, err := strconv.Atoi(it.ReleaseDate[:4]); err == nil && yr > 0 {
				pYear = &yr
			}
		}

		items = append(items, CoverSearchResultItem{
			Source:   "apple",
			Title:    title,
			Artist:   it.ArtistName,
			CoverURL: coverURL,
			Width:    1400,
			Height:   1400,
			Year:     pYear,
		})
	}
	return items
}

func (s *Service) searchDeezerCovers(ctx context.Context, client *http.Client, query string, limit int) []CoverSearchResultItem {
	reqCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	if s.deezer != nil {
		albums, err := s.deezer.SearchAlbums(reqCtx, query)
		if err == nil && len(albums) > 0 {
			items := make([]CoverSearchResultItem, 0, len(albums))
			for _, a := range albums {
				cover := a.BestCoverURL()
				if cover == "" {
					continue
				}
				year := a.ReleaseYear()
				var pYear *int
				if year > 0 {
					pYear = &year
				}
				items = append(items, CoverSearchResultItem{
					Source:   "deezer",
					Title:    a.Title,
					Artist:   a.Artist.Name,
					CoverURL: cover,
					Width:    1400,
					Height:   1400,
					Year:     pYear,
				})
				if len(items) >= limit {
					break
				}
			}
			return items
		}
	}

	u := fmt.Sprintf("https://api.deezer.com/search/album?q=%s&limit=%d", url.QueryEscape(query), limit)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u, nil)
	if err != nil {
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var data struct {
		Data []struct {
			Title  string `json:"title"`
			Artist struct {
				Name string `json:"name"`
			} `json:"artist"`
			CoverXL     string `json:"cover_xl"`
			CoverBig    string `json:"cover_big"`
			CoverMedium string `json:"cover_medium"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil
	}

	items := make([]CoverSearchResultItem, 0, len(data.Data))
	for _, a := range data.Data {
		cov := a.CoverXL
		if cov == "" {
			cov = a.CoverBig
		}
		if cov == "" {
			cov = a.CoverMedium
		}
		if cov == "" {
			continue
		}
		items = append(items, CoverSearchResultItem{
			Source:   "deezer",
			Title:    a.Title,
			Artist:   a.Artist.Name,
			CoverURL: cov,
			Width:    1400,
			Height:   1400,
		})
	}
	return items
}

func (s *Service) searchYandexCovers(ctx context.Context, client *http.Client, query string, limit int) []CoverSearchResultItem {
	reqCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	u := fmt.Sprintf("https://api.music.yandex.net/search?text=%s&type=album&page=0", url.QueryEscape(query))
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var data struct {
		Result struct {
			Albums struct {
				Results []struct {
					Title    string `json:"title"`
					Year     int    `json:"year"`
					CoverURI string `json:"coverUri"`
					Artists  []struct {
						Name string `json:"name"`
					} `json:"artists"`
				} `json:"results"`
			} `json:"albums"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil
	}

	items := make([]CoverSearchResultItem, 0, len(data.Result.Albums.Results))
	for _, a := range data.Result.Albums.Results {
		if a.CoverURI == "" {
			continue
		}
		cov := strings.Replace(a.CoverURI, "%%", "1000x1000", 1)
		if !strings.HasPrefix(cov, "http://") && !strings.HasPrefix(cov, "https://") {
			cov = "https://" + cov
		}
		artist := ""
		if len(a.Artists) > 0 {
			artist = a.Artists[0].Name
		}
		var pYear *int
		if a.Year > 0 {
			yr := a.Year
			pYear = &yr
		}
		items = append(items, CoverSearchResultItem{
			Source:   "yandex",
			Title:    a.Title,
			Artist:   artist,
			CoverURL: cov,
			Width:    1000,
			Height:   1000,
			Year:     pYear,
		})
		if len(items) >= limit {
			break
		}
	}
	return items
}

func (s *Service) searchMusicBrainzCovers(ctx context.Context, client *http.Client, query string, limit int) []CoverSearchResultItem {
	reqCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	lucene := fmt.Sprintf(`release:"%s" OR artist:"%s" OR "%s"`, query, query, query)
	u := fmt.Sprintf("https://musicbrainz.org/ws/2/release/?query=%s&limit=%d&fmt=json", url.QueryEscape(lucene), limit)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "MusicManager/1.0.0 (https://github.com/kex1tu/music-manager)")

	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var data struct {
		Releases []struct {
			ID           string `json:"id"`
			Title        string `json:"title"`
			Date         string `json:"date"`
			ArtistCredit []struct {
				Name       string `json:"name"`
				JoinPhrase string `json:"joinphrase"`
			} `json:"artist-credit"`
		} `json:"releases"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil
	}

	items := make([]CoverSearchResultItem, 0, len(data.Releases))
	for _, r := range data.Releases {
		if r.ID == "" {
			continue
		}
		artist := ""
		for _, ac := range r.ArtistCredit {
			artist += ac.Name + ac.JoinPhrase
		}
		var pYear *int
		if len(r.Date) >= 4 {
			if yr, err := strconv.Atoi(r.Date[:4]); err == nil && yr > 0 {
				pYear = &yr
			}
		}
		items = append(items, CoverSearchResultItem{
			Source:   "musicbrainz",
			Title:    r.Title,
			Artist:   strings.TrimSpace(artist),
			CoverURL: fmt.Sprintf("https://coverartarchive.org/release/%s/front-500", r.ID),
			Width:    1000,
			Height:   1000,
			Year:     pYear,
		})
	}
	return items
}

func (s *Service) downloadImage(ctx context.Context, rawURL string) ([]byte, error) {
	client := s.httpClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download cover status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 20*1024*1024))
}

// SearchDeezerAlbums ищет альбомы в каталоге Deezer.
func (s *Service) SearchDeezerAlbums(ctx context.Context, query string) ([]DeezerSearchAlbumItem, error) {
	if s.deezer == nil {
		return nil, ErrClientNotInitialized
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return []DeezerSearchAlbumItem{}, nil
	}

	albums, err := s.deezer.SearchAlbums(ctx, q)
	if err != nil {
		return nil, err
	}

	items := make([]DeezerSearchAlbumItem, len(albums))
	for i, a := range albums {
		cover := a.BestCoverURL()
		year := a.ReleaseYear()
		var pCover *string
		if cover != "" {
			pCover = &cover
		}
		var pYear *int
		if year > 0 {
			pYear = &year
		}
		items[i] = DeezerSearchAlbumItem{
			ID:       strconv.FormatInt(a.ID, 10),
			Title:    a.Title,
			Artist:   a.Artist.Name,
			CoverURL: pCover,
			NbTracks: a.NbTracks,
			Year:     pYear,
		}
	}
	return items, nil
}

// GetDeezerAlbumDetails возвращает детальную информацию об альбоме Deezer с 8-поточным опросом соавторов.
func (s *Service) GetDeezerAlbumDetails(ctx context.Context, id string) (*DeezerAlbumDetailsResponse, error) {
	if s.deezer == nil {
		return nil, ErrClientNotInitialized
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("empty album id")
	}

	album, err := s.deezer.GetAlbum(ctx, id)
	if err != nil {
		return nil, err
	}

	cover := album.BestCoverURL()
	year := album.ReleaseYear()
	genre := album.PrimaryGenre()

	type trackDetail struct {
		contributors []resolver.DeezerArtist
		isrc         string
		diskNumber   int
	}

	var details []trackDetail
	if album.Tracks != nil {
		total := len(album.Tracks.Data)
		details = make([]trackDetail, total)

		var wg sync.WaitGroup
		sem := make(chan struct{}, 8) // максимум 8 параллельных потоков

		for i, t := range album.Tracks.Data {
			if t.ID <= 0 {
				continue
			}
			wg.Add(1)
			go func(idx int, trackID int64) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				trCtx, trCancel := context.WithTimeout(ctx, 4*time.Second)
				defer trCancel()

				tr, trErr := s.deezer.GetTrack(trCtx, strconv.FormatInt(trackID, 10))
				if trErr == nil && tr != nil {
					details[idx] = trackDetail{
						contributors: tr.Contributors,
						isrc:         tr.ISRC,
						diskNumber:   tr.DiskNumber,
					}
				}
			}(i, t.ID)
		}
		wg.Wait()
	}

	var tracks []DeezerAlbumTrackItem
	if album.Tracks != nil {
		for i, t := range album.Tracks.Data {
			pos := t.TrackPosition
			if pos == 0 {
				pos = i + 1
			}
			tArtist := t.Artist.Name
			if tArtist == "" {
				tArtist = album.Artist.Name
			}

			var artists []string
			if i < len(details) && len(details[i].contributors) > 0 {
				for _, c := range details[i].contributors {
					if c.Name != "" {
						artists = append(artists, c.Name)
					}
				}
			}
			if len(artists) == 0 && len(t.Contributors) > 0 {
				for _, c := range t.Contributors {
					if c.Name != "" {
						artists = append(artists, c.Name)
					}
				}
			}
			if len(artists) == 0 && tArtist != "" {
				artists = []string{tArtist}
			}
			if s.enricher != nil {
				artists = s.enricher.EnrichTrackArtists(ctx, t.Title, artists)
			}
			formattedArtist := id3.FormatMultiValue(artists)

			discNum := 1
			if i < len(details) && details[i].diskNumber > 0 {
				discNum = details[i].diskNumber
			} else if t.DiskNumber > 0 {
				discNum = t.DiskNumber
			}

			isrc := ""
			if i < len(details) && details[i].isrc != "" {
				isrc = details[i].isrc
			} else {
				isrc = t.ISRC
			}

			tracks = append(tracks, DeezerAlbumTrackItem{
				TrackNumber:  pos,
				DiscNumber:   discNum,
				Title:        id3.CleanTitle(t.Title, ""),
				Artist:       formattedArtist,
				Duration:     t.Duration,
				ISRC:         isrc,
				Contributors: artists,
			})
		}
	}

	return &DeezerAlbumDetailsResponse{
		ID:         strconv.FormatInt(album.ID, 10),
		Title:      album.Title,
		Artist:     album.Artist.Name,
		Year:       year,
		Genre:      genre,
		CoverURL:   cover,
		TrackCount: len(tracks),
		Tracks:     tracks,
	}, nil
}

// SearchDeezerTracks ищет треки в каталоге Deezer.
func (s *Service) SearchDeezerTracks(ctx context.Context, query string) ([]DeezerSearchTrackItem, error) {
	if s.deezer == nil {
		return nil, ErrClientNotInitialized
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return []DeezerSearchTrackItem{}, nil
	}

	tracks, err := s.deezer.SearchTracks(ctx, q)
	if err != nil {
		return nil, err
	}

	items := make([]DeezerSearchTrackItem, len(tracks))
	for i, t := range tracks {
		var artists []string
		if len(t.Contributors) > 0 {
			for _, c := range t.Contributors {
				if c.Name != "" {
					artists = append(artists, c.Name)
				}
			}
		}
		if len(artists) == 0 && t.Artist.Name != "" {
			artists = []string{t.Artist.Name}
		}
		if s.enricher != nil {
			artists = s.enricher.EnrichTrackArtists(ctx, t.Title, artists)
		}
		formattedArtist := id3.FormatMultiValue(artists)

		var pCover *string
		var pYear *int
		albumTitle := ""
		albumArtist := t.Artist.Name

		if t.Album != nil {
			albumTitle = t.Album.Title
			cover := t.Album.BestCoverURL()
			if cover != "" {
				pCover = &cover
			}
			y := t.Album.ReleaseYear()
			if y > 0 {
				pYear = &y
			}
			if t.Album.Artist.Name != "" {
				albumArtist = t.Album.Artist.Name
			}
		}

		items[i] = DeezerSearchTrackItem{
			ID:          strconv.FormatInt(t.ID, 10),
			Title:       id3.CleanTitle(t.Title, ""),
			Artist:      formattedArtist,
			Album:       albumTitle,
			AlbumArtist: albumArtist,
			CoverURL:    pCover,
			Duration:    t.Duration,
			Year:        pYear,
		}
	}
	return items, nil
}

// GetDeezerTrackDetails возвращает полную информацию о треке Deezer.
func (s *Service) GetDeezerTrackDetails(ctx context.Context, id string) (map[string]any, error) {
	if s.deezer == nil {
		return nil, ErrClientNotInitialized
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("empty track id")
	}

	track, err := s.deezer.GetTrack(ctx, id)
	if err != nil {
		return nil, err
	}

	var artists []string
	if len(track.Contributors) > 0 {
		for _, c := range track.Contributors {
			if c.Name != "" {
				artists = append(artists, c.Name)
			}
		}
	}
	if len(artists) == 0 && track.Artist.Name != "" {
		artists = []string{track.Artist.Name}
	}
	if s.enricher != nil {
		artists = s.enricher.EnrichTrackArtists(ctx, track.Title, artists)
	}
	formattedArtist := id3.FormatMultiValue(artists)

	albumTitle := ""
	albumArtist := track.Artist.Name
	coverURL := ""
	year := 0
	genre := ""

	if track.Album != nil {
		albumTitle = track.Album.Title
		coverURL = track.Album.BestCoverURL()
		year = track.Album.ReleaseYear()
		genre = track.Album.PrimaryGenre()
		if track.Album.Artist.Name != "" {
			albumArtist = track.Album.Artist.Name
		}
	}

	pos := track.TrackPosition
	if pos <= 0 {
		pos = 1
	}
	disc := track.DiskNumber
	if disc <= 0 {
		disc = 1
	}

	return map[string]any{
		"id":           id,
		"title":        id3.CleanTitle(track.Title, ""),
		"artist":       formattedArtist,
		"contributors": artists,
		"album":        albumTitle,
		"album_artist": albumArtist,
		"year":         year,
		"genre":        genre,
		"cover_url":    coverURL,
		"track_number": pos,
		"disc_number":  disc,
		"duration":     track.Duration,
		"isrc":         track.ISRC,
	}, nil
}

// RecognizeAudio выполняет акустическое распознавание файла через Shazam songrec с безопасным фолбэком.
func (s *Service) RecognizeAudio(ctx context.Context, relPath string) (*RecognizeAudioResponse, error) {
	fullPath, _, err := s.ResolvePath(relPath)
	if err != nil {
		return &RecognizeAudioResponse{Recognized: false}, nil
	}

	st, err := safefs.Stat(fullPath)
	if err != nil || st.IsDir() {
		return &RecognizeAudioResponse{Recognized: false}, nil
	}

	// 1. Попытка через внедренный ShazamCatalog (ShazamProvider)
	if s.shazam != nil {
		rec, recErr := s.shazam.RecognizeAudioFile(ctx, fullPath)
		if recErr == nil && rec != nil {
			return &RecognizeAudioResponse{
				Recognized: true,
				Title:      rec.Title,
				Artist:     rec.Artist,
				Album:      rec.Title,
				Year:       rec.Year,
				Genre:      rec.Genre,
				CoverURL:   rec.CoverURL,
			}, nil
		}
	}

	// 2. Фолбэк на прямой запуск songrec CLI при наличии
	songrecBin, err := exec.LookPath("songrec")
	if err != nil {
		return &RecognizeAudioResponse{Recognized: false}, nil
	}

	recCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := mediaexec.CommandContext(recCtx, songrecBin, "audio-file-to-recognized-song", fullPath)
	defer cmd.Close()
	out, err := cmd.Output()
	if err != nil {
		return &RecognizeAudioResponse{Recognized: false}, nil
	}

	var raw struct {
		Track *struct {
			Title    string `json:"title"`
			Subtitle string `json:"subtitle"`
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

	if err := json.Unmarshal(out, &raw); err != nil || raw.Track == nil {
		return &RecognizeAudioResponse{Recognized: false}, nil
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
	for _, sec := range raw.Track.Sections {
		for _, m := range sec.Metadata {
			switch strings.ToLower(m.Title) {
			case "album":
				album = m.Text
			case "released":
				if y, yErr := strconv.Atoi(strings.TrimSpace(m.Text)); yErr == nil {
					year = y
				}
			}
		}
	}

	return &RecognizeAudioResponse{
		Recognized: true,
		Title:      title,
		Artist:     artist,
		Album:      album,
		Year:       year,
		Genre:      genre,
		CoverURL:   coverURL,
	}, nil
}

// SearchCatalog выполняет параллельный опрос Deezer, MusicBrainz и Shazam с ограничением таймаута.
func (s *Service) SearchCatalog(ctx context.Context, query, audioPath string) (*CatalogSearchResponse, error) {
	q := strings.TrimSpace(query)
	audioPath = strings.TrimSpace(audioPath)
	if q == "" && audioPath == "" {
		return nil, errors.New("missing query or audio_path parameter")
	}

	searchCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()

	var (
		wg        sync.WaitGroup
		deezerRes CatalogSourceResult
		mbRes     CatalogSourceResult
		shazamRes CatalogSourceResult
	)

	// 1. Deezer
	if s.deezer != nil && q != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			albums, err := s.deezer.SearchAlbums(searchCtx, q)
			if err != nil {
				deezerRes.Error = err.Error()
				return
			}
			items := make([]CatalogReleaseSummary, 0, len(albums))
			for i, a := range albums {
				cover := a.BestCoverURL()
				year := a.ReleaseYear()
				score := 100 - (i * 3)
				if score < 10 {
					score = 10
				}
				items = append(items, CatalogReleaseSummary{
					ID:         strconv.FormatInt(a.ID, 10),
					Source:     "deezer",
					Title:      a.Title,
					Artist:     a.Artist.Name,
					CoverURL:   cover,
					Year:       year,
					Date:       strconv.Itoa(year),
					TrackCount: a.NbTracks,
					Score:      score,
				})
			}
			deezerRes.Count = len(items)
			deezerRes.Items = items
		}()
	}

	// 2. MusicBrainz
	if s.musicbrainz != nil && q != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			releases, err := s.musicbrainz.SearchReleases(searchCtx, q)
			if err != nil {
				mbRes.Error = err.Error()
				return
			}
			mbRes.Count = len(releases)
			mbRes.Items = releases
		}()
	}

	// 3. Shazam
	if s.shazam != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var items []CatalogReleaseSummary
			if audioPath != "" {
				if fullPath, _, err := s.ResolvePath(audioPath); err == nil {
					if info, statErr := safefs.Stat(fullPath); statErr == nil && !info.IsDir() {
						rec, recErr := s.shazam.RecognizeAudioFile(searchCtx, fullPath)
						if recErr == nil && rec != nil {
							items = append(items, CatalogReleaseSummary{
								ID:         rec.ID,
								Source:     "shazam",
								Title:      rec.Title,
								Artist:     rec.Artist,
								CoverURL:   rec.CoverURL,
								Year:       rec.Year,
								Date:       rec.Date,
								TrackCount: len(rec.Tracks),
								Label:      rec.Label,
								Score:      100,
							})
						}
					}
				}
			}
			if q != "" {
				matches, err := s.shazam.Search(searchCtx, q)
				if err != nil && len(items) == 0 {
					shazamRes.Error = err.Error()
				} else {
					items = append(items, matches...)
				}
			}
			shazamRes.Count = len(items)
			shazamRes.Items = items
		}()
	}

	wg.Wait()

	return &CatalogSearchResponse{
		Query:       q,
		Deezer:      deezerRes,
		MusicBrainz: mbRes,
		Shazam:      shazamRes,
	}, nil
}

// GetCatalogDeezerAlbum возвращает релиз Deezer в едином формате каталогов с 8-поточным опросом соавторов.
func (s *Service) GetCatalogDeezerAlbum(ctx context.Context, id string) (*CatalogReleaseDetails, error) {
	if s.deezer == nil {
		return nil, ErrClientNotInitialized
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("empty album id")
	}

	album, err := s.deezer.GetAlbum(ctx, id)
	if err != nil {
		return nil, err
	}

	type trackDetail struct {
		contributors []resolver.DeezerArtist
		isrc         string
		diskNumber   int
	}

	var details []trackDetail
	if album.Tracks != nil {
		total := len(album.Tracks.Data)
		details = make([]trackDetail, total)

		var wg sync.WaitGroup
		sem := make(chan struct{}, 8)

		for i, t := range album.Tracks.Data {
			if t.ID <= 0 {
				continue
			}
			wg.Add(1)
			go func(idx int, trackID int64) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				trCtx, trCancel := context.WithTimeout(ctx, 4*time.Second)
				defer trCancel()

				tr, trErr := s.deezer.GetTrack(trCtx, strconv.FormatInt(trackID, 10))
				if trErr == nil && tr != nil {
					details[idx] = trackDetail{
						contributors: tr.Contributors,
						isrc:         tr.ISRC,
						diskNumber:   tr.DiskNumber,
					}
				}
			}(i, t.ID)
		}
		wg.Wait()
	}

	var tracks []CatalogTrackDetail
	if album.Tracks != nil {
		for i, t := range album.Tracks.Data {
			pos := t.TrackPosition
			if pos == 0 {
				pos = i + 1
			}
			tArtist := t.Artist.Name
			if tArtist == "" {
				tArtist = album.Artist.Name
			}

			var artists []string
			if i < len(details) && len(details[i].contributors) > 0 {
				for _, c := range details[i].contributors {
					if c.Name != "" {
						artists = append(artists, c.Name)
					}
				}
			}
			if len(artists) == 0 && len(t.Contributors) > 0 {
				for _, c := range t.Contributors {
					if c.Name != "" {
						artists = append(artists, c.Name)
					}
				}
			}
			if len(artists) > 0 {
				tArtist = strings.Join(artists, "\\\\")
			} else if tArtist != "" {
				artists = []string{tArtist}
			}

			discNum := 1
			isrc := ""
			if i < len(details) {
				if details[i].diskNumber > 0 {
					discNum = details[i].diskNumber
				}
				isrc = details[i].isrc
			}

			tracks = append(tracks, CatalogTrackDetail{
				Position:     pos,
				TrackNumber:  pos,
				DiscNumber:   discNum,
				Title:        t.Title,
				Artist:       tArtist,
				Artists:      artists,
				Duration:     t.Duration,
				DurationMs:   t.Duration * 1000,
				ISRC:         isrc,
				ReleaseTitle: album.Title,
			})
		}
	}

	return &CatalogReleaseDetails{
		ID:         strconv.FormatInt(album.ID, 10),
		Source:     "deezer",
		Title:      album.Title,
		Artist:     album.Artist.Name,
		CoverURL:   album.BestCoverURL(),
		Year:       album.ReleaseYear(),
		Date:       album.ReleaseDate,
		Genre:      album.PrimaryGenre(),
		Genres:     []string{album.PrimaryGenre()},
		TrackCount: len(tracks),
		Tracks:     tracks,
	}, nil
}

// GetCatalogMusicBrainzRelease возвращает детальный релиз MusicBrainz с треклистом.
func (s *Service) GetCatalogMusicBrainzRelease(ctx context.Context, id string) (*CatalogReleaseDetails, error) {
	if s.musicbrainz == nil {
		return nil, ErrClientNotInitialized
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("empty release id")
	}
	return s.musicbrainz.GetRelease(ctx, id)
}

// GetCatalogShazamTrack возвращает детальный трек Shazam (по акустическому отпечатку или ID/названию).
func (s *Service) GetCatalogShazamTrack(ctx context.Context, id, query, audioPath string) (*CatalogReleaseDetails, error) {
	if s.shazam == nil {
		return nil, ErrClientNotInitialized
	}

	audioPath = strings.TrimSpace(audioPath)
	if audioPath != "" {
		fullPath, _, err := s.ResolvePath(audioPath)
		if err != nil {
			return nil, err
		}
		if info, statErr := safefs.Stat(fullPath); statErr != nil || info.IsDir() {
			return nil, ErrFileNotFound
		}
		return s.shazam.RecognizeAudioFile(ctx, fullPath)
	}

	q := strings.TrimSpace(query)
	if q == "" {
		q = strings.TrimSpace(id)
	}
	if q == "" {
		return nil, errors.New("missing id or query parameter")
	}

	matches, err := s.shazam.Search(ctx, q)
	if err != nil || len(matches) == 0 {
		return nil, ErrFileNotFound
	}

	first := matches[0]
	return &CatalogReleaseDetails{
		ID:         first.ID,
		Source:     "shazam",
		Title:      first.Title,
		Artist:     first.Artist,
		CoverURL:   first.CoverURL,
		Year:       first.Year,
		Date:       first.Date,
		TrackCount: 1,
		Tracks: []CatalogTrackDetail{
			{
				Position:     1,
				TrackNumber:  1,
				DiscNumber:   1,
				Title:        first.Title,
				Artist:       first.Artist,
				Artists:      []string{first.Artist},
				ReleaseTitle: first.Title,
			},
		},
	}, nil
}

// ============================================================================
// MusicBrainz OAuth
// ============================================================================

// GetMusicBrainzStatus возвращает статус OAuth подключения к MusicBrainz.
func (s *Service) GetMusicBrainzStatus() MusicBrainzOAuthStatus {
	if s.musicbrainz == nil {
		return MusicBrainzOAuthStatus{
			Configured: false,
			Connected:  false,
		}
	}
	return s.musicbrainz.GetOAuthStatus()
}

// GetMusicBrainzAuthURL возвращает URL страницы авторизации MusicBrainz OAuth 2.0.
func (s *Service) GetMusicBrainzAuthURL(state, redirectURI string) (string, error) {
	if s.musicbrainz == nil {
		return "", ErrClientNotInitialized
	}
	if redirectURI == "" && s.mbConfig.RedirectURI != "" {
		redirectURI = s.mbConfig.RedirectURI
	}
	return s.musicbrainz.GetAuthorizationURL(state, redirectURI), nil
}

// HandleMusicBrainzCallback обрабатывает обмен кода на токен и сохраняет его в базе данных.
func (s *Service) HandleMusicBrainzCallback(ctx context.Context, code, redirectURI string) (string, error) {
	if s.musicbrainz == nil {
		return "", ErrClientNotInitialized
	}
	if redirectURI == "" && s.mbConfig.RedirectURI != "" {
		redirectURI = s.mbConfig.RedirectURI
	}

	tokenResp, err := s.musicbrainz.ExchangeCode(ctx, code, redirectURI)
	if err != nil {
		return "", err
	}

	status := s.musicbrainz.GetOAuthStatus()
	username := status.Username

	// Сохраняем в персистентное хранилище
	if s.tokenStore != nil && tokenResp != nil {
		exp := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
		_ = s.tokenStore.SaveTokens(ctx, tokenResp.AccessToken, tokenResp.RefreshToken, exp, username)
	}

	return username, nil
}

// DisconnectMusicBrainz сбрасывает токены OAuth и возвращает клиент в анонимный режим.
func (s *Service) DisconnectMusicBrainz(ctx context.Context) error {
	if s.musicbrainz != nil {
		_ = s.musicbrainz.Disconnect(ctx)
	}
	if s.tokenStore != nil {
		_ = s.tokenStore.ClearTokens(ctx)
	}
	return nil
}

// ConfiguredRedirectURI возвращает предварительно настроенный RedirectURI для MusicBrainz.
func (s *Service) ConfiguredRedirectURI() string {
	return s.mbConfig.RedirectURI
}

// ============================================================================
// File Pattern Renaming (Puddletag & File Operations)
// ============================================================================

// RenamePattern выполняет пакетное безопасное переименование файлов по шаблону с проверкой песочницы.
func (s *Service) RenamePattern(ctx context.Context, req RenamePatternRequest) ([]RenamePatternResult, error) {
	if s.musicSandbox == nil {
		return nil, ErrSandboxViolation
	}

	results := make([]RenamePatternResult, 0, len(req.Items))
	claimedTargets := make(map[string]bool)

	for _, item := range req.Items {
		itemCurrent := strings.TrimSpace(item.CurrentPath)
		itemNew := strings.TrimSpace(item.NewName)

		if itemCurrent == "" || itemNew == "" {
			results = append(results, RenamePatternResult{
				CurrentPath: item.CurrentPath,
				NewPath:     "",
				Status:      "error",
				Error:       "current_path and new_name cannot be empty",
			})
			continue
		}

		targetProposedRel := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(itemCurrent), itemNew)))

		// 1. Разрешаем текущий путь внутри песочницы
		currentFull, err := s.musicSandbox.Resolve(itemCurrent, true)
		if err != nil {
			results = append(results, RenamePatternResult{
				CurrentPath: item.CurrentPath,
				NewPath:     targetProposedRel,
				Status:      "error",
				Error:       err.Error(),
			})
			continue
		}

		// Проверяем существование исходного файла
		stCurrent, err := safefs.Stat(currentFull)
		if err != nil {
			results = append(results, RenamePatternResult{
				CurrentPath: item.CurrentPath,
				NewPath:     targetProposedRel,
				Status:      "error",
				Error:       "source file does not exist",
			})
			continue
		}
		if stCurrent.IsDir() {
			results = append(results, RenamePatternResult{
				CurrentPath: item.CurrentPath,
				NewPath:     targetProposedRel,
				Status:      "error",
				Error:       "source path is a directory",
			})
			continue
		}

		// 2. Валидируем и разрешаем целевой путь внутри песочницы
		targetFull, err := s.musicSandbox.Resolve(filepath.Join(filepath.Dir(itemCurrent), itemNew), true)
		if err != nil {
			results = append(results, RenamePatternResult{
				CurrentPath: item.CurrentPath,
				NewPath:     targetProposedRel,
				Status:      "error",
				Error:       err.Error(),
			})
			continue
		}

		targetRel, err := s.musicSandbox.ToRelative(targetFull)
		if err != nil {
			results = append(results, RenamePatternResult{
				CurrentPath: item.CurrentPath,
				NewPath:     targetProposedRel,
				Status:      "error",
				Error:       err.Error(),
			})
			continue
		}

		// 3. Если путь не изменился
		if currentFull == targetFull {
			results = append(results, RenamePatternResult{
				CurrentPath: item.CurrentPath,
				NewPath:     targetRel,
				Status:      "ok",
			})
			continue
		}

		// 4. Проверка коллизий
		if _, err := safefs.Stat(targetFull); err == nil {
			results = append(results, RenamePatternResult{
				CurrentPath: item.CurrentPath,
				NewPath:     targetRel,
				Status:      "conflict",
				Error:       "target file already exists",
			})
			continue
		}

		if claimedTargets[targetFull] {
			results = append(results, RenamePatternResult{
				CurrentPath: item.CurrentPath,
				NewPath:     targetRel,
				Status:      "conflict",
				Error:       "target file collision within batch",
			})
			continue
		}
		claimedTargets[targetFull] = true

		// 5. В режиме dry_run возвращаем статус "ok" без изменения файловой системы
		if req.DryRun {
			results = append(results, RenamePatternResult{
				CurrentPath: item.CurrentPath,
				NewPath:     targetRel,
				Status:      "ok",
			})
			continue
		}

		// 6. Убеждаемся, что целевая директория существует
		targetDir := filepath.Dir(targetFull)
		if err := safefs.MkdirAll(targetDir, 0755); err != nil {
			results = append(results, RenamePatternResult{
				CurrentPath: item.CurrentPath,
				NewPath:     targetRel,
				Status:      "error",
				Error:       "failed to create destination directory: " + err.Error(),
			})
			continue
		}

		// 7. Выполняем переименование основного файла
		if err := safefs.Rename(currentFull, targetFull); err != nil {
			results = append(results, RenamePatternResult{
				CurrentPath: item.CurrentPath,
				NewPath:     targetRel,
				Status:      "error",
				Error:       "failed to rename file: " + err.Error(),
			})
			continue
		}

		// 8. Переименовываем сопутствующие файлы текстов (.lrc, .txt), если они существуют
		currentBase := strings.TrimSuffix(currentFull, filepath.Ext(currentFull))
		targetBase := strings.TrimSuffix(targetFull, filepath.Ext(targetFull))
		for _, lyricsExt := range []string{".lrc", ".txt"} {
			curLyr := currentBase + lyricsExt
			tarLyr := targetBase + lyricsExt
			if _, err := safefs.Stat(curLyr); err == nil {
				_ = safefs.Rename(curLyr, tarLyr)
			}
		}

		// 9. Обновляем пути в БД через репозиторий
		currentRel, err := s.musicSandbox.ToRelative(currentFull)
		if err != nil || currentRel == "" {
			currentRel = itemCurrent
		}
		if s.repo != nil {
			_ = s.repo.UpdateTrackFilePath(ctx, currentRel, targetRel)
		}

		results = append(results, RenamePatternResult{
			CurrentPath: item.CurrentPath,
			NewPath:     targetRel,
			Status:      "ok",
		})
	}

	return results, nil
}
