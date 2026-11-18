package downloader

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"music-manager/internal/id3"
	"music-manager/internal/services/resolver"
)

type cachedResolveResult struct {
	meta      *resolver.ReleaseMetadata
	tracks    []resolver.ResolvedTrack
	expiresAt time.Time
}

func (s *Service) resolveURL(ctx context.Context, rawURL, source string) (*resolver.ReleaseMetadata, []resolver.ResolvedTrack, error) {
	if s.resolver == nil {
		return &resolver.ReleaseMetadata{}, nil, nil
	}
	cacheKey := source + ":" + strings.TrimSpace(rawURL)

	s.resolveCacheMu.RLock()
	cached, ok := s.resolveCache[cacheKey]
	s.resolveCacheMu.RUnlock()

	if ok && time.Now().Before(cached.expiresAt) {
		tracksCopy := make([]resolver.ResolvedTrack, len(cached.tracks))
		copy(tracksCopy, cached.tracks)
		metaCopy := *cached.meta
		return &metaCopy, tracksCopy, nil
	}

	meta, tracks, err := s.resolver.Resolve(ctx, rawURL, source)
	if err != nil {
		return nil, nil, err
	}

	if meta == nil {
		meta = &resolver.ReleaseMetadata{}
	}

	s.resolveCacheMu.Lock()
	now := time.Now()
	if len(s.resolveCache) > 100 {
		for k, v := range s.resolveCache {
			if now.After(v.expiresAt) {
				delete(s.resolveCache, k)
			}
		}
	}
	tracksCopy := make([]resolver.ResolvedTrack, len(tracks))
	copy(tracksCopy, tracks)
	metaCopy := *meta
	s.resolveCache[cacheKey] = &cachedResolveResult{
		meta:      &metaCopy,
		tracks:    tracksCopy,
		expiresAt: now.Add(5 * time.Minute),
	}
	s.resolveCacheMu.Unlock()

	return meta, tracks, nil
}

// PreflightRelease выполняет предварительное разрешение релиза и проверку наличия каждого трека в медиатеке.
func (s *Service) PreflightRelease(ctx context.Context, rawURL, source string) (*PreflightResponse, error) {
	cleanURL := resolver.ExtractURLFromInput(rawURL)
	if cleanURL == "" {
		return nil, errors.New("URL cannot be empty")
	}

	if !resolver.IsAllowedURL(cleanURL) {
		return nil, fmt.Errorf("domain is not in allowed sources whitelist: %s", cleanURL)
	}

	var detectedSource string
	if s.resolver != nil {
		det, err := s.resolver.DetectSource(cleanURL)
		if err != nil {
			return nil, err
		}
		detectedSource = det
	} else {
		detectedSource = "auto"
	}

	if source == "" || source == "auto" || source == "radar" {
		source = detectedSource
	}

	meta, tracks, err := s.resolveURL(ctx, cleanURL, source)
	if err != nil {
		return nil, fmt.Errorf("ошибка разрешения источника: %w", err)
	}

	if meta == nil {
		meta = &resolver.ReleaseMetadata{}
	}

	var preflightTracks []PreflightTrackItem
	var newCount, highQualityCount, upgradeCount int

	for i, tr := range tracks {
		item := PreflightTrackItem{
			Index:         i,
			TrackNumber:   tr.TrackNumber,
			DiscNumber:    tr.DiscNumber,
			Title:         tr.Title,
			Artist:        tr.Artist,
			Duration:      tr.Duration,
			Status:        TrackStatusNew,
			DefaultSelect: true,
		}

		if s.repo != nil {
			existing, err := s.repo.CheckTrackInLibrary(ctx, tr.Title, tr.Artist)
			if err == nil && existing != nil {
				item.ExistingTrack = existing
				if existing.QualityTier == "true_320k" {
					item.Status = TrackStatusExistsHighQuality
					item.DefaultSelect = false
					highQualityCount++
				} else {
					item.Status = TrackStatusExistsUpgrade
					item.DefaultSelect = true
					upgradeCount++
				}
			} else {
				newCount++
			}
		} else {
			newCount++
		}

		preflightTracks = append(preflightTracks, item)
	}

	total := len(preflightTracks)
	genre := id3.CanonicalizeGenre(meta.Genre)
	coverURL := meta.CoverURL

	fetchedTracks := total
	if meta.FetchedTracks > 0 {
		fetchedTracks = meta.FetchedTracks
	}
	skippedTracks := meta.SkippedTracks

	return &PreflightResponse{
		Release: PreflightReleaseInfo{
			Title:         meta.AlbumTitle,
			Artist:        meta.AlbumArtist,
			Year:          meta.Year,
			Genre:         genre,
			CoverURL:      coverURL,
			TotalTracks:   total,
			FetchedTracks: fetchedTracks,
			SkippedTracks: skippedTracks,
			IsPlaylist:    meta.IsPlaylist,
		},
		Tracks: preflightTracks,
		Summary: PreflightSummary{
			TotalTracks:              total,
			NewTracksCount:           newCount,
			ExistingHighQualityCount: highQualityCount,
			UpgradeAvailableCount:    upgradeCount,
			FetchedTracksCount:       fetchedTracks,
			SkippedTracksCount:       skippedTracks,
		},
	}, nil
}
