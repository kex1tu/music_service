package audit

import (
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

	"music-manager/internal/core/safefs"
	"music-manager/internal/core/sandbox"
	"music-manager/internal/id3"
	"music-manager/internal/services/resolver"
)

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

	entries, err := safefs.ReadDir(fullDir)
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
		if _, statErr := safefs.Stat(coverFile); os.IsNotExist(statErr) {
			_ = safefs.WriteFile(coverFile, coverInfo.Data, 0644)
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
			_ = safefs.WriteFile(filepath.Join(fullDir, "cover.jpg"), data, 0644)
			_ = safefs.Chmod(filepath.Join(fullDir, "cover.jpg"), 0644)
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
