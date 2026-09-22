package resolver

import (
	"reflect"
	"testing"
)

func TestParseYouTubeTitle(t *testing.T) {
	tests := []struct {
		name         string
		rawTitle     string
		channelName  string
		uploader     string
		wantTitle    string
		wantArtist   string
		wantArtists  []string
		wantFeatured []string
	}{
		{
			name:         "Gazgolder Basta Sansara",
			rawTitle:     "Баста - Сансара (Official Video)",
			channelName:  "ТО \"Gazgolder\"",
			uploader:     "Gazgolder",
			wantTitle:    "Сансара",
			wantArtist:   "Баста",
			wantArtists:  []string{"Баста"},
			wantFeatured: nil,
		},
		{
			name:         "Miyagi feat Rem Digga",
			rawTitle:     "Miyagi & Эндшпиль feat. Рем Дигга - I Got Love (Official Video)",
			channelName:  "Hajime Records",
			uploader:     "Hajime",
			wantTitle:    "I Got Love",
			wantArtist:   "Miyagi & Эндшпиль\\\\Рем Дигга",
			wantArtists:  []string{"Miyagi & Эндшпиль", "Рем Дигга"},
			wantFeatured: []string{"Рем Дигга"},
		},
		{
			name:         "Feat in track title in parentheses",
			rawTitle:     "Баста - Голос (feat. Полина Гагарина) [Премьера клипа 2016]",
			channelName:  "Gazgolder",
			uploader:     "Gazgolder",
			wantTitle:    "Голос",
			wantArtist:   "Баста\\\\Полина Гагарина",
			wantArtists:  []string{"Баста", "Полина Гагарина"},
			wantFeatured: []string{"Полина Гагарина"},
		},
		{
			name:         "Topic channel without dash",
			rawTitle:     "Lose Yourself",
			channelName:  "Eminem - Topic",
			uploader:     "Eminem - Topic",
			wantTitle:    "Lose Yourself",
			wantArtist:   "Eminem",
			wantArtists:  []string{"Eminem"},
			wantFeatured: nil,
		},
		{
			name:         "Trailing slash junk",
			rawTitle:     "Скриптонит - Положение / Премьера клипа",
			channelName:  "Gazgolder",
			uploader:     "Gazgolder",
			wantTitle:    "Положение",
			wantArtist:   "Скриптонит",
			wantArtists:  []string{"Скриптонит"},
			wantFeatured: nil,
		},
		{
			name:         "Producer in brackets",
			rawTitle:     "Markul ft. Oxxxymiron — Fata Morgana (prod. by stereoRYZE)",
			channelName:  "MARKUL",
			uploader:     "MARKUL",
			wantTitle:    "Fata Morgana",
			wantArtist:   "Markul\\\\Oxxxymiron",
			wantArtists:  []string{"Markul", "Oxxxymiron"},
			wantFeatured: []string{"Oxxxymiron"},
		},
		{
			name:         "Hyphen inside artist name",
			rawTitle:     "Jay-Z - Empire State of Mind [Official Music Video]",
			channelName:  "JAY-Z",
			uploader:     "JAY-Z",
			wantTitle:    "Empire State of Mind",
			wantArtist:   "Jay-Z",
			wantArtists:  []string{"Jay-Z"},
			wantFeatured: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseYouTubeTitle(tt.rawTitle, tt.channelName, tt.uploader)
			if got.Title != tt.wantTitle {
				t.Errorf("Title = %q, want %q", got.Title, tt.wantTitle)
			}
			if got.Artist != tt.wantArtist {
				t.Errorf("Artist = %q, want %q", got.Artist, tt.wantArtist)
			}
			if len(got.Artists) != len(tt.wantArtists) {
				t.Errorf("Artists length = %d, want %d (%v vs %v)", len(got.Artists), len(tt.wantArtists), got.Artists, tt.wantArtists)
			} else {
				for i := range got.Artists {
					if got.Artists[i] != tt.wantArtists[i] {
						t.Errorf("Artists[%d] = %q, want %q", i, got.Artists[i], tt.wantArtists[i])
					}
				}
			}
			if len(tt.wantFeatured) > 0 {
				if !reflect.DeepEqual(got.Featured, tt.wantFeatured) {
					t.Errorf("Featured = %v, want %v", got.Featured, tt.wantFeatured)
				}
			}
		})
	}
}
