"""Тесты для клиента Яндекс Музыки (YandexMusicClient)."""

from unittest.mock import AsyncMock, MagicMock

import httpx
import pytest

from app.core.exceptions import DownloadError, NotFoundError
from app.services.yandex_client import YandexMusicClient


@pytest.mark.asyncio
async def test_yandex_parse_url_track_with_album():
    client = YandexMusicClient()
    url = "https://music.yandex.ru/album/12345/track/67890"
    res_type, params = await client.parse_url(url)
    assert res_type == "track"
    assert params["album_id"] == "12345"
    assert params["track_id"] == "67890"


@pytest.mark.asyncio
async def test_yandex_parse_url_track_short():
    client = YandexMusicClient()
    url = "https://music.yandex.kz/track/998877"
    res_type, params = await client.parse_url(url)
    assert res_type == "track"
    assert params["track_id"] == "998877"


@pytest.mark.asyncio
async def test_yandex_parse_url_album():
    client = YandexMusicClient()
    url = "https://music.yandex.by/album/554433"
    res_type, params = await client.parse_url(url)
    assert res_type == "album"
    assert params["album_id"] == "554433"


@pytest.mark.asyncio
async def test_yandex_parse_url_playlist():
    client = YandexMusicClient()
    url = "https://music.yandex.com/users/test-user/playlists/100"
    res_type, params = await client.parse_url(url)
    assert res_type == "playlist"
    assert params["owner"] == "test-user"
    assert params["kind"] == "100"


@pytest.mark.asyncio
async def test_yandex_parse_url_invalid():
    client = YandexMusicClient()
    with pytest.raises(DownloadError, match="не является распознаваемым"):
        await client.parse_url("https://music.yandex.ru/unknown/path")


def test_yandex_format_cover_and_artists():
    assert YandexMusicClient.format_cover(None) is None
    cover = YandexMusicClient.format_cover("avatars.yandex.net/get-music-content/123/456/%%")
    assert cover == "https://avatars.yandex.net/get-music-content/123/456/1000x1000"

    artists = [{"name": "Артист 1"}, {"name": "Артист 2"}]
    assert YandexMusicClient.format_artists(artists) == "Артист 1\\\\Артист 2"
    assert YandexMusicClient.format_artists([]) == "Unknown Artist"


@pytest.mark.asyncio
async def test_yandex_get_track_success():
    mock_resp = MagicMock(spec=httpx.Response)
    mock_resp.status_code = 200
    mock_resp.json.return_value = {
        "track": {
            "id": 12345,
            "title": "Песня",
            "durationMs": 180000,
            "artists": [{"name": "Исполнитель"}],
            "albums": [{"title": "Альбом", "year": 2024}],
            "ogImage": "avatars.yandex.net/get-music-content/1/2/%%",
        }
    }

    mock_client = AsyncMock(spec=httpx.AsyncClient)
    mock_client.is_closed = False
    mock_client.get.return_value = mock_resp

    client = YandexMusicClient(http_client=mock_client)
    track = await client.get_track("12345")
    assert track["title"] == "Песня"
    assert track["id"] == 12345


@pytest.mark.asyncio
async def test_yandex_get_track_not_found():
    mock_resp = MagicMock(spec=httpx.Response)
    mock_resp.status_code = 404

    mock_client = AsyncMock(spec=httpx.AsyncClient)
    mock_client.is_closed = False
    mock_client.get.return_value = mock_resp

    client = YandexMusicClient(http_client=mock_client)
    with pytest.raises(NotFoundError, match="не найден"):
        await client.get_track("999999")


@pytest.mark.asyncio
async def test_yandex_get_album_success():
    mock_resp = MagicMock(spec=httpx.Response)
    mock_resp.status_code = 200
    mock_resp.json.return_value = {
        "title": "Великий Альбом",
        "artists": [{"name": "Группа"}],
        "year": 2023,
        "volumes": [
            [
                {"id": 1, "title": "Трек 1", "durationMs": 150000},
                {"id": 2, "title": "Трек 2", "durationMs": 200000},
            ]
        ],
    }

    mock_client = AsyncMock(spec=httpx.AsyncClient)
    mock_client.is_closed = False
    mock_client.get.return_value = mock_resp

    client = YandexMusicClient(http_client=mock_client)
    album = await client.get_album("777")
    assert album["title"] == "Великий Альбом"
    assert len(album["volumes"][0]) == 2


@pytest.mark.asyncio
async def test_yandex_get_playlist_success():
    mock_resp = MagicMock(spec=httpx.Response)
    mock_resp.status_code = 200
    mock_resp.json.return_value = {
        "playlist": {
            "title": "Мой Плейлист",
            "owner": {"name": "User"},
            "tracks": [{"id": 10, "title": "Трек Плейлиста"}],
        }
    }

    mock_client = AsyncMock(spec=httpx.AsyncClient)
    mock_client.is_closed = False
    mock_client.get.return_value = mock_resp

    client = YandexMusicClient(http_client=mock_client)
    playlist = await client.get_playlist("user", "1")
    assert playlist["title"] == "Мой Плейлист"
    assert len(playlist["tracks"]) == 1


@pytest.mark.asyncio
async def test_yandex_direct_download_url():
    # Без токена возвращает None
    client_no_token = YandexMusicClient()
    assert await client_no_token.get_direct_download_url("12345") is None

    # С токеном
    mock_info_resp = MagicMock(spec=httpx.Response)
    mock_info_resp.status_code = 200
    mock_info_resp.json.return_value = {
        "result": [
            {
                "codec": "mp3",
                "bitrateInKbps": 320,
                "downloadInfoUrl": "https://storage.yandex.net/get-info",
            }
        ]
    }

    xml_content = """<?xml version="1.0" encoding="utf-8"?>
    <download-info>
        <host>s123.yandex.net</host>
        <path>/stream/file.mp3</path>
        <ts>1725900000</ts>
        <s>abcdef123456</s>
    </download-info>"""

    mock_xml_resp = MagicMock(spec=httpx.Response)
    mock_xml_resp.status_code = 200
    mock_xml_resp.text = xml_content

    mock_client = AsyncMock(spec=httpx.AsyncClient)
    mock_client.is_closed = False
    mock_client.get.side_effect = [mock_info_resp, mock_xml_resp]

    client_with_token = YandexMusicClient(token="valid_token", http_client=mock_client)
    direct_url = await client_with_token.get_direct_download_url("12345")
    assert direct_url is not None
    assert "https://s123.yandex.net/get-mp3/" in direct_url
    assert "/1725900000/stream/file.mp3" in direct_url


@pytest.mark.asyncio
async def test_yandex_parse_url_artist():
    """Проверка разбора URL профиля артиста Яндекс Музыки."""
    client = YandexMusicClient()
    res_type, params = await client.parse_url("https://music.yandex.ru/artist/3121")
    assert res_type == "artist"
    assert params["artist_id"] == "3121"


@pytest.mark.asyncio
async def test_yandex_get_artist_discography():
    """Проверка получения информации об артисте и списка альбомов в Яндекс Музыке."""
    mock_resp = MagicMock(spec=httpx.Response)
    mock_resp.status_code = 200
    mock_resp.json.return_value = {
        "artist": {"name": "Кино", "cover": {"uri": "avatars.yandex.net/get-music-content/123/%%"}},
        "albums": [
            {"id": 1, "title": "Группа крови", "year": 1988, "trackCount": 11, "type": "album"}
        ],
    }

    mock_client = AsyncMock(spec=httpx.AsyncClient)
    mock_client.is_closed = False
    mock_client.get.return_value = mock_resp

    client = YandexMusicClient(http_client=mock_client)
    data = await client.get_artist_discography("3121")
    assert data["artist"]["name"] == "Кино"
    assert len(data["albums"]) == 1
    assert data["albums"][0]["title"] == "Группа крови"

