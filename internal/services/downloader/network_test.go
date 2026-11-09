package downloader

import (
	"net/http"
	"os"
	"testing"

	"music-manager/internal/testkit"
)

// No provider requests may leave an ordinary test process. Loopback requests
// still reach httptest; provider-specific tests register their own fixtures.
func TestMain(m *testing.M) {
	http.DefaultTransport = testkit.NewMockTransport()
	os.Exit(m.Run())
}
