package organizer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	title := strings.TrimSpace(albumTitle)
	if title == "" {
		title = "Unknown Album"
	}
	return s.SanitizeComponent(title, "Unknown Album", 180)
}

// ComputeTargetPath calculates destination paths: {Album}/{TrackNumber:02d}. {CleanTitle}.mp3.
func (s *Service) ComputeTargetPath(albumTitle string, trackNum int, title, origFilename string) (albumDir, trackRelPath string) {
	albumDir = s.ComputeAlbumDir(albumTitle)
	if trackNum <= 0 {
		trackNum = 1
	}

	// Clean title: strips repeated track number prefix (e.g. 05. 05. 05. -> '')
	cleanTitle := id3.CleanTitle(title, origFilename)
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
func (s *Service) MoveAlbum(sourceDir string, albumTitle, albumArtist string, tracks []id3.TrackTags, coverBase64 string) (*OrganizerResult, error) {
	albumDir := s.ComputeAlbumDir(albumTitle)

	// Acquire lock for this album directory
	lock := getAlbumLock(albumDir)
	lock.Lock()
	defer lock.Unlock()

	targetFullDir, err := s.musicSandbox.Resolve(albumDir, true)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve target album directory: %w", err)
	}
	if err := os.MkdirAll(targetFullDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create album directory: %w", err)
	}

	isTmp := strings.HasPrefix(sourceDir, "tmp_downloads") || strings.HasPrefix(sourceDir, "tmp/") || strings.Contains(sourceDir, "-") && len(sourceDir) == 36

	var updatedTracks []id3.TrackTags

	for _, tr := range tracks {
		origPath := tr.Path
		var srcFullPath string

		if isTmp && s.tmpSandbox != nil {
			// Resolve in tmp sandbox
			cleanRel := strings.TrimPrefix(origPath, "tmp_downloads/")
			cleanRel = strings.TrimPrefix(cleanRel, "tmp/")
			p, err := s.tmpSandbox.Resolve(cleanRel, false)
			if err == nil {
				srcFullPath = p
			}
		}

		if srcFullPath == "" || !fileExists(srcFullPath) {
			// Resolve in music sandbox
			p, err := s.musicSandbox.Resolve(origPath, false)
			if err == nil {
				srcFullPath = p
			}
		}

		if srcFullPath == "" || !fileExists(srcFullPath) {
			continue // skip missing files
		}

		_, targetRel := s.ComputeTargetPath(albumTitle, tr.TrackNumber, tr.Title, filepath.Base(srcFullPath))
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
		trCopy.Album = albumTitle
		if albumArtist != "" {
			trCopy.AlbumArtist = albumArtist
		}

		// Update tags via ID3 writer
		_ = id3.WriteTags(actualDestFull, trCopy, nil)

		updatedTracks = append(updatedTracks, trCopy)
	}

	// Move cover art if exists in source
	s.moveSourceCover(sourceDir, targetFullDir)

	// Clean up empty directories if source was in music library and now empty
	if !isTmp && sourceDir != "" && sourceDir != "." {
		if srcDirFull, err := s.musicSandbox.Resolve(sourceDir, false); err == nil && srcDirFull != targetFullDir {
			removeIfEmpty(srcDirFull)
		}
	}

	return &OrganizerResult{
		DestinationDir: albumDir,
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
	var candidates []string
	if s.tmpSandbox != nil {
		cleanRel := strings.TrimPrefix(sourceDir, "tmp_downloads/")
		cleanRel = strings.TrimPrefix(cleanRel, "tmp/")
		if p, err := s.tmpSandbox.Resolve(cleanRel, false); err == nil {
			candidates = append(candidates, filepath.Join(p, "cover.jpg"), filepath.Join(p, "cover.png"))
		}
	}
	if p, err := s.musicSandbox.Resolve(sourceDir, false); err == nil {
		candidates = append(candidates, filepath.Join(p, "cover.jpg"), filepath.Join(p, "folder.jpg"))
	}

	targetCover := filepath.Join(targetFullDir, "cover.jpg")
	for _, c := range candidates {
		if fileExists(c) && !fileExists(targetCover) {
			_ = copyFile(c, targetCover)
			break
		}
	}
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
