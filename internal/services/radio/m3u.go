package radio

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"music-manager/internal/core/sandbox"
)

// M3UGenerator отвечает за формирование и атомарное сохранение смарт-плейлистов M3U в медиатеке.
type M3UGenerator struct {
	selector *WaveSelector
	sandbox  *sandbox.PathSandbox
	size     int
}

// NewM3UGenerator создает новый экземпляр M3UGenerator.
func NewM3UGenerator(selector *WaveSelector, sb *sandbox.PathSandbox, size int) *M3UGenerator {
	if size <= 0 {
		size = 60
	}
	return &M3UGenerator{
		selector: selector,
		sandbox:  sb,
		size:     size,
	}
}

// GenerateWaveM3U формирует и сохраняет плейлист «Моя Волна.m3u» в каталоге Playlists/ медиатеки.
func (g *M3UGenerator) GenerateWaveM3U(ctx context.Context) (string, error) {
	tracks, err := g.selector.GeneratePlaylist(ctx, g.size)
	if err != nil {
		return "", fmt.Errorf("failed to generate playlist tracks: %w", err)
	}
	if len(tracks) == 0 {
		return "", fmt.Errorf("no tracks selected for playlist")
	}

	// 1. Построение контента #EXTM3U
	var sb strings.Builder
	sb.WriteString("#EXTM3U\n")

	for _, tr := range tracks {
		durationSec := int(tr.Duration)
		if durationSec <= 0 {
			durationSec = -1
		}
		title := tr.Title
		if tr.Artist != "" {
			title = fmt.Sprintf("%s - %s", tr.Artist, tr.Title)
		}

		sb.WriteString(fmt.Sprintf("#EXTINF:%d,%s\n", durationSec, title))

		// Относительный путь от каталога Playlists/ к файлу трека в MUSIC_DIR
		cleanRel := filepath.ToSlash(filepath.Clean(tr.FilePath))
		sb.WriteString(fmt.Sprintf("../%s\n", cleanRel))
	}

	// 2. Безопасное сохранение через PathSandbox
	relTarget := filepath.Join("Playlists", "Моя Волна.m3u")
	absTarget, err := g.sandbox.Resolve(relTarget, false)
	if err != nil {
		return "", fmt.Errorf("sandbox path resolution failed for %q: %w", relTarget, err)
	}

	// Создаем каталог Playlists, если он не существует
	if err := os.MkdirAll(filepath.Dir(absTarget), 0755); err != nil {
		return "", fmt.Errorf("failed to create Playlists directory: %w", err)
	}

	// Атомарная запись через временный файл
	tmpTarget := fmt.Sprintf("%s.tmp.%d", absTarget, os.Getpid())
	if err := os.WriteFile(tmpTarget, []byte(sb.String()), 0644); err != nil {
		return "", fmt.Errorf("failed to write temporary m3u file: %w", err)
	}
	_ = os.Chmod(tmpTarget, 0644)

	if err := os.Rename(tmpTarget, absTarget); err != nil {
		_ = os.Remove(tmpTarget)
		return "", fmt.Errorf("failed to commit m3u file: %w", err)
	}

	return absTarget, nil
}
