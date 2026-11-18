package audit

import (
	"context"
	"music-manager/internal/db"

	"music-manager/internal/services/lyrics"
	"music-manager/internal/services/organizer"
)

// JobStarter defines the consumer interface for starting standard download jobs.
type JobStarter interface {
	CreateJob(ctx context.Context, rawURL, source string) (jobID string, err error)
}

// BackfillStarter defines the consumer interface for creating backfill download jobs.
type BackfillStarter interface {
	CreateBackfillJob(
		ctx context.Context,
		targetAlbumPath, albumTitle, artistName, deezerAlbumID string,
		trackURLs []string,
		trackNumbers []int,
		positions ...db.BackfillSelection,
	) (jobID string, err error)
}

// ArtistEnricher defines the consumer interface for augmenting artist names with feat collaborators.
type ArtistEnricher interface {
	EnrichTrackArtists(ctx context.Context, title string, currentArtists []string) []string
}

// PathOrganizer defines the consumer interface for file organization and path calculations.
type PathOrganizer interface {
	ComputeTargetPath(artist, album string, trackNumber int, title, originalFilename string) (dirPath, relPath string)
	NormalizeNavidromeLibrary(ctx context.Context, onTrackNormalized func(relPath string)) (*organizer.NavidromeFixReport, error)
}

// LibraryScanner defines the consumer interface for scanning and indexing files.
type LibraryScanner interface {
	ScanAll(ctx context.Context) (int, error)
	IndexFile(fullPath, relPath string) error
}

// LyricsService defines the consumer interface for searching and saving lyrics.
type LyricsService interface {
	SearchLyrics(ctx context.Context, artist, title, album string, duration int) (*lyrics.LyricsSearchResult, error)
	SaveLyrics(relPath string, content string, kind string) error
}

// LibraryDeletionIndex coordinates a filesystem deletion and SQLite cleanup.
type LibraryDeletionIndex interface {
	DeletePath(context.Context, string, bool, func() error) (int64, error)
}
