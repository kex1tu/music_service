package resolver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// searchAlbumsItunes выполняет резервный поиск альбомов через iTunes Search API.
func (c *DeezerClient) searchAlbumsItunes(ctx context.Context, query string) ([]DeezerAlbum, error) {
	itunesURL := fmt.Sprintf("https://itunes.apple.com/search?term=%s&entity=album&limit=25", url.QueryEscape(query))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, itunesURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("itunes fallback status %d", resp.StatusCode)
	}

	var res struct {
		Results []struct {
			CollectionID   int64  `json:"collectionId"`
			CollectionName string `json:"collectionName"`
			ArtistID       int64  `json:"artistId"`
			ArtistName     string `json:"artistName"`
			ArtworkURL100  string `json:"artworkUrl100"`
			ReleaseDate    string `json:"releaseDate"`
			TrackCount     int    `json:"trackCount"`
			PrimaryGenre   string `json:"primaryGenreName"`
			CollectionType string `json:"collectionType"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}

	var albums []DeezerAlbum
	for _, r := range res.Results {
		if r.CollectionID == 0 || r.CollectionName == "" {
			continue
		}
		coverBig := strings.Replace(r.ArtworkURL100, "100x100bb", "600x600bb", 1)
		coverXL := strings.Replace(r.ArtworkURL100, "100x100bb", "1400x1400bb", 1)
		relType := strings.ToLower(r.CollectionType)
		if relType == "" {
			relType = "album"
		}
		alb := DeezerAlbum{
			ID:          r.CollectionID,
			Title:       r.CollectionName,
			CoverMedium: r.ArtworkURL100,
			CoverBig:    coverBig,
			CoverXL:     coverXL,
			ReleaseDate: r.ReleaseDate,
			RecordType:  relType,
			NbTracks:    r.TrackCount,
			Artist: DeezerArtist{
				ID:   r.ArtistID,
				Name: r.ArtistName,
			},
		}
		if r.PrimaryGenre != "" {
			alb.Genres = &DeezerGenres{
				Data: []struct {
					ID   int64  `json:"id"`
					Name string `json:"name"`
				}{
					{ID: 1, Name: r.PrimaryGenre},
				},
			}
		}
		albums = append(albums, alb)
	}
	return albums, nil
}

// getAlbumItunes получает информацию об альбоме и список треков через iTunes Lookup API при сбое Deezer.
func (c *DeezerClient) getAlbumItunes(ctx context.Context, albumID string) (*DeezerAlbum, error) {
	lookupURL := fmt.Sprintf("https://itunes.apple.com/lookup?id=%s&entity=song", albumID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, lookupURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("itunes fallback status %d", resp.StatusCode)
	}

	var res struct {
		Results []struct {
			WrapperType     string `json:"wrapperType"`
			CollectionID    int64  `json:"collectionId"`
			CollectionName  string `json:"collectionName"`
			ArtistID        int64  `json:"artistId"`
			ArtistName      string `json:"artistName"`
			ArtworkURL100   string `json:"artworkUrl100"`
			ReleaseDate     string `json:"releaseDate"`
			TrackCount      int    `json:"trackCount"`
			PrimaryGenre    string `json:"primaryGenreName"`
			CollectionType  string `json:"collectionType"`
			TrackID         int64  `json:"trackId"`
			TrackName       string `json:"trackName"`
			TrackNumber     int    `json:"trackNumber"`
			DiscNumber      int    `json:"discNumber"`
			TrackTimeMillis int    `json:"trackTimeMillis"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}

	if len(res.Results) == 0 {
		return nil, fmt.Errorf("album not found in itunes fallback")
	}

	var album *DeezerAlbum
	var tracks []DeezerTrack

	for _, item := range res.Results {
		if item.WrapperType == "collection" && album == nil {
			coverBig := strings.Replace(item.ArtworkURL100, "100x100bb", "600x600bb", 1)
			coverXL := strings.Replace(item.ArtworkURL100, "100x100bb", "1400x1400bb", 1)
			album = &DeezerAlbum{
				ID:          item.CollectionID,
				Title:       item.CollectionName,
				CoverMedium: item.ArtworkURL100,
				CoverBig:    coverBig,
				CoverXL:     coverXL,
				ReleaseDate: item.ReleaseDate,
				RecordType:  strings.ToLower(item.CollectionType),
				NbTracks:    item.TrackCount,
				Artist: DeezerArtist{
					ID:   item.ArtistID,
					Name: item.ArtistName,
				},
			}
			if item.PrimaryGenre != "" {
				album.Genres = &DeezerGenres{
					Data: []struct {
						ID   int64  `json:"id"`
						Name string `json:"name"`
					}{
						{ID: 1, Name: item.PrimaryGenre},
					},
				}
			}
		} else if item.WrapperType == "track" {
			tArtist := item.ArtistName
			if album != nil && tArtist == "" {
				tArtist = album.Artist.Name
			}
			pos := item.TrackNumber
			if pos <= 0 {
				pos = len(tracks) + 1
			}
			disc := item.DiscNumber
			if disc <= 0 {
				disc = 1
			}
			dur := item.TrackTimeMillis / 1000
			tracks = append(tracks, DeezerTrack{
				ID:            item.TrackID,
				Title:         item.TrackName,
				TrackPosition: pos,
				DiskNumber:    disc,
				Duration:      dur,
				Artist: DeezerArtist{
					ID:   item.ArtistID,
					Name: tArtist,
				},
			})
		}
	}

	if album == nil {
		return nil, fmt.Errorf("album metadata not found in itunes fallback")
	}
	album.Tracks = &DeezerTrackList{
		Data: tracks,
	}
	return album, nil
}
