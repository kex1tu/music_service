"""Тесты клиента публичного API Deezer (DeezerClient)."""

from unittest.mock import AsyncMock, MagicMock

import httpx
import pytest

from app.core.exceptions import DownloadError, NotFoundError
from app.services.deezer_client import DeezerClient


@pytest.mark.asyncio
async def test_parse_url_standard():
    """Проверка разбора стандартных и локализованных URL Deezer."""
    client = DeezerClient()

    res_type, res_id = await client.parse_url("https://www.deezer.com/track/3135556")
    assert res_type == "track"
    assert res_id == "3135556"

    res_type, res_id = await client.parse_url("https://www.deezer.com/ru/album/302127")
    assert res_type == "album"
    assert res_id == "302127"

    res_type, res_id = await client.parse_url("http://deezer.com/en/playlist/908622995")
    assert res_type == "playlist"
    assert res_id == "908622995"


@pytest.mark.asyncio
async def test_parse_url_short_link():
    """Проверка разрешения короткой ссылки deezer.page.link через редирект."""
    mock_http = MagicMock(spec=httpx.AsyncClient)
    mock_http.is_closed = False
    mock_resp = MagicMock(spec=httpx.Response)
    mock_resp.url = httpx.URL("https://www.deezer.com/album/123456")
    mock_http.head = AsyncMock(return_value=mock_resp)

    client = DeezerClient(http_client=mock_http)
    res_type, res_id = await client.parse_url("https://deezer.page.link/ShortCode")
    assert res_type == "album"
    assert res_id == "123456"


@pytest.mark.asyncio
async def test_parse_url_link_deezer_short():
    """Проверка разрешения короткой ссылки link.deezer.com через редирект (трек)."""
    mock_http = MagicMock(spec=httpx.AsyncClient)
    mock_http.is_closed = False
    mock_resp = MagicMock(spec=httpx.Response)
    mock_resp.url = httpx.URL("https://www.deezer.com/track/1043319862")
    mock_http.head = AsyncMock(return_value=mock_resp)

    client = DeezerClient(http_client=mock_http)
    res_type, res_id = await client.parse_url("https://link.deezer.com/s/ABC123xyz")
    assert res_type == "track"
    assert res_id == "1043319862"


@pytest.mark.asyncio
async def test_parse_url_link_deezer_album():
    """Проверка разрешения ссылки link.deezer.com на альбом с языковым префиксом."""
    mock_http = MagicMock(spec=httpx.AsyncClient)
    mock_http.is_closed = False
    mock_resp = MagicMock(spec=httpx.Response)
    mock_resp.url = httpx.URL("https://www.deezer.com/ru/album/1043319862")
    mock_http.head = AsyncMock(return_value=mock_resp)

    client = DeezerClient(http_client=mock_http)
    res_type, res_id = await client.parse_url("https://link.deezer.com/s/AlbumShort")
    assert res_type == "album"
    assert res_id == "1043319862"


@pytest.mark.asyncio
async def test_parse_url_link_deezer_fallback_get():
    """Проверка отката на GET-запрос, если HEAD завершается ошибкой."""
    mock_http = MagicMock(spec=httpx.AsyncClient)
    mock_http.is_closed = False
    mock_http.head = AsyncMock(
        side_effect=httpx.HTTPStatusError(
            "405 Method Not Allowed",
            request=MagicMock(),
            response=MagicMock(),
        )
    )

    mock_resp_get = MagicMock(spec=httpx.Response)
    mock_resp_get.url = httpx.URL("https://www.deezer.com/playlist/908622995")
    mock_http.get = AsyncMock(return_value=mock_resp_get)

    client = DeezerClient(http_client=mock_http)
    res_type, res_id = await client.parse_url("https://link.deezer.com/s/FallbackLink")
    assert res_type == "playlist"
    assert res_id == "908622995"


@pytest.mark.asyncio
async def test_parse_url_without_scheme():
    """Проверка автоматического добавления протокола https:// для ссылок без схемы."""
    mock_http = MagicMock(spec=httpx.AsyncClient)
    mock_http.is_closed = False
    mock_resp = MagicMock(spec=httpx.Response)
    mock_resp.url = httpx.URL("https://www.deezer.com/album/302127")
    mock_http.head = AsyncMock(return_value=mock_resp)

    client = DeezerClient(http_client=mock_http)
    res_type, res_id = await client.parse_url("link.deezer.com/s/NoScheme")
    assert res_type == "album"
    assert res_id == "302127"


@pytest.mark.asyncio
async def test_parse_url_regional_language_code():
    """Проверка поддержки региональных языковых кодов вроде pt-br или es-es."""
    client = DeezerClient()
    res_type, res_id = await client.parse_url("https://www.deezer.com/pt-br/album/1043319862")
    assert res_type == "album"
    assert res_id == "1043319862"


@pytest.mark.asyncio
async def test_parse_url_invalid():
    """Проверка выброса исключения на некорректном URL."""
    client = DeezerClient()
    with pytest.raises(DownloadError) as exc_info:
        await client.parse_url("https://example.com/not-deezer")
    assert "не является распознаваемым URL Deezer" in str(exc_info.value)


@pytest.mark.asyncio
async def test_get_track_success():
    """Проверка получения метаданных трека."""
    mock_http = MagicMock(spec=httpx.AsyncClient)
    mock_http.is_closed = False
    mock_resp = MagicMock(spec=httpx.Response)
    mock_resp.status_code = 200
    mock_resp.json.return_value = {
        "id": 3135556,
        "title": "Harder, Better, Faster, Stronger",
        "duration": 224,
        "artist": {"name": "Daft Punk"},
        "contributors": [{"name": "Daft Punk"}],
        "album": {
            "title": "Discovery",
            "cover_xl": "https://e-cdns-images.dzcdn.net/images/cover/xl.jpg",
        },
    }
    mock_http.get = AsyncMock(return_value=mock_resp)

    client = DeezerClient(http_client=mock_http)
    track = await client.get_track("3135556")
    assert track["id"] == 3135556
    assert track["title"] == "Harder, Better, Faster, Stronger"
    assert track["duration"] == 224


@pytest.mark.asyncio
async def test_get_album_with_pagination():
    """Проверка объединения страниц треков при пагинации альбома."""
    mock_http = MagicMock(spec=httpx.AsyncClient)
    mock_http.is_closed = False

    resp1 = MagicMock(spec=httpx.Response)
    resp1.status_code = 200
    resp1.json.return_value = {
        "id": 302127,
        "title": "Discovery",
        "release_date": "2001-03-07",
        "cover_xl": "https://cover_xl.jpg",
        "tracks": {
            "data": [{"id": 1, "title": "One More Time"}],
            "next": "https://api.deezer.com/album/302127/tracks?index=1",
        },
    }

    resp2 = MagicMock(spec=httpx.Response)
    resp2.status_code = 200
    resp2.json.return_value = {
        "data": [{"id": 2, "title": "Aerodynamic"}],
        "next": None,
    }

    mock_http.get = AsyncMock(side_effect=[resp1, resp2])

    client = DeezerClient(http_client=mock_http)
    album = await client.get_album("302127")
    assert len(album["tracks"]["data"]) == 2
    assert album["tracks"]["data"][0]["title"] == "One More Time"
    assert album["tracks"]["data"][1]["title"] == "Aerodynamic"


@pytest.mark.asyncio
async def test_api_errors():
    """Проверка трансляции ошибок API Deezer в NotFoundError и DownloadError."""
    mock_http = MagicMock(spec=httpx.AsyncClient)
    mock_http.is_closed = False

    # 404 HTTP
    mock_resp_404 = MagicMock(spec=httpx.Response)
    mock_resp_404.status_code = 404
    mock_http.get = AsyncMock(return_value=mock_resp_404)

    client = DeezerClient(http_client=mock_http)
    with pytest.raises(NotFoundError):
        await client.get_track("999999999999")

    # Код ошибки Deezer 800 (Not Found)
    mock_resp_err800 = MagicMock(spec=httpx.Response)
    mock_resp_err800.status_code = 200
    mock_resp_err800.json.return_value = {"error": {"code": 800, "message": "no data"}}
    mock_http.get = AsyncMock(return_value=mock_resp_err800)

    with pytest.raises(NotFoundError):
        await client.get_album("999999999999")

    # Другая ошибка Deezer
    mock_resp_err500 = MagicMock(spec=httpx.Response)
    mock_resp_err500.status_code = 200
    mock_resp_err500.json.return_value = {"error": {"code": 4, "message": "Quota exceeded"}}
    mock_http.get = AsyncMock(return_value=mock_resp_err500)

    with pytest.raises(DownloadError) as exc_info:
        await client.get_album("123")
    assert "Quota exceeded" in str(exc_info.value)


def test_format_artists_and_extract_cover():
    """Проверка вспомогательных функций форматирования артистов и выбора лучшей обложки."""
    data_multi = {
        "contributors": [
            {"name": "Daft Punk"},
            {"name": "Pharrell Williams"},
            {"name": "Nile Rodgers"},
        ],
        "cover_xl": "https://img.com/xl.jpg",
    }
    assert (
        DeezerClient.format_artists(data_multi)
        == "Daft Punk\\\\Pharrell Williams\\\\Nile Rodgers"
    )
    assert DeezerClient.extract_best_cover_url(data_multi) == "https://img.com/xl.jpg"

    data_single = {
        "artist": {"name": "Justice"},
        "cover_big": "https://img.com/big.jpg",
    }
    assert DeezerClient.format_artists(data_single) == "Justice"
    assert DeezerClient.extract_best_cover_url(data_single) == "https://img.com/big.jpg"


def test_format_artists_comma_and_feats():
    """Проверка разбиения составных артистов (запятые, feat, ft, &, x) и извлечения фитов из названий."""
    # 1. Запятая в имени артиста
    assert DeezerClient.format_artists({"artist": {"name": "jay-z, eminem"}}) == "jay-z\\\\eminem"
    assert DeezerClient.format_artists("jay-z, eminem") == "jay-z\\\\eminem"
    assert DeezerClient.format_artists({"artist": {"name": "Eminem, 50 Cent, Dr. Dre"}}) == "Eminem\\\\50 Cent\\\\Dr. Dre"

    # 2. Фиты в title и title_version
    data_feat_title = {
        "title": "Renegade (feat. Eminem)",
        "artist": {"name": "JAŸ-Z"},
    }
    assert DeezerClient.format_artists(data_feat_title) == "JAŸ-Z\\\\Eminem"

    data_title_ver = {
        "title": "Renegade",
        "title_version": "(feat. Eminem)",
        "artist": {"name": "JAŸ-Z"},
    }
    assert DeezerClient.format_artists(data_title_ver) == "JAŸ-Z\\\\Eminem"

    # 3. Разделители &, feat., ft., x, vs, и
    assert DeezerClient.format_artists({"artist": {"name": "Jay-Z & Eminem"}}) == "Jay-Z\\\\Eminem"
    assert DeezerClient.format_artists({"artist": {"name": "Jay-Z feat. Eminem"}}) == "Jay-Z\\\\Eminem"
    assert DeezerClient.format_artists({"artist": {"name": "Jay-Z ft. Eminem"}}) == "Jay-Z\\\\Eminem"
    assert DeezerClient.format_artists({"artist": {"name": "David Guetta x Bebe Rexha"}}) == "David Guetta\\\\Bebe Rexha"
    assert DeezerClient.format_artists({"artist": {"name": "Би-2 и Сплин"}}) == "Би-2\\\\Сплин"

    # 4. Сохранение составных имен (Tyler, The Creator, Earth, Wind & Fire)
    assert DeezerClient.format_artists({"artist": {"name": "Tyler, The Creator"}}) == "Tyler, The Creator"
    assert (
        DeezerClient.format_artists({"artist": {"name": "Tyler, The Creator, A$AP Rocky"}})
        == "Tyler, The Creator\\\\A$AP Rocky"
    )
    assert DeezerClient.format_artists({"artist": {"name": "Earth, Wind & Fire"}}) == "Earth, Wind & Fire"
    assert DeezerClient.format_artists({"artist": {"name": "Crosby, Stills, Nash & Young"}}) == "Crosby, Stills, Nash & Young"

    # 5. Устранение дубликатов (если артист есть и в artist, и в contributors, и в feat)
    data_dup = {
        "title": "Song (feat. Eminem)",
        "artist": {"name": "Jay-Z"},
        "contributors": [{"name": "Jay-Z"}, {"name": "Eminem"}],
    }
    assert DeezerClient.format_artists(data_dup) == "Jay-Z\\\\Eminem"


@pytest.mark.asyncio
async def test_parse_url_artist():
    """Проверка разбора URL профиля артиста Deezer."""
    client = DeezerClient()
    res_type, res_id = await client.parse_url("https://www.deezer.com/artist/27")
    assert res_type == "artist"
    assert res_id == "27"

    res_type, res_id = await client.parse_url("https://www.deezer.com/ru/artist/119")
    assert res_type == "artist"
    assert res_id == "119"


@pytest.mark.asyncio
async def test_get_artist_and_albums():
    """Проверка получения метаданных артиста и списка его релизов."""
    mock_http = MagicMock(spec=httpx.AsyncClient)
    mock_http.is_closed = False

    async def mock_get(url, params=None):
        resp = MagicMock(spec=httpx.Response)
        resp.status_code = 200
        if "artist/27/albums" in url:
            resp.json.return_value = {
                "data": [
                    {"id": 101, "title": "Discovery", "record_type": "album", "release_date": "2001-03-12"},
                    {"id": 102, "title": "One More Time", "record_type": "single", "release_date": "2000-11-13"},
                ]
            }
        else:
            resp.json.return_value = {
                "id": 27,
                "name": "Daft Punk",
                "picture_xl": "https://e-cdns-images.dzcdn.net/images/artist/dp.jpg",
            }
        return resp

    mock_http.get = AsyncMock(side_effect=mock_get)
    client = DeezerClient(http_client=mock_http)

    artist = await client.get_artist("27")
    assert artist["name"] == "Daft Punk"

    albums = await client.get_artist_albums("27")
    assert len(albums) == 2
    assert albums[0]["title"] == "Discovery"
    assert albums[1]["record_type"] == "single"

