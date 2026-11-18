package downloader

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"music-manager/internal/testkit"
)

func TestStagingCleanerPreservesPreparedAndRunningJobs(t *testing.T) {
	env := testkit.NewEnv(t)
	repo := NewRepository(env.DB.Conn())
	service := NewService(ServiceConfig{Repo: repo, TmpSandbox: env.TmpSandbox})
	fixtures := []struct {
		id     string
		status JobStatus
		file   string
		keep   bool
	}{
		{"ready", StatusReady, "converted/cover.jpg", true},
		{"partial", StatusPartialReady, "converted/second.mp3", true},
		{"active", StatusConverting, "raw/source.webm", true},
		{"cancelled", StatusCancelled, "raw/source.webm", false},
		{"orphan-prepared", "", "converted/remaining.mp3", true},
		{"orphan-debris", "", "raw/source.webm", false},
	}

	for _, item := range fixtures {
		env.CreateDummyFile(env.TmpSandbox, filepath.Join(item.id, item.file), []byte(item.id))
		dir, err := env.TmpSandbox.Resolve(item.id, false)
		if err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-2 * service.cleanerMaxAge)
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
		switch item.status {
		case "":
		case StatusReady:
			// The legacy downloader can own the ready job in the same database.
			if err := repo.SaveJob(context.Background(), &Job{ID: item.id, Status: item.status}); err != nil {
				t.Fatal(err)
			}
		default:
			service.AddJob(&Job{ID: item.id, Status: item.status})
		}
	}

	service.cleanOldTempDirs()

	for _, item := range fixtures {
		path, err := env.TmpSandbox.Resolve(filepath.Join(item.id, item.file), false)
		if err != nil {
			t.Fatal(err)
		}
		_, err = os.Stat(path)
		if (item.keep && err != nil) || (!item.keep && !os.IsNotExist(err)) {
			t.Errorf("%s: expected keep=%v, stat: %v", item.id, item.keep, err)
		}
	}
}
