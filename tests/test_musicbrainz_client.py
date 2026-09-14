"""Тесты клиента публичного API MusicBrainz (MusicBrainzClient)."""

import time
from unittest.mock import AsyncMock, MagicMock

import httpx
import pytest

from app.core.exceptions import DownloadError, NotFoundError
from app.services.musicbrainz_client import MUSICBRAINZ_USER_AGENT, MusicBrainzClient


def test_format_artist_credit_single():
    """Проверка форматирования одиночного артиста."""
    artist_credit = [{"name": "Daft Punk", "artist": {"id": "1", "name": "Daft Punk"}}]
    assert MusicBrainzClient.format_artist_credit(artist_credit) == "Daft Punk"


def test_format_artist_credit_multiple():
    """Проверка форматирования с joinphrase (feat., &, и др.)."""
    artist_credit = [
        {"name": "Daft Punk", "joinphrase": " feat. "},
        {"name": "Pharrell Williams", "joinphrase": " & "},
        {"name": "Nile Rodgers", "joinphrase": ""},
    ]
    assert (
        MusicBrainzClient.format_artist_credit(artist_credit)
        == "Daft Punk feat. Pharrell Williams & Nile Rodgers"
    )


def test_format_artist_credit_fallback():
    """Проверка fallback для пустых или некорректных структур."""
    assert MusicBrainzClient.format_artist_credit([]) == "Unknown Artist"
    assert MusicBrainzClient.format_artist_credit(None) == "Unknown Artist"


@pytest.mark.asyncio
async def test_search_releases_success():
    """Проверка успешного поиска релизов в MusicBrainz."""
    mock_http = MagicMock(spec=httpx.AsyncClient)
    mock_http.is_closed = False

    mock_resp = MagicMock(spec=httpx.Response)
    mock_resp.status_code = 200
    mock_resp.json.return_value = {
        "releases": [
            {
                "id": "mbid-12345",
                "title": "Discovery",
                "date": "2001-03-12",
                "country": "FR",
                "status": "Official",
                "track-count": 14,
                "artist-credit": [{"name": "Daft Punk"}],
            }
        ]
    }
    mock_http.get = AsyncMock(return_value=mock_resp)

    client = MusicBrainzClient(http_client=mock_http, min_request_interval=0.01)
    results = await client.search_releases("Discovery")

    assert len(results) == 1
    r = results[0]
    assert r["id"] == "mbid-12345"
    assert r["title"] == "Discovery"
    assert r["artist"] == "Daft Punk"
    assert r["year"] == 2001
    assert r["nb_tracks"] == 14
    assert r["country"] == "FR"
    assert r["status"] == "Official"
    assert "https://coverartarchive.org/release/mbid-12345/front-500" in r["cover_url"]

    # Проверяем заголовок User-Agent
    assert mock_http.get.call_args[1]["headers"]["User-Agent"] == MUSICBRAINZ_USER_AGENT


@pytest.mark.asyncio
async def test_search_releases_empty():
    """Пустой запрос возвращает пустой список без обращения к сети."""
    client = MusicBrainzClient(min_request_interval=0.01)
    results = await client.search_releases("   ")
    assert results == []


@pytest.mark.asyncio
async def test_get_release_details():
    """Проверка детального разбора релиза со всеми дорожками."""
    mock_http = MagicMock(spec=httpx.AsyncClient)
    mock_http.is_closed = False

    mock_resp = MagicMock(spec=httpx.Response)
    mock_resp.status_code = 200
    mock_resp.json.return_value = {
        "id": "mbid-album-1",
        "title": "Random Access Memories",
        "date": "2013-05-17",
        "artist-credit": [{"name": "Daft Punk"}],
        "genres": [{"name": "electronic"}, {"name": "disco"}],
        "media": [
            {
                "position": 1,
                "tracks": [
                    {
                        "position": 1,
                        "title": "Give Life Back to Music",
                        "length": 274000,
                        "artist-credit": [{"name": "Daft Punk"}],
                    },
                    {
                        "position": 2,
                        "title": "Get Lucky",
                        "length": 369000,
                        "artist-credit": [
                            {"name": "Daft Punk", "joinphrase": " feat. "},
                            {"name": "Pharrell Williams"},
                        ],
                    },
                ],
            }
        ],
    }
    mock_http.get = AsyncMock(return_value=mock_resp)

    client = MusicBrainzClient(http_client=mock_http, min_request_interval=0.01)
    data = await client.get_release("mbid-album-1")

    assert data["id"] == "mbid-album-1"
    assert data["title"] == "Random Access Memories"
    assert data["artist"] == "Daft Punk"
    assert data["year"] == 2013
    assert data["genre"] == "Electronic\\\\Disco"
    assert len(data["tracks"]) == 2

    t1 = data["tracks"][0]
    assert t1["track_number"] == 1
    assert t1["title"] == "Give Life Back to Music"
    assert t1["artist"] == "Daft Punk"
    assert t1["duration"] == 274

    t2 = data["tracks"][1]
    assert t2["track_number"] == 2
    assert t2["title"] == "Get Lucky"
    assert t2["artist"] == "Daft Punk feat. Pharrell Williams"
    assert t2["duration"] == 369


@pytest.mark.asyncio
async def test_get_release_not_found():
    """Проверка обработки ошибки 404."""
    mock_http = MagicMock(spec=httpx.AsyncClient)
    mock_http.is_closed = False

    mock_resp = MagicMock(spec=httpx.Response)
    mock_resp.status_code = 404
    mock_http.get = AsyncMock(return_value=mock_resp)

    client = MusicBrainzClient(http_client=mock_http, min_request_interval=0.01)
    with pytest.raises(NotFoundError):
        await client.get_release("nonexistent-mbid")


@pytest.mark.asyncio
async def test_rate_limit_and_retry():
    """Проверка соблюдения рейт-лимита и повтора при 429 / Retry-After."""
    mock_http = MagicMock(spec=httpx.AsyncClient)
    mock_http.is_closed = False

    resp_429 = MagicMock(spec=httpx.Response)
    resp_429.status_code = 429
    resp_429.headers = {"Retry-After": "0.05"}

    resp_200 = MagicMock(spec=httpx.Response)
    resp_200.status_code = 200
    resp_200.json.return_value = {"releases": []}

    mock_http.get = AsyncMock(side_effect=[resp_429, resp_200])

    client = MusicBrainzClient(http_client=mock_http, min_request_interval=0.02)
    start_time = time.monotonic()
    await client.search_releases("Test")
    elapsed = time.monotonic() - start_time

    assert mock_http.get.call_count == 2
    assert elapsed >= 0.05


@pytest.mark.asyncio
async def test_search_covers():
    """Проверка поиска обложек через Cover Art Archive."""
    mock_http = MagicMock(spec=httpx.AsyncClient)
    mock_http.is_closed = False

    mock_resp = MagicMock(spec=httpx.Response)
    mock_resp.status_code = 200
    mock_resp.json.return_value = {
        "releases": [
            {
                "id": "cov-mbid",
                "title": "Nevermind",
                "artist-credit": [{"name": "Nirvana"}],
                "date": "1991-09-24",
            }
        ]
    }
    mock_http.get = AsyncMock(return_value=mock_resp)

    client = MusicBrainzClient(http_client=mock_http, min_request_interval=0.01)
    covers = await client.search_covers("Nevermind", limit=3)

    assert len(covers) == 1
    assert covers[0].source == "musicbrainz"
    assert covers[0].title == "Nevermind"
    assert covers[0].artist == "Nirvana"
    assert covers[0].cover_url == "https://coverartarchive.org/release/cov-mbid/front-500"
    assert covers[0].year == 1991


@pytest.mark.asyncio
async def test_network_error_raises_download_error():
    """Проверка генерации DownloadError при сетевой ошибке после исчерпания попыток."""
    mock_http = MagicMock(spec=httpx.AsyncClient)
    mock_http.is_closed = False
    mock_http.get = AsyncMock(side_effect=httpx.ConnectError("Network unreachable"))

    client = MusicBrainzClient(http_client=mock_http, min_request_interval=0.01)
    with pytest.raises(DownloadError) as exc_info:
        await client.search_releases("Test", limit=2)
    assert "Ошибка сети при обращении к MusicBrainz" in str(exc_info.value)
