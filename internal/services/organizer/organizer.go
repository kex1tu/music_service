package organizer

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/id3"
)

var (
	albumLocksMu sync.Mutex
	albumLocks   = make(map[string]*sync.Mutex)

	// dangerousFsChars matches filesystem forbidden chars: / \ : * ? " < > | \x00 and ASCII control codes (0-31)
	dangerousFsChars = regexp.MustCompile(`[\x00-\x1f/\\:*?"<>|]`)
	multiSepRegex    = regexp.MustCompile(`(\\+|\x00)`)
)

func getAlbumLock(albumDir string) *sync.Mutex {
	albumLocksMu.Lock()
	defer albumLocksMu.Unlock()
	lock, ok := albumLocks[albumDir]
	if !ok {
		lock = &sync.Mutex{}
		albumLocks[albumDir] = lock
	}
	return lock
}

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

// SanitizeComponent sanitizes a directory or filename component:
// - Removes illegal filesystem characters (/, \, :, *, ?, ", <, >, |, \x00, control ASCII 0-31).
// - Replaces multiple separators (\\, \x00) with " & ".
// - Restricts length to maxLength (default 180) with clean rune boundary slicing.
// - Removes trailing dots and spaces to prevent Windows/crossplatform issues.
func SanitizeComponent(name, fallback string, maxLength int) string {
	if maxLength <= 0 {
		maxLength = 180
	}
	clean := strings.TrimSpace(name)
	if clean == "" {
		return fallback
	}

	// 1. Replace artist/genre multi-value separators
	clean = multiSepRegex.ReplaceAllString(clean, " & ")

	// 2. Replace illegal filesystem and control characters with underscore
	clean = dangerousFsChars.ReplaceAllString(clean, "_")

	// 3. Trim leading/trailing dots and spaces
	clean = strings.Trim(clean, ". ")

	// 4. Truncate safely by rune count without breaking UTF-8 sequences
	if utf8.RuneCountInString(clean) > maxLength {
		runes := []rune(clean)
		clean = strings.TrimRight(string(runes[:maxLength]), ". ")
	}

	if clean == "" {
		return fallback
	}
	return clean
}

// CleanTitle removes duplicate leading track number prefixes (e.g. "01. 01. Song" -> "Song").
func CleanTitle(title string, origFilename string) string {
	return id3.CleanTitle(title, origFilename)
}

// ComputeAlbumDir calculates canonical album directory name: {Album} - {Album Artist}.
func ComputeAlbumDir(albumArtist, albumTitle string) string {
	artist := strings.TrimSpace(albumArtist)
	album := strings.TrimSpace(albumTitle)

	if artist == "" {
		artist = "Unknown Artist"
	}
	if album == "" {
		album = "Unknown Album"
	}

	safeAlbum := SanitizeComponent(album, "Unknown Album", 100)
	safeArtist := SanitizeComponent(artist, "Unknown Artist", 100)

	return fmt.Sprintf("%s - %s", safeAlbum, safeArtist)
}

// ComputeTargetPath calculates destination paths: {Album} - {Album Artist}/{TrackNumber:02d}. {CleanTitle}.mp3.
// If albumTitle is empty or indicates a single ("Single"/"Singles"), album is set to track title.
func ComputeTargetPath(albumArtist, albumTitle string, trackNum int, title, origFilename string) (albumDir, trackRelPath string) {
	cleanTitle := CleanTitle(title, origFilename)
	if trackNum <= 0 {
		trackNum = 1
	}

	artist := strings.TrimSpace(albumArtist)
	album := strings.TrimSpace(albumTitle)

	// Single track logic: if album is empty or equal to "Single"/"Singles", album = track title
	if album == "" || strings.EqualFold(album, "single") || strings.EqualFold(album, "singles") {
		if cleanTitle != "" {
			album = cleanTitle
		} else {
			album = fmt.Sprintf("Track_%02d", trackNum)
		}
	}

	albumDir = ComputeAlbumDir(artist, album)
	safeTitle := SanitizeComponent(cleanTitle, fmt.Sprintf("Track_%02d", trackNum), 180)
	safeTitle = strings.TrimSuffix(safeTitle, ".mp3")
	safeTitle = strings.TrimSuffix(safeTitle, ".MP3")
	if safeTitle == "" {
		safeTitle = fmt.Sprintf("Track_%02d", trackNum)
	}

	filename := fmt.Sprintf("%02d. %s.mp3", trackNum, safeTitle)
	trackRelPath = filepath.Join(albumDir, filename)
	return albumDir, trackRelPath
}

// SanitizeComponent receiver method on Service.
func (s *Service) SanitizeComponent(name, fallback string, maxLength int) string {
	return SanitizeComponent(name, fallback, maxLength)
}

// ComputeAlbumDir receiver method on Service.
func (s *Service) ComputeAlbumDir(albumArtist, albumTitle string) string {
	return ComputeAlbumDir(albumArtist, albumTitle)
}

// ComputeTargetPath receiver method on Service.
func (s *Service) ComputeTargetPath(albumArtist, albumTitle string, trackNum int, title, origFilename string) (albumDir, trackRelPath string) {
	return ComputeTargetPath(albumArtist, albumTitle, trackNum, title, origFilename)
}

// SaveBatch organizes and saves a batch of tracks according to the BatchSaveRequest specification.
func (s *Service) SaveBatch(req BatchSaveRequest) (*OrganizerResult, error) {
	fallbackAlbum := req.Album.Album
	fallbackAlbumArtist := req.Album.AlbumArtist

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

	return s.MoveAlbum(req.SourceDir, fallbackAlbum, fallbackAlbumArtist, req.Tracks, req.CoverBase64)
}

// MoveAlbum moves audio files from sourceDir (TMP_DIR or MUSIC_DIR) into canonical MUSIC_DIR structure.
// Canonical structure: music_library / {Album} - {Album Artist} / {TrackNumber:02d}. {Title}.mp3.
// For singles without an explicit album, album name is set to the track's title.
func (s *Service) MoveAlbum(sourceDir string, fallbackAlbum, fallbackAlbumArtist string, tracks []TrackTags, coverBase64 string) (*OrganizerResult, error) {
	if len(tracks) == 0 {
		defaultDir := s.ComputeAlbumDir(fallbackAlbumArtist, fallbackAlbum)
		return &OrganizerResult{
			DestinationDir: defaultDir,
			Tracks:         nil,
		}, nil
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

// FileSHA256 calculates SHA-256 checksum of a file.
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

var companionExtensions = []string{".lrc", ".txt", ".jpg", ".jpeg", ".png", ".webp"}

// MoveTrackWithCompanions moves an audio track and any accompanying lyrics (.lrc, .txt) and images (.jpg, .png).
// If destMP3 already exists and overwrite is false:
// - If identical (size and SHA-256 match), srcMP3 is safely removed (deduplication) and destMP3 is returned.
// - If different, a unique canonical path "{base} ({N}).mp3" is selected.
// Any companion files are moved along with the matching suffix.
func MoveTrackWithCompanions(srcMP3, destMP3 string, overwrite bool) (string, error) {
	if srcMP3 == destMP3 {
		return destMP3, nil
	}
	if !fileExists(srcMP3) {
		return "", fmt.Errorf("source track does not exist: %s", srcMP3)
	}

	targetMP3 := destMP3

	if fileExists(targetMP3) && !overwrite {
		// Compare size and hash
		srcStat, err1 := os.Stat(srcMP3)
		destStat, err2 := os.Stat(targetMP3)
		if err1 == nil && err2 == nil && srcStat.Size() == destStat.Size() {
			srcHash, hErr1 := FileSHA256(srcMP3)
			destHash, hErr2 := FileSHA256(targetMP3)
			if hErr1 == nil && hErr2 == nil && srcHash == destHash {
				// Files are identical! Deduplicate: remove source
				_ = os.Remove(srcMP3)
				cleanCompanionFiles(srcMP3, targetMP3)
				return targetMP3, nil
			}
		}

		// Files differ or cannot be compared: find unique suffixed name
		dir := filepath.Dir(destMP3)
		ext := filepath.Ext(destMP3)
		base := strings.TrimSuffix(filepath.Base(destMP3), ext)

		found := false
		for n := 1; n < 1000; n++ {
			candidate := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, n, ext))
			if !fileExists(candidate) {
				targetMP3 = candidate
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("failed to generate unique filename for %s", destMP3)
		}
	}

	// Ensure destination directory exists
	_ = os.MkdirAll(filepath.Dir(targetMP3), 0755)

	if err := moveFile(srcMP3, targetMP3); err != nil {
		return "", fmt.Errorf("failed to move audio file %s to %s: %w", srcMP3, targetMP3, err)
	}

	// Move companion files (.lrc, .txt, .jpg, .png, etc.)
	moveCompanionFiles(srcMP3, targetMP3)

	return targetMP3, nil
}

func cleanCompanionFiles(srcAudio, destAudio string) {
	srcBase := strings.TrimSuffix(srcAudio, filepath.Ext(srcAudio))
	destBase := strings.TrimSuffix(destAudio, filepath.Ext(destAudio))

	for _, ext := range companionExtensions {
		srcComp := srcBase + ext
		destComp := destBase + ext
		if fileExists(srcComp) {
			if fileExists(destComp) {
				_ = os.Remove(srcComp)
			} else {
				_ = moveFile(srcComp, destComp)
			}
		}
	}
}

func moveCompanionFiles(srcAudio, destAudio string) {
	srcBase := strings.TrimSuffix(srcAudio, filepath.Ext(srcAudio))
	destBase := strings.TrimSuffix(destAudio, filepath.Ext(destAudio))

	for _, ext := range companionExtensions {
		srcComp := srcBase + ext
		destComp := destBase + ext
		if fileExists(srcComp) && srcComp != destComp {
			_ = moveFile(srcComp, destComp)
		}
	}
}

func hasCoverInDir(dir string) bool {
	for _, name := range []string{"cover.jpg", "cover.png", "cover.jpeg", "folder.jpg", "folder.png"} {
		if fileExists(filepath.Join(dir, name)) {
			return true
		}
	}
	return false
}

func extractCoverFromTracks(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".mp3") {
			continue
		}
		mp3Path := filepath.Join(dir, e.Name())
		cover, err := id3.ReadCover(mp3Path)
		if err == nil && cover != nil && len(cover.Data) > 0 {
			ext := ".jpg"
			if cover.MimeType == "image/png" || bytes.HasPrefix(cover.Data, []byte("\x89PNG")) {
				ext = ".png"
			}
			coverPath := filepath.Join(dir, "cover"+ext)
			if !fileExists(coverPath) {
				_ = os.WriteFile(coverPath, cover.Data, 0644)
			}
			trackCoverPath := filepath.Join(dir, strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))+ext)
			if !fileExists(trackCoverPath) {
				_ = os.WriteFile(trackCoverPath, cover.Data, 0644)
			}
			return
		}
	}
}

func syncTrackNamedCovers(dir string) {
	// Find directory-level cover file (cover.jpg, cover.png, folder.jpg, etc.)
	var dirCoverPath string
	for _, name := range []string{"cover.jpg", "cover.png", "cover.jpeg", "folder.jpg", "folder.png"} {
		p := filepath.Join(dir, name)
		if fileExists(p) {
			dirCoverPath = p
			break
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	// For every MP3 track in the directory:
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".mp3") {
			continue
		}
		trackBase := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))

		// Check if track-specific cover exists
		hasTrackCover := false
		var existingTrackCover string
		for _, imgExt := range []string{".jpg", ".jpeg", ".png", ".webp"} {
			tp := filepath.Join(dir, trackBase+imgExt)
			if fileExists(tp) {
				hasTrackCover = true
				existingTrackCover = tp
				break
			}
		}

		if !hasTrackCover {
			if dirCoverPath != "" {
				covExt := filepath.Ext(dirCoverPath)
				_ = copyFile(dirCoverPath, filepath.Join(dir, trackBase+covExt))
			} else {
				mp3Path := filepath.Join(dir, e.Name())
				cover, err := id3.ReadCover(mp3Path)
				if err == nil && cover != nil && len(cover.Data) > 0 {
					ext := ".jpg"
					if cover.MimeType == "image/png" || bytes.HasPrefix(cover.Data, []byte("\x89PNG")) {
						ext = ".png"
					}
					_ = os.WriteFile(filepath.Join(dir, trackBase+ext), cover.Data, 0644)
					if dirCoverPath == "" {
						dirCoverPath = filepath.Join(dir, "cover"+ext)
						_ = os.WriteFile(dirCoverPath, cover.Data, 0644)
					}
				}
			}
		} else if dirCoverPath == "" && existingTrackCover != "" {
			covExt := filepath.Ext(existingTrackCover)
			dirCoverPath = filepath.Join(dir, "cover"+covExt)
			_ = copyFile(existingTrackCover, dirCoverPath)
		}
	}
}

func (s *Service) moveSourceCover(sourceDir, targetFullDir string) {
	if sourceDir == "" {
		return
	}
	targetCover := filepath.Join(targetFullDir, "cover.jpg")
	if fileExists(targetCover) {
		return
	}

	var candidates []string
	if s.tmpSandbox != nil {
		cleanRel := strings.TrimPrefix(sourceDir, "tmp_downloads/")
		cleanRel = strings.TrimPrefix(cleanRel, "tmp/")
		if p, err := s.tmpSandbox.Resolve(cleanRel, false); err == nil {
			candidates = append(candidates,
				filepath.Join(p, "cover.jpg"),
				filepath.Join(p, "cover.png"),
				filepath.Join(p, "cover.jpeg"),
				filepath.Join(p, "folder.jpg"),
				filepath.Join(p, "folder.png"),
				filepath.Join(p, "raw", "cover.jpg"),
				filepath.Join(p, "raw", "cover.png"),
				filepath.Join(p, "converted", "cover.jpg"),
				filepath.Join(p, "converted", "cover.png"),
			)
		}
	}
	if p, err := s.musicSandbox.Resolve(sourceDir, false); err == nil {
		candidates = append(candidates,
			filepath.Join(p, "cover.jpg"),
			filepath.Join(p, "cover.png"),
			filepath.Join(p, "cover.jpeg"),
			filepath.Join(p, "folder.jpg"),
			filepath.Join(p, "folder.png"),
		)
	}

	for _, c := range candidates {
		if fileExists(c) && !fileExists(targetCover) {
			_ = copyFile(c, targetCover)
			break
		}
	}
}

func copyCoverFromSources(sourceDirs map[string]bool, targetFullDir string) {
	targetCover := filepath.Join(targetFullDir, "cover.jpg")
	if fileExists(targetCover) {
		return
	}

	candidates := []string{
		"cover.jpg", "cover.png", "cover.jpeg",
		"folder.jpg", "folder.png", "folder.jpeg",
		filepath.Join("raw", "cover.jpg"), filepath.Join("raw", "cover.png"),
		filepath.Join("converted", "cover.jpg"), filepath.Join("converted", "cover.png"),
	}

	for sDir := range sourceDirs {
		for _, cand := range candidates {
			candPath := filepath.Join(sDir, cand)
			if fileExists(candPath) && !fileExists(targetCover) {
				_ = copyFile(candPath, targetCover)
				return
			}
		}
	}
}

func saveBase64Cover(targetCoverPath string, base64Data string) error {
	if strings.TrimSpace(base64Data) == "" {
		return nil
	}
	if fileExists(targetCoverPath) {
		return nil
	}
	parts := strings.SplitN(base64Data, ",", 2)
	raw := base64Data
	if len(parts) == 2 {
		raw = parts[1]
	}
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	return os.WriteFile(targetCoverPath, data, 0644)
}


func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func moveFile(src, dest string) error {
	if err := os.Rename(src, dest); err == nil {
		return nil
	}
	// Fallback to streaming copy and delete across different mount points (EXDEV)
	if err := copyFile(src, dest); err != nil {
		return err
	}
	return os.Remove(src)
}

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
// RemoveIfEmpty удаляет папку, если в ней нет файлов.
func RemoveIfEmpty(dir string) {
	entries, err := os.ReadDir(dir)
	if err == nil && len(entries) == 0 {
		_ = os.Remove(dir)
	}
}

// CleanEmptyParents рекурсивно удаляет пустые директории вверх по дереву до rootDir (не включая сам rootDir).
func CleanEmptyParents(startDir, rootDir string) {
	cleanStart := filepath.Clean(startDir)
	cleanRoot := filepath.Clean(rootDir)

	curr := cleanStart
	for curr != "" && curr != "." && curr != "/" && curr != cleanRoot {
		// Проверяем, что curr находится внутри rootDir
		rel, err := filepath.Rel(cleanRoot, curr)
		if err != nil || strings.HasPrefix(rel, "..") {
			break
		}

		entries, err := os.ReadDir(curr)
		if err != nil || len(entries) > 0 {
			// В директории еще остались файлы/папки — останавливаем подъем
			break
		}

		// Удаляем пустую директорию
		if err := os.Remove(curr); err != nil {
			break
		}

		curr = filepath.Dir(curr)
	}
}

func removeIfEmpty(dir string) {
	RemoveIfEmpty(dir)
}

