package downloader_test

import (
	"testing"

	"music-manager/internal/features/downloader"
	"music-manager/internal/testkit"
)

func TestLibraryLookupUsesRecordingAndExactArtist(t *testing.T) {
	env := testkit.NewEnv(t)
	if _, err := env.DB.Conn().Exec(`
		INSERT INTO artists(id,name,clean_name) VALUES (11,'Ann','ann'), (12,'Anna','anna');
		INSERT INTO albums(id,artist_id,title,clean_title,folder_path) VALUES (21,11,'Album','album','Album');
		INSERT INTO tracks(album_id, artist_id, title, clean_title, file_path, file_size, mtime, isrc)
		VALUES (21,11,'A-B','a b','Album/01.mp3',1,0,'USAAA0000001');`); err != nil {
		t.Fatal(err)
	}
	repo := downloader.NewRepository(env.DB.Conn())
	wrongISRC, rightISRC := "USAAA0000002", "USAAA0000001"
	for _, tc := range []struct {
		title, artist string
		isrc          *string
		want          bool
	}{
		{"A-B", "Ann", nil, true},
		{"A-B", "Anna", nil, false},
		{"A-B (Live)", "Ann", nil, false},
		{"A-B", "Ann", &wrongISRC, false},
		{"A-B (Different spelling)", "Ann", &rightISRC, true},
	} {
		got, err := repo.CheckTrackInLibrary(t.Context(), tc.title, tc.artist, tc.isrc)
		if err != nil || (got != nil) != tc.want {
			t.Errorf("%q/%q: got %v, want match %t, err=%v", tc.artist, tc.title, got, tc.want, err)
		}
	}
}
