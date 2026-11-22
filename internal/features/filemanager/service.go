package filemanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"music-manager/internal/core/safefs"
	"music-manager/internal/core/sandbox"
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

// LibraryIndex coordinates deletion on disk with the SQLite index.
type LibraryIndex interface {
	DeletePath(ctx context.Context, fullPath string, directory bool, remove func() error) (int64, error)
}

// ServiceConfig определяет зависимости для инициализации сервиса FileManager.
type ServiceConfig struct {
	LibraryIndex        LibraryIndex
	MusicSandbox        *sandbox.PathSandbox
	TmpSandbox          *sandbox.PathSandbox
	Repo                Repository
	Organizer           Organizer
	Scanner             Scanner
	JobCreator          JobCreator
	SourceFinder        SourceFinder
	RedownloadQueue     RedownloadQueue
	RedownloadOrganizer RedownloadOrganizer
}

// Service инкапсулирует операции файлового менеджера, стриминга и организации медиатеки.
type Service struct {
	libraryIndex        LibraryIndex
	musicSandbox        *sandbox.PathSandbox
	tmpSandbox          *sandbox.PathSandbox
	repo                Repository
	organizer           Organizer
	scanner             Scanner
	jobCreator          JobCreator
	sourceFinder        SourceFinder
	redownloadQueue     RedownloadQueue
	redownloadOrganizer RedownloadOrganizer
	trackCache          sync.Map
}

// NewService создает новый экземпляр Service.
func NewService(cfg ServiceConfig) *Service {
	return &Service{
		musicSandbox:        cfg.MusicSandbox,
		tmpSandbox:          cfg.TmpSandbox,
		repo:                cfg.Repo,
		organizer:           cfg.Organizer,
		scanner:             cfg.Scanner,
		libraryIndex:        cfg.LibraryIndex,
		jobCreator:          cfg.JobCreator,
		sourceFinder:        cfg.SourceFinder,
		redownloadQueue:     cfg.RedownloadQueue,
		redownloadOrganizer: cfg.RedownloadOrganizer,
	}
}

// ResolvePath безопасно разрешает путь сначала во временной песочнице, затем в основной.
func (s *Service) ResolvePath(relPath string) (fullPath string, isTmp bool, err error) {
	trimmed := strings.TrimSpace(relPath)
	if trimmed == "" || trimmed == "." {
		if s.musicSandbox == nil {
			return "", false, ErrSandboxViolation
		}
		full, err := s.musicSandbox.Resolve("", true)
		if err != nil {
			return "", false, fmt.Errorf("%w: %v", ErrSandboxViolation, err)
		}
		return full, false, nil
	}

	cleanRel := strings.TrimPrefix(trimmed, "tmp_downloads/")
	cleanRel = strings.TrimPrefix(cleanRel, "tmp/")

	// 1. Попытка разрешить в tmpSandbox
	if s.tmpSandbox != nil {
		if full, err := s.tmpSandbox.Resolve(cleanRel, true); err == nil {
			if st, statErr := safefs.Stat(full); statErr == nil && !st.IsDir() {
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

	// Indexed IDs must follow a move even when an earlier request cached the
	// old path and another file has since appeared there.
	if s.repo != nil {
		t, dbErr := s.repo.GetTrackByIDOrHash(ctx, idOrHash)
		if dbErr == nil && t != nil {
			full, err := s.musicSandbox.Resolve(t.FilePath, false, sandbox.LiteralPath)
			if err != nil || !fileExists(full) {
				return "", "", nil, false, ErrTrackNotFound
			}
			return full, t.FilePath, t, false, nil
		}
	}

	if _, err := strconv.ParseInt(idOrHash, 10, 64); err != nil {
		if val, ok := s.trackCache.Load(idOrHash); ok {
			relPath = val.(string)
		}
	}

	// 3. Fallback: прямой относительный путь
	if relPath == "" {
		if testFull, testErr := s.musicSandbox.Resolve(idOrHash, false); testErr == nil && fileExists(testFull) {
			relPath = idOrHash
		} else if s.tmpSandbox != nil {
			cleanRel := strings.TrimPrefix(idOrHash, "tmp_downloads/")
			cleanRel = strings.TrimPrefix(cleanRel, "tmp/")
			if testTmp, tmpErr := s.tmpSandbox.Resolve(cleanRel, false); tmpErr == nil && fileExists(testTmp) {
				relPath = idOrHash
			}
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

// TrackHash генерирует детерминированный 16-hex хэш относительного пути.
func TrackHash(relPath string) string {
	sum := sha256.Sum256([]byte(relPath))
	return hex.EncodeToString(sum[:8])
}
