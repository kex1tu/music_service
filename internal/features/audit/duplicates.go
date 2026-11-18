package audit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"music-manager/internal/core/safefs"
	"music-manager/internal/id3"
	"music-manager/internal/services/organizer"
)

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

	relPath, err = s.musicSandbox.ToRelative(fullPath)
	if err != nil {
		return err
	}
	remove := func() error {
		if err := safefs.Remove(fullPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove audio file: %w", err)
		}
		base := strings.TrimSuffix(relPath, filepath.Ext(relPath))
		for _, ext := range []string{".lrc", ".txt"} {
			if companion, err := s.musicSandbox.Resolve(base+ext, false); err == nil {
				_ = safefs.Remove(companion)
			}
		}
		return nil
	}

	if s.deletionIndex != nil {
		_, err = s.deletionIndex.DeletePath(ctx, fullPath, false, remove)
	} else if err = remove(); err == nil {
		err = s.repo.DeleteTrackAndCleanup(ctx, relPath)
	}
	if err != nil {
		return err
	}

	// 5. Clean empty parent directories up to sandbox root
	organizer.CleanEmptyParents(filepath.Dir(fullPath), s.musicSandbox.Root())

	return nil
}

var duplicateMutationMu sync.Mutex

func (s *Service) FixDuplicates(ctx context.Context, pathsToDelete []string) (*FixDuplicatesResponse, error) {
	duplicateMutationMu.Lock()
	defer duplicateMutationMu.Unlock()

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
	return s.fixDuplicateGroups(ctx, "")
}

func (s *Service) FixDuplicateGroup(ctx context.Context, groupKey string) (*FixDuplicatesResponse, error) {
	if groupKey == "" {
		return &FixDuplicatesResponse{Success: true}, nil
	}
	return s.fixDuplicateGroups(ctx, groupKey)
}

func (s *Service) fixDuplicateGroups(ctx context.Context, groupKey string) (*FixDuplicatesResponse, error) {
	duplicateMutationMu.Lock()
	defer duplicateMutationMu.Unlock()

	report, err := s.GetDuplicates(ctx)
	if err != nil {
		return nil, err
	}

	result := &FixDuplicatesResponse{}
	for _, grp := range report.Groups {
		if err := ctx.Err(); err != nil {
			result.Errors = append(result.Errors, err.Error())
			break
		}
		if groupKey != "" && grp.Key != groupKey {
			continue
		}

		keep := s.selectCanonicalTrack(grp.Tracks)
		paths := []string{grp.Tracks[keep].Path}
		for i, track := range grp.Tracks {
			if i != keep {
				paths = append(paths, track.Path)
			}
		}

		removed, err := organizer.MergeIndexedDuplicateGroup(ctx, s.trackIndex, s.musicSandbox, paths)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", grp.Key, err))
		}
		if len(removed) < len(paths)-1 {
			result.ReviewGroups++
		}
		for _, path := range removed {
			result.DeletedFiles = append(result.DeletedFiles, path)
			if s.trackIndex == nil {
				if err := s.repo.DeleteTrackAndCleanup(ctx, path); err != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", path, err))
				}
			}
		}
	}

	result.DeletedCount = len(result.DeletedFiles)
	result.Success = len(result.Errors) == 0
	return result, nil
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
