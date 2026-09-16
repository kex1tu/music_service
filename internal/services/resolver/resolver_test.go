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
