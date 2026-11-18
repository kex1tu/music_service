package audit

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"music-manager/internal/core/safefs"
	"music-manager/internal/db"
	"music-manager/internal/id3"
	"music-manager/internal/services/resolver"
)

func (s *Service) GetIncompleteAlbums(ctx context.Context) (*IncompleteAlbumsResponse, error) {
	dtos, err := s.repo.GetIncompleteAlbums(ctx, 100)
	if err != nil {
		return nil, err
	}

	var albums []IncompleteAlbum
	totalMissing := 0

	for _, a := range dtos {
		missing, err := s.repo.GetMissingTracksForAlbum(ctx, a.FolderPath)
		if err != nil {
			return nil, err
		}
		nums := make([]int, 0, len(missing))
		for _, track := range missing {
			nums = append(nums, track.TrackNumber)
		}
		if len(nums) == 0 && a.TotalDiscs <= 1 && a.ExpectedCount > a.PresentCount {
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
			MissingTracks:       missing,
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
	catalog, err := resolver.DeezerCatalogTracks(matchedAlbum)
	if err != nil {
		return nil, err
	}
	s.enrichCatalogArtists(ctx, catalog)

	matched := make(map[db.TrackPosition]bool)
	if meta.ID > 0 {
		matched, err = s.repo.VerifyCatalog(ctx, meta.ID, strconv.FormatInt(matchedAlbum.ID, 10), catalog)
		if err != nil {
			return nil, err
		}
	}

	allTracks := make([]map[string]any, 0, len(catalog))
	missingTracks := make([]map[string]any, 0)
	missingNums := make([]int, 0)
	for _, t := range catalog {
		present := matched[t.TrackPosition]
		info := map[string]any{
			"track_number": t.TrackNumber, "disc_number": t.DiscNumber, "title": t.Title,
			"artist": t.Artist, "duration": t.Duration, "deezer_id": t.DeezerID,
			"deezer_url": t.DeezerURL, "is_present": present, "isrc": t.ISRC,
		}
		allTracks = append(allTracks, info)
		if !present {
			missingNums = append(missingNums, t.TrackNumber)
			missingTracks = append(missingTracks, info)
		}
	}
	presentCount, expectedCount := len(matched), len(catalog)

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

func (s *Service) enrichCatalogArtists(ctx context.Context, catalog []db.CatalogTrack) {
	sem := make(chan struct{}, 3)
	var wg sync.WaitGroup
	for i := range catalog {
		if catalog[i].DeezerID == "" {
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return
		}
		wg.Go(func() {
			defer func() { <-sem }()
			trackCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			track, err := s.deezer.GetTrack(trackCtx, catalog[i].DeezerID)
			if err != nil || track == nil {
				return
			}
			var names []string
			for _, contributor := range track.Contributors {
				if contributor.Name != "" {
					names = append(names, contributor.Name)
				}
			}
			if len(names) > 0 {
				catalog[i].Artist = id3.FormatMultiValue(names)
			}
			if track.ISRC != "" {
				catalog[i].ISRC = track.ISRC
			}
		})
	}
	wg.Wait()
}

func (s *Service) GetBackfillEditorData(ctx context.Context, albumPath, jobID string) (*BackfillEditorDataResponse, error) {
	var localTracks []id3.TrackTags
	var backfillTracks []id3.TrackTags

	if albumPath != "" && s.musicSandbox != nil {
		if _, err := s.musicSandbox.Resolve(albumPath, true); err != nil {
			albumPath = ""
		}
	}
	if albumPath != "" && s.musicSandbox != nil {
		albumPath = db.AlbumRoot(filepath.ToSlash(albumPath))
		dirs := []string{albumPath}
		root, err := s.musicSandbox.Resolve(albumPath, true)
		if err != nil {
			return nil, err
		}
		entries, err := safefs.ReadDir(root)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() && db.DiscFolderNumber(entry.Name()) > 0 {
				dirs = append(dirs, filepath.Join(albumPath, entry.Name()))
			}
		}
		for _, dir := range dirs {
			full, err := s.musicSandbox.Resolve(dir, true)
			if err != nil {
				return nil, err
			}
			items, err := safefs.ReadDir(full)
			if err != nil {
				return nil, err
			}
			for _, entry := range items {
				if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".mp3") {
					continue
				}
				rel := filepath.Join(dir, entry.Name())
				item, err := s.musicSandbox.Resolve(rel, false)
				if err != nil {
					continue
				}
				if track, err := id3.ReadTags(item, rel); err == nil {
					track.DiscNumber = db.LocalDiscNumber(filepath.ToSlash(dir), track.DiscNumber)
					localTracks = append(localTracks, *track)
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
			entries, _ := safefs.ReadDir(fullJobDir)
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
				if _, err := safefs.Stat(full); err == nil {
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
			if _, err := safefs.Stat(full); err == nil {
				coverPath = filepath.Join("tmp_downloads", jobID, "converted", "cover.jpg")
				hasCover = true
			}
		}
	}
	if !hasCover && jobID != "" && s.tmpSandbox != nil {
		rel := filepath.Join(jobID, "cover.jpg")
		if full, err := s.tmpSandbox.Resolve(rel, false); err == nil {
			if _, err := safefs.Stat(full); err == nil {
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

	if req.Path != "" {
		if s.musicSandbox != nil {
			full, err := s.musicSandbox.Resolve(req.Path, true)
			if err != nil {
				return nil, err
			}
			req.Path, err = s.musicSandbox.ToRelative(full)
			if err != nil {
				return nil, err
			}
		}
		req.Path = db.AlbumRoot(filepath.ToSlash(req.Path))
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
		req.Positions...,
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
