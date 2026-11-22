package organizer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"music-manager/internal/core/safefs"
	"music-manager/internal/core/sandbox"
	"music-manager/internal/id3"
)

// TrackTags represents audio metadata for an individual track.
type TrackTags = id3.TrackTags

// AlbumMeta contains top-level album metadata from submission forms.
type AlbumMeta struct {
	AlbumArtist string `json:"album_artist"`
	Album       string `json:"album"`
	Year        *int   `json:"year,omitempty"`
	Genre       string `json:"genre,omitempty"`
}

// BatchSaveRequest defines the payload for batch saving and organizing tracks.
type BatchSaveRequest struct {
	SourceDir   string      `json:"source_dir"`   // e.g. "tmp_downloads/job-uuid" or "music_library/..."
	Album       AlbumMeta   `json:"album"`        // High-level release metadata
	Tracks      []TrackTags `json:"tracks"`       // List of tracks to organize
	CoverBase64 string      `json:"cover_base64"` // Base64-encoded cover art (optional)
}

// OrganizerResult contains details of the organized files and destination directory.
type OrganizerResult struct {
	DestinationDir string        `json:"destination_dir"` // Canonical relative directory (e.g. "Album Name")
	Tracks         []TrackTags   `json:"tracks"`          // Tracks whose tags were saved
	TrackResults   []TrackResult `json:"track_results"`
}

type TrackResult struct {
	SourcePath      string `json:"source_path"`
	DestinationPath string `json:"destination_path,omitempty"`
	Status          string `json:"status"`
	Error           string `json:"error,omitempty"`
}

func (r *OrganizerResult) Complete() bool {
	for _, track := range r.TrackResults {
		if track.Status != "moved" {
			return false
		}
	}
	return true
}

type targetAlbumInfo struct {
	fullDir    string
	sourceDirs map[string]bool
}

// Service manages organizing and moving files in the music library with strict sandbox isolation.
type Service struct {
	musicSandbox *sandbox.PathSandbox
	tmpSandbox   *sandbox.PathSandbox
	moveMu       sync.Mutex
	trackIndex   TrackChanger
}

// NewService creates a new Organizer Service instance.
func NewService(musicSandbox, tmpSandbox *sandbox.PathSandbox) *Service {
	return &Service{
		musicSandbox: musicSandbox,
		tmpSandbox:   tmpSandbox,
	}
}

// SetTrackChanger wires the library index before the organizer starts serving requests.
func (s *Service) SetTrackChanger(index TrackChanger) {
	s.trackIndex = index
}

// SaveBatch organizes and saves a batch of tracks according to the BatchSaveRequest specification.
func (s *Service) SaveBatch(req BatchSaveRequest) (*OrganizerResult, error) {
	fallbackAlbum := req.Album.Album
	fallbackAlbumArtist := req.Album.AlbumArtist

	// Single-track synchronization: if len(req.Tracks) == 1, populate album-level metadata from track if missing
	if len(req.Tracks) == 1 {
		t := &req.Tracks[0]
		if strings.TrimSpace(fallbackAlbumArtist) == "" && strings.TrimSpace(t.Artist) != "" {
			fallbackAlbumArtist = strings.TrimSpace(t.Artist)
			req.Album.AlbumArtist = fallbackAlbumArtist
		}
		if strings.TrimSpace(fallbackAlbum) == "" || strings.EqualFold(fallbackAlbum, "single") || strings.EqualFold(fallbackAlbum, "singles") {
			if strings.TrimSpace(t.Title) != "" {
				fallbackAlbum = strings.TrimSpace(t.Title)
				req.Album.Album = fallbackAlbum
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

	// Apply high-level album fields to tracks when individual track field is omitted
	for i := range req.Tracks {
		if strings.TrimSpace(req.Tracks[i].Album) == "" {
			if strings.TrimSpace(fallbackAlbum) != "" && !strings.EqualFold(fallbackAlbum, "singles") && !strings.EqualFold(fallbackAlbum, "single") {
				req.Tracks[i].Album = strings.TrimSpace(fallbackAlbum)
			} else if strings.TrimSpace(req.Tracks[i].Title) != "" {
				req.Tracks[i].Album = strings.TrimSpace(req.Tracks[i].Title)
			}
		} else if strings.EqualFold(req.Tracks[i].Album, "single") || strings.EqualFold(req.Tracks[i].Album, "singles") {
			if strings.TrimSpace(req.Tracks[i].Title) != "" {
				req.Tracks[i].Album = strings.TrimSpace(req.Tracks[i].Title)
			}
		}

		if strings.TrimSpace(req.Tracks[i].AlbumArtist) == "" && strings.TrimSpace(fallbackAlbumArtist) != "" {
			req.Tracks[i].AlbumArtist = strings.TrimSpace(fallbackAlbumArtist)
		}
		if req.Album.Year != nil && *req.Album.Year > 0 && req.Tracks[i].Year == 0 {
			req.Tracks[i].Year = *req.Album.Year
		}
		if strings.TrimSpace(req.Album.Genre) != "" && strings.TrimSpace(req.Tracks[i].Genre) == "" {
			req.Tracks[i].Genre = strings.TrimSpace(req.Album.Genre)
		}
	}

	// Canonicalize genres
	if req.Album.Genre != "" {
		req.Album.Genre = id3.CanonicalizeGenre(req.Album.Genre)
	}
	for i := range req.Tracks {
		if req.Tracks[i].Genre != "" {
			req.Tracks[i].Genre = id3.CanonicalizeGenre(req.Tracks[i].Genre)
		}
	}

	// Bilateral synchronization between AlbumArtist and track artists
	fallbackAlbumArtist, req.Tracks = id3.SyncAlbumArtistAndTrackArtists(fallbackAlbumArtist, req.Tracks)
	req.Album.AlbumArtist = fallbackAlbumArtist

	return s.MoveAlbum(req.SourceDir, fallbackAlbum, fallbackAlbumArtist, req.Tracks, req.CoverBase64)
}

// MoveAlbum moves audio files from sourceDir (TMP_DIR or MUSIC_DIR) into canonical MUSIC_DIR structure.
// Canonical structure: music_library / {Album} - {Album Artist} / {TrackNumber:02d}. {Title}.mp3.
// For singles without an explicit album, album name is set to the track's title.
func (s *Service) MoveAlbum(sourceDir string, fallbackAlbum, fallbackAlbumArtist string, tracks []TrackTags, coverBase64 string) (*OrganizerResult, error) {
	return s.MoveAlbumContext(context.Background(), sourceDir, fallbackAlbum, fallbackAlbumArtist, tracks, coverBase64)
}

// MoveAlbumContext organizes tracks and returns the filesystem and index outcome for each track.
func (s *Service) MoveAlbumContext(ctx context.Context, sourceDir, fallbackAlbum, fallbackAlbumArtist string, tracks []TrackTags, coverBase64 string) (*OrganizerResult, error) {
	return s.moveAlbum(ctx, sourceDir, fallbackAlbum, fallbackAlbumArtist, tracks, coverBase64, nil)
}

func (s *Service) moveAlbum(ctx context.Context, sourceDir, fallbackAlbum, fallbackAlbumArtist string, tracks []TrackTags, coverBase64 string, backfill *backfillDestination) (*OrganizerResult, error) {
	if len(tracks) == 1 {
		if strings.TrimSpace(fallbackAlbumArtist) == "" && strings.TrimSpace(tracks[0].Artist) != "" {
			fallbackAlbumArtist = strings.TrimSpace(tracks[0].Artist)
		}
		if strings.TrimSpace(fallbackAlbum) == "" || strings.EqualFold(fallbackAlbum, "single") || strings.EqualFold(fallbackAlbum, "singles") {
			if strings.TrimSpace(tracks[0].Title) != "" {
				fallbackAlbum = strings.TrimSpace(tracks[0].Title)
			}
		}
	}

	if len(tracks) == 0 {
		defaultDir := s.ComputeAlbumDir(fallbackAlbumArtist, fallbackAlbum)
		return &OrganizerResult{
			DestinationDir: defaultDir,
		}, nil
	}

	fallbackAlbumArtist, tracks = id3.SyncAlbumArtistAndTrackArtists(fallbackAlbumArtist, tracks)
	for i := range tracks {
		if tracks[i].Genre != "" {
			tracks[i].Genre = id3.CanonicalizeGenre(tracks[i].Genre)
		}
	}

	// 1. Identify all unique target album directories needed by the tracks
	uniqueAlbumDirsMap := make(map[string]bool)
	for _, tr := range tracks {
		trTitle := CleanTitle(tr.Title, filepath.Base(tr.Path))
		trAlbum := strings.TrimSpace(tr.Album)
		if trAlbum == "" {
			trAlbum = strings.TrimSpace(fallbackAlbum)
		}
		if trAlbum == "" || strings.EqualFold(trAlbum, "single") || strings.EqualFold(trAlbum, "singles") {
			if trTitle != "" {
				trAlbum = trTitle
			} else {
				trAlbum = "Unknown Album"
			}
		}

		trArtist := strings.TrimSpace(tr.AlbumArtist)
		if trArtist == "" {
			trArtist = strings.TrimSpace(fallbackAlbumArtist)
		}
		if trArtist == "" {
			trArtist = strings.TrimSpace(tr.Artist)
		}
		if trArtist == "" {
			trArtist = "Unknown Artist"
		}

		targetDir := s.ComputeAlbumDir(trArtist, trAlbum)
		if backfill != nil {
			targetDir = backfill.root
		}
		uniqueAlbumDirsMap[targetDir] = true
	}

	var uniqueAlbumDirs []string
	for dir := range uniqueAlbumDirsMap {
		uniqueAlbumDirs = append(uniqueAlbumDirs, dir)
	}
	// Sort directory keys to ensure deadlock-free locking
	sort.Strings(uniqueAlbumDirs)

	// Serialize MoveAlbum requests even when they choose different destinations
	// for the same staging input.
	s.moveMu.Lock()
	defer s.moveMu.Unlock()

	// 2. Acquire directory mutex locks in deterministic sorted order
	var locks []*sync.Mutex
	for _, dir := range uniqueAlbumDirs {
		l := getAlbumLock(dir)
		l.Lock()
		locks = append(locks, l)
	}
	defer func() {
		for _, l := range locks {
			l.Unlock()
		}
	}()

	albumInfoMap := make(map[string]*targetAlbumInfo)
	var updatedTracks []TrackTags
	trackResults := make([]TrackResult, len(tracks))
	movedSourceDirs := make(map[string]*sandbox.PathSandbox)

	for i, tr := range tracks {
		result := &trackResults[i]
		result.SourcePath = tr.Path
		result.Status = "failed"

		srcFullPath, sourceSandbox, err := s.resolveTrackSource(sourceDir, tr.Path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				result.Status = "missing"
			}
			result.Error = err.Error()
			continue
		}
		srcDir := filepath.Dir(srcFullPath)

		// Determine target metadata per track
		trTitle := CleanTitle(tr.Title, filepath.Base(srcFullPath))
		if trTitle == "" {
			trTitle = fmt.Sprintf("Track_%02d", tr.TrackNumber)
		}

		trAlbum := strings.TrimSpace(tr.Album)
		if trAlbum == "" {
			trAlbum = strings.TrimSpace(fallbackAlbum)
		}
		if trAlbum == "" || strings.EqualFold(trAlbum, "single") || strings.EqualFold(trAlbum, "singles") {
			if trTitle != "" {
				trAlbum = trTitle
			} else {
				trAlbum = "Unknown Album"
			}
		}

		trAlbumArtist := strings.TrimSpace(tr.AlbumArtist)
		if trAlbumArtist == "" {
			trAlbumArtist = strings.TrimSpace(fallbackAlbumArtist)
		}
		if trAlbumArtist == "" {
			trAlbumArtist = strings.TrimSpace(tr.Artist)
		}
		if trAlbumArtist == "" {
			trAlbumArtist = "Unknown Artist"
		}

		albumDir, targetRel := s.ComputeTargetPath(trAlbumArtist, trAlbum, tr.TrackNumber, trTitle, filepath.Base(srcFullPath))
		if backfill != nil {
			albumDir = backfill.root
			existing, _ := s.musicSandbox.ToRelative(srcFullPath)
			targetRel = backfill.trackPath(existing, tr.DiscNumber, filepath.Base(targetRel))
			dir, err := s.musicSandbox.Resolve(filepath.Dir(targetRel), true)
			if err != nil {
				return nil, err
			}
			if err := safefs.MkdirAll(dir, 0755); err != nil {
				return nil, err
			}
		}

		destFullPath, err := s.musicSandbox.Resolve(targetRel, false)
		if err != nil {
			result.Error = err.Error()
			continue
		}

		trCopy := tr
		trCopy.Album = trAlbum
		trCopy.AlbumArtist = trAlbumArtist
		trCopy.Title = trTitle
		if trCopy.TrackNumber <= 0 {
			trCopy.TrackNumber = 1
		}
		var tagErr error
		actualDestFull, err := MoveIndexedTrack(ctx, s.trackIndex, srcFullPath, destFullPath, false, func(actual string) error {
			trCopy.Path, _ = s.musicSandbox.ToRelative(actual)
			if err := id3.WriteTags(actual, trCopy, nil); err != nil {
				tagErr = fmt.Errorf("failed to write tags: %w", err)
			}
			return tagErr
		})
		if err != nil {
			result.Error = err.Error()
		}
		if actualDestFull == "" {
			continue
		}

		result.DestinationPath = trCopy.Path
		switch {
		case tagErr != nil:
			result.Status = "tag_error"
		case err != nil:
			result.Status = "index_error"
		default:
			result.Status = "moved"
		}
		if tagErr == nil {
			updatedTracks = append(updatedTracks, trCopy)
		}

		movedSourceDirs[srcDir] = sourceSandbox
		info := albumInfoMap[albumDir]
		if info == nil {
			info = &targetAlbumInfo{
				fullDir:    filepath.Dir(actualDestFull),
				sourceDirs: make(map[string]bool),
			}
			albumInfoMap[albumDir] = info
		}
		info.sourceDirs[srcDir] = true
		parentDir := filepath.Dir(srcDir)
		if parentDir != srcDir {
			info.sourceDirs[parentDir] = true
		}
	}

	// 5. Handle cover art for each album directory and its tracks
	for _, info := range albumInfoMap {
		// Save base64 cover if provided and not yet created
		if coverBase64 != "" {
			_ = saveBase64Cover(filepath.Join(info.fullDir, "cover.jpg"), coverBase64)
		}

		// Try copying covers from track source directories
		copyCoverFromSources(info.sourceDirs, info.fullDir)

		// Fallback to top-level sourceDir cover
		s.moveSourceCover(sourceDir, info.fullDir)

		// Fallback to extracting embedded APIC cover from moved tracks if no directory cover exists
		if !hasCoverInDir(info.fullDir) {
			extractCoverFromTracks(info.fullDir)
		}

		// Ensure track-named covers ({TrackBaseName}.jpg) exist for all tracks in this directory
		syncTrackNamedCovers(info.fullDir)
	}

	for srcDir, sourceSandbox := range movedSourceDirs {
		CleanEmptyParents(srcDir, sourceSandbox.Root())
	}

	destinationDir := s.ComputeAlbumDir(fallbackAlbumArtist, fallbackAlbum)
	for _, dir := range uniqueAlbumDirs {
		if _, moved := albumInfoMap[dir]; moved {
			destinationDir = dir
			break
		}
	}

	return &OrganizerResult{
		DestinationDir: destinationDir,
		Tracks:         updatedTracks,
		TrackResults:   trackResults,
	}, nil
}

func (s *Service) resolveTrackSource(sourceDir, path string) (string, *sandbox.PathSandbox, error) {
	isStaging := func(p string) bool {
		return p == "tmp_downloads" || strings.HasPrefix(p, "tmp_downloads/") ||
			p == "tmp" || strings.HasPrefix(p, "tmp/")
	}

	if isStaging(sourceDir) || isStaging(path) || (len(sourceDir) == 36 && strings.Contains(sourceDir, "-")) {
		cleanPath := strings.TrimPrefix(strings.TrimPrefix(path, "tmp_downloads/"), "tmp/")
		return stagedTrackPath(s.tmpSandbox, cleanPath)
	}

	// Older import clients send a bare job directory and relative staging paths.
	if s.tmpSandbox != nil {
		if full, source, err := stagedTrackPath(s.tmpSandbox, path); err == nil {
			return full, source, nil
		}
	}
	return regularTrackPath(s.musicSandbox, path)
}

// Queue paths are literal; older import clients may still send URL-encoded names.
func stagedTrackPath(source *sandbox.PathSandbox, path string) (string, *sandbox.PathSandbox, error) {
	full, sb, err := regularTrackPath(source, path, sandbox.LiteralPath)
	if !errors.Is(err, os.ErrNotExist) {
		return full, sb, err
	}
	return regularTrackPath(source, path)
}

func regularTrackPath(source *sandbox.PathSandbox, path string, syntax ...sandbox.PathSyntax) (string, *sandbox.PathSandbox, error) {
	if source == nil {
		return "", nil, fmt.Errorf("source sandbox not configured")
	}
	full, err := source.Resolve(path, false, syntax...)
	if err != nil {
		return "", nil, err
	}
	info, err := safefs.Lstat(full)
	if err != nil {
		return "", nil, err
	}
	if !info.Mode().IsRegular() || !strings.EqualFold(filepath.Ext(full), ".mp3") {
		return "", nil, fmt.Errorf("source is not a regular MP3: %s", path)
	}
	return full, source, nil
}
