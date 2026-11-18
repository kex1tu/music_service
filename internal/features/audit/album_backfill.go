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
			entries, _ := safefs.ReadDir(fullPath)
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
