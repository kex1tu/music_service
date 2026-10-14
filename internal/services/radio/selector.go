package radio

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"music-manager/internal/db"
	"music-manager/internal/services/resolver"
)

// WaveSelector выбирает следующие композиции для радиостанций и генерирует персонализированные плейлисты.
type WaveSelector struct {
	database   *db.DB
	navidrome  *NavidromeReader
	similarity *resolver.SimilarityEngine
	mu         sync.Mutex
	lastPlayed map[int64]time.Time
	recentArts []string
	rnd        *rand.Rand
}

// NewWaveSelector создает новый экземпляр WaveSelector.
func NewWaveSelector(database *db.DB, navi *NavidromeReader, sim *resolver.SimilarityEngine) *WaveSelector {
	return &WaveSelector{
		database:   database,
		navidrome:  navi,
		similarity: sim,
		lastPlayed: make(map[int64]time.Time),
		recentArts: make([]string, 0, 10),
		rnd:        rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// SelectNextTrack выбирает следующий трек для воспроизведения в зависимости от типа станции.
func (s *WaveSelector) SelectNextTrack(ctx context.Context, stationType, stationParam string) (*db.TrackEntity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var candidate *db.TrackEntity
	var err error

	switch stationType {
	case "genre":
		candidate, err = s.selectGenreTrack(ctx, stationParam)
	case "artist":
		candidate, err = s.selectArtistTrack(ctx, stationParam)
	default: // "wave" или любая дефолтная персональная станция
		candidate, err = s.selectWaveTrack(ctx)
	}

	if err != nil {
		return nil, err
	}
	if candidate == nil {
		return nil, fmt.Errorf("no tracks available for station %s (%s)", stationType, stationParam)
	}

	// Фиксируем выбор в памяти для ротации
	s.lastPlayed[candidate.ID] = time.Now()
	cleanArt := strings.ToLower(strings.TrimSpace(candidate.Artist))
	s.recentArts = append(s.recentArts, cleanArt)
	if len(s.recentArts) > 5 {
		s.recentArts = s.recentArts[1:]
	}

	return candidate, nil
}

// GeneratePlaylist формирует сбалансированный плейлист на count треков для экспорта в M3U.
func (s *WaveSelector) GeneratePlaylist(ctx context.Context, count int) ([]*db.TrackEntity, error) {
	if count <= 0 {
		count = 60
	}

	candidates, err := s.database.GetWaveCandidateTracks(ctx, count*3)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("library is empty")
	}

	var stats *NavidromeStats
	if s.navidrome != nil && s.navidrome.IsAvailable() {
		stats, _ = s.navidrome.GetUserListeningStats(ctx)
	}

	// Разделение по сегментам (Comfort 45%, Forgotten 30%, Discovery 25%)
	var comfort, forgotten, discovery []db.TrackEntity
	now := time.Now()

	for _, tr := range candidates {
		isStarred := false
		playCount := 0
		var lastPlayed time.Time

		if stats != nil {
			isStarred = stats.IsStarred(tr.FilePath)
			playCount = stats.GetPlayCount(tr.FilePath)
			lastPlayed = stats.GetLastPlayed(tr.FilePath)
		}

		if isStarred || playCount >= 5 {
			comfort = append(comfort, tr)
		} else if playCount > 0 && (lastPlayed.IsZero() || now.Sub(lastPlayed) > 30*24*time.Hour) {
			forgotten = append(forgotten, tr)
		} else {
			discovery = append(discovery, tr)
		}
	}

	comfortTarget := int(float64(count) * 0.45)
	forgottenTarget := int(float64(count) * 0.30)
	discoveryTarget := count - comfortTarget - forgottenTarget

	chosenIDs := make(map[int64]bool)
	var playlist []*db.TrackEntity
	lastArtists := make([]string, 0, 5)

	appendSegment := func(pool []db.TrackEntity, target int) {
		s.rnd.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
		for _, tr := range pool {
			if len(playlist) >= count {
				return
			}
			if chosenIDs[tr.ID] {
				continue
			}

			art := strings.ToLower(strings.TrimSpace(tr.Artist))
			// Проверка: артист не должен повторяться в последних 3 треках
			recentDup := false
			for _, prev := range lastArtists {
				if prev == art && prev != "" {
					recentDup = true
					break
				}
			}
			if recentDup && len(pool) > target {
				continue
			}

			chosenIDs[tr.ID] = true
			copyTr := tr
			playlist = append(playlist, &copyTr)

			lastArtists = append(lastArtists, art)
			if len(lastArtists) > 3 {
				lastArtists = lastArtists[1:]
			}
		}
	}

	appendSegment(comfort, comfortTarget)
	appendSegment(forgotten, forgottenTarget)
	appendSegment(discovery, discoveryTarget)

	// Если не набрали нужное число треков, добираем из всех оставшихся
	if len(playlist) < count {
		for _, tr := range candidates {
			if len(playlist) >= count {
				break
			}
			if chosenIDs[tr.ID] {
				continue
			}
			chosenIDs[tr.ID] = true
			copyTr := tr
			playlist = append(playlist, &copyTr)
		}
	}

	return playlist, nil
}

func (s *WaveSelector) selectWaveTrack(ctx context.Context) (*db.TrackEntity, error) {
	candidates, err := s.database.GetWaveCandidateTracks(ctx, 100)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no tracks in library")
	}

	var stats *NavidromeStats
	if s.navidrome != nil && s.navidrome.IsAvailable() {
		stats, _ = s.navidrome.GetUserListeningStats(ctx)
	}

	// Выбор целевого сегмента по весам (45% Comfort, 30% Forgotten, 25% Discovery)
	roll := s.rnd.Float64()
	targetSegment := "comfort"
	if roll >= 0.75 {
		targetSegment = "discovery"
	} else if roll >= 0.45 {
		targetSegment = "forgotten"
	}

	var segmentTracks []db.TrackEntity
	now := time.Now()

	for _, tr := range candidates {
		isStarred := false
		playCount := 0
		var lastPlayed time.Time

		if stats != nil {
			isStarred = stats.IsStarred(tr.FilePath)
			playCount = stats.GetPlayCount(tr.FilePath)
			lastPlayed = stats.GetLastPlayed(tr.FilePath)
		}

		switch targetSegment {
		case "comfort":
			if isStarred || playCount >= 5 {
				segmentTracks = append(segmentTracks, tr)
			}
		case "forgotten":
			if playCount > 0 && (lastPlayed.IsZero() || now.Sub(lastPlayed) > 30*24*time.Hour) {
				segmentTracks = append(segmentTracks, tr)
			}
		case "discovery":
			if playCount == 0 {
				segmentTracks = append(segmentTracks, tr)
			}
		}
	}

	// Если в выбранном сегменте мало треков — фоллбэк на все кандидаты
	if len(segmentTracks) == 0 {
		segmentTracks = candidates
	}

	return s.filterAndPick(segmentTracks)
}

func (s *WaveSelector) selectGenreTrack(ctx context.Context, genre string) (*db.TrackEntity, error) {
	genre = strings.TrimSpace(genre)
	candidates, err := s.database.GetTracksByGenre(ctx, genre, 50)
	if err != nil || len(candidates) == 0 {
		// Фоллбэк на обычную волну
		return s.selectWaveTrack(ctx)
	}
	return s.filterAndPick(candidates)
}

func (s *WaveSelector) selectArtistTrack(ctx context.Context, artistName string) (*db.TrackEntity, error) {
	artistName = strings.TrimSpace(artistName)
	// Чередуем треки артиста и треки похожих исполнителей
	related, _ := s.similarity.GetRelatedArtistsFromCache(ctx, artistName, 5)

	var targetArtists []string
	if len(related) > 0 && s.rnd.Float64() < 0.40 {
		// 40% вероятность трека похожего артиста
		targetArtists = related
	} else {
		targetArtists = []string{artistName}
	}

	candidates, err := s.database.GetTracksByArtists(ctx, targetArtists, 30)
	if err != nil || len(candidates) == 0 {
		// Если по артисту ничего не найдено, фоллбэк на кандидатов библиотеки
		return s.selectWaveTrack(ctx)
	}
	return s.filterAndPick(candidates)
}

// filterAndPick выбирает лучший трек с учетом защиты от недавних повторов (6 часов) и исполнителя (3 трека).
func (s *WaveSelector) filterAndPick(pool []db.TrackEntity) (*db.TrackEntity, error) {
	if len(pool) == 0 {
		return nil, fmt.Errorf("candidate pool is empty")
	}

	now := time.Now()
	sixHoursAgo := now.Add(-6 * time.Hour)

	// 1-й проход: строгая фильтрация (не играл за последние 6 часов, артист не играл в последних 3 песнях)
	var strict []db.TrackEntity
	for _, tr := range pool {
		if t, ok := s.lastPlayed[tr.ID]; ok && t.After(sixHoursAgo) {
			continue
		}
		art := strings.ToLower(strings.TrimSpace(tr.Artist))
		isRecentArtist := false
		for _, prev := range s.recentArts {
			if prev == art && prev != "" {
				isRecentArtist = true
				break
			}
		}
		if isRecentArtist {
			continue
		}
		strict = append(strict, tr)
	}

	if len(strict) > 0 {
		chosen := strict[s.rnd.Intn(len(strict))]
		return &chosen, nil
	}

	// 2-й проход: ослабленная фильтрация (только запрет повтора трека за 6 часов)
	var loose []db.TrackEntity
	for _, tr := range pool {
		if t, ok := s.lastPlayed[tr.ID]; ok && t.After(sixHoursAgo) {
			continue
		}
		loose = append(loose, tr)
	}

	if len(loose) > 0 {
		chosen := loose[s.rnd.Intn(len(loose))]
		return &chosen, nil
	}

	// 3-й проход: случайный трек из пула
	chosen := pool[s.rnd.Intn(len(pool))]
	return &chosen, nil
}
