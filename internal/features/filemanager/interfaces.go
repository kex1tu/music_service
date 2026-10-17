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
