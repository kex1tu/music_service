package resolver

import (
	"testing"
)

func TestSoundCloudParser_TitleAndArtist(t *testing.T) {
	tests := []struct {
		name         string
		rawTitle     string
		uploader     string
		wantArtist   string
		wantTitle    string
	}{
		{
			name:       "Track with dash and FREE DL promo junk and aggregator uploader",
			rawTitle:   "DEAD BLONDE - Банкомат [FREE DL]",
			uploader:   "Русская Рэп-Сцена",
			wantArtist: "DEAD BLONDE",
			wantTitle:  "Банкомат",
		},
		{
			name:       "Track with em-dash and Buy=Free DL promo junk and official uploader",
			rawTitle:   "DEAD BLONDE — Перестройка (Buy = Free DL)",
			uploader:   "DEAD BLONDE Official",
			wantArtist: "DEAD BLONDE",
			wantTitle:  "Перестройка",
		},
		{
			name:       "Track without dash where uploader is the artist",
			rawTitle:   "Мальчик на девятке",
			uploader:   "DEAD BLONDE",
			wantArtist: "DEAD BLONDE",
			wantTitle:  "Мальчик на девятке",
		},
		{
			name:       "Aggregator prefix in title",
			rawTitle:   "Русская Рэп-Сцена — DEAD BLONDE - Бесприданница [OUT NOW]",
			uploader:   "Русская Рэп-Сцена",
			wantArtist: "DEAD BLONDE",
			wantTitle:  "Бесприданница",
		},
		{
			name:       "Track with prod. by and free download",
			rawTitle:   "DEAD BLONDE - Первая любовь (prod. by GSPD) [FREE DOWNLOAD]",
			uploader:   "deadblonde-music",
			wantArtist: "DEAD BLONDE",
			wantTitle:  "Первая любовь",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotArtist, gotTitle := ParseSoundCloudTitle(tt.rawTitle, tt.uploader)
			if gotArtist != tt.wantArtist {
				t.Errorf("ParseSoundCloudTitle() artist = %q, want %q", gotArtist, tt.wantArtist)
			}
			if gotTitle != tt.wantTitle {
				t.Errorf("ParseSoundCloudTitle() title = %q, want %q", gotTitle, tt.wantTitle)
			}
		})
	}
}

func TestSoundCloudParser_DEADBLONDE_SetTracks(t *testing.T) {
	tracks := []struct {
		rawTitle string
		uploader string
		wantArt  string
		wantTit  string
	}{
		{"DEAD BLONDE - Банкомат", "Русская Рэп-Сцена", "DEAD BLONDE", "Банкомат"},
		{"DEAD BLONDE - Мальчик на девятке [FREE DL]", "Russian Rap", "DEAD BLONDE", "Мальчик на девятке"},
		{"DEAD BLONDE - Бесприданница", "DEAD BLONDE Official", "DEAD BLONDE", "Бесприданница"},
	}

	for _, tr := range tracks {
		art, tit := ParseSoundCloudTitle(tr.rawTitle, tr.uploader)
		if art != tr.wantArt {
			t.Errorf("expected artist %s, got %s", tr.wantArt, art)
		}
		if tit != tr.wantTit {
			t.Errorf("expected title %s, got %s", tr.wantTit, tit)
		}
	}
}

