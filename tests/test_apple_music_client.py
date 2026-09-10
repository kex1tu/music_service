"""Тесты для клиента Apple Music (AppleMusicClient)."""

from unittest.mock import AsyncMock, MagicMock

import httpx
import pytest

from app.core.exceptions import DownloadError
from app.services.apple_music_client import AppleMusicClient


def test_apple_music_parse_url():
    client = AppleMusicClient()

    # Трек в альбоме
    url_track = "https://music.apple.com/ru/album/discovery/697194953?i=697195787"
    res_type, params = client.parse_url(url_track)
    assert res_type == "track"
    assert params["album_id"] == "697194953"
    assert params["track_id"] == "697195787"

    # Одиночный трек
    url_song = "https://music.apple.com/us/song/one-more-time/697195787"
    res_type, params = client.parse_url(url_song)
    assert res_type == "track"
    assert params["track_id"] == "697195787"

    # Альбом
    url_album = "https://music.apple.com/us/album/discovery/697194953"
    res_type, params = client.parse_url(url_album)
    assert res_type == "album"
    assert params["album_id"] == "697194953"

    # Плейлист
    url_pl = "https://music.apple.com/us/playlist/todays-hits/pl.f4d106fed2bd41149aaacabb233eb5eb"
    res_type, params = client.parse_url(url_pl)
    assert res_type == "playlist"
    assert params["playlist_id"] == "pl.f4d106fed2bd41149aaacabb233eb5eb"

    # Некорректный URL
    with pytest.raises(DownloadError, match="не является распознаваемым"):
        client.parse_url("https://music.apple.com/invalid/format")


def test_apple_music_formatting():
    # Форматирование шаблона обложки
    raw_art = "https://is1-ssl.mzstatic.com/image/thumb/Music115/v4/{w}x{h}bb.jpg"
    assert (
        AppleMusicClient.format_cover(raw_art, 1400, 1400)
        == "https://is1-ssl.mzstatic.com/image/thumb/Music115/v4/1400x1400bb.jpg"
    )
    assert AppleMusicClient.format_cover(None) is None

    # Парсинг ISO 8601 duration
    assert AppleMusicClient.parse_iso_duration("PT3M45S") == 225
    assert AppleMusicClient.parse_iso_duration("PT1H2M10S") == 3730
    assert AppleMusicClient.parse_iso_duration(None) == 0


@pytest.mark.asyncio
async def test_apple_music_get_track_server_data():
    html_content = """
    <html>
      <head>
        <script type="application/json" id="serialized-server-data">
        [
          {
            "data": [
              {
                "id": "697195787",
                "type": "songs",
                "attributes": {
                  "name": "One More Time",
                  "artistName": "Daft Punk",
                  "albumName": "Discovery",
                  "durationInMillis": 320000,
                  "releaseDate": "2001-03-12",
                  "artwork": {
                    "url": "https://is1-ssl.mzstatic.com/image/{w}x{h}bb.jpg"
                  }
                }
              }
            ]
          }
        ]
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

    client = AppleMusicClient(http_client=mock_client)
    track = await client.get_track("https://music.apple.com/us/song/one-more-time/697195787", "697195787")
    assert track["title"] == "One More Time"
    assert track["artist"] == "Daft Punk"
    assert track["year"] == 2001
    assert track["duration"] == 320
    assert "1400x1400bb.jpg" in track["cover_url"]


@pytest.mark.asyncio
async def test_apple_music_get_collection_server_data():
    html_content = """
    <html>
      <head>
        <script type="application/json" id="serialized-server-data">
        [
          {
            "data": [
              {
                "id": "pl.123",
                "type": "playlists",
                "attributes": {
                  "name": "Electronic Hits",
                  "curatorName": "Apple Music Electronic",
                  "artwork": {
                    "url": "https://is1-ssl.mzstatic.com/image/pl/{w}x{h}bb.jpg"
                  }
                },
                "relationships": {
                  "tracks": {
                    "data": [
                      {
                        "id": "t1",
                        "attributes": {
                          "name": "Track 1",
                          "artistName": "Artist 1",
                          "durationInMillis": 180000,
                          "trackNumber": 1
                        }
                      },
                      {
                        "id": "t2",
                        "attributes": {
                          "name": "Track 2",
                          "artistName": "Artist 2",
                          "durationInMillis": 210000,
                          "trackNumber": 2
                        }
                      }
                    ]
                  }
                }
              }
            ]
          }
        ]
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

    client = AppleMusicClient(http_client=mock_client)
    col = await client.get_collection("https://music.apple.com/us/playlist/electronic-hits/pl.123")
    assert col["title"] == "Electronic Hits"
    assert col["artist"] == "Apple Music Electronic"
    assert len(col["tracks"]) == 2
    assert col["tracks"][0]["title"] == "Track 1"
    assert col["tracks"][1]["duration"] == 210
