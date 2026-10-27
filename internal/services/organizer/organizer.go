package organizer

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

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
	DestinationDir string      `json:"destination_dir"` // Canonical relative directory (e.g. "Album Name")
	Tracks         []TrackTags `json:"tracks"`          // List of tracks with updated paths and tags
}

type targetAlbumInfo struct {
	dirName    string
	fullDir    string
	sourceDirs map[string]bool
}

// Service manages organizing and moving files in the music library with strict sandbox isolation.
type Service struct {
	musicSandbox *sandbox.PathSandbox
	tmpSandbox   *sandbox.PathSandbox
}

// NewService creates a new Organizer Service instance.
func NewService(musicSandbox, tmpSandbox *sandbox.PathSandbox) *Service {
	return &Service{
		musicSandbox: musicSandbox,
		tmpSandbox:   tmpSandbox,
	}
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
			Tracks:         nil,
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
		uniqueAlbumDirsMap[targetDir] = true
	}

	var uniqueAlbumDirs []string
	for dir := range uniqueAlbumDirsMap {
		uniqueAlbumDirs = append(uniqueAlbumDirs, dir)
	}
	// Sort directory keys to ensure deadlock-free locking
	sort.Strings(uniqueAlbumDirs)

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

	// 3. Resolve and create all target album directories inside music sandbox
	albumInfoMap := make(map[string]*targetAlbumInfo)
	for _, dir := range uniqueAlbumDirs {
		targetFullDir, err := s.musicSandbox.Resolve(dir, true)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve target album directory '%s': %w", dir, err)
		}
		if err := os.MkdirAll(targetFullDir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create album directory '%s': %w", dir, err)
		}
		_ = os.Chmod(targetFullDir, 0755)
		albumInfoMap[dir] = &targetAlbumInfo{
			dirName:    dir,
			fullDir:    targetFullDir,
			sourceDirs: make(map[string]bool),
		}
	}

	isTmp := strings.HasPrefix(sourceDir, "tmp_downloads") || strings.HasPrefix(sourceDir, "tmp/") || (strings.Contains(sourceDir, "-") && len(sourceDir) == 36)
	var updatedTracks []TrackTags
	var movedSourceDirs []string

	// 4. Move and tag each track
	for _, tr := range tracks {
		origPath := tr.Path
		var srcFullPath string

		isTmpTrack := isTmp || strings.HasPrefix(origPath, "tmp_downloads/") || strings.HasPrefix(origPath, "tmp/")

		if isTmpTrack && s.tmpSandbox != nil {
			cleanRel := strings.TrimPrefix(origPath, "tmp_downloads/")
			cleanRel = strings.TrimPrefix(cleanRel, "tmp/")
			if p, err := s.tmpSandbox.Resolve(cleanRel, false); err == nil && fileExists(p) {
				srcFullPath = p
			}
		}

		if srcFullPath == "" && s.tmpSandbox != nil {
			if p, err := s.tmpSandbox.Resolve(origPath, false); err == nil && fileExists(p) {
				srcFullPath = p
			}
		}

		if srcFullPath == "" {
			if p, err := s.musicSandbox.Resolve(origPath, false); err == nil && fileExists(p) {
				srcFullPath = p
			}
		}

		if srcFullPath == "" || !fileExists(srcFullPath) {
			continue // skip missing files safely
		}

		srcDir := filepath.Dir(srcFullPath)
		movedSourceDirs = append(movedSourceDirs, srcDir)

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

		if info, ok := albumInfoMap[albumDir]; ok {
			info.sourceDirs[srcDir] = true
			parentDir := filepath.Dir(srcDir)
			if parentDir != "." && parentDir != "/" {
				info.sourceDirs[parentDir] = true
			}
		}

		destFullPath, err := s.musicSandbox.Resolve(targetRel, true)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve destination track path: %w", err)
		}

		// Safely move MP3 file with collision detection, deduplication, and companion lyrics
		actualDestFull, err := MoveTrackWithCompanions(srcFullPath, destFullPath, false)
		if err != nil {
			return nil, fmt.Errorf("failed to move audio file %s to %s: %w", srcFullPath, destFullPath, err)
		}

		// Calculate actual relative path inside music sandbox
		actualRel, _ := s.musicSandbox.ToRelative(actualDestFull)

		// Update track tags on disk
		trCopy := tr
		trCopy.Path = actualRel
		trCopy.Album = trAlbum
		trCopy.AlbumArtist = trAlbumArtist
		trCopy.Title = trTitle
		if trCopy.TrackNumber <= 0 {
			trCopy.TrackNumber = 1
		}

		// Write ID3v2.4 tags to disk
		_ = id3.WriteTags(actualDestFull, trCopy, nil)

		updatedTracks = append(updatedTracks, trCopy)
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

	// 6. Clean up empty source directories
	for _, srcDir := range movedSourceDirs {
		removeIfEmpty(srcDir)
		parentDir := filepath.Dir(srcDir)
		if parentDir != "." && parentDir != "/" {
			removeIfEmpty(parentDir)
		}
	}

	if isTmp && sourceDir != "" && s.tmpSandbox != nil {
		cleanRel := strings.TrimPrefix(sourceDir, "tmp_downloads/")
		cleanRel = strings.TrimPrefix(cleanRel, "tmp/")
		if jobFull, err := s.tmpSandbox.Resolve(cleanRel, false); err == nil {
			_ = os.RemoveAll(jobFull)
		}
	} else if !isTmp && sourceDir != "" && sourceDir != "." {
		if srcDirFull, err := s.musicSandbox.Resolve(sourceDir, false); err == nil {
			removeIfEmpty(srcDirFull)
		}
	}

	destinationDir := ""
	if len(uniqueAlbumDirs) >= 1 {
		destinationDir = uniqueAlbumDirs[0]
	} else {
		destinationDir = s.ComputeAlbumDir(fallbackAlbumArtist, fallbackAlbum)
	}

	return &OrganizerResult{
		DestinationDir: destinationDir,
		Tracks:         updatedTracks,
	}, nil
}
