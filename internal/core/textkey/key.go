package textkey

import (
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

var fold = cases.Fold()

// Search returns a case-folded, canonically normalized key for user-facing text.
func Search(s string) string {
	return norm.NFC.String(fold.String(norm.NFC.String(strings.TrimSpace(s))))
}
