package resolver

import "testing"

func TestPublicTrackSourceDropsTemporaryLinksAndParameters(t *testing.T) {
	for raw, want := range map[string]string{
		"https://m.youtube.com/watch?v=test&list=album&token=private#fragment": "https://m.youtube.com/watch?v=test",
		"https://soundcloud.com/artist/song?secret_token=private":              "https://soundcloud.com/artist/song",
		"https://soundcloud.com/artist/sets/album":                             "",
		"https://www.deezer.com/track/123":                                     "",
		"https://stream.example.org/raw?token=private":                         "",
	} {
		if got := PublicTrackSource(raw); got != want {
			t.Errorf("PublicTrackSource(%q) = %q, want %q", raw, got, want)
		}
	}
}
