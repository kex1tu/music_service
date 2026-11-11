package lyrics

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"music-manager/internal/core/sandbox"
)

type mockYandexLyricsClient struct {
	lyrics string
	synced bool
	err    error
	calls  int32
	lastID int64
}

func (m *mockYandexLyricsClient) GetTrackLyrics(ctx context.Context, trackID int64) (string, bool, error) {
	atomic.AddInt32(&m.calls, 1)
	m.lastID = trackID
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	return m.lyrics, m.synced, m.err
}

func TestLyricsService_SearchLyricsWithOptions_YandexPriorityAndSuccessLRC(t *testing.T) {
	tempDir := t.TempDir()
	sb, _ := sandbox.NewPathSandbox(tempDir)

	mockLRCLIB := &mockLyricsProvider{
		result: &LyricsSearchResult{
			Lyrics: "[00:10.00] LRCLIB lyrics",
			Synced: true,
			Source: "lrclib",
		},
	}
	mockYandex := &mockYandexLyricsClient{
		lyrics: "[00:01.00] Yandex official synced lyrics",
		synced: true,
	}

	svc := NewService(sb, mockLRCLIB)
	svc.SetYandexClient(mockYandex)

	res, err := svc.SearchLyricsWithOptions(
		context.Background(),
		"Artist",
		"Title",
		"Album",
		180,
		WithYandexTrackID(12345),
	)
	if err != nil {
		t.Fatalf("SearchLyricsWithOptions failed: %v", err)
	}
	if res == nil {
		t.Fatalf("expected non-nil result")
	}
	if res.Source != "yandex" {
		t.Errorf("expected Source='yandex', got %q", res.Source)
	}
	if !res.Synced {
		t.Errorf("expected Synced=true, got %v", res.Synced)
	}
	if res.Lyrics != "[00:01.00] Yandex official synced lyrics" {
		t.Errorf("lyrics mismatch: %q", res.Lyrics)
	}

	// LRCLIB must NOT be called if Yandex succeeded
	// (we verify Yandex was called with the exact track ID)
	if mockYandex.calls != 1 || mockYandex.lastID != 12345 {
		t.Errorf("mockYandex calls=%d, lastID=%d; want 1 call with 12345", mockYandex.calls, mockYandex.lastID)
	}
}

func TestLyricsService_SearchLyricsWithOptions_YandexSuccessPlainText(t *testing.T) {
	tempDir := t.TempDir()
	sb, _ := sandbox.NewPathSandbox(tempDir)

	mockLRCLIB := &mockLyricsProvider{
		result: &LyricsSearchResult{
			Lyrics: "LRCLIB plain",
			Source: "lrclib",
		},
	}
	mockYandex := &mockYandexLyricsClient{
		lyrics: "Yandex official plain text lyrics without timestamps",
		synced: false,
	}

	svc := NewService(sb, mockLRCLIB)
	svc.SetYandexClient(mockYandex)

	res, err := svc.SearchLyricsWithOptions(
		context.Background(),
		"Artist",
		"Title",
		"Album",
		180,
		WithYandexTrackID(67890),
	)
	if err != nil {
		t.Fatalf("SearchLyricsWithOptions failed: %v", err)
	}
	if res == nil || res.Source != "yandex" || res.Synced || res.Lyrics != "Yandex official plain text lyrics without timestamps" {
		t.Errorf("unexpected result: %+v", res)
	}
}

func TestLyricsService_SearchLyricsWithOptions_YandexNotFoundFallbackToLRCLIB(t *testing.T) {
	tempDir := t.TempDir()
	sb, _ := sandbox.NewPathSandbox(tempDir)

	mockLRCLIB := &mockLyricsProvider{
		result: &LyricsSearchResult{
			Lyrics: "[00:05.00] Fallback lyrics from LRCLIB",
			Synced: true,
			Source: "lrclib",
		},
	}
	mockYandex := &mockYandexLyricsClient{
		err: errors.New("lyrics not found for yandex track"),
	}

	svc := NewService(sb, mockLRCLIB)
	svc.SetYandexClient(mockYandex)

	res, err := svc.SearchLyricsWithOptions(
		context.Background(),
		"Artist",
		"Title",
		"Album",
		180,
		WithYandexTrackID(99999),
	)
	if err != nil {
		t.Fatalf("SearchLyricsWithOptions failed: %v", err)
	}
	if res == nil || res.Source != "lrclib" || res.Lyrics != "[00:05.00] Fallback lyrics from LRCLIB" {
		t.Errorf("expected fallback to lrclib, got %+v", res)
	}
	if mockYandex.calls != 1 {
		t.Errorf("expected Yandex to be tried once, got %d", mockYandex.calls)
	}
}

func TestLyricsService_SearchLyricsWithOptions_YandexAccessErrorFallbackToLRCLIB(t *testing.T) {
	tempDir := t.TempDir()
	sb, _ := sandbox.NewPathSandbox(tempDir)

	mockLRCLIB := &mockLyricsProvider{
		result: &LyricsSearchResult{
			Lyrics: "[00:05.00] Fallback lyrics",
			Synced: true,
			Source: "lrclib",
		},
	}
	mockYandex := &mockYandexLyricsClient{
		err: errors.New("яндекс музыка заблокировала доступ (HTTP 451 Geoblock)"),
	}

	svc := NewService(sb, mockLRCLIB)
	svc.SetYandexClient(mockYandex)

	res, err := svc.SearchLyricsWithOptions(
		context.Background(),
		"Artist",
		"Title",
		"Album",
		180,
		WithYandexTrackID(54321),
	)
	if err != nil {
		t.Fatalf("SearchLyricsWithOptions failed: %v", err)
	}
	if res == nil || res.Source != "lrclib" {
		t.Errorf("expected fallback to lrclib upon Yandex 451, got %+v", res)
	}
}

func TestLyricsService_SearchLyricsWithOptions_ContextCancellationAbortsFallback(t *testing.T) {
	tempDir := t.TempDir()
	sb, _ := sandbox.NewPathSandbox(tempDir)

	lrclibCalls := 0
	mockLRCLIB := &mockLyricsProvider{
		result: &LyricsSearchResult{Lyrics: "should not be called", Source: "lrclib"},
	}
	mockYandex := &mockYandexLyricsClient{
		err: context.Canceled,
	}

	svc := NewService(sb, mockLRCLIB)
	svc.SetYandexClient(mockYandex)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel context

	_, err := svc.SearchLyricsWithOptions(ctx, "Artist", "Title", "", 0, WithYandexTrackID(12345))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if lrclibCalls > 0 {
		t.Errorf("LRCLIB must NOT be called when context is canceled")
	}
}

func TestLyricsService_SearchLyricsWithOptions_NoYandexIDSkipsYandex(t *testing.T) {
	tempDir := t.TempDir()
	sb, _ := sandbox.NewPathSandbox(tempDir)

	mockLRCLIB := &mockLyricsProvider{
		result: &LyricsSearchResult{
			Lyrics: "Direct LRCLIB",
			Source: "lrclib",
		},
	}
	mockYandex := &mockYandexLyricsClient{
		lyrics: "Yandex lyrics",
	}

	svc := NewService(sb, mockLRCLIB)
	svc.SetYandexClient(mockYandex)

	// Call without WithYandexTrackID
	res1, err := svc.SearchLyricsWithOptions(context.Background(), "Artist", "Title", "", 0)
	if err != nil || res1 == nil || res1.Source != "lrclib" {
		t.Errorf("expected direct LRCLIB search, got %+v, err=%v", res1, err)
	}

	// Call with WithYandexTrackID(0)
	res2, err := svc.SearchLyricsWithOptions(context.Background(), "Artist", "Title", "", 0, WithYandexTrackID(0))
	if err != nil || res2 == nil || res2.Source != "lrclib" {
		t.Errorf("expected direct LRCLIB search for ID=0, got %+v, err=%v", res2, err)
	}

	if mockYandex.calls != 0 {
		t.Errorf("Yandex should not have been called, but got %d calls", mockYandex.calls)
	}
}

func TestLyricsService_GetLyricsByYandexID(t *testing.T) {
	tempDir := t.TempDir()
	sb, _ := sandbox.NewPathSandbox(tempDir)

	mockYandex := &mockYandexLyricsClient{
		lyrics: "[00:02.00] Synced line",
		synced: true,
	}

	svc := NewService(sb)
	svc.SetYandexClient(mockYandex)

	// 1. Success
	res, err := svc.GetLyricsByYandexID(context.Background(), 777)
	if err != nil || res == nil || res.Source != "yandex" || !res.Synced || res.Lyrics != "[00:02.00] Synced line" {
		t.Errorf("unexpected GetLyricsByYandexID result: %+v, err=%v", res, err)
	}

	// 2. Invalid ID
	resZero, err := svc.GetLyricsByYandexID(context.Background(), 0)
	if err != nil || resZero != nil {
		t.Errorf("expected nil, nil for ID=0, got %+v, err=%v", resZero, err)
	}

	// 3. Nil client
	svcNoClient := NewService(sb)
	resNoClient, err := svcNoClient.GetLyricsByYandexID(context.Background(), 777)
	if err != nil || resNoClient != nil {
		t.Errorf("expected nil, nil for nil yandex client, got %+v, err=%v", resNoClient, err)
	}
}

func TestLyricsService_YandexTimeoutFallsBackToLRCLIB(t *testing.T) {
	provider := &mockLyricsProvider{result: &LyricsSearchResult{Lyrics: "fallback", Source: "lrclib"}}
	svc := NewService(nil, provider)
	svc.SetYandexClient(&mockYandexLyricsClient{err: context.DeadlineExceeded})

	res, err := svc.SearchLyricsWithOptions(context.Background(), "Artist", "Title", "", 0, WithYandexTrackID(1))
	if err != nil || res == nil || res.Lyrics != "fallback" {
		t.Fatalf("expected LRCLIB fallback after provider timeout, got %#v, %v", res, err)
	}
}
