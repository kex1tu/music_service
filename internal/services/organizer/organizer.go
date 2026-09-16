package organizer

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/id3"
)

var (
	albumLocksMu sync.Mutex
	albumLocks   = make(map[string]*sync.Mutex)
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

// OrganizerResult contains details of the organized album files.
type OrganizerResult struct {
	DestinationDir string          `json:"destination_dir"`
	Tracks         []id3.TrackTags `json:"tracks"`
}

type targetAlbumInfo struct {
	dirName    string
	fullDir    string
	sourceDirs map[string]bool
}

// Service manages organizing and moving files in the music library.
type Service struct {
	musicSandbox *sandbox.PathSandbox
	tmpSandbox   *sandbox.PathSandbox
}

// NewService creates a new OrganizerService.
func NewService(musicSandbox, tmpSandbox *sandbox.PathSandbox) *Service {
	return &Service{
		musicSandbox: musicSandbox,
		tmpSandbox:   tmpSandbox,
	}
}

// SanitizeComponent sanitizes a directory or filename component.
func (s *Service) SanitizeComponent(name, fallback string, maxLength int) string {
	if maxLength <= 0 {
		maxLength = 180
	}
	clean := strings.TrimSpace(name)
	if clean == "" {
		clean = fallback
	}

	// Normalize multiple separators
	clean = strings.ReplaceAll(clean, "\\\\", " & ")
	clean = strings.ReplaceAll(clean, "\x00", " & ")

	// Sanitize through sandbox
	sanitized := sandbox.SanitizeFilename(clean, fallback)
	if len(sanitized) > maxLength {
		sanitized = strings.TrimRight(sanitized[:maxLength], ". ")
	}
	if sanitized == "" {
		sanitized = fallback
	}
	return sanitized
}

// ComputeAlbumDir calculates canonical album directory name: {Album}.
func (s *Service) ComputeAlbumDir(albumTitle string) string {
	album := strings.TrimSpace(albumTitle)
	if album == "" {
		album = "Unknown Album"
	}
	safeAlbum := s.SanitizeComponent(album, "Unknown Album", 180)
	return safeAlbum
}

// ComputeTargetPath calculates destination paths: {Album}/{TrackNumber:02d}. {CleanTitle}.mp3.
// If albumTitle is empty or indicates a single, album is set to title.
func (s *Service) ComputeTargetPath(albumTitle string, trackNum int, title, origFilename string) (albumDir, trackRelPath string) {
	cleanTitle := id3.CleanTitle(title, origFilename)
	if trackNum <= 0 {
		trackNum = 1
	}

	album := strings.TrimSpace(albumTitle)
	if album == "" || strings.EqualFold(album, "single") || strings.EqualFold(album, "singles") {
		if cleanTitle != "" {
			album = cleanTitle
		} else {
			album = fmt.Sprintf("Track_%02d", trackNum)
		}
	}

	albumDir = s.ComputeAlbumDir(album)

	safeTitle := s.SanitizeComponent(cleanTitle, fmt.Sprintf("Track_%02d", trackNum), 180)
	safeTitle = strings.TrimSuffix(safeTitle, ".mp3")
	safeTitle = strings.TrimSuffix(safeTitle, ".MP3")
	if safeTitle == "" {
		safeTitle = fmt.Sprintf("Track_%02d", trackNum)
	}

	filename := fmt.Sprintf("%02d. %s.mp3", trackNum, safeTitle)
	trackRelPath = filepath.Join(albumDir, filename)
	return albumDir, trackRelPath
}

// MoveAlbum moves album files from sourceDir (TMP_DIR or MUSIC_DIR) into canonical MUSIC_DIR structure.
// Canonical structure: music_library / {Album} / {TrackNumber:02d}. {Title}.mp3.
// For singles without an explicit album, album name is set to the track's title.
func (s *Service) MoveAlbum(sourceDir string, fallbackAlbum, fallbackAlbumArtist string, tracks []id3.TrackTags, coverBase64 string) (*OrganizerResult, error) {
	if len(tracks) == 0 {
		defaultDir := s.ComputeAlbumDir(fallbackAlbum)
		return &OrganizerResult{
			DestinationDir: defaultDir,
			Tracks:         nil,
		}, nil
	}

	// 1. Identify all unique target album directories needed by the tracks
	uniqueAlbumDirsMap := make(map[string]bool)
	for _, tr := range tracks {
		trTitle := id3.CleanTitle(tr.Title, filepath.Base(tr.Path))
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
		targetDir := s.ComputeAlbumDir(trAlbum)
		uniqueAlbumDirsMap[targetDir] = true
	}

	var uniqueAlbumDirs []string
	for dir := range uniqueAlbumDirsMap {
		uniqueAlbumDirs = append(uniqueAlbumDirs, dir)
	}
	sort.Strings(uniqueAlbumDirs)

	// 2. Acquire directory mutex locks in deterministic sorted order (prevents deadlocks)
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
	var updatedTracks []id3.TrackTags
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
			continue // skip missing files
		}

		srcDir := filepath.Dir(srcFullPath)
		movedSourceDirs = append(movedSourceDirs, srcDir)

		// Determine target metadata per track
		trTitle := id3.CleanTitle(tr.Title, filepath.Base(srcFullPath))
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

		albumDir, targetRel := s.ComputeTargetPath(trAlbum, tr.TrackNumber, trTitle, filepath.Base(srcFullPath))

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

		// Safely move MP3 file with collision detection and companion lyrics
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

		// Update tags via ID3 writer
		_ = id3.WriteTags(actualDestFull, trCopy, nil)

		updatedTracks = append(updatedTracks, trCopy)
	}

	// 5. Handle cover art for each album directory
	for _, info := range albumInfoMap {
		// Save base64 cover if provided and not yet created
		if coverBase64 != "" {
			_ = saveBase64Cover(filepath.Join(info.fullDir, "cover.jpg"), coverBase64)
		}

		// Try copying covers from track source directories
		copyCoverFromSources(info.sourceDirs, info.fullDir)

		// Fallback to top-level sourceDir cover
		s.moveSourceCover(sourceDir, info.fullDir)
	}

	// 6. Clean up empty source directories
	for _, srcDir := range movedSourceDirs {
		removeIfEmpty(srcDir)
		parentDir := filepath.Dir(srcDir)
		if parentDir != "." && parentDir != "/" {
			removeIfEmpty(parentDir)
		}
	}

	if !isTmp && sourceDir != "" && sourceDir != "." {
		if srcDirFull, err := s.musicSandbox.Resolve(sourceDir, false); err == nil {
			removeIfEmpty(srcDirFull)
		}
	}

	destinationDir := ""
	if len(uniqueAlbumDirs) == 1 {
		destinationDir = uniqueAlbumDirs[0]
	} else if len(uniqueAlbumDirs) > 1 {
		destinationDir = uniqueAlbumDirs[0]
	} else {
		destinationDir = s.ComputeAlbumDir(fallbackAlbum)
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

// MoveTrackWithCompanions moves an audio track and any accompanying lyrics (.lrc, .txt).
// If destMP3 already exists and overwrite is false:
// - if identical (size and SHA-256 match), srcMP3 is safely removed (deduplication) and destMP3 is returned.
// - if different, a unique canonical path "{base} ({N}).mp3" is selected.
// Any companion .lrc/.txt files are moved along with the matching suffix.
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
				cleanCompanionLyrics(srcMP3, targetMP3)
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

	// Move companion lyrics (.lrc, .txt)
	moveCompanionLyrics(srcMP3, targetMP3)

	return targetMP3, nil
}

func cleanCompanionLyrics(srcAudio, destAudio string) {
	srcBase := strings.TrimSuffix(srcAudio, filepath.Ext(srcAudio))
	destBase := strings.TrimSuffix(destAudio, filepath.Ext(destAudio))

	for _, ext := range []string{".lrc", ".txt"} {
		srcLyric := srcBase + ext
		destLyric := destBase + ext
		if fileExists(srcLyric) {
			if fileExists(destLyric) {
				_ = os.Remove(srcLyric)
			} else {
				_ = moveFile(srcLyric, destLyric)
			}
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
				filepath.Join(p, "raw", "cover.jpg"),
				filepath.Join(p, "raw", "cover.png"),
			)
		}
	}
	if p, err := s.musicSandbox.Resolve(sourceDir, false); err == nil {
		candidates = append(candidates,
			filepath.Join(p, "cover.jpg"),
			filepath.Join(p, "folder.jpg"),
			filepath.Join(p, "cover.png"),
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

func moveCompanionLyrics(srcAudio, destAudio string) {
	srcBase := strings.TrimSuffix(srcAudio, filepath.Ext(srcAudio))
	destBase := strings.TrimSuffix(destAudio, filepath.Ext(destAudio))

	for _, ext := range []string{".lrc", ".txt"} {
		srcLyric := srcBase + ext
		destLyric := destBase + ext
		if fileExists(srcLyric) && srcLyric != destLyric {
			_ = moveFile(srcLyric, destLyric)
		}
	}
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func moveFile(src, dest string) error {
	if err := os.Rename(src, dest); err == nil {
		return nil
	}
	// Fallback to copy and delete across different mount points
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

func removeIfEmpty(dir string) {
	entries, err := os.ReadDir(dir)
	if err == nil && len(entries) == 0 {
		_ = os.Remove(dir)
	}
}
