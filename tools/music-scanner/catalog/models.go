package catalog

type RemoteTrack struct {
	Title       string  `json:"title"`
	Artist      string  `json:"artist"`
	TrackNumber int     `json:"track_number"`
	DiscNumber  int     `json:"disc_number"`
	Duration    float64 `json:"duration"`
	DeezerID    string  `json:"deezer_id,omitempty"`
	DeezerURL   string  `json:"deezer_url,omitempty"`
	SpotifyID   string  `json:"spotify_id,omitempty"`
	GeniusURL   string  `json:"genius_url,omitempty"`
	ISRC        string  `json:"isrc,omitempty"`
}

type RemoteAlbum struct {
	ID        string        `json:"id"`
	Title     string        `json:"title"`
	Artist    string        `json:"artist"`
	CoverURL  string        `json:"cover_url"`
	NbTracks  int           `json:"nb_tracks"`
	Tracks    []RemoteTrack `json:"tracks"`
}
