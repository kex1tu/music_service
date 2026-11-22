package id3

import "testing"

func TestSourceURLFrameReadsTerminatedUTF8(t *testing.T) {
	for _, terminator := range []string{"", "\x00"} {
		var tags TrackTags
		applyFrame("TXXX", []byte("\x03"+sourceURLDescription+"\x00https://youtu.be/fixture"+terminator), &tags)
		if tags.SourceURL != "https://youtu.be/fixture" {
			t.Fatalf("source URL not read: %q", tags.SourceURL)
		}
	}
	var tags TrackTags
	applyFrame("TXXX", []byte("\x03"+sourceURLDescription+"\x00https://youtu.be/a\x00other"), &tags)
	if tags.SourceURL != "" {
		t.Fatalf("accepted multiple values: %q", tags.SourceURL)
	}
}
