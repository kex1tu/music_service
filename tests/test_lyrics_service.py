"""Модульные тесты для сервиса текстов песен (LyricsService, LrclibClient, GeniusClient)."""

from unittest.mock import AsyncMock, MagicMock

import httpx
import mutagen.id3
import pytest

from app.core.exceptions import PathTraversalError
from app.core.sandbox import PathSandbox
from app.schemas.lyrics import LyricsSearchResult
from app.services.lyrics_service import GeniusClient, LrclibClient, LyricsService


@pytest.fixture
def mock_httpx_client() -> MagicMock:
    """Создает мок httpx.AsyncClient."""
    client = MagicMock(spec=httpx.AsyncClient)
    client.is_closed = False
    return client


# ==============================================================================
# Тесты LrclibClient
# ==============================================================================


@pytest.mark.asyncio
async def test_lrclib_client_synced(mock_httpx_client: MagicMock):
    """Проверка получения синхронизированного текста (.lrc) из точного поиска /api/get."""
    resp = MagicMock(spec=httpx.Response)
    resp.status_code = 200
    resp.json.return_value = {
        "trackName": "Around the World",
        "artistName": "Daft Punk",
        "syncedLyrics": "[00:15.00] Around the world, around the world\n[00:19.00] Around the world, around the world",
        "plainLyrics": "Around the world, around the world",
    }
    mock_httpx_client.get = AsyncMock(return_value=resp)

    client = LrclibClient(client=mock_httpx_client)
    res = await client.get_lyrics(artist="Daft Punk", title="Around the World", album="Homework", duration=429)

    assert res is not None
    assert res.synced is True
    assert res.source == "lrclib"
    assert res.track_name == "Around the World"
    assert res.artist_name == "Daft Punk"
    assert "[00:15.00]" in (res.lyrics or "")


@pytest.mark.asyncio
async def test_lrclib_client_plain_fallback(mock_httpx_client: MagicMock):
    """Проверка падения /api/get (404) и успешного получения несинхронизированного текста из /api/search."""
    resp_get = MagicMock(spec=httpx.Response)
    resp_get.status_code = 404

    resp_search = MagicMock(spec=httpx.Response)
    resp_search.status_code = 200
    resp_search.json.return_value = [
        {
            "trackName": "One More Time",
            "artistName": "Daft Punk",
            "syncedLyrics": None,
            "plainLyrics": "One more time, we're gonna celebrate",
        }
    ]

    mock_httpx_client.get = AsyncMock(side_effect=[resp_get, resp_search])

    client = LrclibClient(client=mock_httpx_client)
    res = await client.get_lyrics(artist="Daft Punk", title="One More Time")

    assert res is not None
    assert res.synced is False
    assert res.source == "lrclib"
    assert res.lyrics == "One more time, we're gonna celebrate"


@pytest.mark.asyncio
async def test_lrclib_client_not_found(mock_httpx_client: MagicMock):
    """Проверка возврата None, если трек не найден ни в /api/get, ни в /api/search."""
    resp_get = MagicMock(spec=httpx.Response)
    resp_get.status_code = 404

    resp_search = MagicMock(spec=httpx.Response)
    resp_search.status_code = 404

    mock_httpx_client.get = AsyncMock(side_effect=[resp_get, resp_search])

    client = LrclibClient(client=mock_httpx_client)
    res = await client.get_lyrics(artist="Unknown Artist", title="Nonexistent Song")

    assert res is None


@pytest.mark.asyncio
async def test_lrclib_client_network_error(mock_httpx_client: MagicMock):
    """Проверка устойчивости к сетевым ошибкам (ConnectError, TimeoutException)."""
    mock_httpx_client.get = AsyncMock(side_effect=httpx.ConnectError("Failed to connect"))

    client = LrclibClient(client=mock_httpx_client)
    res = await client.get_lyrics(artist="Daft Punk", title="Get Lucky")
    assert res is None

    mock_httpx_client.get = AsyncMock(side_effect=httpx.TimeoutException("Read timeout"))
    res_timeout = await client.get_lyrics(artist="Daft Punk", title="Get Lucky")
    assert res_timeout is None


@pytest.mark.asyncio
async def test_lrclib_client_empty_input():
    """Проверка передачи пустых параметров в LrclibClient."""
    client = LrclibClient()
    assert await client.get_lyrics(artist="", title="") is None
    assert await client.get_lyrics(artist="Artist", title="") is None


# ==============================================================================
# Тесты GeniusClient
# ==============================================================================


@pytest.mark.asyncio
async def test_genius_client_no_token():
    """Проверка того, что без API токена клиент возвращает None без попытки запроса."""
    client = GeniusClient(api_token=None)
    assert await client.search_lyrics(artist="Daft Punk", title="One More Time") is None

    client_empty = GeniusClient(api_token="   ")
    assert await client_empty.search_lyrics(artist="Daft Punk", title="One More Time") is None


@pytest.mark.asyncio
async def test_genius_client_success(mock_httpx_client: MagicMock):
    """Проверка успешного поиска в Genius API."""
    resp = MagicMock(spec=httpx.Response)
    resp.status_code = 200
    resp.json.return_value = {
        "response": {
            "hits": [
                {
                    "result": {
                        "title": "Harder, Better, Faster, Stronger",
                        "primary_artist": {"name": "Daft Punk"},
                        "url": "https://genius.com/Daft-punk-harder-better-faster-stronger-lyrics",
                        "lyrics": "Work it make it do it makes us harder better faster stronger",
                    }
                }
            ]
        }
    }
    mock_httpx_client.get = AsyncMock(return_value=resp)

    client = GeniusClient(api_token="test_genius_token", client=mock_httpx_client)
    res = await client.search_lyrics(artist="Daft Punk", title="Harder, Better, Faster, Stronger")

    assert res is not None
    assert res.synced is False
    assert res.source == "genius"
    assert res.track_name == "Harder, Better, Faster, Stronger"
    assert res.artist_name == "Daft Punk"
    assert "harder better faster stronger" in (res.lyrics or "")


@pytest.mark.asyncio
async def test_genius_client_error(mock_httpx_client: MagicMock):
    """Проверка обработки ошибок авторизации (401), 500 и таймаута Genius API."""
    resp_401 = MagicMock(spec=httpx.Response)
    resp_401.status_code = 401
    mock_httpx_client.get = AsyncMock(return_value=resp_401)

    client = GeniusClient(api_token="invalid_token", client=mock_httpx_client)
    assert await client.search_lyrics(artist="Daft Punk", title="One More Time") is None

    resp_500 = MagicMock(spec=httpx.Response)
    resp_500.status_code = 500
    mock_httpx_client.get = AsyncMock(return_value=resp_500)
    assert await client.search_lyrics(artist="Daft Punk", title="One More Time") is None

    mock_httpx_client.get = AsyncMock(side_effect=httpx.TimeoutException("Genius timeout"))
    assert await client.search_lyrics(artist="Daft Punk", title="One More Time") is None


@pytest.mark.asyncio
async def test_genius_client_empty_hits(mock_httpx_client: MagicMock):
    """Проверка ответа с пустым списком hits."""
    resp = MagicMock(spec=httpx.Response)
    resp.status_code = 200
    resp.json.return_value = {"response": {"hits": []}}
    mock_httpx_client.get = AsyncMock(return_value=resp)

    client = GeniusClient(api_token="valid_token", client=mock_httpx_client)
    res = await client.search_lyrics(artist="Nonexistent", title="Track")
    assert res is None


# ==============================================================================
# Тесты LyricsService (приоритеты поиска и интеграция)
# ==============================================================================


@pytest.mark.asyncio
async def test_lyrics_service_search_priority(music_sandbox: PathSandbox, tmp_sandbox: PathSandbox):
    """Проверка каскада приоритетов поиска: LRCLIB synced > LRCLIB plain > Genius > None."""
    mock_lrclib = MagicMock(spec=LrclibClient)
    mock_genius = MagicMock(spec=GeniusClient)

    service = LyricsService(
        music_sandbox=music_sandbox,
        tmp_sandbox=tmp_sandbox,
        lrclib_client=mock_lrclib,
        genius_client=mock_genius,
    )

    # Сценарий 1: LRCLIB возвращает synced -> LRCLIB synced побеждает
    mock_lrclib.get_lyrics = AsyncMock(
        return_value=LyricsSearchResult(lyrics="[00:01.00] Synced", synced=True, source="lrclib")
    )
    mock_genius.search_lyrics = AsyncMock(
        return_value=LyricsSearchResult(lyrics="Genius plain", synced=False, source="genius")
    )

    res1 = await service.search("Artist", "Title")
    assert res1.source == "lrclib"
    assert res1.synced is True
    assert res1.lyrics == "[00:01.00] Synced"

    # Сценарий 2: LRCLIB возвращает plain (не synced) -> LRCLIB plain побеждает Genius
    mock_lrclib.get_lyrics = AsyncMock(
        return_value=LyricsSearchResult(lyrics="LRCLIB plain", synced=False, source="lrclib")
    )
    res2 = await service.search("Artist", "Title")
    assert res2.source == "lrclib"
    assert res2.synced is False
    assert res2.lyrics == "LRCLIB plain"

    # Сценарий 3: LRCLIB ничего не нашел, Genius нашел -> Genius побеждает
    mock_lrclib.get_lyrics = AsyncMock(return_value=None)
    res3 = await service.search("Artist", "Title")
    assert res3.source == "genius"
    assert res3.synced is False
    assert res3.lyrics == "Genius plain"

    # Сценарий 4: Ни LRCLIB, ни Genius ничего не нашли -> source="none", lyrics=None
    mock_genius.search_lyrics = AsyncMock(return_value=None)
    res4 = await service.search("Artist", "Title")
    assert res4.source == "none"
    assert res4.synced is False
    assert res4.lyrics is None


# ==============================================================================
# Тесты файловых операций LyricsService (get, save, delete, invariants)
# ==============================================================================


def test_get_lyrics_for_track_lrc(music_sandbox: PathSandbox):
    """Проверка чтения существующего .lrc файла."""
    service = LyricsService(music_sandbox=music_sandbox)

    mp3_path = music_sandbox.root / "Daft Punk" / "Discovery" / "01. One More Time.mp3"
    mp3_path.parent.mkdir(parents=True, exist_ok=True)
    mp3_path.touch()

    lrc_content = "[00:10.00] One more time\n[00:15.00] Celebrate"
    lrc_path = mp3_path.with_suffix(".lrc")
    lrc_path.write_text(lrc_content, encoding="utf-8")

    res = service.get_lyrics_for_track("Daft Punk/Discovery/01. One More Time.mp3")
    assert res.kind == "lrc"
    assert res.synced is True
    assert res.lyrics == lrc_content
    assert res.track_path == "Daft Punk/Discovery/01. One More Time.mp3"


def test_get_lyrics_for_track_txt(music_sandbox: PathSandbox):
    """Проверка чтения существующего .txt файла."""
    service = LyricsService(music_sandbox=music_sandbox)

    mp3_path = music_sandbox.root / "Daft Punk" / "Discovery" / "02. Aerodynamic.mp3"
    mp3_path.parent.mkdir(parents=True, exist_ok=True)
    mp3_path.touch()

    txt_content = "Plain lyrics without any timestamps."
    txt_path = mp3_path.with_suffix(".txt")
    txt_path.write_text(txt_content, encoding="utf-8")

    res = service.get_lyrics_for_track("Daft Punk/Discovery/02. Aerodynamic.mp3")
    assert res.kind == "txt"
    assert res.synced is False
    assert res.lyrics == txt_content


def test_get_lyrics_for_track_none(music_sandbox: PathSandbox):
    """Проверка получения статуса none, если файлов текста рядом нет."""
    service = LyricsService(music_sandbox=music_sandbox)

    mp3_path = music_sandbox.root / "Daft Punk" / "Discovery" / "03. Digital Love.mp3"
    mp3_path.parent.mkdir(parents=True, exist_ok=True)
    mp3_path.touch()

    res = service.get_lyrics_for_track("Daft Punk/Discovery/03. Digital Love.mp3")
    assert res.kind == "none"
    assert res.synced is False
    assert res.lyrics is None


def test_save_lyrics_auto_detect_lrc(music_sandbox: PathSandbox):
    """Проверка автоопределения формата .lrc по таймкодам и удаления старого .txt."""
    service = LyricsService(music_sandbox=music_sandbox)

    rel_track = "Artist/Album/01. Track.mp3"
    mp3_file = music_sandbox.root / "Artist" / "Album" / "01. Track.mp3"
    mp3_file.parent.mkdir(parents=True, exist_ok=True)
    mp3_file.touch()

    # Предварительно создаем старый .txt файл
    old_txt = mp3_file.with_suffix(".txt")
    old_txt.write_text("Old plain text", encoding="utf-8")
    assert old_txt.exists()

    lrc_content = "[01:23.45] Synchronized lyrics line"
    saved_path = service.save_lyrics(rel_track, lrc_content, kind="auto")

    assert saved_path.suffix == ".lrc"
    assert saved_path.exists()
    assert saved_path.read_text(encoding="utf-8") == lrc_content
    # Старый .txt файл должен быть удален во избежание конфликтов
    assert not old_txt.exists()


def test_save_lyrics_auto_detect_txt(music_sandbox: PathSandbox):
    """Проверка автоопределения формата .txt без таймкодов и удаления старого .lrc."""
    service = LyricsService(music_sandbox=music_sandbox)

    rel_track = "Artist/Album/02. Track.mp3"
    mp3_file = music_sandbox.root / "Artist" / "Album" / "02. Track.mp3"
    mp3_file.parent.mkdir(parents=True, exist_ok=True)
    mp3_file.touch()

    # Предварительно создаем старый .lrc файл
    old_lrc = mp3_file.with_suffix(".lrc")
    old_lrc.write_text("[00:01.00] Old lrc", encoding="utf-8")
    assert old_lrc.exists()

    plain_content = "Plain lyrics line 1\nPlain lyrics line 2"
    saved_path = service.save_lyrics(rel_track, plain_content, kind="auto")

    assert saved_path.suffix == ".txt"
    assert saved_path.exists()
    assert saved_path.read_text(encoding="utf-8") == plain_content
    # Старый .lrc файл должен быть удален
    assert not old_lrc.exists()


def test_save_lyrics_explicit_kind(music_sandbox: PathSandbox):
    """Проверка явного указания kind='lrc' или kind='txt'."""
    service = LyricsService(music_sandbox=music_sandbox)

    rel_track = "Artist/Album/03. Track.mp3"
    mp3_file = music_sandbox.root / "Artist" / "Album" / "03. Track.mp3"
    mp3_file.parent.mkdir(parents=True, exist_ok=True)
    mp3_file.touch()

    content = "Just text without tags"
    path_lrc = service.save_lyrics(rel_track, content, kind="lrc")
    assert path_lrc.suffix == ".lrc"

    path_txt = service.save_lyrics(rel_track, content, kind="txt")
    assert path_txt.suffix == ".txt"
    assert not path_lrc.exists()


def test_save_lyrics_zero_id3_invariants(music_sandbox: PathSandbox):
    """Проверка технологического инварианта Zero ID3 Lyrics.

    Тексты песен НИКОГДА не вшиваются в MP3 (никаких USLT/SYLT фреймов).
    MP3-файл остается нетронутым при сохранении текста.
    """
    service = LyricsService(music_sandbox=music_sandbox)

    rel_track = "Artist/Album/04. Track.mp3"
    mp3_file = music_sandbox.root / "Artist" / "Album" / "04. Track.mp3"
    mp3_file.parent.mkdir(parents=True, exist_ok=True)

    # Создаем MP3 с валидным базовым ID3v2.4 заголовком
    tags = mutagen.id3.ID3()
    tags.add(mutagen.id3.TIT2(encoding=3, text=["Original Title"]))
    tags.save(str(mp3_file), v2_version=4)

    initial_mtime = mp3_file.stat().st_mtime
    initial_bytes = mp3_file.read_bytes()

    # Сохраняем текст
    service.save_lyrics(rel_track, "[00:10.00] Synced line", kind="auto")

    # Читаем MP3 повторно
    updated_bytes = mp3_file.read_bytes()
    assert initial_bytes == updated_bytes
    assert mp3_file.stat().st_mtime == initial_mtime

    re_read_tags = mutagen.id3.ID3(str(mp3_file))
    # Убеждаемся, что фреймов текстов нет
    assert "USLT" not in re_read_tags
    assert "SYLT" not in re_read_tags
    assert re_read_tags.getall("USLT") == []
    assert re_read_tags.getall("SYLT") == []


def test_delete_lyrics(music_sandbox: PathSandbox):
    """Проверка удаления файлов текстов (.lrc/.txt)."""
    service = LyricsService(music_sandbox=music_sandbox)

    rel_track = "Artist/Album/05. Track.mp3"
    mp3_file = music_sandbox.root / "Artist" / "Album" / "05. Track.mp3"
    mp3_file.parent.mkdir(parents=True, exist_ok=True)
    mp3_file.touch()

    lrc_file = mp3_file.with_suffix(".lrc")
    lrc_file.write_text("lrc", encoding="utf-8")

    assert service.delete_lyrics(rel_track) is True
    assert not lrc_file.exists()

    # Повторное удаление возвращает False (файлов больше нет)
    assert service.delete_lyrics(rel_track) is False


def test_sandbox_path_traversal(music_sandbox: PathSandbox, tmp_sandbox: PathSandbox):
    """Проверка перехвата попыток выхода за пределы песочницы (PathTraversalError)."""
    service = LyricsService(music_sandbox=music_sandbox, tmp_sandbox=tmp_sandbox)

    with pytest.raises(PathTraversalError):
        service.get_lyrics_for_track("../../etc/passwd")

    with pytest.raises(PathTraversalError):
        service.save_lyrics("../../../evil.mp3", "evil content")

    with pytest.raises(PathTraversalError):
        service.delete_lyrics("..\\..\\windows\\system32\\cmd.exe")


def test_dual_sandbox_resolution(music_sandbox: PathSandbox, tmp_sandbox: PathSandbox):
    """Проверка разрешения треков как в постоянной медиатеке, так и во временном буфере."""
    service = LyricsService(music_sandbox=music_sandbox, tmp_sandbox=tmp_sandbox)

    # 1. Трек в tmp_sandbox
    tmp_track = tmp_sandbox.root / "job_abc123" / "converted" / "draft.mp3"
    tmp_track.parent.mkdir(parents=True, exist_ok=True)
    tmp_track.touch()

    service.save_lyrics("job_abc123/converted/draft.mp3", "[00:05.00] Tmp lyrics", kind="lrc")
    assert (tmp_sandbox.root / "job_abc123" / "converted" / "draft.lrc").exists()

    resp = service.get_lyrics_for_track("job_abc123/converted/draft.mp3")
    assert resp.kind == "lrc"
    assert resp.lyrics == "[00:05.00] Tmp lyrics"
