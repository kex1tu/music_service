package resolver

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"music-manager/internal/db"
)

var (
	saviorRegex = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(old\s+ver(?:sion)?|старая\s+версия|до\s+цензуры|без\s+цензуры|uncensored|uncut|original\s+version|оригинал)(?:$|[^\p{L}\p{N}])`)
	badRegex    = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(clean|radio\s+edit|censored|с\s+цензурой|цензурная|sped\s+up|slowed)(?:$|[^\p{L}\p{N}])`)
	remixRegex  = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(remix|ремикс)(?:$|[^\p{L}\p{N}])`)
)

// ExtractSaviorsAndCheckBad анализирует название трека-кандидата.
// Реализует Anti-Glue алгоритм замещения фраз-спасителей и Anti-Collision защиту от ложных
// срабатываний стоп-слов при их совпадении с целевым артистом или названием трека (кейс Clean Bandit).
func ExtractSaviorsAndCheckBad(title, targetArtist, targetTitle string) (hasSavior bool, isBad bool) {
	isRemixTarget := remixRegex.MatchString(targetTitle)

	// 1. Поиск всех маркеров-спасителей
	matches := saviorRegex.FindAllStringSubmatchIndex(title, -1)
	hasSavior = len(matches) > 0

	// 2. Anti-Glue: замещаем захваченную фразу спасителя (группа 1) пробелами
	remainderBytes := []byte(title)
	for _, m := range matches {
		if len(m) >= 4 && m[2] >= 0 && m[3] >= 0 {
			for i := m[2]; i < m[3]; i++ {
				remainderBytes[i] = ' '
			}
		}
	}
	remainder := string(remainderBytes)

	// 3. Защита от коллизий стоп-слов (Anti-Collision, кейс Clean Bandit)
	badMatches := badRegex.FindAllStringSubmatchIndex(remainder, -1)
	targetCombined := strings.ToLower(targetArtist + " " + targetTitle)
	for _, bm := range badMatches {
		if len(bm) >= 4 && bm[2] >= 0 && bm[3] >= 0 {
			matchedWord := strings.ToLower(remainder[bm[2]:bm[3]])
			// Если стоп-слово входит как отдельное слово в имя целевого артиста или название трека — не бракуем
			collisionRegex := regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])` + regexp.QuoteMeta(matchedWord) + `(?:$|[^\p{L}\p{N}])`)
			if collisionRegex.MatchString(targetCombined) {
				continue
			}
			isBad = true
			break
		}
	}

	// 4. Проверка ремикса (если целевой трек не ремикс, а в названии есть remix/ремикс -> бракуем)
	if !isRemixTarget && remixRegex.MatchString(remainder) {
		isBad = true
	}

	return hasSavior, isBad
}

// DetermineValidDurations собирает уникальные хронометражи всех релизов из Deezer и iTunes.
func DetermineValidDurations(ctx context.Context, deezer *DeezerClient, itunes *ITunesClient, artist, title string, fallbackDur int) (validDurations []int, earlyDur int, referenceMissing bool) {
	durationMap := make(map[int]bool)
	earliestYear := 9999

	if deezer != nil {
		if dTracks, err := deezer.SearchTrackVersions(ctx, artist, title); err == nil {
			for _, dt := range dTracks {
				if dt.Duration > 0 {
					durationMap[dt.Duration] = true
					if dt.Album != nil {
						y := dt.Album.ReleaseYear()
						if y > 1900 && y < earliestYear {
							earliestYear = y
							earlyDur = dt.Duration
						}
					}
				}
			}
		}
	}

	if itunes != nil {
		itQuery := strings.TrimSpace(artist + " " + title)
		if iTracks, err := itunes.SearchTracks(ctx, itQuery); err == nil {
			for _, it := range iTracks {
				if it.Duration > 0 {
					durationMap[it.Duration] = true
					if it.Year > 1900 && it.Year < earliestYear {
						earliestYear = it.Year
						earlyDur = it.Duration
					}
				}
			}
		}
		if iTracksUS, err := itunes.SearchTracksWithCountry(ctx, itQuery, "US"); err == nil {
			for _, it := range iTracksUS {
				if it.Duration > 0 {
					durationMap[it.Duration] = true
					if it.Year > 1900 && it.Year < earliestYear {
						earliestYear = it.Year
						earlyDur = it.Duration
					}
				}
			}
		}
	}

	if fallbackDur > 0 {
		durationMap[fallbackDur] = true
	}

	for dur := range durationMap {
		validDurations = append(validDurations, dur)
	}
	sort.Ints(validDurations)

	if len(validDurations) == 0 {
		referenceMissing = true
		earlyDur = 0
		return
	}

	referenceMissing = false
	if earlyDur == 0 && len(validDurations) > 0 {
		earlyDur = validDurations[0]
	}
	return
}

// RankCandidates выполняет фильтрацию, расчет дельты, иерархию статусов и скоринг кандидатов.
func RankCandidates(
	candidates []YtCandidate,
	targetArtist, targetTitle string,
	validDurations []int,
	earlyDur int,
	referenceMissing bool,
	tolerance int,
	candidatesLimit int,
	preferYear int,
	distributors []string,
) []db.SoundCloudCandidateItem {
	if tolerance <= 0 {
		tolerance = 2
	}
	if candidatesLimit <= 0 {
		candidatesLimit = 10
	}
	if preferYear <= 0 {
		preferYear = 2023
	}

	var ranked []db.SoundCloudCandidateItem
	seenURLs := make(map[string]bool)

	for _, cand := range candidates {
		candURL := cand.URL
		if candURL == "" && cand.ID != "" {
			candURL = fmt.Sprintf("https://soundcloud.com/%s", cand.ID)
		}
		if candURL == "" || seenURLs[candURL] {
			continue
		}

		candDur := int(cand.Duration)
		if candDur <= 0 {
			continue
		}

		hasSavior, isBad := ExtractSaviorsAndCheckBad(cand.Title, targetArtist, targetTitle)
		if isBad {
			continue
		}

		// Проверка соответствия названия и артиста (защита от ложных «совпадений»)
		match, titleScore, artistScore := CheckTitleAndArtistMatch(cand.Title, cand.Uploader, targetTitle, targetArtist)
		if !match && titleScore < 0.35 && artistScore < 0.35 {
			// Совершенно чужой трек с идентичным хронометражем бракуется
			continue
		}

		// Минимальная дельта с эталонными хронометражами
		deltaMin := 9999
		if len(validDurations) > 0 {
			for _, ref := range validDurations {
				d := candDur - ref
				if d < 0 {
					d = -d
				}
				if d < deltaMin {
					deltaMin = d
				}
			}
		}

		// Иерархия статусов согласно ТЗ с обязательной верификацией соответствия трека:
		// 1. Маркер-спаситель есть + совпадение трека + эталона нет -> marked
		// 2. Маркер-спаситель есть + совпадение трека + дельта <= tolerance (<= 2с) -> marked
		// 3. Маркера нет + совпадение трека + дельта <= tolerance -> likely
		// 4. Близкие альтернативы (дельта 3-5с при подтвержденном совпадении трека) -> unverified
		var status string
		if hasSavior && match && referenceMissing {
			status = "marked"
		} else if hasSavior && match && deltaMin <= tolerance {
			status = "marked"
		} else if !hasSavior && match && deltaMin <= tolerance {
			status = "likely"
		} else if hasSavior && deltaMin <= 5 {
			status = "unverified"
		} else if match && deltaMin >= 3 && deltaMin <= 5 {
			status = "unverified"
		} else {
			// Отбраковываем кандидата при слишком большой дельте или несоответствии
			continue
		}

		// Скоринг:
		// Score = (hasSavior * 50) + (validDate && year < preferYear * 25) - (isDistributor * 20) - (deltaEarly * 5)
		saviorBonus := 0
		if hasSavior {
			saviorBonus = 50
		}

		dateBonus := 0
		if len(cand.UploadDate) >= 4 {
			if yr, err := strconv.Atoi(cand.UploadDate[:4]); err == nil && yr > 1900 && yr < preferYear {
				dateBonus = 25
			}
		} else if cand.Timestamp > 0 {
			yr := time.Unix(cand.Timestamp, 0).Year()
			if yr > 1900 && yr < preferYear {
				dateBonus = 25
			}
		}

		distributorPenalty := 0
		uploaderLower := strings.ToLower(cand.Uploader)
		if uploaderLower == "" {
			uploaderLower = strings.ToLower(cand.Channel)
		}
		for _, dist := range distributors {
			if dist != "" && strings.Contains(uploaderLower, strings.ToLower(dist)) {
				distributorPenalty = 20
				break
			}
		}

		deltaEarly := deltaMin
		if earlyDur > 0 {
			d := candDur - earlyDur
			if d < 0 {
				d = -d
			}
			deltaEarly = d
		}
		deltaPenalty := deltaEarly * 5

		score := saviorBonus + dateBonus - distributorPenalty - deltaPenalty

		seenURLs[candURL] = true
		ranked = append(ranked, db.SoundCloudCandidateItem{
			URL:        candURL,
			Title:      cand.Title,
			Uploader:   cand.Uploader,
			Duration:   candDur,
			Delta:      deltaMin,
			Score:      score,
			Status:     status,
			UploadDate: cand.UploadDate,
		})
	}

	// Сортировка:
	// 1. Статус: marked (1) > likely (2) > unverified (3)
	// 2. Score DESC
	// 3. Delta ASC
	statusWeight := func(st string) int {
		switch st {
		case "marked":
			return 1
		case "likely":
			return 2
		case "unverified":
			return 3
		case "manual":
			return 0
		default:
			return 4
		}
	}

	sort.Slice(ranked, func(i, j int) bool {
		wI := statusWeight(ranked[i].Status)
		wJ := statusWeight(ranked[j].Status)
		if wI != wJ {
			return wI < wJ
		}
		if ranked[i].Score != ranked[j].Score {
			return ranked[i].Score > ranked[j].Score
		}
		return ranked[i].Delta < ranked[j].Delta
	})

	if len(ranked) > candidatesLimit {
		ranked = ranked[:candidatesLimit]
	}

	return ranked
}
