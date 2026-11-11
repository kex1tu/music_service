package tageditor_test

import (
	"bytes"
	"image"
	"image/jpeg"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"music-manager/internal/core/auth"
	"music-manager/internal/features/tageditor"
	"music-manager/internal/id3"
	"music-manager/internal/services/organizer"
	"music-manager/internal/services/resolver"
	"music-manager/internal/testkit"
)

// setupTagEditorTest boots an isolated TestEnv with SQLite, sandboxes, MockTransport,
// instantiates tageditor.Module and mounts all routes under auth middleware.
func setupTagEditorTest(t *testing.T) (*testkit.TestEnv, *tageditor.Module) {
	t.Helper()
	env := testkit.NewEnv(t)

	repo := tageditor.NewRepository(env.DB.Conn())
	orgSvc := organizer.NewService(env.MusicSandbox, env.TmpSandbox)
	deezerClient := resolver.NewDeezerClient(env.MockHTTP)
	mbClient := resolver.NewMusicBrainzClient("")
	mbClient.SetOAuthConfig("test_client_id", "test_client_secret", "http://localhost:8080/api/auth/musicbrainz/callback")
	mbClient.SetPersistence(repo)
	shazamProvider := resolver.NewShazamProvider(nil)

	svc := tageditor.NewService(tageditor.ServiceConfig{
		MusicSandbox: env.MusicSandbox,
		TmpSandbox:   env.TmpSandbox,
		Repo:         repo,
		Organizer:    orgSvc,
		Deezer:       deezerClient,
		MusicBrainz:  mbClient,
		Shazam:       shazamProvider,
		HTTPClient:   env.Client,
		MusicBrainzConf: tageditor.MusicBrainzConfig{
			ClientID:     "test_client_id",
			ClientSecret: "test_client_secret",
			RedirectURI:  "http://localhost:8080/api/auth/musicbrainz/callback",
		},
	})

	mod := tageditor.NewModule(svc)

	r := chi.NewRouter()
	authMiddleware := auth.Middleware(env.SecretKey)

	// Public callback
	r.Get("/api/auth/musicbrainz/callback", mod.MusicBrainzCallback)

	// Protected routes
	r.Group(func(r chi.Router) {
		r.Use(authMiddleware)
		mod.RegisterRoutes(r)
	})

	env.Mount(r)
	env.SetAuthUser("admin")

	return env, mod
}

// 1. Unauthorized Access Test
func TestTagEditor_UnauthorizedAccess(t *testing.T) {
	env, _ := setupTagEditorTest(t)
	env.ClearAuth()

	endpoints := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/tags?path=song.mp3"},
		{http.MethodPost, "/tags"},
		{http.MethodPost, "/tags/batch"},
		{http.MethodGet, "/tags/cover?path=song.mp3"},
		{http.MethodGet, "/tags/covers/search?query=test"},
		{http.MethodGet, "/tags/deezer/albums?query=test"},
		{http.MethodGet, "/api/catalog/search?query=test"},
		{http.MethodGet, "/api/auth/musicbrainz/status"},
		{http.MethodPost, "/files/rename-pattern"},
		{http.MethodGet, "/api/tags?path=song.mp3"},
	}

	for _, ep := range endpoints {
		t.Run(ep.method+" "+ep.path, func(t *testing.T) {
			req := env.NewRequest(ep.method, ep.path, nil)
			resp := env.Do(req)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("expected 401 Unauthorized for %s %s, got %d", ep.method, ep.path, resp.StatusCode)
			}
		})
	}
}

// 2. Read Single Track Tags
func TestTagEditor_ReadTags_SingleTrack(t *testing.T) {
	env, _ := setupTagEditorTest(t)

	relPath := "Rock/01. Song.mp3"
	tags := id3.TrackTags{
		Title:       "Boulevard of Broken Dreams",
		Artist:      "Green Day",
		Album:       "American Idiot",
		AlbumArtist: "Green Day",
		TrackNumber: 4,
		TrackTotal:  13,
		DiscNumber:  1,
		DiscTotal:   1,
		Year:        2004,
		Genre:       "Punk Rock",
	}
	env.CreateDummyMP3WithTags(relPath, tags, nil)

	resp := env.Get("/tags?path=" + url.QueryEscape(relPath))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d, body: %s", resp.StatusCode, string(env.ReadBody(resp)))
	}

	var data map[string]any
	env.DecodeJSON(resp, &data)

	if data["title"] != "Boulevard of Broken Dreams" {
		t.Errorf("expected title 'Boulevard of Broken Dreams', got %v", data["title"])
	}
	if data["artist"] != "Green Day" {
		t.Errorf("expected artist 'Green Day', got %v", data["artist"])
	}
	albumObj, ok := data["album"].(map[string]any)
	if !ok || albumObj["album"] != "American Idiot" {
		t.Errorf("expected album 'American Idiot', got %v", albumObj)
	}
}

// 3. Read Album Directory with Cover
func TestTagEditor_ReadTags_AlbumDirectory(t *testing.T) {
	env, _ := setupTagEditorTest(t)

	albumDir := "Pink Floyd - The Dark Side of the Moon"
	t1 := filepath.Join(albumDir, "01. Speak to Me.mp3")
	t2 := filepath.Join(albumDir, "02. Breathe.mp3")

	env.CreateDummyMP3WithTags(t1, id3.TrackTags{
		Title:       "Speak to Me",
		Artist:      "Pink Floyd",
		Album:       "The Dark Side of the Moon",
		TrackNumber: 1,
		Year:        1973,
		Genre:       "Progressive Rock",
	}, nil)

	env.CreateDummyMP3WithTags(t2, id3.TrackTags{
		Title:       "Breathe",
		Artist:      "Pink Floyd",
		Album:       "The Dark Side of the Moon",
		TrackNumber: 2,
		Year:        1973,
		Genre:       "Progressive Rock",
	}, nil)

	// Add cover.jpg
	env.CreateDummyFile(env.MusicSandbox, filepath.Join(albumDir, "cover.jpg"), []byte("dummy-jpg-cover-art"))

	resp := env.Get("/tags?path=" + url.QueryEscape(albumDir))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	var data struct {
		Album struct {
			Album       string `json:"album"`
			AlbumArtist string `json:"album_artist"`
			Year        *int   `json:"year"`
			Genre       string `json:"genre"`
			TrackTotal  int    `json:"track_total"`
		} `json:"album"`
		Tracks    []id3.TrackTags `json:"tracks"`
		HasCover  bool            `json:"has_cover"`
		CoverPath *string         `json:"cover_path"`
	}
	env.DecodeJSON(resp, &data)

	if data.Album.Album != "The Dark Side of the Moon" {
		t.Errorf("expected album title, got %q", data.Album.Album)
	}
	if len(data.Tracks) != 2 {
		t.Fatalf("expected 2 tracks, got %d", len(data.Tracks))
	}
	if data.Tracks[0].TrackNumber != 1 || data.Tracks[1].TrackNumber != 2 {
		t.Errorf("tracks not sorted by track_number: %+v", data.Tracks)
	}
	if !data.HasCover {
		t.Errorf("expected has_cover = true")
	}
	expectedCoverRel := albumDir + "/cover.jpg"
	if data.CoverPath == nil || *data.CoverPath != expectedCoverRel {
		t.Errorf("expected cover_path %q, got %v", expectedCoverRel, data.CoverPath)
	}
}

// 4. Save Tags with ID3v2.4 Null-Separated Standard
func TestTagEditor_SaveTags_ID3v24NullSeparated(t *testing.T) {
	env, _ := setupTagEditorTest(t)

	relPath := "Collabs/01. Hit.mp3"
	env.CreateDummyMP3(relPath, "Original Title", "Artist A", "Collab Album")

	// Save tags with double backslash (UI representation)
	savePayload := id3.TrackTags{
		Path:        relPath,
		Title:       "Updated Hit",
		Artist:      "Artist A\\\\Artist B\\\\Artist C", // Multi-artist
		Album:       "Collab Album",
		AlbumArtist: "Artist A",
		TrackNumber: 1,
		TrackTotal:  1,
		Year:        2026,
		Genre:       "Hip Hop\\\\Trap", // Multi-genre
	}

	resp := env.PostJSON("/tags", savePayload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d, body: %s", resp.StatusCode, string(env.ReadBody(resp)))
	}

	// 1. Verify read-back via id3 engine
	absPath, err := env.MusicSandbox.Resolve(relPath, true)
	if err != nil {
		t.Fatal(err)
	}
	readTags, err := id3.ReadTags(absPath, relPath)
	if err != nil {
		t.Fatalf("failed to read back tags: %v", err)
	}

	if readTags.Artist != "Artist A\\\\Artist B\\\\Artist C" {
		t.Errorf("expected UI-formatted artist, got %q", readTags.Artist)
	}
	if readTags.Genre != "Hip-Hop\\\\Trap" {
		t.Errorf("expected UI-formatted genre, got %q", readTags.Genre)
	}

	// 2. Direct binary verification of ID3v2.4 frame contents: must contain \x00 null bytes, NOT "\\"
	fileBytes, err := os.ReadFile(absPath)
	if err != nil {
		t.Fatal(err)
	}

	// ID3v2.4 header check (ID3 + version 4.0)
	if string(fileBytes[:3]) != "ID3" || fileBytes[3] != 4 {
		t.Fatalf("expected ID3v2.4 tag header")
	}

	// TPE1 (Lead performer) frame payload check: "Artist A\x00Artist B\x00Artist C"
	expectedTPE1Payload := []byte("Artist A\x00Artist B\x00Artist C")
	if !bytes.Contains(fileBytes, expectedTPE1Payload) {
		t.Errorf("binary ID3 frame does not contain null-separated artists")
	}

	// Verify no lyrics frames (USLT / SYLT) exist
	if bytes.Contains(fileBytes, []byte("USLT")) || bytes.Contains(fileBytes, []byte("SYLT")) {
		t.Errorf("forbidden lyrics frame (USLT/SYLT) found embedded in MP3")
	}
}

// 5. Batch Tag Save with Single Track Propagation
func TestTagEditor_BatchSaveTags_SingleTrackPropagation(t *testing.T) {
	env, _ := setupTagEditorTest(t)

	relPath := "TempSingle/01. Track.mp3"
	env.CreateDummyMP3(relPath, "Solo Anthem", "Indie Creator", "")

	batchReq := tageditor.BatchSaveRequest{
		SourceDir: "TempSingle",
		Tracks: []id3.TrackTags{
			{
				Path:        relPath,
				Title:       "Solo Anthem",
				Artist:      "Indie Creator",
				TrackNumber: 1,
				Year:        2026,
				Genre:       "Indie Pop",
			},
		},
		Reorganize: true,
	}

	resp := env.PostJSON("/tags/batch", batchReq)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d, body: %s", resp.StatusCode, string(env.ReadBody(resp)))
	}

	var res tageditor.BatchSaveResponse
	env.DecodeJSON(resp, &res)

	if !res.Success {
		t.Fatal("expected success true")
	}

	expectedDir := "Solo Anthem - Indie Creator"
	if res.DestinationDir != expectedDir {
		t.Errorf("expected destination_dir %q, got %q", expectedDir, res.DestinationDir)
	}
}

// 6. Puddletag Pattern Rename with Companion .lrc and .txt Moves
func TestTagEditor_Puddletag_RenamePattern_Execution(t *testing.T) {
	env, _ := setupTagEditorTest(t)

	albumFolder := "Rock Classics"
	oldAudio := filepath.Join(albumFolder, "01. OldTrack.mp3")
	oldLrc := filepath.Join(albumFolder, "01. OldTrack.lrc")
	oldTxt := filepath.Join(albumFolder, "01. OldTrack.txt")

	env.CreateDummyMP3(oldAudio, "OldTrack", "Rockers", "Rock Classics")
	env.CreateDummyFile(env.MusicSandbox, oldLrc, []byte("[00:01.00]First lyric line"))
	env.CreateDummyFile(env.MusicSandbox, oldTxt, []byte("Plain text lyrics"))

	req := tageditor.RenamePatternRequest{
		DryRun: false,
		Items: []tageditor.RenamePatternItem{
			{
				CurrentPath: oldAudio,
				NewName:     "01 - Rockers - NewTrack.mp3",
			},
		},
	}

	resp := env.PostJSON("/files/rename-pattern", req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	var results []tageditor.RenamePatternResult
	env.DecodeJSON(resp, &results)

	if len(results) != 1 || results[0].Status != "ok" {
		t.Fatalf("rename failed: %+v", results)
	}

	expectedNewRel := filepath.ToSlash(filepath.Join(albumFolder, "01 - Rockers - NewTrack.mp3"))
	if results[0].NewPath != expectedNewRel {
		t.Errorf("expected new_path %q, got %q", expectedNewRel, results[0].NewPath)
	}

	// Verify old files no longer exist
	oldAudioAbs, _ := env.MusicSandbox.Resolve(oldAudio, true)
	if _, err := os.Stat(oldAudioAbs); !os.IsNotExist(err) {
		t.Errorf("old audio file still exists")
	}
	oldLrcAbs, _ := env.MusicSandbox.Resolve(oldLrc, true)
	if _, err := os.Stat(oldLrcAbs); !os.IsNotExist(err) {
		t.Errorf("old .lrc file still exists")
	}

	// Verify new files exist
	newAudioAbs, _ := env.MusicSandbox.Resolve(expectedNewRel, true)
	if _, err := os.Stat(newAudioAbs); err != nil {
		t.Errorf("new audio file was not created: %v", err)
	}
	newLrcRel := filepath.ToSlash(filepath.Join(albumFolder, "01 - Rockers - NewTrack.lrc"))
	newLrcAbs, _ := env.MusicSandbox.Resolve(newLrcRel, true)
	if data, err := os.ReadFile(newLrcAbs); err != nil || string(data) != "[00:01.00]First lyric line" {
		t.Errorf("companion .lrc was not migrated properly: %v", err)
	}
}

// 7. Puddletag Pattern Rename DryRun
func TestTagEditor_Puddletag_RenamePattern_DryRun(t *testing.T) {
	env, _ := setupTagEditorTest(t)

	trackRel := "Pop/01. Track.mp3"
	env.CreateDummyMP3(trackRel, "Track", "Artist", "Pop")

	req := tageditor.RenamePatternRequest{
		DryRun: true,
		Items: []tageditor.RenamePatternItem{
			{
				CurrentPath: trackRel,
				NewName:     "01 - Artist - Track.mp3",
			},
		},
	}

	resp := env.PostJSON("/files/rename-pattern", req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	var results []tageditor.RenamePatternResult
	env.DecodeJSON(resp, &results)

	if len(results) != 1 || results[0].Status != "ok" {
		t.Fatalf("expected dry-run ok status, got %+v", results)
	}

	// Ensure file was NOT actually renamed
	absPath, _ := env.MusicSandbox.Resolve(trackRel, true)
	if _, err := os.Stat(absPath); err != nil {
		t.Errorf("dry-run unexpectedly modified filesystem")
	}
}

// 8. Cover Art Extraction (Embedded APIC & Directory)
func TestTagEditor_CoverArt_Extraction(t *testing.T) {
	env, _ := setupTagEditorTest(t)

	var artwork bytes.Buffer
	if err := jpeg.Encode(&artwork, image.NewRGBA(image.Rect(0, 0, 16, 16)), nil); err != nil {
		t.Fatal(err)
	}
	dummyCoverBytes := artwork.Bytes()
	cover := &id3.CoverInfo{
		MimeType: "image/jpeg",
		Data:     dummyCoverBytes,
	}

	relPath := "AlbumWithCover/01. Track.mp3"
	env.CreateDummyMP3WithTags(relPath, id3.TrackTags{
		Title:  "Track",
		Artist: "Artist",
		Album:  "AlbumWithCover",
	}, cover)

	resp := env.Get("/tags/cover?path=" + url.QueryEscape(relPath))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	if resp.Header.Get("Content-Type") != "image/jpeg" {
		t.Errorf("expected Content-Type image/jpeg, got %q", resp.Header.Get("Content-Type"))
	}
	body := env.ReadBody(resp)
	if !bytes.Equal(body, dummyCoverBytes) {
		t.Errorf("extracted cover data does not match embedded APIC")
	}
}

// 9. Path Traversal Attacks Rejection
func TestTagEditor_PathTraversal_Rejection(t *testing.T) {
	env, _ := setupTagEditorTest(t)

	maliciousPaths := []string{
		"../../etc/passwd",
		"../../../root/.ssh/id_rsa",
		"/etc/shadow",
		"folder/../../outside.mp3",
	}

	for _, p := range maliciousPaths {
		t.Run("GetTags_"+p, func(t *testing.T) {
			resp := env.Get("/tags?path=" + p)
			if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound {
				t.Fatalf("expected 403 or 404 for path traversal %q, got %d", p, resp.StatusCode)
			}
		})

		t.Run("SaveTags_"+p, func(t *testing.T) {
			resp := env.PostJSON("/tags", id3.TrackTags{Path: p, Title: "Hacked"})
			if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected 403/404/400 for path traversal %q, got %d", p, resp.StatusCode)
			}
		})

		t.Run("RenamePattern_"+p, func(t *testing.T) {
			req := tageditor.RenamePatternRequest{
				Items: []tageditor.RenamePatternItem{
					{CurrentPath: "valid.mp3", NewName: p},
				},
			}
			resp := env.PostJSON("/files/rename-pattern", req)
			if resp.StatusCode == http.StatusOK {
				var results []tageditor.RenamePatternResult
				env.DecodeJSON(resp, &results)
				if len(results) > 0 && results[0].Status == "ok" {
					t.Fatalf("rename allowed path traversal target %q", p)
				}
			}
		})
	}
}

// 10. Deezer Catalog Search with Mocking
func TestTagEditor_DeezerCatalogSearch_WithContributors(t *testing.T) {
	env, _ := setupTagEditorTest(t)

	// Mock Deezer search response via testkit MockTransport
	env.MockHTTP.RegisterJSON("api.deezer.com/search/album", http.StatusOK, map[string]any{
		"data": []map[string]any{
			{
				"id":        12345,
				"title":     "Collaborations",
				"nb_tracks": 10,
				"artist": map[string]any{
					"name": "Primary Artist",
				},
				"cover_xl": "https://cdn.deezer.com/album.jpg",
			},
		},
		"total": 1,
	})

	resp := env.Get("/tags/deezer/albums?query=Collaborations")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	var items []map[string]any
	env.DecodeJSON(resp, &items)

	if len(items) != 1 {
		t.Fatalf("expected 1 album item, got %d", len(items))
	}
	if items[0]["title"] != "Collaborations" {
		t.Errorf("expected title 'Collaborations', got %v", items[0]["title"])
	}
}

// 11. Golden JSON Snapshot Assertion
func TestTagEditor_GoldenJSON_Snapshots(t *testing.T) {
	env, _ := setupTagEditorTest(t)

	albumDir := "Golden Album - Demo Band"
	env.CreateDummyMP3WithTags(filepath.Join(albumDir, "01. Track One.mp3"), id3.TrackTags{
		Title:       "Track One",
		Artist:      "Demo Band",
		Album:       "Golden Album",
		TrackNumber: 1,
		TrackTotal:  2,
		Year:        2026,
		Genre:       "Rock",
	}, nil)

	env.CreateDummyMP3WithTags(filepath.Join(albumDir, "02. Track Two.mp3"), id3.TrackTags{
		Title:       "Track Two",
		Artist:      "Demo Band",
		Album:       "Golden Album",
		TrackNumber: 2,
		TrackTotal:  2,
		Year:        2026,
		Genre:       "Rock",
	}, nil)

	resp := env.Get("/tags?path=" + url.QueryEscape(albumDir))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	body := env.ReadBody(resp)
	goldenPath := filepath.Join("testdata", "album_tags.golden.json")
	env.AssertGoldenJSON(body, goldenPath)
}

// 12. MusicBrainz OAuth Status and Persistence
func TestTagEditor_MusicBrainz_OAuthStatus(t *testing.T) {
	env, _ := setupTagEditorTest(t)

	resp := env.Get("/api/auth/musicbrainz/status")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	var status resolver.MusicBrainzOAuthStatus
	env.DecodeJSON(resp, &status)
	if !status.Configured {
		t.Errorf("expected MusicBrainz to be configured")
	}

	// Disconnect
	discResp := env.PostJSON("/api/auth/musicbrainz/disconnect", nil)
	if discResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on disconnect, got %d", discResp.StatusCode)
	}
}

// 13. Repository Operations Unit Verification
func TestTagEditor_Repository_Operations(t *testing.T) {
	env := testkit.NewEnv(t)
	repo := tageditor.NewRepository(env.DB.Conn())

	ctx := t.Context()

	// 1. Artist and Album Creation
	artID, err := repo.GetOrCreateArtist(ctx, "Test Artist")
	if err != nil || artID <= 0 {
		t.Fatalf("failed to create artist: %v", err)
	}

	albID, err := repo.GetOrCreateAlbum(ctx, "Test Album", artID, "Test Artist", 2026, "Test Album - Test Artist", false)
	if err != nil || albID <= 0 {
		t.Fatalf("failed to create album: %v", err)
	}

	// 2. Setting values
	if err := repo.SetSetting(ctx, "test_key", "test_val"); err != nil {
		t.Fatalf("failed to set setting: %v", err)
	}
	val, err := repo.GetSetting(ctx, "test_key")
	if err != nil || val != "test_val" {
		t.Fatalf("expected 'test_val', got %q, err: %v", val, err)
	}

	// 3. MusicBrainz token persistence
	exp := time.Now().Add(1 * time.Hour).Truncate(time.Second)
	if err := repo.SaveMusicBrainzTokens(ctx, "access123", "refresh123", exp, "testuser"); err != nil {
		t.Fatalf("failed to save tokens: %v", err)
	}

	acc, ref, user, expLoaded, err := repo.LoadMusicBrainzTokens(ctx)
	if err != nil {
		t.Fatalf("failed to load tokens: %v", err)
	}
	if acc != "access123" || ref != "refresh123" || user != "testuser" {
		t.Errorf("token mismatch: acc=%q ref=%q user=%q", acc, ref, user)
	}
	if expLoaded.Unix() != exp.Unix() {
		t.Errorf("expiry mismatch: got %v, want %v", expLoaded, exp)
	}

	if err := repo.ClearMusicBrainzTokens(ctx); err != nil {
		t.Fatalf("failed to clear tokens: %v", err)
	}
	accCleared, _, _, _, _ := repo.LoadMusicBrainzTokens(ctx)
	if accCleared != "" {
		t.Errorf("expected empty token after clear, got %q", accCleared)
	}
}
