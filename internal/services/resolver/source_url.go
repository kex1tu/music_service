package resolver

import (
	"net/url"
	"strings"
)

// PublicTrackSource keeps only stable URLs that identify an audio page.
func PublicTrackSource(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Port() != "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	path := strings.Trim(u.Path, "/")
	if path == "" {
		return ""
	}
	var query url.Values
	switch {
	case host == "youtube.com" || host == "www.youtube.com" || host == "music.youtube.com" || host == "m.youtube.com":
		if path == "watch" && u.Query().Get("v") != "" {
			query = url.Values{"v": {u.Query().Get("v")}}
		} else if !strings.HasPrefix(path, "shorts/") && !strings.HasPrefix(path, "live/") {
			return ""
		}
	case host == "youtu.be":
		if strings.Contains(path, "/") {
			return ""
		}
	case host == "soundcloud.com" || host == "www.soundcloud.com" || host == "m.soundcloud.com":
		owner, slug, ok := strings.Cut(path, "/")
		if !ok || owner == "" || slug == "" || strings.Contains(slug, "/") || slug == "sets" || owner == "discover" {
			return ""
		}
	case strings.HasSuffix(host, ".bandcamp.com") && strings.HasPrefix(path, "track/"):
	default:
		return ""
	}
	u.Host = host
	u.RawQuery = query.Encode()
	u.ForceQuery = false
	u.Fragment, u.RawFragment = "", ""
	return u.String()
}
