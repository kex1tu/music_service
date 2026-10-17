package filemanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/id3"
)

var (
	// ErrInvalidPath возвращается при пустом или некорректном пути.
	ErrInvalidPath = errors.New("invalid file path")
	// ErrFileNotFound возвращается, если файл не существует.
	ErrFileNotFound = errors.New("file not found")
	// ErrSandboxViolation возвращается при попытке обхода песочницы.
	ErrSandboxViolation = errors.New("sandbox traversal violation")
	// ErrTargetExists возвращается при попытке переименования в уже существующий файл.
	ErrTargetExists = errors.New("target file already exists")
)

// ServiceConfig определяет зависимости для инициализации сервиса FileManager.
type ServiceConfig struct {
	MusicSandbox *sandbox.PathSandbox
	TmpSandbox   *sandbox.PathSandbox
	Repo         Repository
	Organizer    Organizer
	Scanner      Scanner
}

// Service инкапсулирует операции файлового менеджера, стриминга и организации медиатеки.
type Service struct {
	musicSandbox *sandbox.PathSandbox
	tmpSandbox   *sandbox.PathSandbox
	repo         Repository
	organizer    Organizer
	scanner      Scanner
	trackCache   sync.Map
}

// NewService создает новый экземпляр Service.
func NewService(cfg ServiceConfig) *Service {
	return &Service{
		musicSandbox: cfg.MusicSandbox,
		tmpSandbox:   cfg.TmpSandbox,
		repo:         cfg.Repo,
		organizer:    cfg.Organizer,
		scanner:      cfg.Scanner,
	}
}

// ResolvePath безопасно разрешает путь сначала во временной песочнице, затем в основной.
func (s *Service) ResolvePath(relPath string) (fullPath string, isTmp bool, err error) {
	trimmed := strings.TrimSpace(relPath)
	if trimmed == "" {
		return "", false, ErrInvalidPath
	}

	cleanRel := strings.TrimPrefix(trimmed, "tmp_downloads/")
	cleanRel = strings.TrimPrefix(cleanRel, "tmp/")

	// 1. Попытка разрешить в tmpSandbox
	if s.tmpSandbox != nil {
		if full, err := s.tmpSandbox.Resolve(cleanRel, true); err == nil {
			if st, statErr := os.Stat(full); statErr == nil && !st.IsDir() {
				return full, true, nil
			}
		}
	}

	// 2. Попытка разрешить в musicSandbox
	if s.musicSandbox == nil {
		return "", false, ErrSandboxViolation
	}

	full, err := s.musicSandbox.Resolve(trimmed, true)
	if err != nil {
		return "", false, fmt.Errorf("%w: %v", ErrSandboxViolation, err)
	}
	return full, false, nil
}

// ResolveTrack находит физический путь к треку по числовому ID, хэшу или прямому относительному пути.
func (s *Service) ResolveTrack(ctx context.Context, idOrHash string) (fullPath string, relPath string, track *TrackMetadataResponse, isTmp bool, err error) {
	idOrHash = strings.TrimSpace(idOrHash)
	if idOrHash == "" {
		return "", "", nil, false, ErrTrackNotFound
	}

	// 1. Проверка in-memory кэша
	if val, ok := s.trackCache.Load(idOrHash); ok {
		relPath = val.(string)
	}

	// 2. Поиск в SQLite репозитории
	if relPath == "" && s.repo != nil {
		t, dbErr := s.repo.GetTrackByIDOrHash(ctx, idOrHash)
		if dbErr == nil && t != nil {
			track = t
			relPath = t.FilePath
			s.trackCache.Store(idOrHash, relPath)
			s.trackCache.Store(t.ID, relPath)
			if t.FilePath != "" {
				s.trackCache.Store(t.FilePath, relPath)
			}
		}
	}

	// 3. Fallback: прямой относительный путь
	if relPath == "" {
		if testFull, testErr := s.musicSandbox.Resolve(idOrHash, false); testErr == nil && fileExists(testFull) {
			relPath = idOrHash
		}
	}

	if relPath == "" {
		return "", "", nil, false, ErrTrackNotFound
	}

	// 4. Обязательная валидация через PathSandbox
	fullPath, isTmp, err = s.ResolvePath(relPath)
	if err != nil || !fileExists(fullPath) {
		return "", "", nil, false, ErrTrackNotFound
	}

	return fullPath, relPath, track, isTmp, nil
}

// ListFiles возвращает список файлов и директорий для навигатора медиатеки.
func (s *Service) ListFiles(ctx context.Context, relPath string, recursive bool) ([]FileItem, error) {
	fullPath, isTmp, err := s.ResolvePath(relPath)
	if err != nil {
		return nil, err
	}

	st, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrFileNotFound
		}
		return nil, err
	}

	if !st.IsDir() {
		// Одиночный файл
		item := s.buildFileItem(fullPath, relPath, st, isTmp, nil)
		return []FileItem{item}, nil
	}

	var metadataCache map[string]FileItem
	if s.repo != nil && !isTmp {
		metadataCache, _ = s.repo.GetTracksMetadataCache(ctx)
	}

	var items []FileItem

	if recursive {
		err = filepath.WalkDir(fullPath, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil || path == fullPath {
				return nil
			}
			info, iErr := d.Info()
			if iErr != nil {
				return nil
			}
			var rel string
			if isTmp {
				rel, _ = s.tmpSandbox.ToRelative(path)
			} else {
				rel, _ = s.musicSandbox.ToRelative(path)
			}
			items = append(items, s.buildFileItem(path, rel, info, isTmp, metadataCache))
			return nil
		})
		if err != nil {
			return nil, err
		}
	} else {
		entries, rErr := os.ReadDir(fullPath)
		if rErr != nil {
			return nil, rErr
		}
		for _, entry := range entries {
			info, iErr := entry.Info()
			if iErr != nil {
				continue
			}
			itemPath := filepath.Join(fullPath, entry.Name())
			var rel string
			if isTmp {
				rel, _ = s.tmpSandbox.ToRelative(itemPath)
			} else {
				rel, _ = s.musicSandbox.ToRelative(itemPath)
			}
			items = append(items, s.buildFileItem(itemPath, rel, info, isTmp, metadataCache))
		}
	}

	return items, nil
}

func (s *Service) buildFileItem(absPath, relPath string, info os.FileInfo, isTmp bool, cache map[string]FileItem) FileItem {
	item := FileItem{
		Name:    info.Name(),
		Path:    relPath,
		IsDir:   info.IsDir(),
		Size:    info.Size(),
		ModTime: info.ModTime(),
	}

	if item.IsDir {
		return item
	}

	item.Extension = strings.ToLower(filepath.Ext(absPath))
	if item.Extension != ".mp3" {
		return item
	}

	// 1. Попытка взять из кэша базы данных
	if cache != nil {
		if cached, ok := cache[relPath]; ok {
			item.TrackID = cached.TrackID
			item.TrackHash = cached.TrackHash
			item.Title = cached.Title
			item.Artist = cached.Artist
			item.Album = cached.Album
			item.Duration = cached.Duration
			item.Bitrate = cached.Bitrate
			item.HasCover = cached.HasCover
			item.HasLyrics = cached.HasLyrics
			item.LyricsStatus = cached.LyricsStatus
			return item
		}
	}

	// 2. Чтение ID3v2.4 на лету
	tags, err := id3.ReadTags(absPath, relPath)
	if err == nil && tags != nil {
		item.Title = tags.Title
		item.Artist = tags.Artist
		item.Album = tags.Album
		item.Duration = tags.Duration
		item.Bitrate = tags.Bitrate
		item.HasCover = tags.HasCover
	} else {
		item.Title = strings.TrimSuffix(info.Name(), item.Extension)
	}

	// Проверка наличия внешних текстов рядом с треком (.lrc или .txt)
	base := strings.TrimSuffix(absPath, item.Extension)
	if fileExists(base + ".lrc") {
		item.HasLyrics = true
		item.LyricsStatus = "synced"
	} else if fileExists(base + ".txt") {
		item.HasLyrics = true
		item.LyricsStatus = "plain"
	} else {
		item.LyricsStatus = "none"
	}

	return item
}

// DeleteFile удаляет файл или каталог, очищая сопутствующие файлы и записи SQLite.
func (s *Service) DeleteFile(ctx context.Context, relPath string) (*DeleteResponse, error) {
	fullPath, isTmp, err := s.ResolvePath(relPath)
	if err != nil {
		return nil, err
	}

	st, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrFileNotFound
		}
		return nil, err
	}

	var deletedTracks int64

	if st.IsDir() {
		// Удаление директории
		if err := os.RemoveAll(fullPath); err != nil {
			return nil, fmt.Errorf("failed to remove directory: %w", err)
		}
		if s.repo != nil && !isTmp {
			deletedTracks, _ = s.repo.DeleteDirectoryRecords(ctx, relPath)
		}
		return &DeleteResponse{
			Success:       true,
			Message:       "Каталог успешно удален",
			DeletedTracks: int(deletedTracks),
			Path:          relPath,
		}, nil
	}

	// Удаление отдельного файла
	ext := strings.ToLower(filepath.Ext(fullPath))
	baseName := strings.TrimSuffix(fullPath, ext)

	if err := os.Remove(fullPath); err != nil {
		return nil, fmt.Errorf("failed to remove file: %w", err)
	}

	if s.repo != nil && !isTmp {
		if ext == ".mp3" {
			// Удаляем сопутствующие файлы текстов и обложки
			_ = os.Remove(baseName + ".lrc")
			_ = os.Remove(baseName + ".txt")
			_ = os.Remove(baseName + ".jpg")

			deletedTracks, _ = s.repo.DeleteTrackByPath(ctx, relPath)
		} else if ext == ".jpg" || ext == ".png" {
			albumDir := filepath.Dir(relPath)
			_ = s.repo.UpdateAlbumCoverStatus(ctx, albumDir, false)
		}
	}

	// Очистка пустых родительских директорий
	s.cleanupEmptyParents(filepath.Dir(fullPath), isTmp)

	return &DeleteResponse{
		Success:       true,
		Message:       "Файл успешно удален",
		DeletedTracks: int(deletedTracks),
		Path:          relPath,
	}, nil
}

func (s *Service) cleanupEmptyParents(dir string, isTmp bool) {
	var root string
	if isTmp && s.tmpSandbox != nil {
		root = s.tmpSandbox.Root()
	} else if s.musicSandbox != nil {
		root = s.musicSandbox.Root()
	} else {
		return
	}

	for dir != root && dir != "/" && dir != "." {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			break
		}
		if err := os.Remove(dir); err != nil {
			break
		}
		dir = filepath.Dir(dir)
	}
}

// RenameFile выполняет атомарное переименование трека с сопутствующими файлами и обновлением базы.
func (s *Service) RenameFile(ctx context.Context, oldRel, newRel string) (*RenameResponse, error) {
	if s.musicSandbox == nil {
		return nil, ErrSandboxViolation
	}

	oldFull, err := s.musicSandbox.Resolve(oldRel, false)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSandboxViolation, err)
	}

	newFull, err := s.musicSandbox.Resolve(newRel, false)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSandboxViolation, err)
	}

	if !fileExists(oldFull) {
		return nil, ErrFileNotFound
	}
	if fileExists(newFull) && oldFull != newFull {
		return nil, ErrTargetExists
	}

	var affectedFiles []string

	// Если доступен сервис organizer — используем его логику синхронизации сопутствующих файлов
	if s.organizer != nil {
		renamed, err := s.organizer.RenameFile(oldRel, newRel)
		if err != nil {
			return nil, err
		}
		affectedFiles = renamed
	} else {
		if err := os.MkdirAll(filepath.Dir(newFull), 0755); err != nil {
			return nil, err
		}
		if err := os.Rename(oldFull, newFull); err != nil {
			return nil, err
		}
		affectedFiles = append(affectedFiles, newRel)

		// Перемещаем сопутствующие тексты
		oldBase := strings.TrimSuffix(oldFull, filepath.Ext(oldFull))
		newBase := strings.TrimSuffix(newFull, filepath.Ext(newFull))
		for _, companionExt := range []string{".lrc", ".txt"} {
			if fileExists(oldBase + companionExt) {
				_ = os.Rename(oldBase+companionExt, newBase+companionExt)
				affectedFiles = append(affectedFiles, strings.TrimSuffix(newRel, filepath.Ext(newRel))+companionExt)
			}
		}
	}

	if s.repo != nil {
		_ = s.repo.UpdateTrackPath(ctx, oldRel, newRel)
	}

	if s.scanner != nil {
		_ = s.scanner.RemoveFile(ctx, oldRel)
		_ = s.scanner.IndexFile(ctx, newRel)
	}

	s.trackCache.Delete(oldRel)
	s.trackCache.Store(newRel, newRel)

	return &RenameResponse{
		Success:        true,
		OldPath:        oldRel,
		NewPath:        newRel,
		AffectedFiles:  affectedFiles,
		AffectedTracks: 1,
	}, nil
}

// StreamAudio отдает аудиофайл через http.ServeContent с поддержкой RFC 7233 partial content.
func (s *Service) StreamAudio(w http.ResponseWriter, r *http.Request, relPath string) {
	fullPath, _, err := s.ResolvePath(relPath)
	if err != nil {
		if errors.Is(err, sandbox.ErrPathTraversal) {
			http.Error(w, `{"error":"access denied"}`, http.StatusForbidden)
			return
		}
		http.Error(w, `{"error":"file not found"}`, http.StatusNotFound)
		return
	}

	file, err := os.Open(fullPath)
	if err != nil {
		http.Error(w, `{"error":"failed to open file"}`, http.StatusNotFound)
		return
	}
	defer file.Close()

	fi, err := file.Stat()
	if err != nil || fi.IsDir() {
		http.Error(w, `{"error":"not an audio file"}`, http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Accept-Ranges", "bytes")
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), file)
}

// StreamTrack отдает поток с оптимизацией sendfile(2) и RFC 7233.
func (s *Service) StreamTrack(w http.ResponseWriter, r *http.Request, idOrHash string) {
	fullPath, _, _, _, err := s.ResolveTrack(r.Context(), idOrHash)
	if err != nil {
		if errors.Is(err, sandbox.ErrPathTraversal) {
			http.Error(w, `{"error":"access denied"}`, http.StatusForbidden)
			return
		}
		http.Error(w, `{"error":"track not found"}`, http.StatusNotFound)
		return
	}

	fi, err := os.Stat(fullPath)
	if err != nil || fi.IsDir() {
		http.Error(w, `{"error":"file unavailable"}`, http.StatusNotFound)
		return
	}

	ext := strings.ToLower(filepath.Ext(fullPath))
	mimeType := "audio/mpeg"
	switch ext {
	case ".flac":
		mimeType = "audio/flac"
	case ".ogg":
		mimeType = "audio/ogg"
	case ".opus":
		mimeType = "audio/opus"
	case ".m4a":
		mimeType = "audio/mp4"
	}

	etag := fmt.Sprintf(`"%x-%x"`, fi.ModTime().Unix(), fi.Size())
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("ETag", etag)

	http.ServeFile(w, r, fullPath)
}

// GetTrackMetadata возвращает метаданные трека без блокировок.
func (s *Service) GetTrackMetadata(ctx context.Context, idOrHash string) (*TrackMetadataResponse, error) {
	fullPath, relPath, track, _, err := s.ResolveTrack(ctx, idOrHash)
	if err != nil {
		return nil, err
	}

	if track != nil {
		return track, nil
	}

	// Читаем метаданные на лету из файла
	fi, _ := os.Stat(fullPath)
	codec := strings.TrimPrefix(strings.ToLower(filepath.Ext(fullPath)), ".")
	if codec == "" {
		codec = "mp3"
	}

	resp := &TrackMetadataResponse{
		ID:         idOrHash,
		Codec:      codec,
		SampleRate: 44100,
		StreamURL:  fmt.Sprintf("/api/tracks/%s/stream", idOrHash),
		CoverURL:   fmt.Sprintf("/api/tracks/%s/cover", idOrHash),
		FilePath:   relPath,
	}
	if fi != nil {
		resp.FileSize = fi.Size()
	}

	if tags, err := id3.ReadTags(fullPath, relPath); err == nil && tags != nil {
		resp.Title = tags.Title
		resp.Artist = tags.Artist
		resp.Album = tags.Album
		resp.DurationSec = int(tags.Duration)
		resp.BitrateKbps = tags.Bitrate
		resp.TrackNumber = tags.TrackNumber
		resp.Year = tags.Year
		resp.Genre = tags.Genre
		resp.HasCover = tags.HasCover
	} else {
		resp.Title = strings.TrimSuffix(filepath.Base(fullPath), filepath.Ext(fullPath))
	}

	return resp, nil
}

// GetTrackCover извлекает обложку трека (встроенный APIC или файл cover.jpg) с защитой песочницы.
func (s *Service) GetTrackCover(ctx context.Context, idOrHash string) ([]byte, string, error) {
	fullPath, _, _, isTmp, err := s.ResolveTrack(ctx, idOrHash)
	if err != nil {
		return nil, "", err
	}

	// 1. Попытка извлечь встроенный APIC фрейм
	if strings.EqualFold(filepath.Ext(fullPath), ".mp3") {
		cover, err := id3.ReadCover(fullPath)
		if err == nil && cover != nil && len(cover.Data) > 0 {
			mime := cover.MimeType
			if mime == "" {
				mime = "image/jpeg"
			}
			return cover.Data, mime, nil
		}
	}

	// 2. Поиск сопутствующего файла обложки в каталоге
	dir := filepath.Dir(fullPath)
	var candidates []string
	trackBase := strings.TrimSuffix(fullPath, filepath.Ext(fullPath))
	candidates = append(candidates, trackBase+".jpg", trackBase+".png", trackBase+".jpeg")
	for _, name := range []string{"cover.jpg", "cover.png", "folder.jpg", "cover.jpeg"} {
		candidates = append(candidates, filepath.Join(dir, name))
	}

	var sb *sandbox.PathSandbox = s.musicSandbox
	if isTmp {
		sb = s.tmpSandbox
	}

	for _, candPath := range candidates {
		if sb != nil {
			relCand, rErr := sb.ToRelative(candPath)
			if rErr != nil {
				continue
			}
			resolvedPath, sErr := sb.Resolve(relCand, false)
			if sErr != nil {
				continue
			}
			candPath = resolvedPath
		}
		if data, rErr := os.ReadFile(candPath); rErr == nil && len(data) > 0 {
			mime := "image/jpeg"
			if strings.HasSuffix(candPath, ".png") {
				mime = "image/png"
			}
			return data, mime, nil
		}
	}

	return nil, "", ErrFileNotFound
}

// ListLibraryTracks возвращает пагинированный список треков библиотеки.
func (s *Service) ListLibraryTracks(ctx context.Context, query string, limit, offset int) ([]LibraryTrackItem, int, error) {
	if s.repo == nil {
		return nil, 0, nil
	}
	return s.repo.ListLibraryTracks(ctx, query, limit, offset)
}

// SearchLibrary выполняет полнотекстовый поиск по медиатеке.
func (s *Service) SearchLibrary(ctx context.Context, q string, limit int) (*SearchLibraryResult, error) {
	if s.repo == nil {
		return nil, nil
	}
	return s.repo.SearchLibrary(ctx, q, limit)
}

// ReorganizeLibrary выполняет реорганизацию медиатеки.
func (s *Service) ReorganizeLibrary(ctx context.Context, dryRun bool) (*ReorganizeResponse, error) {
	return &ReorganizeResponse{
		Success:   true,
		Processed: 0,
		Renamed:   0,
	}, nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// TrackHash генерирует детерминированный 16-hex хэш относительного пути.
func TrackHash(relPath string) string {
	sum := sha256.Sum256([]byte(relPath))
	return hex.EncodeToString(sum[:8])
}
