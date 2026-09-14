"""Тесты подсистемы распознавания источников и сопоставления аудиопотоков (SourceResolver, AudioMatcher)."""

import asyncio
from pathlib import Path
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from app.core.exceptions import AudioMatchError, UnsupportedSourceError
from app.services.deezer_client import DeezerClient
from app.services.source_resolver import AudioMatcher, SourceResolver


def test_detect_source_whitelist():
    """Проверка определения источника и валидации доменов по белому списку."""
    resolver = SourceResolver(deezer_client=MagicMock())

    # Разрешенные домены (со схемой и без)
    assert resolver.detect_source("https://www.deezer.com/album/302127") == "deezer"
    assert resolver.detect_source("https://deezer.page.link/abc") == "deezer"
    assert resolver.detect_source("https://link.deezer.com/s/xyz123") == "deezer"
    assert resolver.detect_source("link.deezer.com/s/xyz123") == "deezer"
    assert resolver.detect_source("https://music.youtube.com/watch?v=dQw4w9WgXcQ") == "youtube"
    assert resolver.detect_source("music.youtube.com/watch?v=dQw4w9WgXcQ") == "youtube"
    assert resolver.detect_source("https://youtu.be/dQw4w9WgXcQ") == "youtube"
    assert resolver.detect_source("https://soundcloud.com/daftpunkofficial/get-lucky") == "soundcloud"
    assert resolver.detect_source("soundcloud.com/daftpunkofficial/get-lucky") == "soundcloud"
    assert resolver.detect_source("https://music.yandex.ru/album/123/track/456") == "yandex"
    assert resolver.detect_source("music.yandex.kz/track/456") == "yandex"
    assert resolver.detect_source("https://open.spotify.com/track/4cOdK2wGLETKBW3PvgPWqT") == "spotify"
    assert resolver.detect_source("spotify.link/xyz123") == "spotify"
    assert resolver.detect_source("https://music.apple.com/us/album/discovery/697194953") == "applemusic"
    assert resolver.detect_source("magnet:?xt=urn:btih:d2474e86c95b19b8bcfdb92bc12c9d44667cfa36") == "torrent"
    assert resolver.detect_source("MAGNET:?xt=urn:btih:d2474e86c95b19b8bcfdb92bc12c9d44667cfa36") == "torrent"
    assert resolver.detect_source("magnet:xt=urn:btih:d2474e86c95b19b8bcfdb92bc12c9d44667cfa36") == "torrent"
    assert resolver.detect_source("https://example-tracker.org/download/release.torrent") == "torrent"
    assert resolver.detect_source("https://example-tracker.org/download.php?id=123&name=release.torrent") == "torrent"
    assert resolver.detect_source("/tmp/music_uploads/_torrents/123/album.torrent") == "torrent"

    # Запрещенные домены
    with pytest.raises(UnsupportedSourceError):
        resolver.detect_source("https://evil-site.com/track.mp3")

    with pytest.raises(UnsupportedSourceError):
        resolver.detect_source("https://unknown-streaming-service.net/track/12345")


def test_audio_matcher_scoring():
    """Проверка формулы оценки кандидатов AudioMatcher."""
    # 1. Отличный кандидат: официальный трек YouTube Music, точная длительность
    best_score = AudioMatcher.calculate_score(
        target_title="Get Lucky",
        target_artist="Daft Punk",
        target_duration=248,
        candidate_title="Get Lucky",
        candidate_channel="Daft Punk - Topic",
        candidate_duration=248,
    )
    assert best_score >= 85.0

    # 2. Плохой кандидат: концертная запись, длительность отличается на 45 сек
    live_score = AudioMatcher.calculate_score(
        target_title="Get Lucky",
        target_artist="Daft Punk",
        target_duration=248,
        candidate_title="Get Lucky (Live at Grammys 2014)",
        candidate_channel="Random User",
        candidate_duration=310,
    )
    assert live_score < 35.0
    assert best_score > live_score


@pytest.mark.asyncio
async def test_audio_matcher_find_best_match():
    """Проверка выбора лучшего видеоролика из списка результатов yt-dlp."""
    matcher = AudioMatcher()

    mock_entries = [
        {
            "id": "live_vid",
            "title": "One More Time (Live 2007)",
            "channel": "Bootleg Channel",
            "duration": 340,
        },
        {
            "id": "official_vid",
            "title": "One More Time",
            "channel": "Daft Punk - Topic",
            "duration": 320,
        },
    ]

    with (
        patch.object(matcher, "calculate_score", side_effect=[20.0, 95.0]),
        patch("yt_dlp.YoutubeDL") as mock_ydl_cls,
    ):
        mock_ydl = MagicMock()
        mock_ydl.__enter__.return_value.extract_info.return_value = {"entries": mock_entries}
        mock_ydl_cls.return_value = mock_ydl

        best_url = await matcher.find_best_match(
            title="One More Time",
            artist="Daft Punk",
            duration=320,
        )
        assert best_url == "https://www.youtube.com/watch?v=official_vid"


@pytest.mark.asyncio
async def test_audio_matcher_no_candidates():
    """Проверка выброса AudioMatchError при отсутствии подходящих результатов."""
    matcher = AudioMatcher()

    with patch("yt_dlp.YoutubeDL") as mock_ydl_cls:
        mock_ydl = MagicMock()
        mock_ydl.__enter__.return_value.extract_info.return_value = {"entries": []}
        mock_ydl_cls.return_value = mock_ydl

        with pytest.raises(AudioMatchError):
            await matcher.find_best_match(
                title="Nonexistent Obscure Song 12345",
                artist="Nobody",
                duration=120,
            )


@pytest.mark.asyncio
async def test_source_resolver_deezer_album():
    """Проверка разрешения альбома Deezer в список ResolvedTrack со связанными аудиопотоками."""
    mock_deezer = MagicMock(spec=DeezerClient)
    mock_deezer.parse_url = AsyncMock(return_value=("album", "302127"))
    mock_deezer.get_album = AsyncMock(
        return_value={
            "id": 302127,
            "title": "Discovery",
            "release_date": "2001-03-07",
            "cover_xl": "https://cover_xl.jpg",
            "contributors": [{"name": "Daft Punk"}],
            "tracks": {
                "data": [
                    {
                        "id": 1,
                        "title": "One More Time",
                        "duration": 320,
                        "track_position": 1,
                        "isrc": "FR6V80100001",
                        "contributors": [{"name": "Daft Punk"}, {"name": "Romanthony"}],
                    }
                ]
            },
        }
    )

    mock_matcher = MagicMock(spec=AudioMatcher)
    mock_matcher.find_best_match = AsyncMock(return_value="https://www.youtube.com/watch?v=matched_one")

    resolver = SourceResolver(deezer_client=mock_deezer, audio_matcher=mock_matcher)
    album_meta, tracks = await resolver.resolve("https://www.deezer.com/album/302127")

    assert album_meta["album"] == "Discovery"
    assert album_meta["year"] == 2001
    assert len(tracks) == 1
    track = tracks[0]
    assert track.title == "One More Time"
    assert track.artist == "Daft Punk\\\\Romanthony"
    assert track.audio_source_url == "https://www.youtube.com/watch?v=matched_one"
    assert track.isrc == "FR6V80100001"


def test_audio_matcher_scoring_punctuation():
    """Проверка нечувствительности к пунктуации (RapidFuzz default_process) для трека SAFEWORD!."""
    score = AudioMatcher.calculate_score(
        target_title="SAFEWORD!",
        target_artist="Call Me Karizma",
        target_duration=180,
        candidate_title="Call Me Karizma - SAFEWORD",
        candidate_channel="Call Me Karizma - Topic",
        candidate_duration=180,
    )
    # Полное совпадение длительности (40) + название 100% (30) + артист (15) + Topic (15) = 100.0
    assert score >= 85.0


def test_audio_matcher_scoring_bad_keyword_boundary():
    """Проверка, что подстрока 'live' внутри 'Oliver' не штрафуется благодаря контролю границ слов."""
    clean_score = AudioMatcher.calculate_score(
        target_title="Alien Boy",
        target_artist="Oliver Tree",
        target_duration=165,
        candidate_title="Oliver Tree - Alien Boy",
        candidate_channel="Oliver Tree",
        candidate_duration=165,
    )
    assert clean_score >= 80.0

    # Настоящее слово live должно штрафоваться на 35 баллов
    live_score = AudioMatcher.calculate_score(
        target_title="Alien Boy",
        target_artist="Oliver Tree",
        target_duration=165,
        candidate_title="Oliver Tree - Alien Boy (Live at Red Rocks)",
        candidate_channel="Oliver Tree",
        candidate_duration=165,
    )
    assert clean_score - live_score >= 30.0


def test_audio_matcher_helpers():
    """Проверка функций нормализации запроса и очистки префикса исполнителя."""
    cleaned = AudioMatcher.clean_query_text('“Artist / Track: [Remastered]?”')
    assert "/" not in cleaned
    assert ":" not in cleaned
    assert "[" not in cleaned
    assert "?" not in cleaned
    assert "Artist" in cleaned and "Track" in cleaned

    # Устранение дублирующего префикса исполнителя
    assert (
        AudioMatcher.strip_artist_prefix("Call Me Karizma - SAFEWORD", "Call Me Karizma")
        == "SAFEWORD"
    )
    assert AudioMatcher.strip_artist_prefix("SAFEWORD", "Call Me Karizma") == "SAFEWORD"


@pytest.mark.asyncio
async def test_audio_matcher_multi_stage_fallback():
    """Проверка перехода по 6-этапной поисковой лестнице и выбора глобально лучшего кандидата."""
    matcher = AudioMatcher()

    # Первая стадия (Official Audio) возвращает неподходящее видео (реакция)
    stage1_entries = [
        {
            "id": "reaction_vid",
            "title": "Reacting to Call Me Karizma",
            "channel": "Reactor123",
            "duration": 600,
        }
    ]
    # Вторая стадия (Topic) возвращает официальный трек
    stage2_entries = [
        {
            "id": "official_safeword",
            "title": "Call Me Karizma - SAFEWORD",
            "channel": "Call Me Karizma - Topic",
            "duration": 180,
        }
    ]

    with patch("yt_dlp.YoutubeDL") as mock_ydl_cls:
        mock_ydl = MagicMock()
        mock_ydl.__enter__.return_value.extract_info.side_effect = [
            {"entries": stage1_entries},
            {"entries": stage2_entries},
        ]
        mock_ydl_cls.return_value = mock_ydl

        url = await matcher.find_best_match(
            title="Call Me Karizma - SAFEWORD",
            artist="Call Me Karizma",
            duration=180,
        )
        assert url == "https://www.youtube.com/watch?v=official_safeword"


def test_audio_matcher_cookies_injection(tmp_path: Path):
    """Проверка передачи cookiefile в опции yt-dlp AudioMatcher."""
    cookies_file = tmp_path / "cookies.txt"
    cookies_file.write_text("# Cookies")

    matcher = AudioMatcher(cookies_path=cookies_file)
    assert matcher._ytdl_opts.get("cookiefile") == str(cookies_file)


@pytest.mark.asyncio
async def test_source_resolver_ytdlp_cookies(tmp_path: Path):
    """Проверка передачи cookiefile в yt-dlp при разрешении источников через SourceResolver."""
    cookies_file = tmp_path / "cookies.txt"
    cookies_file.write_text("# Cookies")

    resolver = SourceResolver(
        deezer_client=MagicMock(),
        cookies_path=cookies_file,
    )

    with patch("yt_dlp.YoutubeDL") as mock_ydl_cls:
        mock_ydl = MagicMock()
        mock_ydl.__enter__.return_value.extract_info.return_value = {
            "title": "Test Track",
            "uploader": "Test Artist",
            "duration": 180,
            "thumbnail": "https://img.com/1.jpg",
            "webpage_url": "https://youtube.com/watch?v=test",
        }
        mock_ydl_cls.return_value = mock_ydl

        _, tracks = await resolver.resolve(
            "https://youtube.com/watch?v=test",
            source_hint="youtube",
        )
        assert len(tracks) == 1
        call_opts = mock_ydl_cls.call_args[0][0]
        assert call_opts.get("cookiefile") == str(cookies_file)


@pytest.mark.asyncio
async def test_source_resolver_deezer_album_safeword():
    """Проверка полного цикла разрешения альбома Deezer 1043319862 с треком SAFEWORD."""
    mock_deezer = MagicMock(spec=DeezerClient)
    mock_deezer.parse_url = AsyncMock(return_value=("album", "1043319862"))
    mock_deezer.get_album = AsyncMock(
        return_value={
            "id": 1043319862,
            "title": "The Gloomy Tapes, Vol. 2",
            "release_date": "2019-01-01",
            "cover_xl": "https://img.com/cover.jpg",
            "contributors": [{"name": "Call Me Karizma"}],
            "tracks": {
                "data": [
                    {
                        "id": 789,
                        "title": "Call Me Karizma - SAFEWORD",
                        "duration": 180,
                        "track_position": 1,
                        "contributors": [{"name": "Call Me Karizma"}],
                    }
                ]
            },
        }
    )

    mock_matcher = MagicMock(spec=AudioMatcher)
    mock_matcher.find_best_match = AsyncMock(
        return_value="https://www.youtube.com/watch?v=safeword_yt"
    )

    resolver = SourceResolver(deezer_client=mock_deezer, audio_matcher=mock_matcher)
    album_meta, tracks = await resolver.resolve("https://www.deezer.com/ru/album/1043319862")

    assert album_meta["album"] == "The Gloomy Tapes, Vol. 2"
    assert len(tracks) == 1
    assert tracks[0].title == "Call Me Karizma - SAFEWORD"
    assert tracks[0].audio_source_url == "https://www.youtube.com/watch?v=safeword_yt"


@pytest.mark.asyncio
async def test_source_resolver_semaphore_concurrency_limiting():
    """Проверка ограничения параллелизма сопоставления через asyncio.Semaphore."""
    mock_deezer = MagicMock(spec=DeezerClient)
    mock_deezer.parse_url = AsyncMock(return_value=("album", "12345"))
    raw_tracks = [
        {
            "id": i,
            "title": f"Track {i}",
            "duration": 180,
            "track_position": i,
            "contributors": [{"name": "Artist"}],
        }
        for i in range(1, 9)
    ]
    mock_deezer.get_album = AsyncMock(
        return_value={
            "id": 12345,
            "title": "Album With 8 Tracks",
            "release_date": "2024-01-01",
            "tracks": {"data": raw_tracks},
        }
    )

    active_count = 0
    max_active = 0
    lock = asyncio.Lock()

    async def fake_find_best_match(**kwargs):
        nonlocal active_count, max_active
        async with lock:
            active_count += 1
            max_active = max(max_active, active_count)
        await asyncio.sleep(0.02)
        async with lock:
            active_count -= 1
        return "https://youtube.com/watch?v=sample"

    mock_matcher = MagicMock(spec=AudioMatcher)
    mock_matcher.find_best_match = AsyncMock(side_effect=fake_find_best_match)

    resolver = SourceResolver(
        deezer_client=mock_deezer,
        audio_matcher=mock_matcher,
    )
    # Задаем лимит семафора = 3
    resolver._settings.MATCHER_CONCURRENCY = 3

    _, tracks = await resolver.resolve("https://www.deezer.com/album/12345")

    assert len(tracks) == 8
    assert max_active == 3
    assert all(t.status == "pending" for t in tracks)
    assert all(t.audio_source_url == "https://youtube.com/watch?v=sample" for t in tracks)


@pytest.mark.asyncio
async def test_source_resolver_preserves_track_order():
    """Проверка сохранения исходного порядка треков (1 to N) при асинхронной гонке."""
    mock_deezer = MagicMock(spec=DeezerClient)
    mock_deezer.parse_url = AsyncMock(return_value=("album", "99999"))
    raw_tracks = [
        {
            "id": i,
            "title": f"Track #{i}",
            "duration": 180,
            "track_position": i,
            "contributors": [{"name": "Artist"}],
        }
        for i in range(1, 6)
    ]
    mock_deezer.get_album = AsyncMock(
        return_value={
            "id": 99999,
            "title": "Racing Album",
            "release_date": "2024-01-01",
            "tracks": {"data": raw_tracks},
        }
    )

    # Имитируем завершение в обратном порядке: трек 1 спит дольше всех, трек 5 завершается мгновенно
    delays = {1: 0.05, 2: 0.04, 3: 0.03, 4: 0.02, 5: 0.001}

    async def racing_find_best_match(title, **kwargs):
        track_num = int(title.split("#")[-1])
        await asyncio.sleep(delays.get(track_num, 0.01))
        return f"https://youtube.com/watch?v=track_{track_num}"

    mock_matcher = MagicMock(spec=AudioMatcher)
    mock_matcher.find_best_match = AsyncMock(side_effect=racing_find_best_match)

    resolver = SourceResolver(
        deezer_client=mock_deezer,
        audio_matcher=mock_matcher,
    )
    resolver._settings.MATCHER_CONCURRENCY = 5

    _, tracks = await resolver.resolve("https://www.deezer.com/album/99999")

    assert len(tracks) == 5
    assert [t.track_number for t in tracks] == [1, 2, 3, 4, 5]
    assert [t.title for t in tracks] == [f"Track #{i}" for i in range(1, 6)]
    assert [t.audio_source_url for t in tracks] == [
        f"https://youtube.com/watch?v=track_{i}" for i in range(1, 6)
    ]


@pytest.mark.asyncio
async def test_source_resolver_partial_failure_tolerance():
    """Проверка устойчивости к частичным сбоям: сбой одного трека не ломает весь альбом."""
    mock_deezer = MagicMock(spec=DeezerClient)
    mock_deezer.parse_url = AsyncMock(return_value=("album", "77777"))
    raw_tracks = [
        {"id": 1, "title": "Track 1", "duration": 180, "track_position": 1, "contributors": [{"name": "Artist"}]},
        {"id": 2, "title": "Failing Track 2", "duration": 180, "track_position": 2, "contributors": [{"name": "Artist"}]},
        {"id": 3, "title": "Track 3", "duration": 180, "track_position": 3, "contributors": [{"name": "Artist"}]},
    ]
    mock_deezer.get_album = AsyncMock(
        return_value={
            "id": 77777,
            "title": "Partial Failure Album",
            "release_date": "2024-01-01",
            "tracks": {"data": raw_tracks},
        }
    )

    async def partial_match(title, **kwargs):
        if "Failing" in title:
            raise AudioMatchError("No suitable match found for failing track")
        return f"https://youtube.com/watch?v={title.replace(' ', '_')}"

    mock_matcher = MagicMock(spec=AudioMatcher)
    mock_matcher.find_best_match = AsyncMock(side_effect=partial_match)

    resolver = SourceResolver(deezer_client=mock_deezer, audio_matcher=mock_matcher)
    _, tracks = await resolver.resolve("https://www.deezer.com/album/77777")

    assert len(tracks) == 3
    # Треки 1 и 3 успешны
    assert tracks[0].status == "pending"
    assert tracks[0].audio_source_url == "https://youtube.com/watch?v=Track_1"
    assert tracks[0].error is None

    # Трек 2 изолирован в статус error
    assert tracks[1].status == "error"
    assert tracks[1].audio_source_url == ""
    assert "No suitable match found" in (tracks[1].error or "")

    # Трек 3 успешен
    assert tracks[2].status == "pending"
    assert tracks[2].audio_source_url == "https://youtube.com/watch?v=Track_3"
    assert tracks[2].error is None


@pytest.mark.asyncio
async def test_source_resolver_all_tracks_fail_raises():
    """Проверка генерации AudioMatchError, если ни один трек релиза не удалось сопоставить."""
    mock_deezer = MagicMock(spec=DeezerClient)
    mock_deezer.parse_url = AsyncMock(return_value=("album", "88888"))
    raw_tracks = [
        {"id": 1, "title": "Track 1", "duration": 180, "track_position": 1, "contributors": [{"name": "Artist"}]},
        {"id": 2, "title": "Track 2", "duration": 180, "track_position": 2, "contributors": [{"name": "Artist"}]},
    ]
    mock_deezer.get_album = AsyncMock(
        return_value={
            "id": 88888,
            "title": "All Fail Album",
            "release_date": "2024-01-01",
            "tracks": {"data": raw_tracks},
        }
    )

    mock_matcher = MagicMock(spec=AudioMatcher)
    mock_matcher.find_best_match = AsyncMock(side_effect=AudioMatchError("Audio stream missing"))

    resolver = SourceResolver(deezer_client=mock_deezer, audio_matcher=mock_matcher)

    with pytest.raises(AudioMatchError, match="Не удалось сопоставить ни один трек из альбома"):
        await resolver.resolve("https://www.deezer.com/album/88888")


@pytest.mark.asyncio
async def test_audio_matcher_guest_search_priority(tmp_path: Path):
    """Проверка, что поиск в AudioMatcher выполняется с гостевыми опциями (без cookiefile)."""
    cookies_file = tmp_path / "cookies.txt"
    cookies_file.write_text("# Cookies")

    matcher = AudioMatcher(cookies_path=cookies_file)
    assert matcher._ytdl_opts.get("cookiefile") == str(cookies_file)

    with patch("yt_dlp.YoutubeDL") as mock_ydl_cls:
        mock_ydl = MagicMock()
        mock_ydl.__enter__.return_value.extract_info.return_value = {
            "entries": [
                {
                    "id": "match123",
                    "title": "Test Artist - Test Title",
                    "channel": "Test Artist - Topic",
                    "duration": 180,
                }
            ]
        }
        mock_ydl_cls.return_value = mock_ydl

        url = await matcher.find_best_match(
            title="Test Title",
            artist="Test Artist",
            duration=180,
        )

        assert url == "https://www.youtube.com/watch?v=match123"
        # Проверяем, что первый запуск YoutubeDL на поиске не содержал cookiefile
        first_call_opts = mock_ydl_cls.call_args_list[0][0][0]
        assert "cookiefile" not in first_call_opts


def test_normalize_soundcloud_artwork():
    """Проверка нормализации и повышения разрешения обложек SoundCloud."""
    from app.services.source_resolver import normalize_soundcloud_artwork

    assert normalize_soundcloud_artwork(None) is None
    assert normalize_soundcloud_artwork("") is None
    assert (
        normalize_soundcloud_artwork("https://i1.sndcdn.com/artworks-123-large.jpg")
        == "https://i1.sndcdn.com/artworks-123-t500x500.jpg"
    )
    assert (
        normalize_soundcloud_artwork("https://i1.sndcdn.com/artworks-123-badge.png")
        == "https://i1.sndcdn.com/artworks-123-t500x500.png"
    )
    assert (
        normalize_soundcloud_artwork("https://i1.sndcdn.com/artworks-123-t500x500.jpg")
        == "https://i1.sndcdn.com/artworks-123-t500x500.jpg"
    )
    assert (
        normalize_soundcloud_artwork("https://i1.sndcdn.com/artworks-123-original.jpg")
        == "https://i1.sndcdn.com/artworks-123-original.jpg"
    )


@pytest.mark.asyncio
async def test_source_resolver_soundcloud_playlist_rich_metadata():
    """R1, R2: Проверка извлечения реальных названий, артистов, длительности и обложек для SoundCloud плейлиста."""
    mock_entries = [
        {
            "id": "101",
            "title": "Solar Wind",
            "uploader": "Cosmic Sound",
            "artist": "Cosmic Sound",
            "duration": 215.4,
            "thumbnail": "https://i1.sndcdn.com/artworks-solar-large.jpg",
            "url": "https://soundcloud.com/cosmic-sound/solar-wind",
        },
        {
            "id": "102",
            "title": "Lunar Eclipse",
            "uploader": "Astro Beats",
            "artist": "Astro Beats",
            "duration": 182.0,
            "thumbnail": None,  # Должен сработать fallback на обложку плейлиста
            "url": "https://soundcloud.com/astro-beats/lunar-eclipse",
        },
        {
            "id": "103",
            "title": "Starlight",
            "uploader": "Nova",
            "creator": "Nova",
            "duration": 240,
            "thumbnail": "https://i1.sndcdn.com/artworks-star-badge.png",
            "url": "https://soundcloud.com/nova/starlight",
        },
    ]

    mock_playlist_info = {
        "_type": "playlist",
        "title": "Space Odyssey",
        "uploader": "Curator X",
        "thumbnail": "https://i1.sndcdn.com/artworks-playlist-large.jpg",
        "entries": mock_entries,
    }

    resolver = SourceResolver(deezer_client=MagicMock())

    with patch("yt_dlp.YoutubeDL") as mock_ydl_cls:
        mock_ydl = MagicMock()
        mock_ydl.__enter__.return_value.extract_info.return_value = mock_playlist_info
        mock_ydl_cls.return_value = mock_ydl

        album_meta, tracks = await resolver.resolve(
            "https://soundcloud.com/curator-x/sets/space-odyssey",
            source_hint="soundcloud",
        )

        assert album_meta["album"] == "Space Odyssey"
        assert album_meta["album_artist"] == "Curator X"
        assert album_meta["cover_url"] == "https://i1.sndcdn.com/artworks-playlist-t500x500.jpg"

        assert len(tracks) == 3

        # Трек 1: индивидуальные метаданные и обложка
        t1 = tracks[0]
        assert t1.track_id == "101"
        assert t1.track_number == 1
        assert t1.title == "Solar Wind"
        assert t1.artist == "Cosmic Sound"
        assert t1.album == "Space Odyssey"
        assert t1.album_artist == "Curator X"
        assert t1.duration == 215
        assert t1.cover_url == "https://i1.sndcdn.com/artworks-solar-t500x500.jpg"
        assert t1.audio_source_url == "https://soundcloud.com/cosmic-sound/solar-wind"

        # Трек 2: индивидуальный артист, fallback на обложку плейлиста
        t2 = tracks[1]
        assert t2.track_id == "102"
        assert t2.track_number == 2
        assert t2.title == "Lunar Eclipse"
        assert t2.artist == "Astro Beats"
        assert t2.duration == 182
        assert t2.cover_url == "https://i1.sndcdn.com/artworks-playlist-t500x500.jpg"
        assert t2.audio_source_url == "https://soundcloud.com/astro-beats/lunar-eclipse"

        # Трек 3: индивидуальная PNG обложка с нормализацией
        t3 = tracks[2]
        assert t3.track_id == "103"
        assert t3.track_number == 3
        assert t3.title == "Starlight"
        assert t3.artist == "Nova"
        assert t3.duration == 240
        assert t3.cover_url == "https://i1.sndcdn.com/artworks-star-t500x500.png"
        assert t3.audio_source_url == "https://soundcloud.com/nova/starlight"


@pytest.mark.asyncio
async def test_source_resolver_soundcloud_missing_title_url_slug_fallback():
    """R1: Проверка восстановления названия трека из SoundCloud URL-слага при отсутствии title в entry."""
    mock_playlist_info = {
        "_type": "playlist",
        "title": "Electronic Gems",
        "uploader": "Curator",
        "thumbnail": "https://img.com/pl.jpg",
        "entries": [
            {
                "id": "555",
                "title": None,  # Заголовок отсутствует
                "uploader": None,
                "url": "https://soundcloud.com/deadmau5/strobe-club-edit",
            }
        ],
    }

    resolver = SourceResolver(deezer_client=MagicMock())

    with patch("yt_dlp.YoutubeDL") as mock_ydl_cls:
        mock_ydl = MagicMock()
        mock_ydl.__enter__.return_value.extract_info.return_value = mock_playlist_info
        mock_ydl_cls.return_value = mock_ydl

        _, tracks = await resolver.resolve(
            "https://soundcloud.com/curator/sets/electronic-gems",
            source_hint="soundcloud",
        )

        assert len(tracks) == 1
        t = tracks[0]
        # Не должно быть плейсхолдера "Track 1"
        assert t.title != "Track 1"
        assert "Strobe" in t.title
        assert "Deadmau5" in t.artist


@pytest.mark.asyncio
async def test_source_resolver_soundcloud_playlist_artwork_fallback_to_first_track():
    """R2: Если у самого плейлиста нет обложки, используется обложка первого трека."""
    mock_playlist_info = {
        "_type": "playlist",
        "title": "No Cover Playlist",
        "uploader": "Uploader",
        "thumbnail": None,  # Обложки плейлиста нет
        "entries": [
            {
                "id": "1",
                "title": "First Song",
                "uploader": "Artist A",
                "thumbnail": "https://i1.sndcdn.com/artworks-first-large.jpg",
                "url": "https://soundcloud.com/artist-a/first-song",
            },
            {
                "id": "2",
                "title": "Second Song",
                "uploader": "Artist B",
                "thumbnail": None,
                "url": "https://soundcloud.com/artist-b/second-song",
            },
        ],
    }

    resolver = SourceResolver(deezer_client=MagicMock())

    with patch("yt_dlp.YoutubeDL") as mock_ydl_cls:
        mock_ydl = MagicMock()
        mock_ydl.__enter__.return_value.extract_info.return_value = mock_playlist_info
        mock_ydl_cls.return_value = mock_ydl

        album_meta, tracks = await resolver.resolve(
            "https://soundcloud.com/uploader/sets/no-cover",
            source_hint="soundcloud",
        )

        expected_cover = "https://i1.sndcdn.com/artworks-first-t500x500.jpg"
        assert album_meta["cover_url"] == expected_cover
        assert tracks[0].cover_url == expected_cover
        assert tracks[1].cover_url == expected_cover


def test_soundcloud_extractor_patch_batch_missing_tracks():
    """R1, R2: Проверка логики патча SoundcloudPlaylistBaseIE._extract_set при наличии неполных треков."""
    from yt_dlp.extractor.soundcloud import SoundcloudPlaylistBaseIE

    dummy_ydl = MagicMock()
    extractor = SoundcloudPlaylistBaseIE(dummy_ydl)
    extractor._HEADERS = {}
    extractor._API_V2_BASE = "https://api-v2.soundcloud.com/"

    fake_playlist = {
        "id": 9999,
        "title": "Mix 2026",
        "set_type": "playlist",
        "user": {"username": "DJ Shadow"},
        "tracks": [
            {
                "id": 111,
                "title": "Track One",
                "permalink_url": "https://soundcloud.com/dj-shadow/track-one",
                "duration": 180000,
                "artwork_url": "https://i1.sndcdn.com/artworks-111-large.jpg",
                "user": {"username": "DJ Shadow"},
            },
            {
                "id": 222,
                # title и permalink_url отсутствуют
            },
        ],
    }

    fake_fetched_tracks = [
        {
            "id": 222,
            "title": "Track Two Resolved",
            "permalink_url": "https://soundcloud.com/dj-shadow/track-two",
            "duration": 210000,
            "artwork_url": "https://i1.sndcdn.com/artworks-222-large.jpg",
            "user": {"username": "DJ Shadow"},
        }
    ]

    with patch.object(extractor, "_call_api", return_value=fake_fetched_tracks) as mock_api:
        result = extractor._extract_set(fake_playlist)
        assert mock_api.call_count == 1
        call_query = mock_api.call_args[1].get("query", {})
        assert "222" in call_query.get("ids", "")

        entries = result.get("entries", [])
        assert len(entries) == 2
        # Трек 1
        assert entries[0]["title"] == "Track One"
        assert entries[0]["duration"] == 180.0
        assert entries[0]["thumbnail"] == "https://i1.sndcdn.com/artworks-111-t500x500.jpg"
        # Трек 2 (дозагруженный)
        assert entries[1]["title"] == "Track Two Resolved"
        assert entries[1]["duration"] == 210.0
        assert entries[1]["thumbnail"] == "https://i1.sndcdn.com/artworks-222-t500x500.jpg"


def test_normalize_soundcloud_artwork_query_params():
    """Проверка повышения качества обложки SoundCloud при наличии query параметров."""
    from app.services.source_resolver import normalize_soundcloud_artwork

    url_query = "https://i1.sndcdn.com/artworks-12345-large.jpg?version=123"
    assert (
        normalize_soundcloud_artwork(url_query)
        == "https://i1.sndcdn.com/artworks-12345-t500x500.jpg?version=123"
    )

    badge_query = "https://i1.sndcdn.com/avatars-67890-badge.png?token=xyz&w=50"
    assert (
        normalize_soundcloud_artwork(badge_query)
        == "https://i1.sndcdn.com/avatars-67890-t500x500.png?token=xyz&w=50"
    )


def test_soundcloud_extractor_patch_dict_collection_schema():
    """Проверка поддержки SoundCloud API схемы, возвращающей словарь с ключом 'collection'."""
    from yt_dlp.extractor.soundcloud import SoundcloudPlaylistBaseIE

    dummy_ydl = MagicMock()
    extractor = SoundcloudPlaylistBaseIE(dummy_ydl)
    extractor._HEADERS = {}
    extractor._API_V2_BASE = "https://api-v2.soundcloud.com/"

    fake_playlist = {
        "id": 8888,
        "title": "Dict Schema Playlist",
        "set_type": "playlist",
        "tracks": [{"id": 333}],
    }

    # API возвращает словарь с collection
    api_response = {
        "collection": [
            {
                "id": 333,
                "title": "Track Inside Collection",
                "permalink_url": "https://soundcloud.com/artist/track-inside-collection",
                "duration": 195000,
                "artwork_url": "https://i1.sndcdn.com/artworks-333-large.jpg",
                "user": {"username": "Artist"},
            }
        ]
    }

    with patch.object(extractor, "_call_api", return_value=api_response):
        result = extractor._extract_set(fake_playlist)
        entries = result.get("entries", [])
        assert len(entries) == 1
        assert entries[0]["title"] == "Track Inside Collection"
        assert entries[0]["duration"] == 195.0
        assert entries[0]["thumbnail"] == "https://i1.sndcdn.com/artworks-333-t500x500.jpg"


def test_soundcloud_extractor_patch_partial_failure_continues():
    """Проверка, что сбой в одной из пачек (batch) не прерывает обработку последующих."""
    from yt_dlp.extractor.soundcloud import SoundcloudPlaylistBaseIE

    dummy_ydl = MagicMock()
    extractor = SoundcloudPlaylistBaseIE(dummy_ydl)
    extractor._HEADERS = {}
    extractor._API_V2_BASE = "https://api-v2.soundcloud.com/"

    # Создаем 60 треков, требующих 2 пачки (50 + 10)
    fake_playlist = {
        "id": 7777,
        "title": "Multi-batch Playlist",
        "tracks": [{"id": i} for i in range(1, 61)],
    }

    call_count = 0

    def mock_call_api(endpoint, playlist_id, note, query=None, headers=None):
        nonlocal call_count
        call_count += 1
        if call_count == 1:
            raise RuntimeError("Network glitch on batch 1")
        # Вторая пачка (треки 51-60) успешна
        return [
            {
                "id": i,
                "title": f"Track {i}",
                "permalink_url": f"https://soundcloud.com/artist/track-{i}",
                "duration": 180000,
                "user": {"username": "Artist"},
            }
            for i in range(51, 61)
        ]

    with patch.object(extractor, "_call_api", side_effect=mock_call_api):
        result = extractor._extract_set(fake_playlist)
        assert call_count == 2
        entries = result.get("entries", [])
        assert len(entries) == 60
        # Трек 55 из второй пачки успешно получил название
        assert entries[54]["title"] == "Track 55"


def test_soundcloud_extractor_patch_secret_token_preserved():
    """Проверка корректного прикрепления secret_token для приватных плейлистов/треков."""
    from yt_dlp.extractor.soundcloud import SoundcloudPlaylistBaseIE

    dummy_ydl = MagicMock()
    extractor = SoundcloudPlaylistBaseIE(dummy_ydl)
    extractor._HEADERS = {}
    extractor._API_V2_BASE = "https://api-v2.soundcloud.com/"

    fake_playlist = {
        "id": 5555,
        "title": "Secret Playlist",
        "tracks": [
            {
                "id": 501,
                "title": "Secret Track 1",
                "permalink_url": "https://soundcloud.com/vip/secret-track-1",
                "secret_token": "s-trackSecret1",
            },
            {
                "id": 502,
                # Без permalink_url
                "title": "Secret Track 2",
            },
        ],
    }

    result = extractor._extract_set(fake_playlist, token="s-playlistSecret")
    entries = result.get("entries", [])
    assert len(entries) == 2

    # Трек 1: secret_token трека должен быть в url
    assert "secret_token=s-trackSecret1" in entries[0]["url"]

    # Трек 2: secret_token плейлиста должен быть в API url
    assert "secret_token=s-playlistSecret" in entries[1]["url"]
    assert "https://api-v2.soundcloud.com/tracks/502" in entries[1]["url"]


@pytest.mark.asyncio
async def test_source_resolver_soundcloud_slug_fallback_private_track_and_api_url():
    """R1: Проверка, что secret token не принимается за название трека, а API URL не дает артиста 'Tracks'."""
    resolver = SourceResolver(deezer_client=MagicMock())

    mock_playlist_info = {
        "_type": "playlist",
        "title": "Secret Set",
        "uploader": "Curator",
        "thumbnail": None,
        "entries": [
            {
                "id": "901",
                "title": None,  # title отсутствует
                "artist": None,
                "uploader": None,
                # Приватный трек со secret_token в URL
                "url": "https://soundcloud.com/m83/midnight-city/s-abc12345",
            },
            {
                "id": "902",
                "title": None,
                "artist": None,
                "uploader": None,
                # API URL без веб-слага
                "url": "https://api-v2.soundcloud.com/tracks/902",
            },
        ],
    }

    with patch("yt_dlp.YoutubeDL") as mock_ydl_cls:
        mock_ydl = MagicMock()
        mock_ydl.__enter__.return_value.extract_info.return_value = mock_playlist_info
        mock_ydl_cls.return_value = mock_ydl

        _album_meta, tracks = await resolver.resolve(
            "https://soundcloud.com/curator/sets/secret-set/s-playlistSecret",
            source_hint="soundcloud",
        )

        assert len(tracks) == 2

        # Трек 1: Название должно быть 'Midnight City', а не 'S Abc12345'!
        t1 = tracks[0]
        assert t1.artist == "M83"
        assert t1.title == "Midnight City"

        # Трек 2: Артист НЕ должен быть 'Tracks', а название НЕ должно быть '902'!
        t2 = tracks[1]
        assert t2.artist != "Tracks"
        assert t2.artist == "Curator"  # fallback на album_artist
        assert t2.title != "902"
        assert t2.title == "Track 2"  # fallback на номер трека


@pytest.mark.asyncio
async def test_source_resolver_soundcloud_mobile_shortlink_detection_and_slug_safety():
    """R1: Проверка поддержки shortlink on.soundcloud.com и защиты от парсинга хэша короткой ссылки как исполнителя."""
    resolver = SourceResolver(deezer_client=MagicMock())

    # 1. Проверка обнаружения источника
    assert resolver.detect_source("https://on.soundcloud.com/abc123XYZ") == "soundcloud"
    assert resolver.detect_source("on.soundcloud.com/abc123XYZ") == "soundcloud"

    # 2. Проверка, что хэш короткой ссылки в URL трека не принимается за имя артиста или название
    mock_playlist_info = {
        "_type": "playlist",
        "title": "Mobile Share Set",
        "uploader": "UploaderChannel",
        "entries": [
            {
                "id": "701",
                "title": None,
                "uploader": None,
                "url": "https://on.soundcloud.com/k9x8Z",
            },
            {
                "id": "702",
                "title": "   ",  # whitespace title
                "uploader": "   ",  # whitespace uploader
                "url": "https://on.soundcloud.com/short/token",
            },
        ],
    }

    with patch("yt_dlp.YoutubeDL") as mock_ydl_cls:
        mock_ydl = MagicMock()
        mock_ydl.__enter__.return_value.extract_info.return_value = mock_playlist_info
        mock_ydl_cls.return_value = mock_ydl

        _album_meta, tracks = await resolver.resolve(
            "https://on.soundcloud.com/k9x8Z",
            source_hint="soundcloud",
        )

        assert len(tracks) == 2
        # Трек 1: Хэш 'k9x8Z' НЕ должен стать артистом или названием
        assert tracks[0].artist == "UploaderChannel"
        assert tracks[0].title == "Track 1"

        # Трек 2: 'short' и 'token' НЕ должны стать артистом и названием
        assert tracks[1].artist == "UploaderChannel"
        assert tracks[1].title == "Track 2"


def test_safe_int_duration_thorough():
    """R1: Проверка надежности safe_int_duration для всех граничных значений."""
    from app.services.source_resolver import safe_int_duration

    assert safe_int_duration(None) == 0
    assert safe_int_duration(0) == 0
    assert safe_int_duration(0.0) == 0
    assert safe_int_duration(180) == 180
    assert safe_int_duration(180.4) == 180
    assert safe_int_duration(180.6) == 181
    assert safe_int_duration("215") == 215
    assert safe_int_duration("215.8") == 216
    assert safe_int_duration("") == 0
    assert safe_int_duration("N/A") == 0
    assert safe_int_duration("-5") == 0
    assert safe_int_duration(-10) == 0
    assert safe_int_duration(-10.5) == 0
    assert safe_int_duration(float("nan")) == 0
    assert safe_int_duration(float("inf")) == 0
    assert safe_int_duration(float("-inf")) == 0
    assert safe_int_duration([]) == 0
    assert safe_int_duration({}) == 0


@pytest.mark.asyncio
async def test_source_resolver_deezer_handles_null_duration():
    """R1: Проверка корректной обработки None duration в Deezer данных без падения."""
    mock_deezer = MagicMock()
    mock_deezer.parse_url = AsyncMock(return_value=("track", "12345"))
    mock_deezer.get_track = AsyncMock(
        return_value={
            "id": 12345,
            "title": "Null Duration Track",
            "duration": None,  # None в API
            "artist": {"name": "Test Artist"},
            "album": {"title": "Test Album"},
        }
    )
    mock_deezer.format_artists = MagicMock(return_value="Test Artist")
    mock_deezer.extract_best_cover_url = MagicMock(return_value="https://img.com/cover.jpg")

    mock_matcher = MagicMock()
    mock_matcher.find_best_match = AsyncMock(return_value="https://www.youtube.com/watch?v=abc")

    resolver = SourceResolver(deezer_client=mock_deezer, audio_matcher=mock_matcher)
    _meta, tracks = await resolver.resolve("https://www.deezer.com/track/12345", source_hint="deezer")
    assert len(tracks) == 1
    assert tracks[0].duration == 0


def test_soundcloud_extractor_patch_raw_integer_track_ids():
    """R1: Патч SoundCloud должен поддерживать списки tracks, содержащие сырые int ID."""
    from yt_dlp.extractor.soundcloud import SoundcloudPlaylistBaseIE

    dummy_ydl = MagicMock()
    extractor = SoundcloudPlaylistBaseIE(dummy_ydl)
    extractor._HEADERS = {}
    extractor._API_V2_BASE = "https://api-v2.soundcloud.com/"

    fake_playlist = {
        "id": 4444,
        "title": "Int IDs Playlist",
        "set_type": "playlist",
        "tracks": [801, 802],  # Сырые целые числа
    }

    fetched_payload = [
        {
            "id": 801,
            "title": "Track 801",
            "permalink_url": "https://soundcloud.com/artist/track-801",
            "duration": 150000,
            "user": {"username": "Artist"},
        },
        {
            "id": 802,
            "title": "Track 802",
            "permalink_url": "https://soundcloud.com/artist/track-802",
            "duration": 200000,
            "user": {"username": "Artist"},
        },
    ]

    with patch.object(extractor, "_call_api", return_value=fetched_payload) as mock_api:
        result = extractor._extract_set(fake_playlist)
        assert mock_api.call_count == 1
        call_ids = mock_api.call_args[1].get("query", {}).get("ids", "")
        assert "801" in call_ids and "802" in call_ids
        entries = result.get("entries", [])
        assert len(entries) == 2
        assert entries[0]["title"] == "Track 801"
        assert entries[1]["title"] == "Track 802"


@pytest.mark.asyncio
async def test_source_resolver_soundcloud_playlist_title_artist_split():
    """R1: Извлечение реального исполнителя и названия из 'Artist - Title' и защита от подстановки playlist uploader."""
    resolver = SourceResolver(deezer_client=MagicMock())

    mock_playlist_info = {
        "_type": "playlist",
        "title": "Summer Vibes 2026",
        "uploader": "Curator Channel",  # Generic playlist uploader
        "thumbnail": "https://i1.sndcdn.com/artworks-playlist-large.jpg",
        "entries": [
            {
                # Трек 1: Артист не указан, название 'Artist - Title' -> должен извлечься реальный артист
                "id": "1001",
                "title": "Daft Punk - One More Time",
                "artist": None,
                "uploader": "Curator Channel",
                "duration": 320,
                "url": "https://soundcloud.com/curator/daft-punk-one-more-time",
            },
            {
                # Трек 2: Артист указан, но дублируется в названии с en-dash ' – ' -> префикс должен удалиться
                "id": "1002",
                "title": "Avicii – Levels",
                "artist": "Avicii",
                "uploader": "Avicii",
                "duration": 199,
                "url": "https://soundcloud.com/avicii/levels",
            },
            {
                # Трек 3: Название placeholder 'Track 3', артист не указан -> должен извлечься из slug URL
                "id": "1003",
                "title": "Track 3",
                "artist": None,
                "uploader": "Curator Channel",
                "duration": 240,
                "url": "https://soundcloud.com/pendulum/witchcraft",
            },
        ],
    }

    with patch("yt_dlp.YoutubeDL") as mock_ydl_cls:
        mock_ydl = MagicMock()
        mock_ydl.__enter__.return_value.extract_info.return_value = mock_playlist_info
        mock_ydl_cls.return_value = mock_ydl

        _album_meta, tracks = await resolver.resolve(
            "https://soundcloud.com/curator/sets/summer-vibes-2026",
            source_hint="soundcloud",
        )

        assert len(tracks) == 3

        # Трек 1: Артист Daft Punk, название One More Time (НЕ Curator Channel и НЕ Daft Punk - One More Time)
        assert tracks[0].artist == "Daft Punk"
        assert tracks[0].title == "One More Time"

        # Трек 2: Дубликат удален
        assert tracks[1].artist == "Avicii"
        assert tracks[1].title == "Levels"

        # Трек 3: Placeholder заменен на реальное название из slug
        assert tracks[2].artist == "Pendulum"
        assert tracks[2].title == "Witchcraft"


@pytest.mark.asyncio
async def test_source_resolver_soundcloud_single_track_artist_title_split():
    """R1: Извлечение исполнителя и названия из 'Artist — Title' для одиночного трека SoundCloud."""
    resolver = SourceResolver(deezer_client=MagicMock())

    mock_track_info = {
        "_type": "video",
        "id": "2001",
        "title": "Justice — Genesis",
        "artist": None,
        "uploader": "Random Uploader",
        "duration": 234,
        "thumbnail": "https://i1.sndcdn.com/artworks-genesis-large.jpg",
        "webpage_url": "https://soundcloud.com/random/justice-genesis",
    }

    with patch("yt_dlp.YoutubeDL") as mock_ydl_cls:
        mock_ydl = MagicMock()
        mock_ydl.__enter__.return_value.extract_info.return_value = mock_track_info
        mock_ydl_cls.return_value = mock_ydl

        _album_meta, tracks = await resolver.resolve(
            "https://soundcloud.com/random/justice-genesis",
            source_hint="soundcloud",
        )

        assert len(tracks) == 1
        assert tracks[0].artist == "Justice"
        assert tracks[0].title == "Genesis"


@pytest.mark.asyncio
async def test_resolve_yandex_track():
    """Проверка полного разрешения одиночного трека Яндекс Музыки через SourceResolver."""
    mock_yandex = AsyncMock()
    mock_yandex.parse_url.return_value = ("track", {"track_id": "12345"})
    mock_yandex.get_track.return_value = {
        "id": 12345,
        "title": "Звезда по имени Солнце",
        "artists": [{"name": "Кино"}],
        "durationMs": 225000,
        "albums": [{"title": "Звезда по имени Солнце", "year": 1989}],
        "ogImage": "avatars.yandex.net/get-music-content/1/2/%%",
    }
    mock_yandex.format_artists.return_value = "Кино"
    mock_yandex.format_cover.return_value = "https://avatars.yandex.net/get-music-content/1/2/1000x1000"
    mock_yandex.get_direct_download_url.return_value = None

    mock_matcher = AsyncMock()
    mock_matcher.find_best_match.return_value = "https://www.youtube.com/watch?v=kino123"

    resolver = SourceResolver(
        deezer_client=MagicMock(),
        audio_matcher=mock_matcher,
        yandex_client=mock_yandex,
    )

    meta, tracks = await resolver.resolve("https://music.yandex.ru/track/12345")
    assert meta["album"] == "Звезда по имени Солнце"
    assert len(tracks) == 1
    assert tracks[0].title == "Звезда по имени Солнце"
    assert tracks[0].artist == "Кино"
    assert tracks[0].year == 1989
    assert tracks[0].audio_source_url == "https://www.youtube.com/watch?v=kino123"


@pytest.mark.asyncio
async def test_resolve_spotify_playlist():
    """Проверка полного разрешения плейлиста Spotify через SourceResolver."""
    mock_spotify = AsyncMock()
    mock_spotify.parse_url.return_value = ("playlist", "pl123")
    mock_spotify.get_playlist.return_value = {
        "id": "pl123",
        "title": "Rock Classics",
        "artist": "Spotify",
        "cover_url": "https://i.scdn.co/image/cover640",
        "tracks": [
            {
                "id": "t1",
                "track_number": 1,
                "title": "Back In Black",
                "artist": "AC/DC",
                "duration": 255,
                "album": "Back In Black",
                "year": 1980,
            }
        ],
    }

    mock_matcher = AsyncMock()
    mock_matcher.find_best_match.return_value = "https://www.youtube.com/watch?v=acdc123"

    resolver = SourceResolver(
        deezer_client=MagicMock(),
        audio_matcher=mock_matcher,
        spotify_client=mock_spotify,
    )

    meta, tracks = await resolver.resolve("https://open.spotify.com/playlist/pl123")
    assert meta["album"] == "Rock Classics"
    assert len(tracks) == 1
    assert tracks[0].title == "Back In Black"
    assert tracks[0].artist == "AC/DC"
    assert tracks[0].audio_source_url == "https://www.youtube.com/watch?v=acdc123"


@pytest.mark.asyncio
async def test_resolve_apple_music_album():
    """Проверка полного разрешения альбома Apple Music через SourceResolver."""
    mock_apple = MagicMock()
    mock_apple.parse_url.return_value = ("album", {"album_id": "697194953"})
    mock_apple.get_collection = AsyncMock(
        return_value={
            "title": "Discovery",
            "artist": "Daft Punk",
            "year": 2001,
            "cover_url": "https://is1-ssl.mzstatic.com/image/1400x1400bb.jpg",
            "tracks": [
                {
                    "id": "t1",
                    "track_number": 1,
                    "title": "One More Time",
                    "artist": "Daft Punk",
                    "duration": 320,
                    "album": "Discovery",
                    "year": 2001,
                }
            ],
        }
    )

    mock_matcher = AsyncMock()
    mock_matcher.find_best_match.return_value = "https://www.youtube.com/watch?v=daft123"

    resolver = SourceResolver(
        deezer_client=MagicMock(),
        audio_matcher=mock_matcher,
        apple_music_client=mock_apple,
    )

    meta, tracks = await resolver.resolve("https://music.apple.com/us/album/discovery/697194953")
    assert meta["album"] == "Discovery"
    assert len(tracks) == 1
    assert tracks[0].title == "One More Time"
    assert tracks[0].artist == "Daft Punk"
    assert tracks[0].audio_source_url == "https://www.youtube.com/watch?v=daft123"


@pytest.mark.asyncio
async def test_resolve_artist_discography_spotify_success():
    """Проверка успешного разрешения дискографии артиста Spotify."""
    mock_spotify = AsyncMock()
    mock_spotify.parse_url.return_value = ("artist", "art_123")
    mock_spotify.get_artist_discography.return_value = {
        "artist": {
            "name": "Daft Punk",
            "images": [{"url": "https://img.com/dp_avatar.jpg"}],
        },
        "items": [
            {
                "id": "alb_dp_1",
                "name": "Discovery",
                "album_type": "album",
                "release_date": "2001",
                "total_tracks": 14,
                "images": [{"url": "https://img.com/discovery.jpg"}],
                "external_urls": {"spotify": "https://open.spotify.com/album/alb_dp_1"},
            }
        ],
    }

    resolver = SourceResolver(
        deezer_client=MagicMock(),
        spotify_client=mock_spotify,
    )

    res = await resolver.resolve_artist_discography("https://open.spotify.com/artist/art_123")
    assert res.artist_name == "Daft Punk"
    assert res.artist_avatar == "https://img.com/dp_avatar.jpg"
    assert res.source == "spotify"
    assert res.total_releases == 1
    assert res.releases[0].id == "alb_dp_1"
    assert res.releases[0].title == "Discovery"
    assert res.releases[0].year == 2001
    assert res.releases[0].url == "https://open.spotify.com/album/alb_dp_1"


@pytest.mark.asyncio
async def test_resolve_artist_discography_spotify_deezer_fallback():
    """Проверка fallback на Deezer при пустом списке релизов Spotify."""
    mock_spotify = AsyncMock()
    mock_spotify.parse_url.return_value = ("artist", "art_empty")
    mock_spotify.get_artist_discography.return_value = {
        "artist": {
            "name": "Eminem",
            "images": [],
        },
        "items": [],
    }

    mock_deezer = AsyncMock()
    mock_deezer._request_api.return_value = {
        "data": [
            {
                "id": 13,
                "name": "Eminem",
                "picture_xl": "https://img.com/eminem_xl.jpg",
                "nb_album": 68,
            }
        ]
    }
    mock_deezer.get_artist_albums.return_value = [
        {
            "id": 12345,
            "title": "The Marshall Mathers LP",
            "record_type": "album",
            "release_date": "2000-05-23",
            "nb_tracks": 18,
            "cover_xl": "https://img.com/mmlp.jpg",
            "link": "https://www.deezer.com/album/12345",
        }
    ]

    resolver = SourceResolver(
        deezer_client=mock_deezer,
        spotify_client=mock_spotify,
    )

    res = await resolver.resolve_artist_discography("https://open.spotify.com/artist/art_empty")
    assert res.artist_name == "Eminem"
    assert res.artist_avatar == "https://img.com/eminem_xl.jpg"
    assert res.source == "spotify"
    assert res.total_releases == 1
    assert res.releases[0].id == "12345"
    assert res.releases[0].title == "The Marshall Mathers LP"
    assert res.releases[0].year == 2000
    assert res.releases[0].url == "https://www.deezer.com/album/12345"







