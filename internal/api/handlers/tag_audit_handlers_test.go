package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"music-manager/internal/id3"
	"music-manager/internal/services/resolver"
)

func TestAnalyzeTrackIssues(t *testing.T) {
	// 1. Dirty title with official video and unparsed feat
	item := analyzeTrackIssues(
		1,
		"Artist/Album/01. Song.mp3",
		"Miyagi & Эндшпиль - Половина моя (Official Video) (feat. Рем Дигга)",
		"Miyagi & Andy Panda - Topic",
		"Unknown Album",
		0,
		1,
		180.0,
	)

	if item == nil {
		t.Fatal("expected issues to be detected")
	}

	hasDirtyTitle := false
	hasUnparsedFeat := false
	hasLabelArtist := false
	hasMissingYear := false
	hasMissingAlbum := false

	for _, iss := range item.Issues {
		switch iss {
		case "dirty_title":
			hasDirtyTitle = true
		case "unparsed_feat":
			hasUnparsedFeat = true
		case "label_as_artist":
			hasLabelArtist = true
		case "missing_year":
			hasMissingYear = true
		case "missing_album":
			hasMissingAlbum = true
		}
	}

	if !hasDirtyTitle {
		t.Errorf("expected dirty_title issue")
	}
	if !hasUnparsedFeat {
		t.Errorf("expected unparsed_feat issue")
	}
	if !hasLabelArtist {
		t.Errorf("expected label_as_artist issue")
	}
	if !hasMissingYear {
		t.Errorf("expected missing_year issue")
	}
	if !hasMissingAlbum {
		t.Errorf("expected missing_album issue")
	}

	// Verify proposed title is cleaned
	if item.Proposed.Title != "Половина моя" {
		t.Errorf("expected proposed title 'Половина моя', got %q", item.Proposed.Title)
	}

	// Verify Topic suffix removed from artist
	if item.Proposed.Artist == "" || item.Proposed.Artist == "Miyagi & Andy Panda - Topic" {
		t.Errorf("expected clean proposed artist, got %q", item.Proposed.Artist)
	}
}

func TestFileHandler_TagIssues_And_Fix(t *testing.T) {
	h, _, _, database, musicDir, _ := setupTestFileHandler(t)
	defer database.Close()

	// Create album dir and an MP3 with dirty title
	albumDir := filepath.Join(musicDir, "Test Album")
	_ = os.MkdirAll(albumDir, 0755)
	mp3Path := filepath.Join(albumDir, "01. Dirty Track.mp3")
	_ = os.WriteFile(mp3Path, []byte("audio payload data"), 0644)

	tags := id3.TrackTags{
		Title:       "Dirty Track (Official Video)",
		Artist:      "Cool Artist",
		Album:       "Test Album",
		TrackNumber: 1,
		Year:        0, // missing year
	}
	if err := id3.WriteTags(mp3Path, tags, nil); err != nil {
		t.Fatal(err)
	}

	// Scan file to populate DB
	relPath := "Test Album/01. Dirty Track.mp3"
	if err := h.scanner.IndexFile(mp3Path, relPath); err != nil {
		t.Fatal(err)
	}

	// 1. GET /files/audit/tag-issues
	req := httptest.NewRequest(http.MethodGet, "/files/audit/tag-issues", nil)
	w := httptest.NewRecorder()
	h.TagIssues(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Issues     []TagIssueItem `json:"issues"`
		TotalCount int            `json:"total_count"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	if resp.TotalCount == 0 || len(resp.Issues) == 0 {
		t.Fatalf("expected tag issues to be found, got %d", resp.TotalCount)
	}

	issue := resp.Issues[0]
	if issue.FilePath != relPath {
		t.Errorf("expected file path %s, got %s", relPath, issue.FilePath)
	}

	// 2. POST /files/audit/tag-issues/fix
	fixReqBody := FixTagIssueRequest{
		Path:   relPath,
		Title:  issue.Proposed.Title,
		Artist: issue.Proposed.Artist,
		Album:  "Test Album",
		Year:   2022,
	}
	bodyBytes, _ := json.Marshal(fixReqBody)
	fixReq := httptest.NewRequest(http.MethodPost, "/files/audit/tag-issues/fix", bytes.NewReader(bodyBytes))
	fixW := httptest.NewRecorder()
	h.FixTagIssue(fixW, fixReq)

	if fixW.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", fixW.Code, fixW.Body.String())
	}

	// Verify tags on disk
	updatedTags, err := id3.ReadTags(mp3Path, relPath)
	if err != nil {
		t.Fatal(err)
	}
	if updatedTags.Title != "Dirty Track" {
		t.Errorf("expected clean title 'Dirty Track', got %q", updatedTags.Title)
	}
	if updatedTags.Year != 2022 {
		t.Errorf("expected year 2022, got %d", updatedTags.Year)
	}
}

func TestFileHandler_FixAllTagIssues(t *testing.T) {
	h, _, _, database, musicDir, _ := setupTestFileHandler(t)
	defer database.Close()

	albumDir := filepath.Join(musicDir, "FixAll Album")
	_ = os.MkdirAll(albumDir, 0755)

	t1Path := filepath.Join(albumDir, "01. Clip Track.mp3")
	_ = os.WriteFile(t1Path, []byte("audio payload data"), 0644)
	_ = id3.WriteTags(t1Path, id3.TrackTags{
		Title:       "Clip Track [Official Music Video]",
		Artist:      "Artist One",
		Album:       "FixAll Album",
		TrackNumber: 1,
		Year:        2020,
	}, nil)
	_ = h.scanner.IndexFile(t1Path, "FixAll Album/01. Clip Track.mp3")

	t2Path := filepath.Join(albumDir, "02. Feat Track.mp3")
	_ = os.WriteFile(t2Path, []byte("audio payload data"), 0644)
	_ = id3.WriteTags(t2Path, id3.TrackTags{
		Title:       "Feat Track (feat. Special Guest)",
		Artist:      "Artist One",
		Album:       "FixAll Album",
		TrackNumber: 2,
		Year:        2020,
	}, nil)
	_ = h.scanner.IndexFile(t2Path, "FixAll Album/02. Feat Track.mp3")

	// Call FixAllTagIssues
	req := httptest.NewRequest(http.MethodPost, "/files/audit/tag-issues/fix-all", nil)
	w := httptest.NewRecorder()
	h.FixAllTagIssues(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Success    bool `json:"success"`
		FixedCount int  `json:"fixed_count"`
		TotalFound int  `json:"total_found"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	if resp.FixedCount != 2 {
		t.Errorf("expected 2 fixed tracks, got %d (found %d)", resp.FixedCount, resp.TotalFound)
	}

	// Verify t1 title cleaned
	tags1, _ := id3.ReadTags(t1Path, "FixAll Album/01. Clip Track.mp3")
	if tags1.Title != "Clip Track" {
		t.Errorf("expected 'Clip Track', got %q", tags1.Title)
	}

	// Verify t2 title and artist cleaned
	tags2, _ := id3.ReadTags(t2Path, "FixAll Album/02. Feat Track.mp3")
	if tags2.Title != "Feat Track" {
		t.Errorf("expected 'Feat Track', got %q", tags2.Title)
	}
	if tags2.Artist != "Artist One\\\\Special Guest" {
		t.Errorf("expected multi-artist 'Artist One\\\\Special Guest', got %q", tags2.Artist)
	}
}

func TestFileHandler_ApplyCatalogTags(t *testing.T) {
	h, _, _, database, musicDir, _ := setupTestFileHandler(t)
	defer database.Close()

	albumDir := filepath.Join(musicDir, "Catalog Album")
	_ = os.MkdirAll(albumDir, 0755)
	mp3Path := filepath.Join(albumDir, "01. Old Track.mp3")
	_ = os.WriteFile(mp3Path, []byte("audio payload data"), 0644)

	_ = id3.WriteTags(mp3Path, id3.TrackTags{
		Title:       "Old Track",
		Artist:      "Old Artist",
		Album:       "Old Album",
		TrackNumber: 1,
		Year:        2010,
	}, nil)

	relPath := "Catalog Album/01. Old Track.mp3"
	_ = h.scanner.IndexFile(mp3Path, relPath)

	reqBody := ApplyCatalogTagsRequest{
		Path:        relPath,
		Title:       "New Mastered Track",
		Artist:      "Artist A\\\\Artist B",
		Album:       "Remastered 2024",
		Year:        2024,
		TrackNumber: 3,
	}
	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/files/audit/tag-issues/apply-catalog", bytes.NewReader(bodyBytes))
	w := httptest.NewRecorder()
	h.ApplyCatalogTags(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	tags, err := id3.ReadTags(mp3Path, relPath)
	if err != nil {
		t.Fatal(err)
	}

	if tags.Title != "New Mastered Track" {
		t.Errorf("expected title 'New Mastered Track', got %q", tags.Title)
	}
	if tags.Artist != "Artist A\\\\Artist B" {
		t.Errorf("expected artist 'Artist A\\\\Artist B', got %q", tags.Artist)
	}
	if tags.Album != "Remastered 2024" {
		t.Errorf("expected album 'Remastered 2024', got %q", tags.Album)
	}
	if tags.Year != 2024 {
		t.Errorf("expected year 2024, got %d", tags.Year)
	}
	if tags.TrackNumber != 3 {
		t.Errorf("expected track number 3, got %d", tags.TrackNumber)
	}
}

func TestFileHandler_FixSafeTagIssues(t *testing.T) {
	h, _, _, database, musicDir, _ := setupTestFileHandler(t)
	defer database.Close()

	albumDir := filepath.Join(musicDir, "SafeFix Album")
	_ = os.MkdirAll(albumDir, 0755)

	// Track 1: safe issue (dirty title with Official Video)
	t1Path := filepath.Join(albumDir, "01. Clean Me.mp3")
	_ = os.WriteFile(t1Path, []byte("audio payload data 1"), 0644)
	_ = id3.WriteTags(t1Path, id3.TrackTags{
		Title:       "Clean Me (Official Music Video)",
		Artist:      "Good Artist",
		Album:       "SafeFix Album",
		TrackNumber: 1,
		Year:        2021,
	}, nil)
	_ = h.scanner.IndexFile(t1Path, "SafeFix Album/01. Clean Me.mp3")

	// Track 2: review needed issue (unparsed feat) - should NOT be auto-fixed by fix-safe
	t2Path := filepath.Join(albumDir, "02. Review Me.mp3")
	_ = os.WriteFile(t2Path, []byte("audio payload data 2"), 0644)
	_ = id3.WriteTags(t2Path, id3.TrackTags{
		Title:       "Review Me (feat. Unknown Guest)",
		Artist:      "Good Artist",
		Album:       "SafeFix Album",
		TrackNumber: 2,
		Year:        2021,
	}, nil)
	_ = h.scanner.IndexFile(t2Path, "SafeFix Album/02. Review Me.mp3")

	req := httptest.NewRequest(http.MethodPost, "/files/audit/tag-issues/fix-safe", nil)
	w := httptest.NewRecorder()
	h.FixSafeTagIssues(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Success    bool `json:"success"`
		FixedCount int  `json:"fixed_count"`
		TotalFound int  `json:"total_found"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	if resp.FixedCount != 1 {
		t.Errorf("expected 1 fixed safe track, got %d (found %d)", resp.FixedCount, resp.TotalFound)
	}

	// Verify t1 was cleaned
	tags1, _ := id3.ReadTags(t1Path, "SafeFix Album/01. Clean Me.mp3")
	if tags1.Title != "Clean Me" {
		t.Errorf("expected 'Clean Me', got %q", tags1.Title)
	}

	// Verify t2 was NOT touched by fix-safe
	tags2, _ := id3.ReadTags(t2Path, "SafeFix Album/02. Review Me.mp3")
	if tags2.Title != "Review Me (feat. Unknown Guest)" {
		t.Errorf("expected untouched title for t2, got %q", tags2.Title)
	}
}

func TestFileHandler_FixSelectedTagIssues(t *testing.T) {
	h, _, _, database, musicDir, _ := setupTestFileHandler(t)
	defer database.Close()

	albumDir := filepath.Join(musicDir, "SelectedFix Album")
	_ = os.MkdirAll(albumDir, 0755)

	tPath := filepath.Join(albumDir, "01. Selected Track.mp3")
	_ = os.WriteFile(tPath, []byte("audio payload data"), 0644)
	_ = id3.WriteTags(tPath, id3.TrackTags{
		Title:       "Raw Track Name",
		Artist:      "Artist A",
		Album:       "Old Album",
		TrackNumber: 1,
		Year:        2015,
	}, nil)
	relPath := "SelectedFix Album/01. Selected Track.mp3"
	_ = h.scanner.IndexFile(tPath, relPath)

	reqBody := FixSelectedTagsRequest{
		Items: []FixTagIssueRequest{
			{
				Path:   relPath,
				Title:  "Polished Track Name",
				Artist: "Artist A\\\\Artist B",
				Album:  "New Album",
				Year:   2024,
			},
		},
	}
	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/files/audit/tag-issues/fix-selected", bytes.NewReader(bodyBytes))
	w := httptest.NewRecorder()
	h.FixSelectedTagIssues(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Success    bool `json:"success"`
		FixedCount int  `json:"fixed_count"`
		TotalFound int  `json:"total_found"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	if resp.FixedCount != 1 {
		t.Errorf("expected 1 fixed track, got %d", resp.FixedCount)
	}

	tags, _ := id3.ReadTags(tPath, relPath)
	if tags.Title != "Polished Track Name" {
		t.Errorf("expected 'Polished Track Name', got %q", tags.Title)
	}
	if tags.Artist != "Artist A\\\\Artist B" {
		t.Errorf("expected 'Artist A\\\\Artist B', got %q", tags.Artist)
	}
	if tags.Album != "New Album" {
		t.Errorf("expected 'New Album', got %q", tags.Album)
	}
	if tags.Year != 2024 {
		t.Errorf("expected 2024, got %d", tags.Year)
	}
}

func TestFileHandler_CatalogMatchTrack(t *testing.T) {
	h, _, _, database, _, _ := setupTestFileHandler(t)
	defer database.Close()

	// Mock iTunes
	mockItunes := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"resultCount": 1,
			"results": [
				{
					"trackName": "Mock Track",
					"artistName": "Mock Artist",
					"collectionName": "Mock Album",
					"releaseDate": "2023-01-01T00:00:00Z",
					"trackNumber": 1,
					"trackTimeMillis": 180000,
					"artworkUrl100": "http://example.com/cover100.jpg"
				}
			]
		}`))
	}))
	defer mockItunes.Close()

	itunesClient := resolver.NewITunesClient(mockItunes.Client())
	itunesClient.SetBaseURL(mockItunes.URL)
	h.itunes = itunesClient

	req := httptest.NewRequest(http.MethodGet, "/files/audit/catalog-match?artist=Mock+Artist&title=Mock+Track", nil)
	w := httptest.NewRecorder()
	h.CatalogMatchTrack(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Current map[string]any      `json:"current"`
		Matches []CatalogTrackMatch `json:"matches"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	if len(resp.Matches) == 0 {
		t.Fatalf("expected at least 1 match from mocked iTunes, got 0")
	}

	topMatch := resp.Matches[0]
	if topMatch.Source != "itunes" {
		t.Errorf("expected source 'itunes', got %q", topMatch.Source)
	}
	if topMatch.Title != "Mock Track" {
		t.Errorf("expected title 'Mock Track', got %q", topMatch.Title)
	}
	if topMatch.MatchScore < 80 {
		t.Errorf("expected high match score >= 80, got %d", topMatch.MatchScore)
	}
}

