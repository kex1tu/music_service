package resolver

import "testing"

func TestExtractYandexTrackID(t *testing.T) {
	tests := []struct {
		name           string
		catalogTrackID string
		trackID        string
		wantID         int64
		wantOK         bool
	}{
		{
			name:           "Valid CatalogTrackID standard",
			catalogTrackID: "ya_123456",
			trackID:        "ya_123456",
			wantID:         123456,
			wantOK:         true,
		},
		{
			name:           "Valid CatalogTrackID with playlist slot trackID",
			catalogTrackID: "ya_7890",
			trackID:        "ya_7890_3",
			wantID:         7890,
			wantOK:         true,
		},
		{
			name:           "Empty CatalogTrackID, fallback to TrackID with slot",
			catalogTrackID: "",
			trackID:        "ya_5555_12",
			wantID:         5555,
			wantOK:         true,
		},
		{
			name:           "Empty CatalogTrackID, simple TrackID",
			catalogTrackID: "",
			trackID:        "ya_4321",
			wantID:         4321,
			wantOK:         true,
		},
		{
			name:           "Deezer track ID with dz_ prefix",
			catalogTrackID: "dz_123456",
			trackID:        "dz_123456",
			wantID:         0,
			wantOK:         false,
		},
		{
			name:           "Raw numeric ID without ya_ prefix",
			catalogTrackID: "123456",
			trackID:        "123456",
			wantID:         0,
			wantOK:         false,
		},
		{
			name:           "SoundCloud track ID",
			catalogTrackID: "sc_99999",
			trackID:        "sc_99999",
			wantID:         0,
			wantOK:         false,
		},
		{
			name:           "YouTube ID",
			catalogTrackID: "yt_dQw4w9WgXcQ",
			trackID:        "yt_dQw4w9WgXcQ",
			wantID:         0,
			wantOK:         false,
		},
		{
			name:           "Zero ID",
			catalogTrackID: "ya_0",
			trackID:        "ya_0",
			wantID:         0,
			wantOK:         false,
		},
		{
			name:           "Negative ID",
			catalogTrackID: "ya_-100",
			trackID:        "ya_-100",
			wantID:         0,
			wantOK:         false,
		},
		{
			name:           "Non-numeric suffix",
			catalogTrackID: "ya_abc",
			trackID:        "ya_abc",
			wantID:         0,
			wantOK:         false,
		},
		{
			name:           "Empty strings",
			catalogTrackID: "",
			trackID:        "",
			wantID:         0,
			wantOK:         false,
		},
		{
			name:           "Whitespace only",
			catalogTrackID: "   ",
			trackID:        "   ",
			wantID:         0,
			wantOK:         false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id, ok := ExtractYandexTrackID(tc.catalogTrackID, tc.trackID)
			if ok != tc.wantOK {
				t.Fatalf("ExtractYandexTrackID(%q, %q) ok = %v, want %v", tc.catalogTrackID, tc.trackID, ok, tc.wantOK)
			}
			if id != tc.wantID {
				t.Fatalf("ExtractYandexTrackID(%q, %q) id = %d, want %d", tc.catalogTrackID, tc.trackID, id, tc.wantID)
			}
		})
	}
}

func TestResolvedTrack_GetYandexTrackID(t *testing.T) {
	// 1. Explicit YandexTrackID
	tr1 := ResolvedTrack{
		YandexTrackID:  42,
		CatalogTrackID: "ya_42",
		TrackID:        "ya_42",
	}
	id, ok := tr1.GetYandexTrackID()
	if !ok || id != 42 {
		t.Errorf("tr1: got id=%d, ok=%v; want 42, true", id, ok)
	}

	// 2. YandexTrackID == 0 but CatalogTrackID is ya_999
	tr2 := ResolvedTrack{
		CatalogTrackID: "ya_999",
		TrackID:        "ya_999_2",
	}
	id, ok = tr2.GetYandexTrackID()
	if !ok || id != 999 {
		t.Errorf("tr2: got id=%d, ok=%v; want 999, true", id, ok)
	}

	// 3. Third-party Deezer track
	tr3 := ResolvedTrack{
		CatalogTrackID: "dz_12345",
		TrackID:        "dz_12345",
	}
	id, ok = tr3.GetYandexTrackID()
	if ok || id != 0 {
		t.Errorf("tr3: got id=%d, ok=%v; want 0, false", id, ok)
	}
}
