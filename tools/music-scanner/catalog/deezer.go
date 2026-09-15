package catalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

var httpClient = &http.Client{
	Timeout: 10 * time.Second,
}

type deezerSearchResponse struct {
	Data []struct {
		ID       int64  `json:"id"`
		Title    string `json:"title"`
		NbTracks int    `json:"nb_tracks"`
		CoverXL  string `json:"cover_xl"`
		Artist   struct {
			Name string `json:"name"`
		} `json:"artist"`
	} `json:"data"`
}

type deezerTracksResponse struct {
	Data []struct {
		ID            int64  `json:"id"`
		Title         string `json:"title"`
		TrackPosition int    `json:"track_position"`
		DiskNumber    int    `json:"disk_number"`
		Duration      int    `json:"duration"`
		ISRC          string `json:"isrc"`
		Link          string `json:"link"`
		Artist        struct {
			Name string `json:"name"`
		} `json:"artist"`
	} `json:"data"`
}

// FetchDeezerAlbumTracks ищет релиз в Deezer и возвращает список всех треков.
func FetchDeezerAlbumTracks(artist string, album string) (*RemoteAlbum, error) {
	query := fmt.Sprintf(`artist:"%s" album:"%s"`, artist, album)
	searchURL := "https://api.deezer.com/search/album?q=" + url.QueryEscape(query)

	req, err := http.NewRequest("GET", searchURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "MusicManager/2.0")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("deezer search request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("deezer search returned status %d", resp.StatusCode)
	}

	var searchRes deezerSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&searchRes); err != nil {
		return nil, err
	}

	if len(searchRes.Data) == 0 {
		return nil, fmt.Errorf("album not found in deezer")
	}

	bestMatch := searchRes.Data[0]
	albumIDStr := strconv.FormatInt(bestMatch.ID, 10)

	// Получаем треки альбома
	tracksURL := fmt.Sprintf("https://api.deezer.com/album/%d/tracks?limit=100", bestMatch.ID)
	reqTracks, err := http.NewRequest("GET", tracksURL, nil)
	if err != nil {
		return nil, err
	}
	reqTracks.Header.Set("User-Agent", "MusicManager/2.0")

	respTracks, err := httpClient.Do(reqTracks)
	if err != nil {
		return nil, fmt.Errorf("deezer tracks request failed: %w", err)
	}
	defer respTracks.Body.Close()

	if respTracks.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("deezer tracks returned status %d", respTracks.StatusCode)
	}

	var tracksRes deezerTracksResponse
	if err := json.NewDecoder(respTracks.Body).Decode(&tracksRes); err != nil {
		return nil, err
	}

	result := &RemoteAlbum{
		ID:       albumIDStr,
		Title:    bestMatch.Title,
		Artist:   bestMatch.Artist.Name,
		CoverURL: bestMatch.CoverXL,
		NbTracks: bestMatch.NbTracks,
	}

	for _, t := range tracksRes.Data {
		trackNumber := t.TrackPosition
		if trackNumber <= 0 {
			trackNumber = 1
		}
		discNumber := t.DiskNumber
		if discNumber <= 0 {
			discNumber = 1
		}
		artistName := t.Artist.Name
		if artistName == "" {
			artistName = bestMatch.Artist.Name
		}

		result.Tracks = append(result.Tracks, RemoteTrack{
			Title:       t.Title,
			Artist:      artistName,
			TrackNumber: trackNumber,
			DiscNumber:  discNumber,
			Duration:    float64(t.Duration),
			DeezerID:    strconv.FormatInt(t.ID, 10),
			DeezerURL:   t.Link,
			ISRC:        t.ISRC,
		})
	}

	return result, nil
}
