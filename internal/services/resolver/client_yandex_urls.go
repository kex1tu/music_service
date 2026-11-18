package resolver

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// YandexResourceRef содержит разобранные идентификаторы ресурса Яндекс Музыки.
type YandexResourceRef struct {
	Type         string // "artist", "album", "track", "playlist"
	ID           int64  // ID для artist, album, track
	Owner        string // Owner login/UID для owner/kind плейлистов
	Kind         int64  // Номер/kind для owner/kind плейлистов
	PlaylistUUID string // UUID для плейлистов по UUID
	RawURL       string
}

func isYandexHost(host string) bool {
	h := strings.ToLower(host)
	yandexHosts := []string{
		"music.yandex.ru", "music.yandex.com",
		"music.yandex.kz", "music.yandex.by", "music.yandex.uz",
	}
	for _, yh := range yandexHosts {
		if h == yh || strings.HasSuffix(h, "."+yh) {
			return true
		}
	}
	return false
}

var uuidRegex = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

var uuidSimpleRegex = regexp.MustCompile(`(?i)^[0-9a-f]{32}$`)

func isUUID(s string) bool {
	clean := strings.TrimSpace(s)
	return uuidRegex.MatchString(clean) || uuidSimpleRegex.MatchString(clean)
}

// ParseResource извлекает тип ресурса ("artist", "album", "track", "playlist") и его специфичные идентификаторы из ссылки или iframe Яндекс Музыки.
func (c *YandexMusicClient) ParseResource(rawURL string) (*YandexResourceRef, error) {
	cleanURL := ExtractURLFromInput(rawURL)
	if cleanURL == "" {
		return nil, errors.New("empty URL")
	}
	if !strings.HasPrefix(cleanURL, "http://") && !strings.HasPrefix(cleanURL, "https://") {
		cleanURL = "https://" + cleanURL
	}
	u, err := url.Parse(cleanURL)
	if err != nil {
		return nil, fmt.Errorf("некорректный URL: %w", err)
	}

	if !isYandexHost(u.Hostname()) {
		return nil, fmt.Errorf("хост %q не принадлежит белому списку доменов Яндекс Музыки", u.Hostname())
	}

	ref := &YandexResourceRef{RawURL: cleanURL}

	// 1. Проверяем Fragment (актуально для iframe: #playlist/{owner}/{kind}, #playlist/{uuid}, #album/{id}, #track/{id})
	frag := strings.Trim(u.Fragment, "/")
	if frag != "" {
		fragParts := strings.Split(frag, "/")
		if len(fragParts) > 0 {
			switch strings.ToLower(fragParts[0]) {
			case "playlist", "playlists":
				if len(fragParts) >= 3 {
					owner, _ := url.PathUnescape(fragParts[1])
					kind, pErr := strconv.ParseInt(fragParts[2], 10, 64)
					if pErr == nil && kind > 0 {
						ref.Type = "playlist"
						ref.Owner = owner
						ref.Kind = kind
						return ref, nil
					}
				}
				if len(fragParts) >= 2 {
					candidate := fragParts[1]
					if isUUID(candidate) || (len(candidate) >= 16 && strings.Contains(candidate, "-")) {
						ref.Type = "playlist"
						ref.PlaylistUUID = candidate
						return ref, nil
					}
				}
			case "album":
				if len(fragParts) >= 2 {
					if id, pErr := strconv.ParseInt(fragParts[1], 10, 64); pErr == nil && id > 0 {
						ref.Type = "album"
						ref.ID = id
						return ref, nil
					}
				}
			case "track":
				if len(fragParts) >= 2 {
					if id, pErr := strconv.ParseInt(fragParts[1], 10, 64); pErr == nil && id > 0 {
						ref.Type = "track"
						ref.ID = id
						return ref, nil
					}
				}
			case "artist":
				if len(fragParts) >= 2 {
					if id, pErr := strconv.ParseInt(fragParts[1], 10, 64); pErr == nil && id > 0 {
						ref.Type = "artist"
						ref.ID = id
						return ref, nil
					}
				}
			}
		}
	}

	// 2. Проверяем Path
	pathParts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i, part := range pathParts {
		lowerPart := strings.ToLower(part)
		switch lowerPart {
		case "users":
			if i+3 < len(pathParts) && strings.EqualFold(pathParts[i+2], "playlists") {
				owner, _ := url.PathUnescape(pathParts[i+1])
				kind, pErr := strconv.ParseInt(pathParts[i+3], 10, 64)
				if pErr == nil && kind > 0 {
					ref.Type = "playlist"
					ref.Owner = owner
					ref.Kind = kind
					return ref, nil
				}
			}
		case "playlists", "playlist":
			if i+2 < len(pathParts) {
				owner, _ := url.PathUnescape(pathParts[i+1])
				if kind, pErr := strconv.ParseInt(pathParts[i+2], 10, 64); pErr == nil && kind > 0 {
					ref.Type = "playlist"
					ref.Owner = owner
					ref.Kind = kind
					return ref, nil
				}
			}
			if i+1 < len(pathParts) {
				candidate := pathParts[i+1]
				if isUUID(candidate) || (len(candidate) >= 16 && strings.Contains(candidate, "-")) {
					ref.Type = "playlist"
					ref.PlaylistUUID = candidate
					return ref, nil
				}
			}
		case "album":
			if i+1 < len(pathParts) {
				if id, pErr := strconv.ParseInt(pathParts[i+1], 10, 64); pErr == nil && id > 0 {
					ref.Type = "album"
					ref.ID = id
					return ref, nil
				}
			}
		case "track":
			if i+1 < len(pathParts) {
				if id, pErr := strconv.ParseInt(pathParts[i+1], 10, 64); pErr == nil && id > 0 {
					ref.Type = "track"
					ref.ID = id
					return ref, nil
				}
			}
		case "artist":
			if i+1 < len(pathParts) {
				if id, pErr := strconv.ParseInt(pathParts[i+1], 10, 64); pErr == nil && id > 0 {
					ref.Type = "artist"
					ref.ID = id
					return ref, nil
				}
			}
		}
	}

	return nil, fmt.Errorf("не удалось извлечь ресурс (playlist/artist/album/track) из ссылки Яндекс Музыки %q", rawURL)
}

// ParseURL извлекает тип ресурса ("artist", "album", "track", "playlist") и ID из ссылки Яндекс Музыки.
// Сохраняет полную обратную совместимость с существующими вызовами.
func (c *YandexMusicClient) ParseURL(rawURL string) (string, int64, error) {
	ref, err := c.ParseResource(rawURL)
	if err != nil {
		return "", 0, err
	}
	switch ref.Type {
	case "artist", "album", "track":
		return ref.Type, ref.ID, nil
	case "playlist":
		if ref.Kind > 0 {
			return "playlist", ref.Kind, nil
		}
		return "playlist", 0, nil
	default:
		return ref.Type, ref.ID, nil
	}
}
