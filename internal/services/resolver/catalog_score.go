package resolver

import "math"

// CatalogCandidate represents metadata of a track candidate retrieved from an online catalog.
type CatalogCandidate struct {
	Title    string
	Artist   string
	Duration int
}

// ComputeCatalogMatchScore returns an uncalibrated metadata correspondence
// rating (0..100), shared with source and release matching. Missing evidence
// limits the rating; it is not a probability and never authorizes selection.
func ComputeCatalogMatchScore(targetTitle, targetArtist string, targetDuration float64, candidate CatalogCandidate) int {
	return EvaluateTrack(targetTitle, targetArtist, int(math.Round(targetDuration)), candidate.Title, candidate.Artist, candidate.Duration).Score
}
