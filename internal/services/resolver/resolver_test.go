package resolver

import (
	"context"
	"testing"
)

func TestDetectSource_TorrentAndMagnet(t *testing.T) {
	r := NewSourceResolver(nil, nil, "")

	tests := []struct {
		input string
		want  string
	}{
		{"magnet:?xt=urn:btih:12345&dn=Radiohead+-+OK+Computer", "torrent"},
		{"https://example.com/releases/album.torrent", "torrent"},
		{"https://example.com/releases/album.torrent?passkey=xyz", "torrent"},
		{"https://www.deezer.com/album/123", "deezer"},
		{"https://www.youtube.com/watch?v=xyz", "youtube"},
	}

	for _, tt := range tests {
		got, err := r.DetectSource(tt.input)
		if err != nil {
			t.Errorf("DetectSource(%q) error: %v", tt.input, err)
			continue
		}
		if got != tt.want {
			t.Errorf("DetectSource(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestCleanReleaseTitle(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Radiohead - OK Computer [FLAC]", "Radiohead - OK Computer"},
		{"Daft Punk - Discovery (2001) [MP3 320]", "Daft Punk - Discovery (2001)"},
		{"Pink_Floyd_-_The_Wall_(Vinyl_Rip)", "Pink Floyd - The Wall"},
		{"Linkin+Park+-+Meteora+[24bit]", "Linkin Park - Meteora"},
	}

	for _, tt := range tests {
		got := cleanReleaseTitle(tt.input)
		if got != tt.want {
			t.Errorf("cleanReleaseTitle(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestResolve_TorrentNotFound(t *testing.T) {
	r := NewSourceResolver(nil, nil, "")
	_, _, err := r.Resolve(context.Background(), "magnet:?xt=urn:btih:abc&dn=NonExistentReleaseXYZ", "torrent")
	if err == nil {
		t.Errorf("expected error for non-existent torrent release, got nil")
	}
}

func TestFindBestMatch_MaloletkaRemix(t *testing.T) {
	m := NewAudioMatcher("")
	matchURL, err := m.FindBestMatch(context.Background(), "Малолетка (Розочка Remix)", "Макс Корж", 196)
	if err != nil {
		t.Fatalf("FindBestMatch failed: %v", err)
	}
	if matchURL == "" {
		t.Fatalf("expected non-empty matchURL, got empty")
	}
	t.Logf("Found match URL: %s", matchURL)
}

func TestDetectSource_YouTubeMusicArtist(t *testing.T) {
	r := NewSourceResolver(nil, nil, "")

	urls := []string{
		"https://music.youtube.com/channel/UC2XdaAVUannpujzv32jcouQ",
		"https://music.youtube.com/channel/UC2XdaAVUannpujzv32jcouQ/releases",
	}

	for _, u := range urls {
		src, err := r.DetectSource(u)
		if err != nil {
			t.Fatalf("DetectSource(%q) failed: %v", u, err)
		}
		if src != "youtube" {
			t.Errorf("expected source 'youtube', got %q", src)
		}
	}
}
