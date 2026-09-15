package id3

import (
	"fmt"

	id3v2 "github.com/bogem/id3v2/v2"
)

// RewriteGenre обновляет тег жанра (TCON) в указанном MP3 файле.
func RewriteGenre(filePath string, newGenre string) error {
	tag, err := id3v2.Open(filePath, id3v2.Options{Parse: true})
	if err != nil {
		return fmt.Errorf("failed to open mp3 for id3 editing: %w", err)
	}
	defer tag.Close()

	tag.SetGenre(newGenre)
	if err := tag.Save(); err != nil {
		return fmt.Errorf("failed to save updated id3 genre: %w", err)
	}
	return nil
}
