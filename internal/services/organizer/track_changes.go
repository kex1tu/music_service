package organizer

import "context"

// TrackChanger keeps the index synchronized with a filesystem mutation.
type TrackChanger interface {
	ChangeTrack(ctx context.Context, source, target string, merged bool, change func() error) error
}

// ApplyTrackChange also supports callers that operate without a library index.
func ApplyTrackChange(ctx context.Context, index TrackChanger, source, target string, merged bool, change func() error) error {
	if index == nil {
		return change()
	}
	return index.ChangeTrack(ctx, source, target, merged, change)
}

// MoveIndexedTrack passes the final collision-resolved path to the index before
// touching either audio file. afterMove can write tags as part of the same intent.
func MoveIndexedTrack(ctx context.Context, index TrackChanger, source, target string, overwrite bool, afterMove func(string) error) (string, error) {
	return moveTrackWithCompanions(source, target, overwrite, func(actual string, merged bool, move func() error) (string, error) {
		var moved string
		err := ApplyTrackChange(ctx, index, source, actual, merged, func() error {
			if err := move(); err != nil {
				return err
			}
			moved = actual
			if afterMove != nil {
				return afterMove(actual)
			}
			return nil
		})
		return moved, err
	})
}
