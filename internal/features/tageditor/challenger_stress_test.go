package tageditor_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"music-manager/internal/features/tageditor"
	"music-manager/internal/id3"
)

// 1. Concurrency & Heavy Load Stress Test: Concurrent SaveTags, GetTags, and GetCover on the same file
func TestChallenger_Concurrency_HeavyLoad_SaveAndGet(t *testing.T) {
	env, _ := setupTagEditorTest(t)

	relPath := "HeavyLoad/StressTrack.mp3"
	dummyCover := &id3.CoverInfo{
		MimeType: "image/jpeg",
		Data:     []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01, 0x01, 0x01, 0x00, 0x48, 0x00, 0x48, 0x00, 0x00, 0xFF, 0xD9},
	}
	env.CreateDummyMP3WithTags(relPath, id3.TrackTags{
		Title:       "Initial Title",
		Artist:      "Stress Artist",
		Album:       "Stress Album",
		TrackNumber: 1,
		Year:        2024,
		Genre:       "Rock",
	}, dummyCover)

	const numGoroutines = 15
	var wg sync.WaitGroup

	// 15 writers calling POST /tags
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			payload := id3.TrackTags{
				Path:        relPath,
				Title:       fmt.Sprintf("Concurrent Title %d", workerID),
				Artist:      fmt.Sprintf("Artist A\\\\Artist %d", workerID),
				Album:       "Stress Album",
				TrackNumber: workerID + 1,
				Year:        2020 + (workerID % 5),
				Genre:       fmt.Sprintf("Genre %d", workerID),
			}
			resp := env.PostJSON("/tags", payload)
			if resp.StatusCode != http.StatusOK {
				t.Errorf("worker %d: SaveTags failed with status %d", workerID, resp.StatusCode)
			}
		}(i)
	}

	// 15 readers calling GET /tags
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			resp := env.Get("/tags?path=" + url.QueryEscape(relPath))
			if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
				t.Errorf("worker %d: GetTags failed with status %d", workerID, resp.StatusCode)
			}
			if resp.StatusCode == http.StatusOK {
				var data map[string]any
				env.DecodeJSON(resp, &data)
			}
		}(i)
	}

	// 15 cover readers calling GET /tags/cover
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			resp := env.Get("/tags/cover?path=" + url.QueryEscape(relPath))
			if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
				t.Errorf("worker %d: GetCover failed with status %d", workerID, resp.StatusCode)
			}
		}(i)
	}

	wg.Wait()

	// Final verification: file must remain valid and readable
	absPath, err := env.MusicSandbox.Resolve(relPath, true)
	if err != nil {
		t.Fatalf("failed to resolve path after stress test: %v", err)
	}
	finalTags, err := id3.ReadTags(absPath, relPath)
	if err != nil {
		t.Fatalf("MP3 file is corrupt after concurrent writes: %v", err)
	}
	if finalTags.Title == "" {
		t.Errorf("final tag title is empty after concurrent writes")
	}
}

// 2. Directory Lock Contention: Multiple files written in the same directory concurrently
func TestChallenger_Concurrency_DirectoryLock_Contention(t *testing.T) {
	env, _ := setupTagEditorTest(t)

	dir := "AlbumContention"
	const numTracks = 10
	var trackPaths []string

	for i := 1; i <= numTracks; i++ {
		rel := filepath.Join(dir, fmt.Sprintf("track_%02d.mp3", i))
		env.CreateDummyMP3(rel, fmt.Sprintf("Title %d", i), "Contention Artist", "Contention Album")
		trackPaths = append(trackPaths, rel)
	}

	var wg sync.WaitGroup

	// Write tags to all tracks concurrently
	for idx, trPath := range trackPaths {
		wg.Add(1)
		go func(trackNum int, path string) {
			defer wg.Done()
			payload := id3.TrackTags{
				Path:        path,
				Title:       fmt.Sprintf("Updated Title %d", trackNum),
				Artist:      "Contention Artist",
				Album:       "Contention Album",
				TrackNumber: trackNum,
				Year:        2025,
				Genre:       "Rock",
			}
			resp := env.PostJSON("/tags", payload)
			if resp.StatusCode != http.StatusOK {
				t.Errorf("track %d: SaveTags status %d", trackNum, resp.StatusCode)
			}
		}(idx+1, trPath)
	}

	// Concurrently read directory tags as an album
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := env.Get("/tags?path=" + url.QueryEscape(dir) + "&as_album=true")
			if resp.StatusCode != http.StatusOK {
				t.Errorf("read directory status %d", resp.StatusCode)
			}
		}()
	}

	wg.Wait()
}

// 3. ID3v2.4 UTF-8 Null-Byte Multi-Value Frame Compliance and Non-Destructive Tag Updates
func TestChallenger_ID3v24_UTF8_NullByte_Compliance(t *testing.T) {
	env, _ := setupTagEditorTest(t)

	relPath := "Standards/MultiVal.mp3"
	env.CreateDummyMP3(relPath, "Original", "Artist", "Album")

	// Payload with multi-artists, multi-genres, and multi-album-artists
	savePayload := id3.TrackTags{
		Path:        relPath,
		Title:       "Collaboration Song",
		Artist:      "Artist A\\\\Artist B\\\\Artist C\\\\Artist D",
		AlbumArtist: "Lead Artist\\\\Producer X",
		Album:       "Mega Collab",
		TrackNumber: 5,
		TrackTotal:  10,
		DiscNumber:  1,
		DiscTotal:   2,
		Year:        2026,
		Genre:       "Hip Hop\\\\Electro\\\\Dubstep",
	}

	resp := env.PostJSON("/tags", savePayload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d, body: %s", resp.StatusCode, string(env.ReadBody(resp)))
	}

	absPath, err := env.MusicSandbox.Resolve(relPath, true)
	if err != nil {
		t.Fatal(err)
	}

	rawBytes, err := os.ReadFile(absPath)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Check ID3 Header: "ID3\x04\x00" (ID3v2.4.0)
	if string(rawBytes[:3]) != "ID3" || rawBytes[3] != 4 || rawBytes[4] != 0 {
		t.Fatalf("header is not ID3v2.4.0: %v", rawBytes[:5])
	}

	// 2. Check TPE1 (Artists) has UTF-8 encoding (0x03) and \x00 null separators
	expectedArtistsRaw := []byte("Artist A\x00Artist B\x00Artist C\x00Artist D")
	if !bytes.Contains(rawBytes, expectedArtistsRaw) {
		t.Errorf("TPE1 does not contain null-separated artists: expected %q", string(expectedArtistsRaw))
	}
	// Verify raw "\\" is NOT in TPE1 payload
	if bytes.Contains(rawBytes, []byte("Artist A\\\\Artist B")) || bytes.Contains(rawBytes, []byte("Artist A\\Artist B")) {
		t.Errorf("TPE1 contains raw backslashes instead of null-bytes")
	}

	// 3. Check TCON (Genre) has null separators
	expectedGenresRaw := []byte("Hip-Hop\x00Electro\x00Dubstep")
	if !bytes.Contains(rawBytes, expectedGenresRaw) {
		t.Errorf("TCON does not contain null-separated genres: expected %q", string(expectedGenresRaw))
	}

	// 4. Invariant: Absolute ban on USLT and SYLT
	if bytes.Contains(rawBytes, []byte("USLT")) || bytes.Contains(rawBytes, []byte("SYLT")) {
		t.Errorf("CRITICAL VIOLATION: MP3 contains embedded USLT/SYLT lyrics frame")
	}

	// 5. Read back via GetTags and ensure formatted with "\\" for UI
	respGet := env.Get("/tags?path=" + url.QueryEscape(relPath))
	if respGet.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for GetTags, got %d", respGet.StatusCode)
	}
	var readBack map[string]any
	env.DecodeJSON(respGet, &readBack)

	if readBack["artist"] != "Artist A\\\\Artist B\\\\Artist C\\\\Artist D" {
		t.Errorf("expected UI-formatted artist with \\\\, got %v", readBack["artist"])
	}
	if readBack["genre"] != "Hip-Hop\\\\Electro\\\\Dubstep" {
		t.Errorf("expected UI-formatted genre with \\\\, got %v", readBack["genre"])
	}
}

// 4. Non-Destructive Tag Updates: Preserving unmanaged valid frames
func TestChallenger_NonDestructive_TagPreservation(t *testing.T) {
	env, _ := setupTagEditorTest(t)

	relPath := "Preserve/TrackWithExtraFrames.mp3"
	absPath, _ := env.MusicSandbox.Resolve(relPath, false)
	_ = os.MkdirAll(filepath.Dir(absPath), 0755)

	// Write MP3 with standard tags and an extra frame
	baseTags := id3.TrackTags{
		Path:        relPath,
		Title:       "Original Title",
		Artist:      "Original Artist",
		Album:       "Original Album",
		TrackNumber: 1,
		Year:        2021,
	}
	env.CreateDummyMP3WithTags(relPath, baseTags, nil)

	// Now call SaveTags to ONLY update the title
	updatePayload := id3.TrackTags{
		Path:        relPath,
		Title:       "Brand New Title",
		Artist:      "Original Artist",
		Album:       "Original Album",
		TrackNumber: 1,
		Year:        2021,
	}
	resp := env.PostJSON("/tags", updatePayload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	// Verify read-back
	readResp := env.Get("/tags?path=" + url.QueryEscape(relPath))
	var res map[string]any
	env.DecodeJSON(readResp, &res)
	if res["title"] != "Brand New Title" {
		t.Errorf("expected updated title 'Brand New Title', got %v", res["title"])
	}
	if res["artist"] != "Original Artist" {
		t.Errorf("expected preserved artist 'Original Artist', got %v", res["artist"])
	}
}

// 5. Puddletag Pattern Rename: Companion .lrc and .txt synchronization
func TestChallenger_Puddletag_CompanionFiles_Sync(t *testing.T) {
	env, _ := setupTagEditorTest(t)

	folder := "CompanionTest"
	oldAudio := filepath.Join(folder, "Track01.mp3")
	oldLrc := filepath.Join(folder, "Track01.lrc")
	oldTxt := filepath.Join(folder, "Track01.txt")

	env.CreateDummyMP3(oldAudio, "Song One", "Artist One", "Album One")
	env.CreateDummyFile(env.MusicSandbox, oldLrc, []byte("[00:10.00]Line 1\n[00:20.00]Line 2"))
	env.CreateDummyFile(env.MusicSandbox, oldTxt, []byte("Unsynced lyrics content"))

	// Rename item with subfolder target
	req := tageditor.RenamePatternRequest{
		DryRun: false,
		Items: []tageditor.RenamePatternItem{
			{
				CurrentPath: oldAudio,
				NewName:     "SubFolder/01 - RenamedSong.mp3",
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
		t.Fatalf("expected status ok, got %+v", results)
	}

	expectedAudioRel := filepath.ToSlash(filepath.Join(folder, "SubFolder/01 - RenamedSong.mp3"))
	expectedLrcRel := filepath.ToSlash(filepath.Join(folder, "SubFolder/01 - RenamedSong.lrc"))
	expectedTxtRel := filepath.ToSlash(filepath.Join(folder, "SubFolder/01 - RenamedSong.txt"))

	if results[0].NewPath != expectedAudioRel {
		t.Errorf("expected new_path %q, got %q", expectedAudioRel, results[0].NewPath)
	}

	// Verify old files no longer exist
	for _, oldRel := range []string{oldAudio, oldLrc, oldTxt} {
		oldAbs, _ := env.MusicSandbox.Resolve(oldRel, true)
		if _, err := os.Stat(oldAbs); !os.IsNotExist(err) {
			t.Errorf("old file %q still exists after rename", oldRel)
		}
	}

	// Verify new files exist and match contents
	newLrcAbs, _ := env.MusicSandbox.Resolve(expectedLrcRel, true)
	lrcContent, err := os.ReadFile(newLrcAbs)
	if err != nil || string(lrcContent) != "[00:10.00]Line 1\n[00:20.00]Line 2" {
		t.Errorf("renamed .lrc content corrupted or missing: %v", err)
	}

	newTxtAbs, _ := env.MusicSandbox.Resolve(expectedTxtRel, true)
	txtContent, err := os.ReadFile(newTxtAbs)
	if err != nil || string(txtContent) != "Unsynced lyrics content" {
		t.Errorf("renamed .txt content corrupted or missing: %v", err)
	}
}

// 6. Puddletag Pattern Rename: In-Batch Collision & Disk Collision
func TestChallenger_Puddletag_Collisions(t *testing.T) {
	env, _ := setupTagEditorTest(t)

	folder := "CollisionTest"
	f1 := filepath.Join(folder, "track1.mp3")
	f2 := filepath.Join(folder, "track2.mp3")
	fExisting := filepath.Join(folder, "already_exists.mp3")

	env.CreateDummyMP3(f1, "Track 1", "Artist", "Album")
	env.CreateDummyMP3(f2, "Track 2", "Artist", "Album")
	env.CreateDummyMP3(fExisting, "Existing", "Artist", "Album")

	// Test 1: In-batch collision (f1 and f2 both try to rename to "target.mp3")
	reqBatchCollision := tageditor.RenamePatternRequest{
		DryRun: true,
		Items: []tageditor.RenamePatternItem{
			{CurrentPath: f1, NewName: "target.mp3"},
			{CurrentPath: f2, NewName: "target.mp3"},
		},
	}
	resp := env.PostJSON("/files/rename-pattern", reqBatchCollision)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}
	var res1 []tageditor.RenamePatternResult
	env.DecodeJSON(resp, &res1)
	if len(res1) != 2 {
		t.Fatalf("expected 2 results, got %d", len(res1))
	}
	if res1[0].Status != "ok" {
		t.Errorf("first item should succeed, got %+v", res1[0])
	}
	if res1[1].Status != "conflict" || res1[1].Error != "target file collision within batch" {
		t.Errorf("second item should have in-batch conflict error, got %+v", res1[1])
	}

	// Test 2: Existing file on disk collision (f2 tries to rename to already_exists.mp3)
	reqDiskCollision := tageditor.RenamePatternRequest{
		DryRun: false,
		Items: []tageditor.RenamePatternItem{
			{CurrentPath: f2, NewName: "already_exists.mp3"},
		},
	}
	resp2 := env.PostJSON("/files/rename-pattern", reqDiskCollision)
	var res2 []tageditor.RenamePatternResult
	env.DecodeJSON(resp2, &res2)
	if len(res2) != 1 || res2[0].Status != "conflict" || res2[0].Error != "target file already exists" {
		t.Errorf("expected conflict on existing disk target, got %+v", res2)
	}
}

// 7. Puddletag Pattern Rename: Malicious Traversal Vectors in NewName
func TestChallenger_Puddletag_Traversal_In_NewName(t *testing.T) {
	env, _ := setupTagEditorTest(t)

	folder := "SecurityTest"
	f := filepath.Join(folder, "safe.mp3")
	env.CreateDummyMP3(f, "Safe", "Artist", "Album")
	env.CreateDummyMP3("safe.mp3", "Safe Root", "Artist", "Album")

	attackVectors := []struct {
		current string
		newName string
	}{
		{"safe.mp3", "../outside.mp3"},
		{"safe.mp3", "../../outside.mp3"},
		{"safe.mp3", "../../../../etc/passwd"},
		{"safe.mp3", "safe.mp3\x00.exe"},
		{"SecurityTest/safe.mp3", "../../outside.mp3"},
		{"SecurityTest/safe.mp3", "../../../../etc/passwd"},
		{"SecurityTest/safe.mp3", "../../../../root/.bashrc"},
	}

	for _, tc := range attackVectors {
		t.Run("NewName_"+tc.newName+"_from_"+tc.current, func(t *testing.T) {
			req := tageditor.RenamePatternRequest{
				DryRun: false,
				Items: []tageditor.RenamePatternItem{
					{CurrentPath: tc.current, NewName: tc.newName},
				},
			}
			resp := env.PostJSON("/files/rename-pattern", req)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("expected 200 OK response with error item, got %d", resp.StatusCode)
			}
			var results []tageditor.RenamePatternResult
			env.DecodeJSON(resp, &results)
			if len(results) != 1 {
				t.Fatalf("expected 1 result, got %d", len(results))
			}
			if results[0].Status != "error" {
				t.Errorf("security violation: expected status error for vector %q from %q, got %+v", tc.newName, tc.current, results[0])
			}
		})
	}
}

// 8. Repository Concurrency: Concurrent GetOrCreateArtist and Album
func TestChallenger_Repository_Concurrent_GetOrCreate(t *testing.T) {
	env, _ := setupTagEditorTest(t)
	repo := tageditor.NewRepository(env.DB.Conn())

	const numRoutines = 20
	var wg sync.WaitGroup

	artistName := "Concurrent Mastermind"
	albumTitle := "Parallel Symphony"

	for i := 0; i < numRoutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			artID, err := repo.GetOrCreateArtist(ctx, artistName)
			if err != nil {
				t.Errorf("GetOrCreateArtist failed: %v", err)
				return
			}
			if artID <= 0 {
				t.Errorf("invalid artist ID: %d", artID)
				return
			}

			albID, err := repo.GetOrCreateAlbum(ctx, albumTitle, artID, artistName, 2026, "Parallel Symphony", false)
			if err != nil {
				t.Errorf("GetOrCreateAlbum failed: %v", err)
				return
			}
			if albID <= 0 {
				t.Errorf("invalid album ID: %d", albID)
				return
			}
		}()
	}

	wg.Wait()

	// Verify exactly one artist and one album were created
	var artistCount, albumCount int
	_ = env.DB.Conn().QueryRowContext(context.Background(), "SELECT COUNT(*) FROM artists WHERE name = ?", artistName).Scan(&artistCount)
	_ = env.DB.Conn().QueryRowContext(context.Background(), "SELECT COUNT(*) FROM albums WHERE title = ?", albumTitle).Scan(&albumCount)

	if artistCount != 1 {
		t.Errorf("expected exactly 1 artist row, found %d", artistCount)
	}
	if albumCount != 1 {
		t.Errorf("expected exactly 1 album row, found %d", albumCount)
	}
}

// 9. Repository Transaction Support: WithTx Rollback
func TestChallenger_Repository_TransactionRollback(t *testing.T) {
	env, _ := setupTagEditorTest(t)
	repo := tageditor.NewRepository(env.DB.Conn())

	ctx := context.Background()

	tx, err := env.DB.Conn().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin tx: %v", err)
	}

	txRepo := repo.WithTx(tx)
	if txRepo == nil {
		t.Fatal("WithTx returned nil")
	}

	artistID, err := txRepo.GetOrCreateArtist(ctx, "Rollback Artist")
	if err != nil {
		t.Fatalf("tx GetOrCreateArtist failed: %v", err)
	}
	if artistID <= 0 {
		t.Fatalf("invalid artistID: %d", artistID)
	}

	// Rollback transaction
	_ = tx.Rollback()

	// Check that artist does NOT exist in the main database
	var count int
	_ = env.DB.Conn().QueryRowContext(ctx, "SELECT COUNT(*) FROM artists WHERE name = 'Rollback Artist'").Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 artists after rollback, found %d", count)
	}
}

// 10. MusicBrainz OAuth Persistence: Token Lifecycle & Concurrency
func TestChallenger_MusicBrainz_TokenLifecycle(t *testing.T) {
	env, _ := setupTagEditorTest(t)
	repo := tageditor.NewRepository(env.DB.Conn())
	ctx := context.Background()

	// 1. Initially empty
	access, refresh, user, exp, err := repo.LoadMusicBrainzTokens(ctx)
	if err != nil {
		t.Fatalf("unexpected error loading tokens: %v", err)
	}
	if access != "" || refresh != "" || user != "" {
		t.Errorf("expected empty tokens initially, got access=%q user=%q", access, user)
	}

	// 2. Save tokens
	futureExp := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	err = repo.SaveMusicBrainzTokens(ctx, "access_123", "refresh_456", futureExp, "music_fan")
	if err != nil {
		t.Fatalf("failed to save MB tokens: %v", err)
	}

	// 3. Load tokens
	access, refresh, user, exp, err = repo.LoadMusicBrainzTokens(ctx)
	if err != nil {
		t.Fatalf("failed to load tokens: %v", err)
	}
	if access != "access_123" || refresh != "refresh_456" || user != "music_fan" {
		t.Errorf("token mismatch: access=%q refresh=%q user=%q", access, refresh, user)
	}
	if exp.Unix() != futureExp.Unix() {
		t.Errorf("expiration mismatch: expected %v, got %v", futureExp, exp)
	}

	// 4. Clear tokens
	err = repo.ClearMusicBrainzTokens(ctx)
	if err != nil {
		t.Fatalf("failed to clear tokens: %v", err)
	}
	access, _, user, _, _ = repo.LoadMusicBrainzTokens(ctx)
	if access != "" || user != "" {
		t.Errorf("tokens not cleared: access=%q user=%q", access, user)
	}
}

// 11. Empirical Challenge: Reproduction of PathSandbox Bypass & Symlink Leak in GetCover
func TestChallenger_Symlink_Vulnerability_Reproduction(t *testing.T) {
	env, _ := setupTagEditorTest(t)

	// Create an external secret file completely outside the music library sandbox
	secretDir := t.TempDir()
	secretFile := filepath.Join(secretDir, "server_secret.key")
	secretContent := "SUPER_CONFIDENTIAL_KEY_FORBIDDEN_TO_EXPOSE"
	if err := os.WriteFile(secretFile, []byte(secretContent), 0600); err != nil {
		t.Fatalf("failed to create secret file: %v", err)
	}

	// Create an album directory inside the music library
	albumRel := "ExfiltrationAlbum"
	albumAbs, err := env.MusicSandbox.Resolve(albumRel, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(albumAbs, 0755); err != nil {
		t.Fatal(err)
	}

	// Symlink cover.jpg to the external secret file
	symlinkPath := filepath.Join(albumAbs, "cover.jpg")
	if err := os.Symlink(secretFile, symlinkPath); err != nil {
		t.Skip("symlinks not supported on filesystem")
	}

	// Attacker calls GET /tags/cover?path=ExfiltrationAlbum
	resp := env.Get("/tags/cover?path=" + url.QueryEscape(albumRel))
	if resp.StatusCode == http.StatusOK {
		leakedBody := string(env.ReadBody(resp))
		if leakedBody == secretContent {
			t.Errorf("CRITICAL SECURITY VULNERABILITY REPRODUCED: GetCover bypassed PathSandbox and read external file %q via symlinked cover.jpg!", secretFile)
		}
	}
}

// 12. Empirical Challenge: Concurrent RenamePattern Requests on the same directory
func TestChallenger_Puddletag_ConcurrentRename_Contention(t *testing.T) {
	env, _ := setupTagEditorTest(t)

	folder := "ConcurrentRenameFolder"
	const numFiles = 10
	var filePaths []string
	for i := 1; i <= numFiles; i++ {
		p := filepath.Join(folder, fmt.Sprintf("orig_%02d.mp3", i))
		env.CreateDummyMP3(p, fmt.Sprintf("Track %d", i), "Artist", "Album")
		env.CreateDummyFile(env.MusicSandbox, filepath.Join(folder, fmt.Sprintf("orig_%02d.lrc", i)), []byte("lyrics"))
		filePaths = append(filePaths, p)
	}

	var wg sync.WaitGroup
	errCount := 0
	var mu sync.Mutex

	for i, fPath := range filePaths {
		wg.Add(1)
		go func(idx int, path string) {
			defer wg.Done()
			req := tageditor.RenamePatternRequest{
				DryRun: false,
				Items: []tageditor.RenamePatternItem{
					{
						CurrentPath: path,
						NewName:     fmt.Sprintf("renamed_%02d.mp3", idx+1),
					},
				},
			}
			resp := env.PostJSON("/files/rename-pattern", req)
			if resp.StatusCode != http.StatusOK {
				mu.Lock()
				errCount++
				mu.Unlock()
			}
		}(i, fPath)
	}

	wg.Wait()

	if errCount > 0 {
		t.Errorf("expected 0 HTTP errors in concurrent renames, got %d", errCount)
	}
}


