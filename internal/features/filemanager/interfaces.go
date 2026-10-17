package filemanager

import (
	"context"
)

// Organizer представляет контракт сервиса организации и переименования медиатеки.
type Organizer interface {
	RenameFile(oldRelPath, newRelPath string) ([]string, error)
	SyncCompanions(oldBaseRel, newBaseRel string) error
}

// Scanner представляет контракт фонового индексатора библиотеки.
type Scanner interface {
	IndexFile(ctx context.Context, relPath string) error
	RemoveFile(ctx context.Context, relPath string) error
}

// JobCreator инкапсулирует постановку задач в очередь загрузчика.
type JobCreator interface {
	CreateJob(ctx context.Context, rawURL, source string) (string, error)
}

// SourceFinder выполняет поиск ссылок на аудиоисточники для треков и альбомов.
type SourceFinder interface {
	FindTrackURL(ctx context.Context, artist, title, album string, duration int) (string, error)
	FindAlbumURL(ctx context.Context, artist, album string) (string, error)
}
