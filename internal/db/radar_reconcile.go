package db

import (
	"context"
	"fmt"
	"strings"

	"music-manager/internal/id3"
)

// ReconcileAllRadarReleases проверяет все релизы со статусом 'new' или 'downloaded' и при наличии в медиатеке переводит в in_library.
func (db *DB) ReconcileAllRadarReleases(ctx context.Context) (int, error) {
	// 1. Быстро загружаем карту альбомов медиатеки
	albumRows, err := db.conn.QueryContext(ctx, `
		SELECT a.clean_title, COALESCE(a.album_artist, ''), COALESCE(ar.name, ''), COALESCE(ar.clean_name, ''), a.folder_path
		FROM albums a
		JOIN artists ar ON a.artist_id = ar.id;
	`)
	if err != nil {
		return 0, err
	}
	defer albumRows.Close()

	type localAlb struct {
		cleanTitle  string
		albumArtist string
		artistName  string
		artistClean string
		folderPath  string
	}
	albumMap := make(map[string][]localAlb)
	for albumRows.Next() {
		var alb localAlb
		if err := albumRows.Scan(&alb.cleanTitle, &alb.albumArtist, &alb.artistName, &alb.artistClean, &alb.folderPath); err == nil {
			albumMap[alb.cleanTitle] = append(albumMap[alb.cleanTitle], alb)
			normClean := id3.CleanReleaseTitle(alb.cleanTitle)
			if normClean != "" && normClean != alb.cleanTitle {
				albumMap[normClean] = append(albumMap[normClean], alb)
			}
		}
	}
	albumRows.Close()

	// 2. Быстро загружаем набор заголовков треков
	trackRows, err := db.conn.QueryContext(ctx, `SELECT clean_title FROM tracks;`)
	if err != nil {
		return 0, err
	}
	defer trackRows.Close()

	trackSet := make(map[string]struct{})
	for trackRows.Next() {
		var ct string
		if err := trackRows.Scan(&ct); err == nil {
			trackSet[ct] = struct{}{}
			normCt := id3.CleanReleaseTitle(ct)
			if normCt != "" {
				trackSet[normCt] = struct{}{}
			}
		}
	}
	trackRows.Close()

	// 3. Выгружаем релизы для проверки
	radarRows, err := db.conn.QueryContext(ctx, `
		SELECT id, clean_title, artist_name, clean_artist_name
		FROM radar_releases
		WHERE is_radar = 1 OR status IN ('new', 'downloaded');
	`)
	if err != nil {
		return 0, err
	}
	defer radarRows.Close()

	var matchedIDs []int64
	for radarRows.Next() {
		var id int64
		var cleanTitle, artistName, cleanArtistName string
		if err := radarRows.Scan(&id, &cleanTitle, &artistName, &cleanArtistName); err != nil {
			continue
		}

		inLibrary := false

		// 3.1. Сверка по альбомам с учетом мульти-артистов и папок
		matchedAlb := false
		tokens := extractArtistTokens(artistName)
		if len(tokens) == 0 {
			tokens = []string{cleanArtistName}
		}

		normClean := id3.CleanReleaseTitle(cleanTitle)

		if candidates, ok := albumMap[cleanTitle]; ok {
			for _, c := range candidates {
				if matchesAnyArtist(tokens, c.artistName) ||
					matchesAnyArtist(tokens, c.albumArtist) ||
					matchesAnyArtist(tokens, c.artistClean) ||
					containsAnyToken(c.folderPath, tokens) {
					inLibrary = true
					matchedAlb = true
					break
				}
			}
		}

		if !matchedAlb && normClean != "" {
			if candidates, ok := albumMap[normClean]; ok {
				for _, c := range candidates {
					if matchesAnyArtist(tokens, c.artistName) ||
						matchesAnyArtist(tokens, c.albumArtist) ||
						matchesAnyArtist(tokens, c.artistClean) ||
						containsAnyToken(c.folderPath, tokens) {
						inLibrary = true
						matchedAlb = true
						break
					}
				}
			}
		}

		if !matchedAlb {
			for albClean, candidates := range albumMap {
				if (len(albClean) >= 4 && strings.Contains(cleanTitle, albClean)) ||
					(len(cleanTitle) >= 4 && strings.Contains(albClean, cleanTitle)) ||
					(normClean != "" && len(albClean) >= 4 && strings.Contains(normClean, albClean)) {
					for _, c := range candidates {
						if matchesAnyArtist(tokens, c.artistName) ||
							matchesAnyArtist(tokens, c.albumArtist) ||
							matchesAnyArtist(tokens, c.artistClean) ||
							containsAnyToken(c.folderPath, tokens) {
							inLibrary = true
							matchedAlb = true
							break
						}
					}
					if matchedAlb {
						break
					}
				}
			}
		}

		// 3.2. Сверка по трекам (для синглов)
		if !inLibrary {
			if _, ok := trackSet[cleanTitle]; ok {
				inLibrary = true
			} else if normClean != "" {
				if _, ok := trackSet[normClean]; ok {
					inLibrary = true
				}
			}
		}

		if inLibrary {
			matchedIDs = append(matchedIDs, id)
		}
	}
	radarRows.Close()

	if len(matchedIDs) == 0 {
		return 0, nil
	}

	// 4. Пакетное обновление в транзакции чанками по 500
	tx, err := db.conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	batchSize := 500
	for i := 0; i < len(matchedIDs); i += batchSize {
		end := i + batchSize
		if end > len(matchedIDs) {
			end = len(matchedIDs)
		}
		chunk := matchedIDs[i:end]

		placeholders := make([]string, len(chunk))
		args := make([]any, len(chunk))
		for j, id := range chunk {
			placeholders[j] = "?"
			args[j] = id
		}
		query := fmt.Sprintf("UPDATE radar_releases SET status = 'in_library', is_radar = 0, updated_at = CURRENT_TIMESTAMP WHERE id IN (%s)", strings.Join(placeholders, ","))
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}

	return len(matchedIDs), nil
}

func extractArtistTokens(raw string) []string {
	if raw == "" {
		return nil
	}
	replacer := strings.NewReplacer(
		",", "|",
		"&", "|",
		"feat.", "|",
		"feat", "|",
		"ft.", "|",
		"ft", "|",
		"vs.", "|",
		"vs", "|",
		"with", "|",
		"\\", "|",
		"/", "|",
		";", "|",
		"\x00", "|",
	)
	normalized := replacer.Replace(raw)
	parts := strings.Split(normalized, "|")
	var tokens []string
	seen := make(map[string]bool)
	for _, p := range parts {
		cleaned := CleanArtistKey(p)
		if cleaned != "" && !seen[cleaned] {
			seen[cleaned] = true
			tokens = append(tokens, cleaned)
		}
	}
	return tokens
}

func containsAnyToken(str string, tokens []string) bool {
	lower := strings.ToLower(str)
	for _, t := range tokens {
		if t != "" && strings.Contains(lower, t) {
			return true
		}
	}
	return false
}

func matchesAnyArtist(tokens []string, target string) bool {
	cleanedTarget := CleanArtistKey(target)
	if cleanedTarget == "" {
		return false
	}
	for _, t := range tokens {
		if t == cleanedTarget || strings.Contains(cleanedTarget, t) || strings.Contains(t, cleanedTarget) {
			return true
		}
	}
	return false
}
