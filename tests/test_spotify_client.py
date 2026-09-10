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
    assert playlist["title"] == "Today's Top Hits"
    assert playlist["artist"] == "Spotify"
    assert len(playlist["tracks"]) == 2
    assert playlist["tracks"][0]["title"] == "Hit Song 1"
    assert playlist["tracks"][1]["duration"] == 210
