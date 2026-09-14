"""Тесты для клиента Spotify (SpotifyClient)."""

from unittest.mock import AsyncMock, MagicMock

import httpx
import pytest

from app.core.exceptions import DownloadError
from app.services.spotify_client import SpotifyClient


@pytest.mark.asyncio
async def test_spotify_parse_url_track():
    client = SpotifyClient()
    res_type, res_id = await client.parse_url("https://open.spotify.com/track/4cOdK2wGLETKBW3PvgPWqT")
    assert res_type == "track"
    assert res_id == "4cOdK2wGLETKBW3PvgPWqT"


@pytest.mark.asyncio
async def test_spotify_parse_url_album():
    client = SpotifyClient()
    res_type, res_id = await client.parse_url("https://open.spotify.com/album/1DFixLWuPkv3KT3TnV35m3")
    assert res_type == "album"
    assert res_id == "1DFixLWuPkv3KT3TnV35m3"


@pytest.mark.asyncio
async def test_spotify_parse_url_playlist():
    client = SpotifyClient()
    res_type, res_id = await client.parse_url("https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M")
    assert res_type == "playlist"
    assert res_id == "37i9dQZF1DXcBWIGoYBM5M"


@pytest.mark.asyncio
async def test_spotify_parse_url_invalid():
    client = SpotifyClient()
    with pytest.raises(DownloadError, match="не является распознаваемым"):
        await client.parse_url("https://open.spotify.com/unknown/12345")


def test_spotify_formatting():
    artists = [{"name": "Daft Punk"}, {"name": "Pharrell Williams"}]
    assert SpotifyClient.format_artists(artists) == "Daft Punk\\\\Pharrell Williams"
    assert SpotifyClient.format_artists("Single Artist") == "Single Artist"
    assert SpotifyClient.format_artists(None) == "Unknown Artist"

    entity = {
        "coverArt": {
            "sources": [
                {"url": "https://i.scdn.co/image/small", "width": 300},
                {"url": "https://i.scdn.co/image/big", "width": 640},
            ]
        }
    }
    assert SpotifyClient.extract_cover_from_embed(entity) == "https://i.scdn.co/image/big"


@pytest.mark.asyncio
async def test_spotify_get_track_embed():
    html_content = """
    <html>
      <head>
        <script id="__NEXT_DATA__" type="application/json">
        {
          "props": {
            "pageProps": {
              "state": {
                "data": {
                  "entity": {
                    "name": "Get Lucky",
                    "duration": 248000,
                    "subtitle": "Daft Punk",
                    "artists": [{"name": "Daft Punk"}, {"name": "Pharrell Williams"}],
                    "coverArt": {
                      "sources": [{"url": "https://i.scdn.co/image/large", "width": 640}]
                    },
                    "album": {"name": "Random Access Memories"}
                  }
                }
              }
            }
          }
        }
        </script>
      </head>
      <body></body>
    </html>
    """

    mock_resp = MagicMock(spec=httpx.Response)
    mock_resp.status_code = 200
    mock_resp.text = html_content

    mock_client = AsyncMock(spec=httpx.AsyncClient)
    mock_client.is_closed = False
    mock_client.get.return_value = mock_resp

    client = SpotifyClient(http_client=mock_client)
    track = await client.get_track("track_123")
    assert track["title"] == "Get Lucky"
    assert track["artist"] == "Daft Punk\\\\Pharrell Williams"
    assert track["duration"] == 248
    assert track["cover_url"] == "https://i.scdn.co/image/large"


@pytest.mark.asyncio
async def test_spotify_get_playlist_embed():
    html_content = """
    <html>
      <head>
        <script id="__NEXT_DATA__" type="application/json">
        {
          "props": {
            "pageProps": {
              "state": {
                "data": {
                  "entity": {
                    "name": "Today's Top Hits",
                    "subtitle": "Spotify",
                    "coverArt": {
                      "sources": [{"url": "https://i.scdn.co/image/pl_cover", "width": 640}]
                    },
                    "trackList": [
                      {
                        "id": "t1",
                        "title": "Hit Song 1",
                        "subtitle": "Artist 1",
                        "duration": 180000
                      },
                      {
                        "id": "t2",
                        "title": "Hit Song 2",
                        "subtitle": "Artist 2",
                        "duration": 210000
                      }
                    ]
                  }
                }
              }
            }
          }
        }
        </script>
      </head>
    </html>
    """

    mock_resp = MagicMock(spec=httpx.Response)
    mock_resp.status_code = 200
    mock_resp.text = html_content

    mock_client = AsyncMock(spec=httpx.AsyncClient)
    mock_client.is_closed = False
    mock_client.get.return_value = mock_resp

    client = SpotifyClient(http_client=mock_client)
    playlist = await client.get_playlist("pl_123")
    assert playlist["tracks"][0]["title"] == "Hit Song 1"
    assert playlist["tracks"][1]["duration"] == 210


@pytest.mark.asyncio
async def test_spotify_parse_url_artist():
    """Проверка разбора URL профиля артиста Spotify."""
    client = SpotifyClient()
    res_type, res_id = await client.parse_url("https://open.spotify.com/artist/4tZwfgrHOc3mvqYxwDoOD5")
    assert res_type == "artist"
    assert res_id == "4tZwfgrHOc3mvqYxwDoOD5"


@pytest.mark.asyncio
async def test_spotify_get_artist_discography():
    """Проверка получения дискографии артиста Spotify."""
    mock_client = AsyncMock(spec=httpx.AsyncClient)
    mock_client.is_closed = False

    async def mock_get(url, headers=None, params=None):
        resp = MagicMock(spec=httpx.Response)
        resp.status_code = 200
        if "albums" in url:
            resp.json.return_value = {
                "items": [
                    {
                        "id": "alb_1",
                        "name": "Album 1",
                        "album_type": "album",
                        "release_date": "2022-01-01",
                        "total_tracks": 10,
                        "images": [{"url": "https://img.com/alb1.jpg"}],
                    }
                ]
            }
        else:
            resp.json.return_value = {
                "name": "Test Artist",
                "images": [{"url": "https://img.com/artist.jpg"}],
            }
        return resp

    mock_client.get = AsyncMock(side_effect=mock_get)
    client = SpotifyClient(client_id="cid", client_secret="csec", http_client=mock_client)
    client._access_token = "valid_token"
    client._token_expires_at = 9999999999.0

    data = await client.get_artist_discography("artist_123")
    assert data["artist"]["name"] == "Test Artist"
    assert len(data["items"]) == 1
    assert data["items"][0]["name"] == "Album 1"


@pytest.mark.asyncio
async def test_spotify_get_artist_discography_web_scrape():
    """Проверка скрейпинга публичной веб-страницы Spotify с initialState."""
    import base64
    import json

    state_obj = {
        "entities": {
            "items": {
                "spotify:artist:eminem_id": {
                    "profile": {"name": "Eminem"},
                    "visuals": {
                        "avatarImage": {
                            "sources": [{"url": "https://img.com/avatar.jpg", "width": 640}]
                        }
                    },
                    "discography": {
                        "albums": {
                            "items": [
                                {
                                    "releases": {
                                        "items": [
                                            {
                                                "name": "The Eminem Show",
                                                "type": "ALBUM",
                                                "uri": "spotify:album:alb_em_1",
                                                "date": {"year": 2002},
                                                "coverArt": {
                                                    "sources": [{"url": "https://img.com/em_show.jpg", "width": 640}]
                                                },
                                            }
                                        ]
                                    }
                                }
                            ]
                        },
                        "singles": {
                            "items": [
                                {
                                    "releases": {
                                        "items": [
                                            {
                                                "name": "Lose Yourself",
                                                "type": "SINGLE",
                                                "uri": "spotify:album:sng_em_1",
                                                "date": {"year": 2002},
                                                "coverArt": {
                                                    "sources": [{"url": "https://img.com/lose_yourself.jpg", "width": 640}]
                                                },
                                            }
                                        ]
                                    }
                                }
                            ]
                        },
                    },
                }
            }
        }
    }
    b64_data = base64.b64encode(json.dumps(state_obj).encode("utf-8")).decode("utf-8")
    html_content = f"""
    <html>
      <head>
        <script id="initialState" type="text/plain">{b64_data}</script>
      </head>
      <body></body>
    </html>
    """

    mock_resp = MagicMock(spec=httpx.Response)
    mock_resp.status_code = 200
    mock_resp.text = html_content

    mock_client = AsyncMock(spec=httpx.AsyncClient)
    mock_client.is_closed = False
    mock_client.get.return_value = mock_resp

    client = SpotifyClient(http_client=mock_client)
    data = await client.get_artist_discography("eminem_id")

    assert data["artist"]["name"] == "Eminem"
    assert data["artist"]["images"][0]["url"] == "https://img.com/avatar.jpg"
    assert len(data["items"]) == 2
    assert data["items"][0]["id"] == "alb_em_1"
    assert data["items"][0]["name"] == "The Eminem Show"
    assert data["items"][0]["album_type"] == "album"
    assert data["items"][0]["release_date"] == "2002"
    assert data["items"][0]["images"][0]["url"] == "https://img.com/em_show.jpg"
    assert data["items"][1]["id"] == "sng_em_1"
    assert data["items"][1]["name"] == "Lose Yourself"
    assert data["items"][1]["album_type"] == "single"


@pytest.mark.asyncio
async def test_spotify_get_artist_discography_embed_fallback():
    """Проверка fallback на embed при отсутствии initialState на веб-странице."""
    import json

    # Первый запрос — веб-страница без initialState
    web_html = "<html><head></head><body>No state</body></html>"
    # Второй запрос — embed
    embed_obj = {
        "props": {
            "pageProps": {
                "state": {
                    "data": {
                        "entity": {
                            "name": "Fallback Artist",
                            "images": [{"url": "https://img.com/embed_art.jpg"}],
                            "discography": {"all": {"items": []}},
                        }
                    }
                }
            }
        }
    }
    embed_html = f"""
    <html>
      <head>
        <script id="__NEXT_DATA__" type="application/json">{json.dumps(embed_obj)}</script>
      </head>
    </html>
    """

    async def mock_get(url, headers=None, params=None, follow_redirects=True):
        resp = MagicMock(spec=httpx.Response)
        resp.status_code = 200
        if "embed" in url:
            resp.text = embed_html
        else:
            resp.text = web_html
        return resp

    mock_client = AsyncMock(spec=httpx.AsyncClient)
    mock_client.is_closed = False
    mock_client.get = AsyncMock(side_effect=mock_get)

    client = SpotifyClient(http_client=mock_client)
    data = await client.get_artist_discography("fallback_id")

    assert data["artist"]["name"] == "Fallback Artist"
    assert data["artist"]["images"][0]["url"] == "https://img.com/embed_art.jpg"
    assert data["items"] == []


