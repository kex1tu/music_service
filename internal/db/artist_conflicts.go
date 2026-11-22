package db

type ArtistChoice struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	DeezerID string `json:"deezer_id,omitempty"`
}

type ArtistConflict struct {
	Path        string         `json:"path"`
	Name        string         `json:"name"`
	AlbumArtist bool           `json:"album_artist"`
	Choices     []ArtistChoice `json:"choices"`
}
