package audit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/id3"
	"music-manager/internal/services/organizer"
	"music-manager/internal/services/resolver"
)

var (
	ErrFolderNotFound      = errors.New("folder not found in library")
	ErrFileNotFound        = errors.New("file not found in library")
	ErrAlbumNotFound       = errors.New("album not found")
	ErrBatchAlreadyRunning = errors.New("batch operation is already running")
	ErrSandboxViolation    = errors.New("path traversal attempt outside sandbox root")
	ErrInvalidRequest      = errors.New("invalid request parameters")
)

// ServiceConfig defines dependencies for constructing the Audit service.
type ServiceConfig struct {
	MusicSandbox    *sandbox.PathSandbox
	TmpSandbox      *sandbox.PathSandbox
	Repo            AuditRepository
	DB              DBTX
	JobStarter      JobStarter
	BackfillStarter BackfillStarter
	Organizer       PathOrganizer
	Scanner         LibraryScanner
	Lyrics          LyricsService
	Enricher        ArtistEnricher
	Deezer          *resolver.DeezerClient
	Yandex          *resolver.YandexMusicClient
	YTMusic         *resolver.YouTubeMusicClient
	ITunes          *resolver.ITunesClient
	HTTPClient      *http.Client
}

// Service encapsulates library integrity auditing, duplicate detection and resolution,
// path reorganization, tag health auditing, batch lyrics/cover workers, and catalog matching.
type Service struct {
	musicSandbox    *sandbox.PathSandbox
	tmpSandbox      *sandbox.PathSandbox
	repo            AuditRepository
	db              DBTX
	jobStarter      JobStarter
	backfillStarter BackfillStarter
	organizer       PathOrganizer
	scanner         LibraryScanner
	lyrics          LyricsService
	enricher        ArtistEnricher
	deezer          *resolver.DeezerClient
	yandex          *resolver.YandexMusicClient
	ytmusic         *resolver.YouTubeMusicClient
	itunes          *resolver.ITunesClient
	httpClient      *http.Client

	// Lifecycle and concurrency controls
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// Background batch lyrics worker state
	lyricsMu     sync.RWMutex
	lyricsState  BatchProgress
	lyricsCancel context.CancelFunc

	// Background batch covers worker state
	coversMu     sync.RWMutex
	coversState  BatchProgress
	coversCancel context.CancelFunc
}

// NewService creates a new Service instance.
func NewService(cfg ServiceConfig) *Service {
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		musicSandbox:    cfg.MusicSandbox,
		tmpSandbox:      cfg.TmpSandbox,
		repo:            cfg.Repo,
		db:              cfg.DB,
		jobStarter:      cfg.JobStarter,
		backfillStarter: cfg.BackfillStarter,
		organizer:       cfg.Organizer,
		scanner:         cfg.Scanner,
		lyrics:          cfg.Lyrics,
		enricher:        cfg.Enricher,
		deezer:          cfg.Deezer,
		yandex:          cfg.Yandex,
		ytmusic:         cfg.YTMusic,
		itunes:          cfg.ITunes,
		httpClient:      client,
		ctx:             ctx,
		cancel:          cancel,
	}
}

// Close gracefully stops any active background batch operations and waits for completion.
func (s *Service) Close() error {
	s.cancel()

	s.lyricsMu.Lock()
	if s.lyricsCancel != nil {
		s.lyricsCancel()
	}
	s.lyricsMu.Unlock()

	s.coversMu.Lock()
	if s.coversCancel != nil {
		s.coversCancel()
	}
	s.coversMu.Unlock()

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-time.After(5 * time.Second):
		return errors.New("audit service graceful shutdown timed out")
	}
}

// ============================================================================
// 1. Audit Summary & Library Search
// ============================================================================

func (s *Service) GetSummary(ctx context.Context) (*AuditSummary, error) {
	summary, err := s.repo.GetAuditSummary(ctx)
	if err != nil {
		return nil, err
	}
	if summary == nil {
		summary = &AuditSummary{}
	}

	// Compute real-time tag issues count
	tracks, err := s.repo.GetTracksForTagAudit(ctx)
	if err == nil {
		count := 0
		for _, t := range tracks {
			if analyzeTrackIssues(t.ID, t.FilePath, t.Title, t.Artist, t.Album, t.Year, t.TrackNumber, t.Duration, t.AlbumArtist) != nil {
				count++
			}
		}
		summary.TagIssuesCount = count
	}

	return summary, nil
}

func (s *Service) Refresh(ctx context.Context) (*AuditSummary, error) {
	if s.scanner != nil {
		_, _ = s.scanner.ScanAll(ctx)
	}
	return s.GetSummary(ctx)
}

func (s *Service) Search(ctx context.Context, query string, limit int) (*SearchLibraryResult, error) {
	return s.repo.SearchLibrary(ctx, query, limit)
}

// ============================================================================
// 2. Duplicates Audit & Strict Sandbox Resolution
// ============================================================================

func (s *Service) GetDuplicates(ctx context.Context) (*DuplicatesResponse, error) {
	candidates, err := s.repo.GetTracksForDuplicatesScan(ctx)
	if err != nil {
		return nil, err
	}

	groupsMap := make(map[string][]TrackCandidateDTO)
	for _, tr := range candidates {
		albumDir := filepath.Dir(tr.FilePath)
		cleanTit := strings.ToLower(strings.TrimSpace(id3.CleanTitle(tr.Title, "")))
		key := fmt.Sprintf("%s::%s", albumDir, cleanTit)
		groupsMap[key] = append(groupsMap[key], tr)
	}

	var groups []DuplicateGroup
	totalDupes := 0

	for key, trList := range groupsMap {
		if len(trList) < 2 {
			continue
		}
		first := trList[0]
		albumDir := filepath.Dir(first.FilePath)

		var tracks []DuplicateFile
		for _, t := range trList {
			sz := t.SizeBytes
			d := t.Duration
			br := t.Bitrate
			tracks = append(tracks, DuplicateFile{
				Path:       t.FilePath,
				Filename:   filepath.Base(t.FilePath),
				SizeBytes:  &sz,
				Duration:   &d,
				Bitrate:    &br,
				ModifiedAt: time.Now().UTC().Format(time.RFC3339),
			})
		}
		totalDupes += len(tracks) - 1
		groups = append(groups, DuplicateGroup{
			Key:      key,
			Title:    first.Title,
			Artist:   first.Artist,
			AlbumDir: albumDir,
			Album:    first.Album,
			Tracks:   tracks,
		})
	}

	// Deterministic sort of duplicate groups
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].Key < groups[j].Key
	})

	return &DuplicatesResponse{
		Groups:              groups,
		TotalDuplicateFiles: totalDupes,
	}, nil
}

func (s *Service) deleteTrackAndCompanions(ctx context.Context, relPath string) error {
	if s.musicSandbox == nil {
		return errors.New("music sandbox not initialized")
	}

	// 1. Resolve target file through PathSandbox strictly
	fullPath, err := s.musicSandbox.Resolve(relPath, false)
	if err != nil {
		return fmt.Errorf("%w: %s (%v)", ErrSandboxViolation, relPath, err)
	}

	// 2. Remove audio file
	if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove audio file: %w", err)
	}

	// 3. Remove companion .lrc and .txt files through strict sandbox resolution
	dirRel := filepath.Dir(relPath)
	baseNoExt := strings.TrimSuffix(filepath.Base(relPath), filepath.Ext(relPath))

	for _, ext := range []string{".lrc", ".txt"} {
		companionRel := filepath.Join(dirRel, baseNoExt+ext)
		if companionFull, cErr := s.musicSandbox.Resolve(companionRel, false); cErr == nil {
			_ = os.Remove(companionFull)
		}
	}

	// 4. Clean up SQLite records
	if err := s.repo.DeleteTrackAndCleanup(ctx, relPath); err != nil {
		return fmt.Errorf("failed to clean up database track %s: %w", relPath, err)
	}

	// 5. Clean empty parent directories up to sandbox root
	organizer.CleanEmptyParents(filepath.Dir(fullPath), s.musicSandbox.Root())

	return nil
}

func (s *Service) FixDuplicates(ctx context.Context, pathsToDelete []string) (*FixDuplicatesResponse, error) {
	var deleted []string
	var errs []string

	for _, p := range pathsToDelete {
		if err := s.deleteTrackAndCompanions(ctx, p); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", p, err))
		} else {
			deleted = append(deleted, p)
		}
	}

	return &FixDuplicatesResponse{
		Success:      len(errs) == 0,
		DeletedCount: len(deleted),
		DeletedFiles: deleted,
		Errors:       errs,
	}, nil
}

func (s *Service) FixAllDuplicates(ctx context.Context) (*FixDuplicatesResponse, error) {
	report, err := s.GetDuplicates(ctx)
	if err != nil {
		return nil, err
	}

	var deleted []string
	var errs []string

	for _, grp := range report.Groups {
		if len(grp.Tracks) < 2 {
			continue
		}

		canonicalIdx := s.selectCanonicalTrack(grp.Tracks)

		for i, tr := range grp.Tracks {
			if i == canonicalIdx {
				continue // Preserve canonical track
			}

			if err := s.deleteTrackAndCompanions(ctx, tr.Path); err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", tr.Path, err))
			} else {
				deleted = append(deleted, tr.Path)
			}
		}
	}

	return &FixDuplicatesResponse{
		Success:      len(errs) == 0,
		DeletedCount: len(deleted),
		DeletedFiles: deleted,
		Errors:       errs,
	}, nil
}

func (s *Service) ResolveDuplicates(ctx context.Context, groupKey, keepPath string, pathsToDelete []string) (*FixDuplicatesResponse, error) {
	if len(pathsToDelete) == 0 {
		return &FixDuplicatesResponse{Success: true}, nil
	}

	var filtered []string
	for _, p := range pathsToDelete {
		if keepPath != "" && p == keepPath {
			continue
		}
		filtered = append(filtered, p)
	}

	return s.FixDuplicates(ctx, filtered)
}

func (s *Service) selectCanonicalTrack(tracks []DuplicateFile) int {
	indices := make([]int, len(tracks))
	for i := range indices {
		indices[i] = i
	}

	sort.SliceStable(indices, func(i, j int) bool {
		a := tracks[indices[i]]
		b := tracks[indices[j]]
		aName := strings.ToLower(a.Filename)
		bName := strings.ToLower(b.Filename)

		aIsDup := strings.Contains(aName, " (1)") || strings.Contains(aName, " (2)") ||
			strings.Contains(aName, "_dup") || strings.Contains(aName, " copy") || strings.Contains(aName, " - копия")
		bIsDup := strings.Contains(bName, " (1)") || strings.Contains(bName, " (2)") ||
			strings.Contains(bName, "_dup") || strings.Contains(bName, " copy") || strings.Contains(bName, " - копия")

		if aIsDup != bIsDup {
			return !aIsDup
		}

		aBr := 0
		if a.Bitrate != nil {
			aBr = *a.Bitrate
		}
		bBr := 0
		if b.Bitrate != nil {
			bBr = *b.Bitrate
		}
		if aBr != bBr {
			return aBr > bBr
		}

		var aSz, bSz int64
		if a.SizeBytes != nil {
			aSz = *a.SizeBytes
		}
		if b.SizeBytes != nil {
			bSz = *b.SizeBytes
		}
		if aSz != bSz {
			return aSz > bSz
		}

		return len(aName) < len(bName)
	})

	return indices[0]
}

// ============================================================================
// 3. Invalid Paths & Reorganization
// ============================================================================

func (s *Service) GetInvalidPaths(ctx context.Context) ([]InvalidPathRecord, error) {
	return s.repo.GetInvalidPaths(ctx)
}

func (s *Service) FixPath(ctx context.Context, relPath string, overwrite bool) (*FixPathResponse, error) {
	if s.musicSandbox == nil {
		return nil, errors.New("music sandbox not initialized")
	}

	fullPath, err := s.musicSandbox.Resolve(relPath, false)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFileNotFound, err)
	}

	tags, err := id3.ReadTags(fullPath, relPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read tags: %w", err)
	}

	album := strings.TrimSpace(tags.Album)
	if album == "" || strings.EqualFold(album, "single") || strings.EqualFold(album, "singles") {
		if strings.TrimSpace(tags.Title) != "" {
			album = strings.TrimSpace(tags.Title)
		}
	}
	artist := strings.TrimSpace(tags.AlbumArtist)
	if artist == "" {
		artist = strings.TrimSpace(tags.Artist)
	}
	if artist == "" {
		artist = "Unknown Artist"
	}

	var targetRel string
	if s.organizer != nil {
		_, targetRel = s.organizer.ComputeTargetPath(artist, album, tags.TrackNumber, tags.Title, filepath.Base(fullPath))
	} else {
		targetRel = filepath.Join(artist, album, fmt.Sprintf("%02d. %s.mp3", tags.TrackNumber, tags.Title))
	}

	targetFull, err := s.musicSandbox.Resolve(targetRel, true)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to resolve target %s: %v", ErrSandboxViolation, targetRel, err)
	}

	if fullPath != targetFull {
		oldDir := filepath.Dir(fullPath)
		actualDest, err := organizer.MoveTrackWithCompanions(fullPath, targetFull, overwrite)
		if err != nil {
			return nil, fmt.Errorf("failed to move file: %w", err)
		}

		actualRel, _ := s.musicSandbox.ToRelative(actualDest)
		_ = s.repo.UpdateTrackPath(ctx, relPath, actualRel)
		_ = s.repo.DeleteAuditIssueByPath(ctx, relPath, "invalid_path")
		targetRel = actualRel

		organizer.CleanEmptyParents(oldDir, s.musicSandbox.Root())
	}

	return &FixPathResponse{
		Success:    true,
		SourcePath: relPath,
		TargetPath: targetRel,
		Message:    "Путь успешно исправлен на канонический",
	}, nil
}

func (s *Service) FixAllPaths(ctx context.Context) (*FixAllPathsResponse, error) {
	paths, err := s.repo.GetInvalidPaths(ctx)
	if err != nil {
		return nil, err
	}

	fixed := 0
	var errs []string
	oldDirs := make(map[string]bool)

	for _, p := range paths {
		fullPath, err := s.musicSandbox.Resolve(p.FilePath, false)
		if err != nil {
			continue
		}
		targetFull, err := s.musicSandbox.Resolve(p.SuggestedPath, true)
		if err != nil {
			continue
		}

		oldDir := filepath.Dir(fullPath)
		actualDest, err := organizer.MoveTrackWithCompanions(fullPath, targetFull, false)

		if err == nil {
			actualRel, _ := s.musicSandbox.ToRelative(actualDest)
			_ = s.repo.UpdateTrackPath(ctx, p.FilePath, actualRel)
			_ = s.repo.DeleteAuditIssueByPath(ctx, p.FilePath, "invalid_path")
			fixed++
			oldDirs[oldDir] = true
		} else {
			errs = append(errs, fmt.Sprintf("%s: %v", p.FilePath, err))
		}
	}

	root := s.musicSandbox.Root()
	for dir := range oldDirs {
		organizer.CleanEmptyParents(dir, root)
	}

	return &FixAllPathsResponse{
		Success:      len(errs) == 0,
		TotalInvalid: len(paths),
		FixedCount:   fixed,
		Errors:       errs,
	}, nil
}

func (s *Service) Reorganize(ctx context.Context) (*ReorganizeLibraryResponse, error) {
	paths, err := s.repo.GetInvalidPaths(ctx)
	if err != nil {
		return nil, err
	}

	moved := 0
	oldDirs := make(map[string]bool)

	for _, tr := range paths {
		if tr.SuggestedPath != tr.FilePath {
			srcFull, err := s.musicSandbox.Resolve(tr.FilePath, false)
			if err != nil {
				continue
			}
			dstFull, err := s.musicSandbox.Resolve(tr.SuggestedPath, true)
			if err != nil {
				continue
			}

			oldDir := filepath.Dir(srcFull)
			actualDest, err := organizer.MoveTrackWithCompanions(srcFull, dstFull, false)

			if err == nil {
				actualRel, _ := s.musicSandbox.ToRelative(actualDest)
				moved++
				_ = s.repo.UpdateTrackPath(ctx, tr.FilePath, actualRel)
				_ = s.repo.DeleteAuditIssueByPath(ctx, tr.FilePath, "invalid_path")
				oldDirs[oldDir] = true
			}
		}
	}

	root := s.musicSandbox.Root()
	for dir := range oldDirs {
		organizer.CleanEmptyParents(dir, root)
	}

	if s.scanner != nil {
		_, _ = s.scanner.ScanAll(ctx)
	}

	return &ReorganizeLibraryResponse{
		Success:     true,
		TotalTracks: len(paths),
		MovedTracks: moved,
		TotalAlbums: 1,
		Errors:      []string{},
	}, nil
}

func (s *Service) NormalizeNavidrome(ctx context.Context) (*NavidromeReport, error) {
	if s.organizer == nil {
		return nil, errors.New("organizer service is not initialized")
	}

	onTrack := func(relPath string) {
		if s.scanner != nil {
			if full, err := s.musicSandbox.Resolve(relPath, false); err == nil {
				_ = s.scanner.IndexFile(full, relPath)
			}
		}
	}

	rep, err := s.organizer.NormalizeNavidromeLibrary(ctx, onTrack)
	if err != nil {
		return nil, err
	}

	return &NavidromeReport{
		DirectoriesFixed: rep.DirectoriesFixed,
		FilesPermsFixed:  rep.FilesPermsFixed,
		TagsNormalized:   rep.TagsNormalized,
		SinglesFixed:     rep.SinglesFixed,
		TotalScanned:     rep.TotalScanned,
	}, nil
}

// ============================================================================
// 4. Missing Lyrics & Background Worker
// ============================================================================

func (s *Service) GetMissingLyrics(ctx context.Context, limit int) ([]MissingLyricsItem, error) {
	return s.repo.GetTracksMissingLyrics(ctx, limit)
}

func (s *Service) StartBatchLyrics(ctx context.Context) (BatchProgress, error) {
	s.lyricsMu.Lock()
	if s.lyricsState.IsRunning {
		state := s.lyricsState
		s.lyricsMu.Unlock()
		return state, ErrBatchAlreadyRunning
	}

	tracks, err := s.repo.GetTracksForLyricsFetch(ctx)
	if err != nil {
		s.lyricsMu.Unlock()
		return BatchProgress{}, err
	}

	if len(tracks) == 0 {
		s.lyricsMu.Unlock()
		return BatchProgress{IsRunning: false}, nil
	}

	workerCtx, cancel := context.WithCancel(s.ctx)
	s.lyricsCancel = cancel
	s.lyricsState = BatchProgress{
		IsRunning: true,
		Total:     len(tracks),
		Current:   0,
		Found:     0,
		Failed:    0,
	}
	s.lyricsMu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.lyricsMu.Lock()
			s.lyricsState.IsRunning = false
			s.lyricsState.CurrentTrack = ""
			s.lyricsMu.Unlock()
		}()

		for i, tr := range tracks {
			select {
			case <-workerCtx.Done():
				return
			default:
			}

			s.lyricsMu.Lock()
			s.lyricsState.Current = i + 1
			s.lyricsState.CurrentTrack = fmt.Sprintf("%s - %s", tr.Artist, tr.Title)
			s.lyricsMu.Unlock()

			if s.lyrics != nil {
				res, err := s.lyrics.SearchLyrics(workerCtx, tr.Artist, tr.Title, tr.Album, tr.Duration)
				if err == nil && res != nil && res.Lyrics != "" {
					saveErr := s.lyrics.SaveLyrics(tr.FilePath, res.Lyrics, "auto")
					if saveErr == nil {
						kind := "txt"
						if res.Synced {
							kind = "lrc"
						}
						_ = s.repo.SetTrackLyricsStatusAndResolve(workerCtx, tr.FilePath, kind)

						s.lyricsMu.Lock()
						s.lyricsState.Found++
						s.lyricsMu.Unlock()
					} else {
						s.lyricsMu.Lock()
						s.lyricsState.Failed++
						s.lyricsMu.Unlock()
					}
				} else {
					s.lyricsMu.Lock()
					s.lyricsState.Failed++
					s.lyricsMu.Unlock()
				}
			}

			time.Sleep(100 * time.Millisecond)
		}
	}()

	s.lyricsMu.RLock()
	defer s.lyricsMu.RUnlock()
	return s.lyricsState, nil
}

func (s *Service) GetBatchLyricsProgress() BatchProgress {
	s.lyricsMu.RLock()
	defer s.lyricsMu.RUnlock()
	return s.lyricsState
}

func (s *Service) CancelBatchLyrics() bool {
	s.lyricsMu.Lock()
	defer s.lyricsMu.Unlock()
	if s.lyricsCancel != nil && s.lyricsState.IsRunning {
		s.lyricsCancel()
		s.lyricsState.IsRunning = false
		return true
	}
	return false
}

// ============================================================================
// 5. Missing Covers & Background Worker
// ============================================================================

func (s *Service) GetMissingCovers(ctx context.Context, limit int) ([]MissingCoverItem, error) {
	return s.repo.GetAlbumsMissingCovers(ctx, limit)
}

func (s *Service) BackfillAlbumCover(ctx context.Context, folderPath string, albumID int64) (*BackfillCoverResponse, error) {
	folderPath = strings.TrimSpace(folderPath)
	if folderPath == "" && albumID > 0 {
		meta, _ := s.repo.GetAlbumMetadataByPath(ctx, strconv.FormatInt(albumID, 10))
		if meta != nil {
			folderPath = meta.FolderPath
		}
	}

	if folderPath == "" {
		return nil, fmt.Errorf("%w: missing folder_path or album_id", ErrInvalidRequest)
	}

	fullDir, err := s.musicSandbox.Resolve(folderPath, false)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFolderNotFound, err)
	}

	meta, _ := s.repo.GetAlbumMetadataByPath(ctx, folderPath)
	albumTitle := ""
	artistName := ""
	if meta != nil {
		albumTitle = meta.Title
		artistName = meta.Artist
	}
	if albumTitle == "" {
		albumTitle = filepath.Base(fullDir)
	}

	coverBytes, mime, cErr := s.fetchBestCoverOnline(ctx, artistName, albumTitle)
	if cErr != nil {
		return nil, fmt.Errorf("failed to find cover online: %w", cErr)
	}

	tracksUpdated, sErr := s.applyCoverToAlbumDir(fullDir, folderPath, coverBytes, mime)
	if sErr != nil {
		return nil, fmt.Errorf("failed to save cover: %w", sErr)
	}

	return &BackfillCoverResponse{
		Success:       true,
		FolderPath:    folderPath,
		TracksUpdated: tracksUpdated,
		Album:         albumTitle,
		Artist:        artistName,
	}, nil
}

func (s *Service) StartBatchCovers(ctx context.Context) (BatchProgress, error) {
	s.coversMu.Lock()
	if s.coversState.IsRunning {
		state := s.coversState
		s.coversMu.Unlock()
		return state, ErrBatchAlreadyRunning
	}

	missing, err := s.repo.GetAlbumsMissingCovers(ctx, 1000)
	if err != nil {
		s.coversMu.Unlock()
		return BatchProgress{}, err
	}

	if len(missing) == 0 {
		s.coversMu.Unlock()
		return BatchProgress{IsRunning: false}, nil
	}

	workerCtx, cancel := context.WithCancel(s.ctx)
	s.coversCancel = cancel
	s.coversState = BatchProgress{
		IsRunning: true,
		Total:     len(missing),
		Current:   0,
		Found:     0,
		Failed:    0,
	}
	s.coversMu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.coversMu.Lock()
			s.coversState.IsRunning = false
			s.coversState.CurrentAlbum = ""
			s.coversMu.Unlock()
		}()

		for i, a := range missing {
			select {
			case <-workerCtx.Done():
				return
			default:
			}

			s.coversMu.Lock()
			s.coversState.Current = i + 1
			s.coversState.CurrentAlbum = fmt.Sprintf("%s - %s", a.Artist, a.Title)
			s.coversMu.Unlock()

			fullDir, rErr := s.musicSandbox.Resolve(a.Path, false)
			if rErr != nil {
				s.coversMu.Lock()
				s.coversState.Failed++
				s.coversMu.Unlock()
				continue
			}

			coverBytes, mime, cErr := s.fetchBestCoverOnline(workerCtx, a.Artist, a.Title)
			if cErr != nil || len(coverBytes) == 0 {
				s.coversMu.Lock()
				s.coversState.Failed++
				s.coversMu.Unlock()
				time.Sleep(300 * time.Millisecond)
				continue
			}

			if _, err := s.applyCoverToAlbumDir(fullDir, a.Path, coverBytes, mime); err == nil {
				s.coversMu.Lock()
				s.coversState.Found++
				s.coversMu.Unlock()
			} else {
				s.coversMu.Lock()
				s.coversState.Failed++
				s.coversMu.Unlock()
			}

			time.Sleep(350 * time.Millisecond)
		}
	}()

	s.coversMu.RLock()
	defer s.coversMu.RUnlock()
	return s.coversState, nil
}

func (s *Service) GetBatchCoversProgress() BatchProgress {
	s.coversMu.RLock()
	defer s.coversMu.RUnlock()
	return s.coversState
}

func (s *Service) CancelBatchCovers() bool {
	s.coversMu.Lock()
	defer s.coversMu.Unlock()
	if s.coversCancel != nil && s.coversState.IsRunning {
		s.coversCancel()
		s.coversState.IsRunning = false
		return true
	}
	return false
}

func (s *Service) fetchBestCoverOnline(ctx context.Context, artist, album string) ([]byte, string, error) {
	client := s.httpClient
	cleanArt := strings.TrimSpace(artist)
	cleanAlb := strings.TrimSpace(album)
	query := fmt.Sprintf("%s %s", cleanArt, cleanAlb)

	// 1. Try iTunes Search API (1400x1400 HQ art)
	if s.itunes != nil {
		tracks, err := s.itunes.SearchTracks(ctx, query)
		if err == nil && len(tracks) > 0 {
			for _, tr := range tracks {
				if tr.CoverURL != "" {
					hqArt := strings.Replace(tr.CoverURL, "100x100bb", "1400x1400bb", 1)
					if data, mime, err := downloadImageWithMime(ctx, client, hqArt); err == nil && len(data) > 0 {
						return data, mime, nil
					}
				}
			}
		}
	}

	// 2. Try Deezer API (1000x1000 cover_xl)
	if s.deezer != nil {
		albums, err := s.deezer.SearchAlbums(ctx, query)
		if err == nil && len(albums) > 0 {
			for _, alb := range albums {
				bestURL := alb.BestCoverURL()
				if bestURL != "" {
					if data, mime, err := downloadImageWithMime(ctx, client, bestURL); err == nil && len(data) > 0 {
						return data, mime, nil
					}
				}
			}
		}
	}

	return nil, "", fmt.Errorf("cover not found online for %q", query)
}

func (s *Service) applyCoverToAlbumDir(fullDir, folderRelPath string, coverBytes []byte, mime string) (int, error) {
	if len(coverBytes) == 0 {
		return 0, fmt.Errorf("empty cover bytes")
	}

	ext := ".jpg"
	if mime == "image/png" || bytes.HasPrefix(coverBytes, []byte("\x89PNG")) {
		ext = ".png"
		mime = "image/png"
	} else {
		mime = "image/jpeg"
	}

	coverPath := filepath.Join(fullDir, "cover"+ext)
	if err := os.WriteFile(coverPath, coverBytes, 0644); err != nil {
		return 0, fmt.Errorf("failed to write album cover: %w", err)
	}
	_ = os.Chmod(coverPath, 0644)

	entries, err := os.ReadDir(fullDir)
	if err != nil {
		return 0, err
	}

	coverInfo := &id3.CoverInfo{
		MimeType: mime,
		Data:     coverBytes,
	}

	tracksUpdated := 0
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".mp3") {
			continue
		}

		baseName := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		trackCoverPath := filepath.Join(fullDir, baseName+ext)
		_ = os.WriteFile(trackCoverPath, coverBytes, 0644)
		_ = os.Chmod(trackCoverPath, 0644)

		mp3Path := filepath.Join(fullDir, e.Name())
		relTrack, _ := s.musicSandbox.ToRelative(mp3Path)

		tags, rErr := id3.ReadTags(mp3Path, relTrack)
		if rErr == nil && tags != nil {
			if wErr := id3.WriteTags(mp3Path, *tags, coverInfo); wErr == nil {
				tracksUpdated++
				if s.scanner != nil {
					_ = s.scanner.IndexFile(mp3Path, relTrack)
				}
			}
		}
	}

	relCover, _ := s.musicSandbox.ToRelative(coverPath)
	_ = s.repo.UpdateAlbumCover(context.Background(), folderRelPath, relCover, mime)

	return tracksUpdated, nil
}

func downloadImageWithMime(ctx context.Context, client *http.Client, urlStr string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, "", err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}

	mime := resp.Header.Get("Content-Type")
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	if strings.Contains(mime, "png") || bytes.HasPrefix(data, []byte("\x89PNG")) {
		mime = "image/png"
	} else {
		mime = "image/jpeg"
	}

	return data, mime, nil
}

// ============================================================================
// 6. Non-English Genres
// ============================================================================

func (s *Service) GetNonEnGenres(ctx context.Context) ([]NonEnGenreItem, error) {
	return s.repo.GetNonEnGenreTracks(ctx)
}

func (s *Service) RetagGenres(ctx context.Context) (*RetagGenresResponse, error) {
	items, err := s.repo.GetNonEnGenreTracks(ctx)
	if err != nil {
		return nil, err
	}

	var updatedItems []NonEnGenreItem
	var errs []string
	updatedCount := 0

	for _, t := range items {
		canon := t.CanonicalGenre
		if canon == "" {
			normRes := id3.AnalyzeAndNormalizeGenre(t.CurrentGenre)
			canon = normRes.CanonicalGenre
		}
		if canon == "" {
			canon = t.CurrentGenre
		}

		fullPath, err := s.musicSandbox.Resolve(t.Path, false)
		if err != nil {
			errs = append(errs, fmt.Sprintf("invalid path %s: %v", t.Path, err))
			continue
		}

		currentTags, err := id3.ReadTags(fullPath, t.Path)
		if err != nil {
			errs = append(errs, fmt.Sprintf("read tags error %s: %v", t.Path, err))
			continue
		}

		currentTags.Genre = canon
		if err := id3.WriteTags(fullPath, *currentTags, nil); err != nil {
			errs = append(errs, fmt.Sprintf("write tags error %s: %v", t.Path, err))
			continue
		}

		// Update repository
		var trackID int64
		if s.db != nil {
			_ = s.db.QueryRowContext(ctx, "SELECT id FROM tracks WHERE file_path = ?;", t.Path).Scan(&trackID)
		}
		if trackID > 0 {
			_ = s.repo.RetagTrackGenre(ctx, trackID, canon)
		}

		updatedCount++
		updatedItems = append(updatedItems, NonEnGenreItem{
			Path:           t.Path,
			CurrentGenre:   t.CurrentGenre,
			CanonicalGenre: canon,
			IsAutoFixed:    true,
		})
	}

	return &RetagGenresResponse{
		Success:      len(errs) == 0,
		UpdatedCount: updatedCount,
		TotalChecked: len(items),
		Items:        updatedItems,
		Errors:       errs,
	}, nil
}

// ============================================================================
// 7. Incomplete Albums & Backfill
// ============================================================================

func (s *Service) GetIncompleteAlbums(ctx context.Context) (*IncompleteAlbumsResponse, error) {
	dtos, err := s.repo.GetIncompleteAlbums(ctx, 100)
	if err != nil {
		return nil, err
	}

	var albums []IncompleteAlbum
	totalMissing := 0

	for _, a := range dtos {
		nums, _ := s.repo.GetMissingTrackNumbers(ctx, a.ID)
		if len(nums) == 0 && a.ExpectedCount > a.PresentCount {
			present, _ := s.repo.GetPresentTrackNumbers(ctx, a.ID)
			presentMap := make(map[int]bool)
			for _, p := range present {
				presentMap[p] = true
			}
			for n := 1; n <= a.ExpectedCount; n++ {
				if !presentMap[n] {
					nums = append(nums, n)
				}
			}
		}

		totalMissing += len(nums)
		isCatMatched := a.DeezerAlbumID != nil && *a.DeezerAlbumID != ""
		albums = append(albums, IncompleteAlbum{
			Path:                a.FolderPath,
			Album:               a.Title,
			Artist:              a.Artist,
			Year:                a.Year,
			HasCover:            a.HasCover,
			CoverURL:            a.CoverPath,
			PresentCount:        a.PresentCount,
			ExpectedCount:       a.ExpectedCount,
			MissingTrackNumbers: nums,
			IsCatalogMatched:    isCatMatched,
			DeezerAlbumID:       a.DeezerAlbumID,
		})
	}

	return &IncompleteAlbumsResponse{
		Albums:                albums,
		TotalIncompleteAlbums: len(albums),
		TotalMissingTracks:    totalMissing,
	}, nil
}

func (s *Service) CatalogMatchAlbum(ctx context.Context, albumPath, deezerID string) (map[string]any, error) {
	meta, err := s.repo.GetAlbumMetadataByPath(ctx, albumPath)
	if err != nil {
		meta = &IncompleteAlbumDTO{FolderPath: albumPath}
	}

	if deezerID == "" && meta.DeezerAlbumID != nil {
		deezerID = *meta.DeezerAlbumID
	}

	var matchedAlbum *resolver.DeezerAlbum
	if deezerID != "" && s.deezer != nil {
		matchedAlbum, _ = s.deezer.GetAlbum(ctx, deezerID)
	}

	if matchedAlbum == nil && s.deezer != nil && meta.Artist != "" && meta.Title != "" {
		q := fmt.Sprintf(`artist:"%s" album:"%s"`, meta.Artist, meta.Title)
		albums, _ := s.deezer.SearchAlbums(ctx, q)
		if len(albums) > 0 {
			matchedAlbum, _ = s.deezer.GetAlbum(ctx, strconv.FormatInt(albums[0].ID, 10))
		}
	}

	if matchedAlbum == nil {
		return map[string]any{
			"path":                  albumPath,
			"album":                 filepath.Base(albumPath),
			"present_count":         meta.PresentCount,
			"expected_count":        0,
			"missing_track_numbers": []int{},
			"missing_tracks":        []any{},
			"all_tracks":            []any{},
			"is_catalog_matched":    false,
		}, nil
	}

	cover := matchedAlbum.BestCoverURL()
	year := matchedAlbum.ReleaseYear()
	expectedCount := matchedAlbum.NbTracks

	presentNums, _ := s.repo.GetPresentTrackNumbers(ctx, meta.ID)
	presentMap := make(map[int]bool)
	for _, p := range presentNums {
		presentMap[p] = true
	}

	var allTracks []map[string]any
	var missingTracks []map[string]any
	var missingNums []int
	var missingDTOs []MissingTrackEntityDTO
	presentCount := 0

	if matchedAlbum.Tracks != nil {
		type detailedTrackInfo struct {
			contributors []resolver.DeezerArtist
		}
		trackDetails := make([]detailedTrackInfo, len(matchedAlbum.Tracks.Data))
		if s.deezer != nil {
			var wg sync.WaitGroup
			sem := make(chan struct{}, 3) // Ограничение: максимум 3 параллельных запроса для защиты от Rate-Limit
			for i, t := range matchedAlbum.Tracks.Data {
				if t.ID <= 0 {
					continue
				}
				wg.Add(1)
				go func(idx int, trackID int64) {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()

					tCtx, tCancel := context.WithTimeout(ctx, 3*time.Second)
					defer tCancel()
					if tr, trErr := s.deezer.GetTrack(tCtx, strconv.FormatInt(trackID, 10)); trErr == nil && tr != nil {
						trackDetails[idx] = detailedTrackInfo{contributors: tr.Contributors}
					}
				}(i, t.ID)
			}
			wg.Wait()
		}

		for i, t := range matchedAlbum.Tracks.Data {
			pos := t.TrackPosition
			if pos == 0 {
				pos = i + 1
			}
			isPresent := presentMap[pos]
			if isPresent {
				presentCount++
			} else {
				missingNums = append(missingNums, pos)
			}

			discNum := t.DiskNumber
			if discNum <= 0 {
				discNum = 1
			}

			trackArtist := t.Artist.Name
			var contribs []resolver.DeezerArtist
			if i < len(trackDetails) && len(trackDetails[i].contributors) > 0 {
				contribs = trackDetails[i].contributors
			} else if len(t.Contributors) > 0 {
				contribs = t.Contributors
			}
			if len(contribs) > 0 {
				var artList []string
				for _, c := range contribs {
					if c.Name != "" {
						artList = append(artList, c.Name)
					}
				}
				if len(artList) > 0 {
					trackArtist = id3.FormatMultiValue(artList)
				}
			}

			trInfo := map[string]any{
				"track_number": pos,
				"disc_number":  discNum,
				"title":        id3.CleanTitle(t.Title, ""),
				"artist":       trackArtist,
				"duration":     t.Duration,
				"deezer_id":    strconv.FormatInt(t.ID, 10),
				"deezer_url":   fmt.Sprintf("https://www.deezer.com/track/%d", t.ID),
				"is_present":   isPresent,
			}
			allTracks = append(allTracks, trInfo)
			if !isPresent {
				missingTracks = append(missingTracks, trInfo)
				missingDTOs = append(missingDTOs, MissingTrackEntityDTO{
					AlbumID:          meta.ID,
					Title:            id3.CleanTitle(t.Title, ""),
					Artist:           trackArtist,
					TrackNumber:      pos,
					DiscNumber:       discNum,
					ExpectedDuration: t.Duration,
					DeezerID:         strconv.FormatInt(t.ID, 10),
					DeezerURL:        fmt.Sprintf("https://www.deezer.com/track/%d", t.ID),
				})
			}
		}
	}

	isComplete := len(missingTracks) == 0 && (expectedCount <= 1 || presentCount >= expectedCount)
	if meta.ID > 0 {
		_ = s.repo.UpdateAlbumVerification(ctx, meta.ID, expectedCount, presentCount, strconv.FormatInt(matchedAlbum.ID, 10), isComplete)
		_ = s.repo.ReplaceMissingTracks(ctx, meta.ID, missingDTOs)
	}

	return map[string]any{
		"path":                  albumPath,
		"album":                 matchedAlbum.Title,
		"artist":                matchedAlbum.Artist.Name,
		"year":                  year,
		"has_cover":             cover != "",
		"cover_url":             cover,
		"present_count":         presentCount,
		"expected_count":        expectedCount,
		"missing_track_numbers": missingNums,
		"missing_tracks":        missingTracks,
		"all_tracks":            allTracks,
		"is_catalog_matched":    true,
		"deezer_album_id":       strconv.FormatInt(matchedAlbum.ID, 10),
	}, nil
}

func (s *Service) GetBackfillEditorData(ctx context.Context, albumPath, jobID string) (*BackfillEditorDataResponse, error) {
	var localTracks []id3.TrackTags
	var backfillTracks []id3.TrackTags

	if albumPath != "" && s.musicSandbox != nil {
		if fullPath, err := s.musicSandbox.Resolve(albumPath, true); err == nil {
			entries, _ := os.ReadDir(fullPath)
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".mp3") {
					itemRel := filepath.Join(albumPath, e.Name())
					itemFull := filepath.Join(fullPath, e.Name())
					if t, err := id3.ReadTags(itemFull, itemRel); err == nil {
						localTracks = append(localTracks, *t)
					}
				}
			}
		}
	}

	canonicalArtist := ""
	canonicalAlbum := ""
	canonicalGenre := ""
	canonicalYear := 0

	for _, t := range localTracks {
		if canonicalArtist == "" {
			if t.AlbumArtist != "" {
				canonicalArtist = t.AlbumArtist
			} else if t.Artist != "" {
				canonicalArtist = t.Artist
			}
		}
		if canonicalAlbum == "" && t.Album != "" {
			canonicalAlbum = t.Album
		}
		if canonicalGenre == "" && t.Genre != "" {
			canonicalGenre = t.Genre
		}
		if canonicalYear == 0 && t.Year > 0 {
			canonicalYear = t.Year
		}
	}

	if jobID != "" && s.tmpSandbox != nil {
		jobDirRel := filepath.Join(jobID, "converted")
		if fullJobDir, err := s.tmpSandbox.Resolve(jobDirRel, false); err == nil {
			entries, _ := os.ReadDir(fullJobDir)
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".mp3") {
					itemRel := filepath.Join("tmp_downloads", jobID, "converted", e.Name())
					itemFull := filepath.Join(fullJobDir, e.Name())
					if t, err := id3.ReadTags(itemFull, itemRel); err == nil {
						backfillTracks = append(backfillTracks, *t)
					}
				}
			}
		}
	}

	combined := append(localTracks, backfillTracks...)
	sort.SliceStable(combined, func(i, j int) bool {
		if combined[i].DiscNumber != combined[j].DiscNumber {
			return combined[i].DiscNumber < combined[j].DiscNumber
		}
		if combined[i].TrackNumber != combined[j].TrackNumber {
			return combined[i].TrackNumber < combined[j].TrackNumber
		}
		return combined[i].Title < combined[j].Title
	})

	var tracksAny []any
	for _, t := range combined {
		tracksAny = append(tracksAny, t)
	}

	var jobIDs []string
	if jobID != "" {
		jobIDs = append(jobIDs, jobID)
	}

	coverPath := ""
	hasCover := false

	if albumPath != "" && s.musicSandbox != nil {
		for _, name := range []string{"cover.jpg", "cover.png", "folder.jpg", "front.jpg"} {
			rel := filepath.Join(albumPath, name)
			if full, err := s.musicSandbox.Resolve(rel, true); err == nil {
				if _, err := os.Stat(full); err == nil {
					coverPath = rel
					hasCover = true
					break
				}
			}
		}
	}
	if !hasCover && jobID != "" && s.tmpSandbox != nil {
		rel := filepath.Join(jobID, "converted", "cover.jpg")
		if full, err := s.tmpSandbox.Resolve(rel, false); err == nil {
			if _, err := os.Stat(full); err == nil {
				coverPath = filepath.Join("tmp_downloads", jobID, "converted", "cover.jpg")
				hasCover = true
			}
		}
	}
	if !hasCover && jobID != "" && s.tmpSandbox != nil {
		rel := filepath.Join(jobID, "cover.jpg")
		if full, err := s.tmpSandbox.Resolve(rel, false); err == nil {
			if _, err := os.Stat(full); err == nil {
				coverPath = filepath.Join("tmp_downloads", jobID, "cover.jpg")
				hasCover = true
			}
		}
	}
	if !hasCover {
		for _, t := range combined {
			if t.HasCover {
				hasCover = true
				break
			}
		}
	}

	return &BackfillEditorDataResponse{
		Album: map[string]any{
			"album_artist": canonicalArtist,
			"album":        canonicalAlbum,
			"genre":        canonicalGenre,
			"year":         canonicalYear,
			"track_total":  len(combined),
		},
		Tracks:          tracksAny,
		SourceDir:       albumPath,
		CoverPath:       coverPath,
		HasCover:        hasCover,
		JobIDs:          jobIDs,
		MissingCount:    0,
		BackfilledCount: len(combined),
	}, nil
}

func (s *Service) DownloadMissing(ctx context.Context, req DownloadMissingRequest) ([]*BackfillJobInfo, error) {
	if s.backfillStarter == nil {
		return nil, errors.New("backfill starter is not configured")
	}

	meta, _ := s.repo.GetAlbumMetadataByPath(ctx, req.Path)
	albumTitle := ""
	artistName := ""
	deezerID := ""
	if meta != nil {
		albumTitle = meta.Title
		artistName = meta.Artist
		if meta.DeezerAlbumID != nil {
			deezerID = *meta.DeezerAlbumID
		}
	}
	if req.DeezerAlbumID != nil && *req.DeezerAlbumID != "" {
		deezerID = *req.DeezerAlbumID
	}
	if albumTitle == "" {
		albumTitle = filepath.Base(req.Path)
	}

	jobID, err := s.backfillStarter.CreateBackfillJob(
		ctx,
		req.Path,
		albumTitle,
		artistName,
		deezerID,
		req.TrackURLs,
		req.TrackNumbers,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to trigger backfill job: %w", err)
	}

	jobInfo := &BackfillJobInfo{
		ID:              jobID,
		Status:          "queued",
		IsBackfill:      true,
		TargetAlbumPath: &req.Path,
		AlbumTitle:      &albumTitle,
		ArtistName:      &artistName,
	}

	return []*BackfillJobInfo{jobInfo}, nil
}

// ============================================================================
// 8. Tag Issues & Catalog Matching
// ============================================================================

func (s *Service) GetTagIssues(ctx context.Context, filter string, limit int) (*TagIssuesResponse, error) {
	tracks, err := s.repo.GetTracksForTagAudit(ctx)
	if err != nil {
		return nil, err
	}

	var issues []TagIssue
	totalCount := 0

	for _, t := range tracks {
		item := analyzeTrackIssues(t.ID, t.FilePath, t.Title, t.Artist, t.Album, t.Year, t.TrackNumber, t.Duration, t.AlbumArtist)
		if item == nil {
			continue
		}

		if filter != "" && filter != "all" {
			matched := false
			if filter == "safe" || filter == "safe_autofix" {
				matched = (item.Severity == "safe_autofix")
			} else if filter == "review" || filter == "review_needed" {
				matched = (item.Severity == "review_needed")
			} else {
				for _, iss := range item.Issues {
					if iss == filter {
						matched = true
						break
					}
					if filter == "missing_tags" && (iss == "missing_year" || iss == "missing_album") {
						matched = true
						break
					}
				}
			}
			if !matched {
				continue
			}
		}

		totalCount++
		if limit <= 0 || len(issues) < limit {
			issues = append(issues, *item)
		}
	}

	return &TagIssuesResponse{
		Issues:     issues,
		TotalCount: totalCount,
	}, nil
}

func (s *Service) FixTagIssue(ctx context.Context, req FixTagIssueRequest) (*id3.TrackTags, error) {
	if s.musicSandbox == nil {
		return nil, errors.New("music sandbox not initialized")
	}

	fullPath, err := s.musicSandbox.Resolve(req.Path, false)
	if err != nil {
		if errors.Is(err, sandbox.ErrPathTraversal) {
			return nil, ErrSandboxViolation
		}
		return nil, fmt.Errorf("%w: %v", ErrFileNotFound, err)
	}

	tags, err := id3.ReadTags(fullPath, req.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to read tags: %w", err)
	}

	if strings.TrimSpace(req.Title) != "" {
		tags.Title = strings.TrimSpace(req.Title)
	}
	if strings.TrimSpace(req.Artist) != "" {
		tags.Artist = strings.TrimSpace(req.Artist)
	}
	if strings.TrimSpace(req.Album) != "" {
		tags.Album = strings.TrimSpace(req.Album)
	}
	if req.Year > 0 {
		tags.Year = req.Year
	}

	if s.enricher != nil {
		currentArts := id3.ParseMultiValue(tags.Artist)
		enriched := s.enricher.EnrichTrackArtists(ctx, tags.Title, currentArts)
		if len(enriched) > len(currentArts) {
			tags.Artist = id3.FormatMultiValue(enriched)
		}
	}

	if err := id3.WriteTags(fullPath, *tags, nil); err != nil {
		return nil, fmt.Errorf("failed to write tags: %w", err)
	}

	if s.scanner != nil {
		_ = s.scanner.IndexFile(fullPath, req.Path)
	}

	return tags, nil
}

func (s *Service) FixSafeTagIssues(ctx context.Context) (*FixBatchResponse, error) {
	report, err := s.GetTagIssues(ctx, "safe_autofix", 0)
	if err != nil {
		return nil, err
	}

	fixedCount := 0
	var errs []string

	for _, item := range report.Issues {
		fullPath, err := s.musicSandbox.Resolve(item.FilePath, false)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", item.FilePath, err))
			continue
		}

		tags, err := id3.ReadTags(fullPath, item.FilePath)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", item.FilePath, err))
			continue
		}

		if item.Proposed.Title != "" {
			tags.Title = item.Proposed.Title
		}
		if item.Proposed.Artist != "" {
			tags.Artist = item.Proposed.Artist
		}
		if item.Proposed.AlbumArtist != "" {
			tags.AlbumArtist = item.Proposed.AlbumArtist
		}

		if err := id3.WriteTags(fullPath, *tags, nil); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", item.FilePath, err))
			continue
		}

		if s.scanner != nil {
			_ = s.scanner.IndexFile(fullPath, item.FilePath)
		}
		fixedCount++
	}

	return &FixBatchResponse{
		Success:    len(errs) == 0,
		FixedCount: fixedCount,
		TotalFound: len(report.Issues),
		Errors:     errs,
	}, nil
}

func (s *Service) FixSelectedTagIssues(ctx context.Context, items []FixTagIssueRequest) (*FixBatchResponse, error) {
	fixedCount := 0
	var errs []string

	for _, it := range items {
		if it.Path == "" {
			continue
		}
		_, err := s.FixTagIssue(ctx, it)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", it.Path, err))
		} else {
			fixedCount++
		}
	}

	return &FixBatchResponse{
		Success:    len(errs) == 0,
		FixedCount: fixedCount,
		TotalFound: len(items),
		Errors:     errs,
	}, nil
}

func (s *Service) FixAllTagIssues(ctx context.Context) (*FixBatchResponse, error) {
	report, err := s.GetTagIssues(ctx, "all", 0)
	if err != nil {
		return nil, err
	}

	fixedCount := 0
	var errs []string

	for _, item := range report.Issues {
		fullPath, err := s.musicSandbox.Resolve(item.FilePath, false)
		if err != nil {
			continue
		}

		tags, err := id3.ReadTags(fullPath, item.FilePath)
		if err != nil {
			continue
		}

		if item.Proposed.Title != "" {
			tags.Title = item.Proposed.Title
		}
		if item.Proposed.Artist != "" {
			tags.Artist = item.Proposed.Artist
		}
		if item.Proposed.AlbumArtist != "" {
			tags.AlbumArtist = item.Proposed.AlbumArtist
		}
		if item.Proposed.Album != "" {
			tags.Album = item.Proposed.Album
		}
		if item.Proposed.Year > 0 && tags.Year == 0 {
			tags.Year = item.Proposed.Year
		}

		if err := id3.WriteTags(fullPath, *tags, nil); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", item.FilePath, err))
			continue
		}

		if s.scanner != nil {
			_ = s.scanner.IndexFile(fullPath, item.FilePath)
		}
		fixedCount++
	}

	return &FixBatchResponse{
		Success:    len(errs) == 0,
		FixedCount: fixedCount,
		TotalFound: len(report.Issues),
		Errors:     errs,
	}, nil
}

func (s *Service) CatalogMatchTrack(ctx context.Context, path, title, artist string) (*TrackCatalogMatchResult, error) {
	var currentItem struct {
		Path        string  `json:"path"`
		Title       string  `json:"title"`
		Artist      string  `json:"artist"`
		Album       string  `json:"album"`
		Year        int     `json:"year"`
		TrackNumber int     `json:"track_number"`
		Duration    float64 `json:"duration"`
	}

	if path != "" {
		currentItem.Path = path
		tr, _ := s.repo.GetTrackForTagAuditByPath(ctx, path)
		if tr != nil {
			currentItem.Title = tr.Title
			currentItem.Artist = tr.Artist
			currentItem.Album = tr.Album
			currentItem.Year = tr.Year
			currentItem.TrackNumber = tr.TrackNumber
			currentItem.Duration = tr.Duration
		}
	}

	if currentItem.Title == "" {
		currentItem.Title = title
	}
	if currentItem.Artist == "" {
		currentItem.Artist = artist
	}

	query := fmt.Sprintf("%s %s", resolver.ExtractPrimaryArtist(currentItem.Artist), resolver.CleanQueryText(currentItem.Title))
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("%w: missing search query", ErrInvalidRequest)
	}

	var matches []CatalogTrackMatch
	seen := make(map[string]bool)
	var mu sync.Mutex

	addMatch := func(m CatalogTrackMatch) {
		key := strings.ToLower(fmt.Sprintf("%s|%s|%s", m.Artist, m.Title, m.Album))
		if !seen[key] {
			seen[key] = true
			matches = append(matches, m)
		}
	}

	searchCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()

	var wg sync.WaitGroup

	if s.yandex != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ymTracks, err := s.yandex.SearchTrack(searchCtx, query); err == nil {
				mu.Lock()
				for _, yt := range ymTracks {
					if len(matches) >= 30 {
						break
					}
					artists := yt.AllArtistNames()
					albumTitle := ""
					year := 0
					coverURL := ""
					trackNum := yt.TrackPosition
					if trackNum <= 0 {
						trackNum = 1
					}
					if len(yt.Albums) > 0 {
						albumTitle = yt.Albums[0].Title
						year = yt.Albums[0].Year
						coverURL = yt.Albums[0].BestCoverURL()
					}
					addMatch(CatalogTrackMatch{
						Source:      "yandex",
						Title:       yt.Title,
						Artist:      id3.FormatMultiValue(artists),
						Artists:     artists,
						Album:       albumTitle,
						Year:        year,
						TrackNumber: trackNum,
						Duration:    int(yt.DurationMs / 1000),
						CoverURL:    coverURL,
					})
				}
				mu.Unlock()
			}
		}()
	}

	if s.deezer != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if dzTracks, err := s.deezer.SearchTracks(searchCtx, query); err == nil {
				mu.Lock()
				for _, dt := range dzTracks {
					if len(matches) >= 30 {
						break
					}
					var artists []string
					if len(dt.Contributors) > 0 {
						for _, c := range dt.Contributors {
							if c.Name != "" {
								artists = append(artists, c.Name)
							}
						}
					}
					if len(artists) == 0 && dt.Artist.Name != "" {
						artists = []string{dt.Artist.Name}
					}
					albumTitle := ""
					year := 0
					coverURL := ""
					if dt.Album != nil {
						albumTitle = dt.Album.Title
						year = dt.Album.ReleaseYear()
						coverURL = dt.Album.BestCoverURL()
					}
					pos := dt.TrackPosition
					if pos <= 0 {
						pos = 1
					}
					addMatch(CatalogTrackMatch{
						Source:      "deezer",
						Title:       dt.Title,
						Artist:      id3.FormatMultiValue(artists),
						Artists:     artists,
						Album:       albumTitle,
						Year:        year,
						TrackNumber: pos,
						Duration:    dt.Duration,
						CoverURL:    coverURL,
					})
				}
				mu.Unlock()
			}
		}()
	}

	if s.itunes != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if itunesResults, err := s.itunes.SearchTracks(searchCtx, query); err == nil {
				mu.Lock()
				for _, im := range itunesResults {
					if len(matches) >= 30 {
						break
					}
					addMatch(CatalogTrackMatch{
						Source:      "itunes",
						Title:       im.Title,
						Artist:      id3.FormatMultiValue(im.Artists),
						Artists:     im.Artists,
						Album:       im.Album,
						Year:        im.Year,
						TrackNumber: im.TrackNumber,
						Duration:    im.Duration,
						CoverURL:    im.CoverURL,
					})
				}
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	for i := range matches {
		matches[i].MatchScore = resolver.ComputeCatalogMatchScore(
			currentItem.Title,
			currentItem.Artist,
			currentItem.Duration,
			resolver.CatalogCandidate{
				Title:    matches[i].Title,
				Artist:   matches[i].Artist,
				Duration: matches[i].Duration,
			},
		)
	}

	sort.SliceStable(matches, func(i, j int) bool {
		return matches[i].MatchScore > matches[j].MatchScore
	})

	if len(matches) > 0 && matches[0].MatchScore >= 65 {
		matches[0].IsBestFeat = true
	}

	return &TrackCatalogMatchResult{
		Current: currentItem,
		Matches: matches,
	}, nil
}

func (s *Service) CatalogMatchAlbumDetails(ctx context.Context, folderPath string) (*AlbumCatalogMatchResponse, error) {
	fullDir, err := s.musicSandbox.Resolve(folderPath, false)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFolderNotFound, err)
	}

	entries, err := os.ReadDir(fullDir)
	if err != nil {
		return nil, err
	}

	type localTrack struct {
		relPath     string
		fullPath    string
		title       string
		artist      string
		trackNumber int
	}

	var localTracks []localTrack
	detectedAlbum := ""
	detectedArtist := ""

	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".mp3") {
			continue
		}
		mp3Path := filepath.Join(fullDir, e.Name())
		rel, _ := s.musicSandbox.ToRelative(mp3Path)
		tags, _ := id3.ReadTags(mp3Path, rel)

		tit := e.Name()
		art := ""
		num := 1
		if tags != nil {
			if tags.Title != "" {
				tit = tags.Title
			}
			art = tags.Artist
			if tags.TrackNumber > 0 {
				num = tags.TrackNumber
			}
			if detectedAlbum == "" && tags.Album != "" && !strings.EqualFold(tags.Album, "singles") && !strings.EqualFold(tags.Album, "single") {
				detectedAlbum = tags.Album
			}
			if detectedArtist == "" {
				if tags.AlbumArtist != "" {
					detectedArtist = tags.AlbumArtist
				} else if tags.Artist != "" {
					detectedArtist = resolver.ExtractPrimaryArtist(tags.Artist)
				}
			}
		}

		localTracks = append(localTracks, localTrack{
			relPath:     rel,
			fullPath:    mp3Path,
			title:       tit,
			artist:      art,
			trackNumber: num,
		})
	}

	sort.Slice(localTracks, func(i, j int) bool {
		return localTracks[i].trackNumber < localTracks[j].trackNumber
	})

	if len(localTracks) == 0 {
		return nil, fmt.Errorf("%w: no mp3 tracks found", ErrFileNotFound)
	}

	if detectedAlbum == "" {
		detectedAlbum = filepath.Base(fullDir)
	}

	searchQuery := fmt.Sprintf("%s %s", detectedArtist, detectedAlbum)
	var matchedDeezerAlbum *resolver.DeezerAlbum

	if s.deezer != nil {
		albums, sErr := s.deezer.SearchAlbums(ctx, searchQuery)
		if sErr == nil && len(albums) > 0 {
			fullAlbum, gErr := s.deezer.GetAlbum(ctx, strconv.FormatInt(albums[0].ID, 10))
			if gErr == nil {
				matchedDeezerAlbum = fullAlbum
			}
		}
	}

	if matchedDeezerAlbum == nil || matchedDeezerAlbum.Tracks == nil || len(matchedDeezerAlbum.Tracks.Data) == 0 {
		return nil, fmt.Errorf("%w: album not found in catalog for %q", ErrAlbumNotFound, searchQuery)
	}

	catalogTracks := matchedDeezerAlbum.Tracks.Data
	catalogCover := matchedDeezerAlbum.BestCoverURL()
	catalogGenre := id3.CanonicalizeGenre(matchedDeezerAlbum.PrimaryGenre())
	catalogYear := matchedDeezerAlbum.ReleaseYear()

	type detailedTrackInfo struct {
		contributors []resolver.DeezerArtist
	}
	trackDetails := make([]detailedTrackInfo, len(catalogTracks))
	if s.deezer != nil {
		var wg sync.WaitGroup
		sem := make(chan struct{}, 3) // Защита от Rate-Limit: максимум 3 параллельных запроса
		for i, ct := range catalogTracks {
			if ct.ID <= 0 {
				continue
			}
			wg.Add(1)
			go func(idx int, trackID int64) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				tCtx, tCancel := context.WithTimeout(ctx, 3*time.Second)
				defer tCancel()
				if tr, trErr := s.deezer.GetTrack(tCtx, strconv.FormatInt(trackID, 10)); trErr == nil && tr != nil {
					trackDetails[idx] = detailedTrackInfo{contributors: tr.Contributors}
				}
			}(i, ct.ID)
		}
		wg.Wait()
	}

	var diffs []AlbumCatalogTrackDiff
	for i, lt := range localTracks {
		var matchedCatTrack *resolver.DeezerTrack
		catTrackIndex := -1
		for cIdx, ct := range catalogTracks {
			cPos := ct.TrackPosition
			if cPos <= 0 {
				cPos = 1
			}
			if cPos == lt.trackNumber {
				cCopy := ct
				matchedCatTrack = &cCopy
				catTrackIndex = cIdx
				break
			}
		}

		if matchedCatTrack == nil && i < len(catalogTracks) {
			cCopy := catalogTracks[i]
			matchedCatTrack = &cCopy
			catTrackIndex = i
		}

		diffItem := AlbumCatalogTrackDiff{
			TrackNumber: lt.trackNumber,
			LocalTitle:  lt.title,
			LocalArtist: lt.artist,
			LocalPath:   lt.relPath,
		}

		if matchedCatTrack != nil {
			catArt := matchedCatTrack.Artist.Name
			var catArtists []string
			var contribs []resolver.DeezerArtist
			if catTrackIndex >= 0 && catTrackIndex < len(trackDetails) && len(trackDetails[catTrackIndex].contributors) > 0 {
				contribs = trackDetails[catTrackIndex].contributors
			} else if len(matchedCatTrack.Contributors) > 0 {
				contribs = matchedCatTrack.Contributors
			}

			if len(contribs) > 0 {
				for _, c := range contribs {
					if c.Name != "" {
						catArtists = append(catArtists, c.Name)
					}
				}
			}
			if len(catArtists) == 0 && catArt != "" {
				catArtists = []string{catArt}
			}

			diffItem.CatalogTitle = matchedCatTrack.Title
			diffItem.CatalogArtist = id3.FormatMultiValue(catArtists)
			diffItem.CatalogArtists = catArtists
			diffItem.CatalogDuration = matchedCatTrack.Duration
			diffItem.CatalogTrackNumber = matchedCatTrack.TrackPosition
			diffItem.IsMatched = true
		}

		diffs = append(diffs, diffItem)
	}

	return &AlbumCatalogMatchResponse{
		FolderPath:      folderPath,
		AlbumTitle:      detectedAlbum,
		AlbumArtist:     detectedArtist,
		CatalogAlbum:    matchedDeezerAlbum.Title,
		CatalogArtist:   matchedDeezerAlbum.Artist.Name,
		CatalogCoverURL: catalogCover,
		CatalogYear:     catalogYear,
		CatalogGenre:    catalogGenre,
		Source:          "deezer",
		Tracks:          diffs,
	}, nil
}

func (s *Service) ApplyCatalogTags(ctx context.Context, req ApplyCatalogTagsRequest) error {
	fullPath, err := s.musicSandbox.Resolve(req.Path, false)
	if err != nil {
		if errors.Is(err, sandbox.ErrPathTraversal) {
			return ErrSandboxViolation
		}
		return fmt.Errorf("%w: %v", ErrFileNotFound, err)
	}

	tags, err := id3.ReadTags(fullPath, req.Path)
	if err != nil {
		return fmt.Errorf("failed to read tags: %w", err)
	}

	if req.Title != "" {
		tags.Title = req.Title
	}
	if req.Artist != "" {
		tags.Artist = req.Artist
	}
	if req.Album != "" {
		tags.Album = req.Album
	}
	if req.Year > 0 {
		tags.Year = req.Year
	}
	if req.TrackNumber > 0 {
		tags.TrackNumber = req.TrackNumber
	}

	if strings.TrimSpace(tags.AlbumArtist) == "" || resolver.AreArtistsEquivalent(tags.AlbumArtist, tags.Artist) {
		primaryArt := resolver.ExtractPrimaryArtist(tags.Artist)
		if primaryArt != "" {
			tags.AlbumArtist = primaryArt
		}
	}

	var coverInfo *id3.CoverInfo
	if req.CoverURL != "" {
		client := s.httpClient
		if cResp, cErr := client.Get(req.CoverURL); cErr == nil && cResp.StatusCode == http.StatusOK {
			b, _ := io.ReadAll(cResp.Body)
			cResp.Body.Close()
			if len(b) > 0 {
				coverInfo = &id3.CoverInfo{
					MimeType: "image/jpeg",
					Data:     b,
				}
			}
		}
	}

	if err := id3.WriteTags(fullPath, *tags, coverInfo); err != nil {
		return fmt.Errorf("failed to write tags: %w", err)
	}

	if coverInfo != nil && len(coverInfo.Data) > 0 {
		albumDir := filepath.Dir(fullPath)
		coverFile := filepath.Join(albumDir, "cover.jpg")
		if _, statErr := os.Stat(coverFile); os.IsNotExist(statErr) {
			_ = os.WriteFile(coverFile, coverInfo.Data, 0644)
		}
	}

	if s.scanner != nil {
		_ = s.scanner.IndexFile(fullPath, req.Path)
	}

	return nil
}

func (s *Service) ApplyCatalogAlbum(ctx context.Context, req ApplyCatalogAlbumRequest) (*FixBatchResponse, error) {
	fullDir, err := s.musicSandbox.Resolve(req.FolderPath, false)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFolderNotFound, err)
	}

	var coverInfo *id3.CoverInfo
	if req.CoverURL != "" {
		client := s.httpClient
		if data, mime, cErr := downloadImageWithMime(ctx, client, req.CoverURL); cErr == nil && len(data) > 0 {
			coverInfo = &id3.CoverInfo{
				MimeType: mime,
				Data:     data,
			}
			_ = os.WriteFile(filepath.Join(fullDir, "cover.jpg"), data, 0644)
			_ = os.Chmod(filepath.Join(fullDir, "cover.jpg"), 0644)
		}
	}

	tracksUpdated := 0
	var errs []string

	for _, tr := range req.Tracks {
		fullPath, rErr := s.musicSandbox.Resolve(tr.Path, false)
		if rErr != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", tr.Path, rErr))
			continue
		}

		tags, _ := id3.ReadTags(fullPath, tr.Path)
		if tags == nil {
			tags = &id3.TrackTags{Path: tr.Path}
		}

		if tr.Title != "" {
			tags.Title = tr.Title
		}
		if tr.Artist != "" {
			tags.Artist = tr.Artist
		}
		if req.Album != "" {
			tags.Album = req.Album
		}
		if req.AlbumArtist != "" {
			tags.AlbumArtist = req.AlbumArtist
		} else if tr.Artist != "" {
			tags.AlbumArtist = resolver.ExtractPrimaryArtist(tr.Artist)
		}
		if tr.TrackNumber > 0 {
			tags.TrackNumber = tr.TrackNumber
		}
		if req.Year > 0 {
			tags.Year = req.Year
		}
		if req.Genre != "" {
			tags.Genre = id3.CanonicalizeGenre(req.Genre)
		} else if tags.Genre != "" {
			tags.Genre = id3.CanonicalizeGenre(tags.Genre)
		}

		if wErr := id3.WriteTags(fullPath, *tags, coverInfo); wErr == nil {
			tracksUpdated++
			if s.scanner != nil {
				_ = s.scanner.IndexFile(fullPath, tr.Path)
			}
		} else {
			errs = append(errs, fmt.Sprintf("%s: %v", tr.Path, wErr))
		}
	}

	return &FixBatchResponse{
		Success:    len(errs) == 0,
		FixedCount: tracksUpdated,
		TotalFound: len(req.Tracks),
		Errors:     errs,
	}, nil
}
