package id3

import (
	"testing"
)

func TestCleanReleaseTitle(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Nevermind (Deluxe Edition)", "nevermind"},
		{"Random Access Memories [10th Anniversary Edition]", "random access memories"},
		{"In Utero (2013 Remaster)", "in utero"},
		{"Starboy [Explicit]", "starboy"},
		{"Blinding Lights (Single)", "blinding lights"},
		{"My Beautiful Dark Twisted Fantasy", "my beautiful dark twisted fantasy"},
		{"K.O.", "k o"},
		{"A.L.O.N.E. (EP)", "a l o n e"},
	}

	for _, tt := range tests {
		got := CleanReleaseTitle(tt.input)
		if got != tt.want {
			t.Errorf("CleanReleaseTitle(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
