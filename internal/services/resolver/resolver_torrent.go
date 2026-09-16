package resolver

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var releaseTagRegex = regexp.MustCompile(`(?i)(\[[^\]]*\]|\([^\)]*(?:vinyl|flac|mp3|web|remaster|deluxe|edition|cd|lossless|24bit|16bit)[^\)]*\))`)

// cleanReleaseTitle очищает название торрент-релиза от технических тегов качества и битрейта.
func cleanReleaseTitle(s string) string {
	s = releaseTagRegex.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.ReplaceAll(s, "+", " ")
	s = strings.TrimSpace(s)
	return s
}

// resolveTorrentOrDeezer пытается сопоставить торрент-ссылку или magnet с каталогом Deezer для загрузки метаданных.
func (r *SourceResolver) resolveTorrentOrDeezer(ctx context.Context, rawURL string) (*ReleaseMetadata, []ResolvedTrack, error) {
	// 1. If URL is Deezer, resolve directly
	if strings.Contains(strings.ToLower(rawURL), "deezer.com") {
		return r.resolveDeezer(ctx, rawURL)
	}

	// 2. If magnet link, extract dn (display name)
	lower := strings.ToLower(rawURL)
	if strings.HasPrefix(lower, "magnet:") {
		u, err := url.Parse(rawURL)
		if err == nil {
			dn := u.Query().Get("dn")
			if dn != "" {
				cleanQuery := cleanReleaseTitle(dn)
				if cleanQuery != "" && r.deezerClient != nil {
					albums, searchErr := r.deezerClient.SearchAlbums(ctx, cleanQuery)
					if searchErr == nil && len(albums) > 0 {
						dzURL := fmt.Sprintf("https://www.deezer.com/album/%d", albums[0].ID)
						return r.resolveDeezer(ctx, dzURL)
					}
				}
			}
		}
	}

	// 3. Fallback: if user passed query text or release title directly as rawURL
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") && !strings.HasPrefix(lower, "magnet:") {
		cleanQuery := cleanReleaseTitle(rawURL)
		if cleanQuery != "" && r.deezerClient != nil {
			albums, searchErr := r.deezerClient.SearchAlbums(ctx, cleanQuery)
			if searchErr == nil && len(albums) > 0 {
				dzURL := fmt.Sprintf("https://www.deezer.com/album/%d", albums[0].ID)
				return r.resolveDeezer(ctx, dzURL)
			}
		}
	}

	return nil, nil, fmt.Errorf("прямая загрузка BitTorrent (.torrent/magnet) временно недоступна; в каталоге Deezer релиз не найден")
}
