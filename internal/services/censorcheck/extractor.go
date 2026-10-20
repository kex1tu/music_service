package censorcheck

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	// lrcTimeRegex распознает таймкоды вида [01:14.20], [00:05.5], [02:30], [01:05.123]
	lrcTimeRegex = regexp.MustCompile(`\[(\d{1,2}):(\d{2})(?:\.(\d{1,3}))?\]`)
)

type candidateSpot struct {
	timeSec  float64
	timeStr  string
	word     string
	category SpotCategory
	lineText string
	priority int // 1 = DrugCoAP, 2 = Suspect, 3 = Profanity
}

// ExtractSpots анализирует синхронизированный текст LRC и возвращает до maxSpots контрольных точек.
// Приоритет отдается словам по ст. 6.13 КоАП РФ (1), затем маркерам цензуры (2), затем мату (3).
// Итоговые точки отсортированы хронологически по времени появления в треке.
func ExtractSpots(lrcContent string, maxSpots int) []QuickListenSpot {
	if strings.TrimSpace(lrcContent) == "" {
		return []QuickListenSpot{}
	}
	if maxSpots <= 0 {
		maxSpots = 3
	}

	lines := strings.Split(lrcContent, "\n")
	var candidates []candidateSpot

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Игнорируем служебные ID3-теги LRC ([ti:...], [ar:...], [by:...])
		if strings.HasPrefix(line, "[ti:") || strings.HasPrefix(line, "[ar:") ||
			strings.HasPrefix(line, "[al:") || strings.HasPrefix(line, "[by:") ||
			strings.HasPrefix(line, "[offset:") {
			continue
		}

		// Извлекаем все таймкоды из строки
		matches := lrcTimeRegex.FindAllStringSubmatch(line, -1)
		if len(matches) == 0 {
			continue
		}

		// Очищаем строку от таймкодов
		cleanLine := lrcTimeRegex.ReplaceAllString(line, "")
		cleanLine = strings.TrimSpace(cleanLine)
		if cleanLine == "" {
			continue
		}

		// Парсим таймкоды в секунды
		var timestamps []float64
		for _, m := range matches {
			min, _ := strconv.Atoi(m[1])
			sec, _ := strconv.Atoi(m[2])
			frac := 0.0
			if len(m) > 3 && m[3] != "" {
				val, _ := strconv.Atoi(m[3])
				switch len(m[3]) {
				case 1:
					frac = float64(val) / 10.0
				case 2:
					frac = float64(val) / 100.0
				case 3:
					frac = float64(val) / 1000.0
				}
			}
			timestamps = append(timestamps, float64(min*60+sec)+frac)
		}

		// 1. Поиск многословных маркеров цензуры в строке
		lineLower := strings.ToLower(cleanLine)
		var lineTriggers []struct {
			word     string
			category SpotCategory
			priority int
		}

		for _, marker := range multiWordSuspectMarkers {
			if strings.Contains(lineLower, marker) {
				lineTriggers = append(lineTriggers, struct {
					word     string
					category SpotCategory
					priority int
				}{
					word:     marker,
					category: CategorySuspect,
					priority: 2,
				})
			}
		}

		// 2. Пословная токенизация
		tokens := tokenRegex.FindAllString(cleanLine, -1)
		seenInLine := make(map[string]struct{})
		for _, token := range tokens {
			tokenLower := strings.ToLower(token)
			if _, seen := seenInLine[tokenLower]; seen {
				continue
			}

			cat, ok := classifyWord(token)
			if !ok {
				continue
			}

			// Контекстная проверка для слова "план"
			if cat == CategoryDrugCoAP && !isDrugPlanContext(token, cleanLine) {
				continue
			}

			seenInLine[tokenLower] = struct{}{}
			prio := 3
			if cat == CategoryDrugCoAP {
				prio = 1
			} else if cat == CategorySuspect {
				prio = 2
			}

			lineTriggers = append(lineTriggers, struct {
				word     string
				category SpotCategory
				priority int
			}{
				word:     token,
				category: cat,
				priority: prio,
			})
		}

		// Для каждого найденного триггера и каждого таймкода добавляем кандидата
		for _, tSec := range timestamps {
			timeStr := fmt.Sprintf("%02d:%02d", int(tSec)/60, int(tSec)%60)
			for _, trig := range lineTriggers {
				candidates = append(candidates, candidateSpot{
					timeSec:  tSec,
					timeStr:  timeStr,
					word:     trig.word,
					category: trig.category,
					lineText: cleanLine,
					priority: trig.priority,
				})
			}
		}
	}

	if len(candidates) == 0 {
		return []QuickListenSpot{}
	}

	// Сортировка кандидатов по приоритету (1 -> 2 -> 3), затем по времени
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].priority != candidates[j].priority {
			return candidates[i].priority < candidates[j].priority
		}
		return candidates[i].timeSec < candidates[j].timeSec
	})

	// Выбираем до maxSpots неколлидирующих точек (расстояние >= 4 секунд)
	var selected []candidateSpot
	for _, cand := range candidates {
		if len(selected) >= maxSpots {
			break
		}

		// Проверка расстояния от уже выбранных точек
		isTooClose := false
		for _, sel := range selected {
			if math.Abs(sel.timeSec-cand.timeSec) < 4.0 {
				isTooClose = true
				break
			}
		}
		if !isTooClose {
			selected = append(selected, cand)
		}
	}

	// Если не набрали maxSpots из-за строгой дистанции, ослабляем фильтр дистанции
	if len(selected) < maxSpots {
		for _, cand := range candidates {
			if len(selected) >= maxSpots {
				break
			}
			alreadyHas := false
			for _, sel := range selected {
				if sel.timeSec == cand.timeSec && sel.word == cand.word {
					alreadyHas = true
					break
				}
			}
			if !alreadyHas {
				selected = append(selected, cand)
			}
		}
	}

	// Сортируем итоговые точки строго хронологически по времени песни
	sort.Slice(selected, func(i, j int) bool {
		return selected[i].timeSec < selected[j].timeSec
	})

	result := make([]QuickListenSpot, len(selected))
	for i, s := range selected {
		result[i] = QuickListenSpot{
			TimeSec:  s.timeSec,
			TimeStr:  s.timeStr,
			Word:     s.word,
			Category: s.category,
			LineText: s.lineText,
		}
	}

	return result
}
