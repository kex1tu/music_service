package tageditor_test

import (
	"net/http"
	"testing"

	"music-manager/internal/features/tageditor"
)

func TestTagEditor_MultiSourceCoverSearch(t *testing.T) {
	env, _ := setupTagEditorTest(t)

	// Регистрируем мок-ответы для всех 4 источников в MockHTTP
	env.MockHTTP.RegisterJSON("itunes.apple.com/search", http.StatusOK, map[string]any{
		"resultCount": 1,
		"results": []map[string]any{
			{
				"collectionName": "Discovery",
				"artistName":     "Daft Punk",
				"artworkUrl100":  "https://is1-ssl.mzstatic.com/image/thumb/Music/100x100bb.jpg",
				"releaseDate":    "2001-03-13T08:00:00Z",
			},
		},
	})

	env.MockHTTP.RegisterJSON("api.deezer.com/search/album", http.StatusOK, map[string]any{
		"data": []map[string]any{
			{
				"id":    302127,
				"title": "Discovery",
				"artist": map[string]any{
					"name": "Daft Punk",
				},
				"cover_xl": "https://e-cdns-images.dzcdn.net/images/cover/1000x1000-000000-80-0-0.jpg",
			},
		},
	})

	env.MockHTTP.RegisterJSON("api.music.yandex.net/search", http.StatusOK, map[string]any{
		"result": map[string]any{
			"albums": map[string]any{
				"results": []map[string]any{
					{
						"title":    "Discovery",
						"coverUri": "avatars.yandex.net/get-music-content/12345/%%",
						"year":     2001,
						"artists": []map[string]any{
							{"name": "Daft Punk"},
						},
					},
				},
			},
		},
	})

	env.MockHTTP.RegisterJSON("musicbrainz.org/ws/2/release", http.StatusOK, map[string]any{
		"releases": []map[string]any{
			{
				"id":    "5ad66522-edce-3a77-b5fa-7956ee879239",
				"title": "Discovery",
				"date":  "2001-03-13",
				"artist-credit": []map[string]any{
					{"name": "Daft Punk", "joinphrase": ""},
				},
			},
		},
	})

	// 1. Поиск с валидным запросом
	resp := env.Get("/tags/covers/search?query=Daft+Punk+Discovery")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected HTTP 200 from covers search, got %d", resp.StatusCode)
	}

	var items []tageditor.CoverSearchResultItem
	env.DecodeJSON(resp, &items)

	if len(items) == 0 {
		t.Fatal("expected at least one cover result from multi-source search")
	}

	sourcesFound := make(map[string]bool)
	for _, it := range items {
		sourcesFound[it.Source] = true
	}

	for _, expectedSource := range []string{"apple", "deezer", "yandex", "musicbrainz"} {
		if !sourcesFound[expectedSource] {
			t.Errorf("expected source %q to be present in multi-source results, found: %+v", expectedSource, sourcesFound)
		}
	}

	// 2. Пустой запрос должен возвращать пустой массив
	respEmpty := env.Get("/tags/covers/search?query=")
	if respEmpty.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for empty query, got %d", respEmpty.StatusCode)
	}
	var emptyItems []tageditor.CoverSearchResultItem
	env.DecodeJSON(respEmpty, &emptyItems)
	if len(emptyItems) != 0 {
		t.Errorf("expected 0 items for empty query, got %d", len(emptyItems))
	}
}
