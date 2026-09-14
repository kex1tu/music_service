"""Тесты аудита медиатеки (GET /files/audit/*, POST /files/audit/*)."""

from pathlib import Path
from unittest.mock import AsyncMock, MagicMock

import pytest
from fastapi.testclient import TestClient

from app.core.sandbox import PathSandbox
from app.dependencies import get_download_service, get_library_audit_service
from app.schemas.lyrics import LyricsSearchResult
from app.schemas.tag import TrackTags
from app.services.download_service import DownloadJob
from app.services.library_audit_service import (
    LibraryAuditService,
    normalize_clean_string,
)


def test_normalize_clean_string():
    """Проверка очистки названий и авторов для ключа сопоставления дубликатов."""
    assert normalize_clean_string("Daft Punk feat. Romanthony") == "daft punk"
    assert normalize_clean_string("Song Title (Feat. Drake & Kanye)") == "song title"
    assert normalize_clean_string("Track 01 - Rock & Roll!") == "track 01 rock roll"


@pytest.fixture
def populated_audit_service(tmp_path: Path):
    """Создает сервис аудита с тестовым набором для проверки всех видов нарушений."""
    from app.core.sandbox import PathSandbox
    from app.services.tag_service import TagService

    sandbox = PathSandbox(tmp_path)
    tag_svc = TagService(music_sandbox=sandbox)
    audit_svc = LibraryAuditService(music_sandbox=sandbox, tag_service=tag_svc)

    audit_svc._cache = {
        # 1. Трек без слов и без обложки
        "AlbumA/01. Track Without Lyrics.mp3": {
            "mtime": 1000.0,
            "size": 5000000,
            "track": TrackTags(
                path="AlbumA/01. Track Without Lyrics.mp3",
                track_number=1,
                title="Track Without Lyrics",
                artist="Artist A",
                album="AlbumA",
                album_artist="Artist A",
                duration=180,
                has_cover=False,
                lyrics_status="none",
            ),
        },
        # 2. Трек с текстом .lrc и обложкой
        "AlbumB/01. Perfect Track.mp3": {
            "mtime": 1000.0,
            "size": 6000000,
            "track": TrackTags(
                path="AlbumB/01. Perfect Track.mp3",
                track_number=1,
                title="Perfect Track",
                artist="Artist B",
                album="AlbumB",
                album_artist="Artist B",
                duration=200,
                has_cover=True,
                lyrics_status="lrc",
            ),
        },
        # 3. Дубликат трека: Тот же исполнитель и песня в другой папке с той же длительностью
        "Singles/Artist A - Track Without Lyrics (Radio Edit).mp3": {
            "mtime": 1000.0,
            "size": 5000100,
            "track": TrackTags(
                path="Singles/Artist A - Track Without Lyrics (Radio Edit).mp3",
                track_number=1,
                title="Track Without Lyrics",
                artist="Artist A",
                album="Singles",
                album_artist="Artist A",
                duration=181,  # ±1 секунда разницы
                has_cover=False,
                lyrics_status="none",
            ),
        },
    }
    audit_svc.ensure_index = AsyncMock()
    return audit_svc


def test_audit_unauthorized(client: TestClient):
    """Проверка запрета доступа к эндпоинтам аудита без авторизации."""
    assert client.get("/files/audit/summary").status_code == 401
    assert client.get("/files/audit/missing-lyrics").status_code == 401
    assert client.post("/files/audit/missing-lyrics/fetch-all").status_code == 401
    assert client.get("/files/audit/missing-covers").status_code == 401
    assert client.get("/files/audit/duplicates").status_code == 401


def test_audit_summary_and_missing_lyrics(authenticated_client: TestClient, populated_audit_service):
    """Проверка сводки аудита и списка треков без текстов."""
    authenticated_client.app.dependency_overrides[get_library_audit_service] = lambda: populated_audit_service
    try:
        # 1. Сводка
        summary_resp = authenticated_client.get("/files/audit/summary")
        assert summary_resp.status_code == 200
        s_data = summary_resp.json()
        assert s_data["total_tracks"] == 3
        assert s_data["missing_lyrics_count"] == 2
        assert s_data["missing_covers_count"] >= 1
        assert s_data["duplicates_count"] >= 1

        # 2. Список недостающих текстов
        lyrics_resp = authenticated_client.get("/files/audit/missing-lyrics")
        assert lyrics_resp.status_code == 200
        missing_lyrics = lyrics_resp.json()
        assert len(missing_lyrics) == 2
        assert any(item["title"] == "Track Without Lyrics" for item in missing_lyrics)
    finally:
        authenticated_client.app.dependency_overrides.pop(get_library_audit_service, None)


def test_audit_missing_covers(authenticated_client: TestClient, populated_audit_service):
    """Проверка обнаружения релизов без графических обложек."""
    authenticated_client.app.dependency_overrides[get_library_audit_service] = lambda: populated_audit_service
    try:
        resp = authenticated_client.get("/files/audit/missing-covers")
        assert resp.status_code == 200
        missing_covers = resp.json()
        assert len(missing_covers) >= 1
        assert any(c["artist"] == "Artist A" for c in missing_covers)
    finally:
        authenticated_client.app.dependency_overrides.pop(get_library_audit_service, None)


def test_audit_duplicates_detection(authenticated_client: TestClient, populated_audit_service):
    """Проверка сопоставления и возврата дубликатов."""
    authenticated_client.app.dependency_overrides[get_library_audit_service] = lambda: populated_audit_service
    try:
        resp = authenticated_client.get("/files/audit/duplicates")
        assert resp.status_code == 200
        dup_data = resp.json()
        assert dup_data["total_duplicate_files"] >= 1
        assert len(dup_data["groups"]) >= 1
        group = dup_data["groups"][0]
        assert group["artist"] == "Artist A"
        assert len(group["tracks"]) == 2
    finally:
        authenticated_client.app.dependency_overrides.pop(get_library_audit_service, None)


def test_audit_batch_lyrics_fetch(authenticated_client: TestClient, populated_audit_service):
    """Проверка запуска пакетного поиска текстов и опроса прогресса."""
    mock_lyrics_service = MagicMock()
    mock_lyrics_service.search = AsyncMock(
        return_value=LyricsSearchResult(
            source="lrclib",
            lyrics="[00:01.00] La la la",
            synced=True,
        )
    )
    mock_lyrics_service.save_lyrics = MagicMock()
    populated_audit_service._lyrics_service = mock_lyrics_service

    authenticated_client.app.dependency_overrides[get_library_audit_service] = lambda: populated_audit_service
    try:
        # 1. Запуск сбора
        start_resp = authenticated_client.post("/files/audit/missing-lyrics/fetch-all")
        assert start_resp.status_code == 200
        prog_data = start_resp.json()
        assert prog_data["is_running"] is True
        assert prog_data["total"] == 2

        # 2. Опрос прогресса
        check_resp = authenticated_client.get("/files/audit/missing-lyrics/progress")
        assert check_resp.status_code == 200
        assert "is_running" in check_resp.json()
    finally:
        authenticated_client.app.dependency_overrides.pop(get_library_audit_service, None)


@pytest.mark.asyncio
async def test_incomplete_albums_detection(tmp_path: Path):
    """Проверка обнаружения неполных альбомов (по track_total и по разрывам нумерации)."""
    from app.core.sandbox import PathSandbox
    from app.services.tag_service import TagService

    sandbox = PathSandbox(tmp_path)
    tag_svc = TagService(music_sandbox=sandbox)
    audit_svc = LibraryAuditService(music_sandbox=sandbox, tag_service=tag_svc)

    # 1. Альбом с явным TRCK=14, но только 13 треками (пропущен трек 8)
    cache = {}
    for i in range(1, 15):
        if i == 8:
            continue  # Пропускаем трек 8
        cache[f"Kendrick Lamar/DAMN/{i:02d}. Track.mp3"] = {
            "mtime": 1000.0,
            "size": 5000000,
            "track": TrackTags(
                path=f"Kendrick Lamar/DAMN/{i:02d}. Track.mp3",
                track_number=i,
                track_total=14,
                title=f"Track {i}",
                artist="Kendrick Lamar",
                album="DAMN.",
                album_artist="Kendrick Lamar",
                duration=180,
            ),
        }

    # 2. Альбом без track_total, но с разрывом [1, 2, 4] (пропущен трек 3)
    for i in [1, 2, 4]:
        cache[f"Radiohead/In Rainbows/{i:02d}. Song.mp3"] = {
            "mtime": 1000.0,
            "size": 5000000,
            "track": TrackTags(
                path=f"Radiohead/In Rainbows/{i:02d}. Song.mp3",
                track_number=i,
                track_total=None,
                title=f"Song {i}",
                artist="Radiohead",
                album="In Rainbows",
                album_artist="Radiohead",
                duration=200,
            ),
        }

    # 3. Полный сингл (1 трек, track_number=1, без track_total) — НЕ должен считаться неполным
    cache["Singles/Artist - Hit.mp3"] = {
        "mtime": 1000.0,
        "size": 4000000,
        "track": TrackTags(
            path="Singles/Artist - Hit.mp3",
            track_number=1,
            track_total=None,
            title="Hit",
            artist="Artist",
            album="Singles",
            album_artist="Artist",
            duration=150,
        ),
    }

    # 4. Полный альбом (3 трека из 3, track_total=3) — НЕ должен считаться неполным
    for i in [1, 2, 3]:
        cache[f"Artist/Complete EP/{i:02d}. Song.mp3"] = {
            "mtime": 1000.0,
            "size": 4000000,
            "track": TrackTags(
                path=f"Artist/Complete EP/{i:02d}. Song.mp3",
                track_number=i,
                track_total=3,
                title=f"Song {i}",
                artist="Artist",
                album="Complete EP",
                album_artist="Artist",
                duration=150,
            ),
        }

    audit_svc._cache = cache
    audit_svc.ensure_index = AsyncMock()

    result = await audit_svc.get_incomplete_albums()
    assert result.total_incomplete_albums == 2
    assert result.total_missing_tracks == 2

    # Проверяем альбом Kendrick Lamar / DAMN.
    damn_album = next((a for a in result.albums if a.album == "DAMN."), None)
    assert damn_album is not None
    assert damn_album.present_count == 13
    assert damn_album.expected_count == 14
    assert damn_album.missing_track_numbers == [8]
    assert len(damn_album.missing_tracks) == 1
    assert damn_album.missing_tracks[0].track_number == 8

    # Проверяем альбом Radiohead / In Rainbows
    rh_album = next((a for a in result.albums if a.album == "In Rainbows"), None)
    assert rh_album is not None
    assert rh_album.present_count == 3
    assert rh_album.expected_count == 4
    assert rh_album.missing_track_numbers == [3]


@pytest.mark.asyncio
async def test_match_album_catalog_and_download_endpoint(
    authenticated_client: TestClient,
    tmp_path: Path,
):
    """Проверка сверки неполного альбома с каталогом Deezer и эндпоинта дозагрузки."""
    from app.core.sandbox import PathSandbox
    from app.dependencies import get_download_service, get_library_audit_service
    from app.schemas.download import JobStatus
    from app.services.download_service import DownloadJob
    from app.services.tag_service import TagService

    sandbox = PathSandbox(tmp_path)
    album_dir = tmp_path / "Kendrick Lamar" / "DAMN"
    album_dir.mkdir(parents=True, exist_ok=True)

    tag_svc = TagService(music_sandbox=sandbox)
    mock_deezer = MagicMock()

    mock_deezer.search_albums = AsyncMock(
        return_value=[
            {
                "id": 302127,
                "title": "DAMN.",
                "artist": {"name": "Kendrick Lamar"},
            }
        ]
    )
    mock_deezer.get_album = AsyncMock(
        return_value={
            "id": 302127,
            "title": "DAMN.",
            "artist": {"name": "Kendrick Lamar"},
            "nb_tracks": 2,
            "tracks": {
                "data": [
                    {
                        "id": 361204701,
                        "title": "BLOOD.",
                        "track_position": 1,
                        "disk_number": 1,
                        "duration": 118,
                    },
                    {
                        "id": 361204781,
                        "title": "PRIDE.",
                        "track_position": 2,
                        "disk_number": 1,
                        "duration": 275,
                    },
                ]
            },
        }
    )

    audit_svc = LibraryAuditService(
        music_sandbox=sandbox,
        tag_service=tag_svc,
        deezer_client=mock_deezer,
    )
    # В медиатеке присутствует только трек 1 (BLOOD.)
    audit_svc._cache = {
        "Kendrick Lamar/DAMN/01 - BLOOD..mp3": {
            "mtime": 1000.0,
            "size": 3000000,
            "track": TrackTags(
                path="Kendrick Lamar/DAMN/01 - BLOOD..mp3",
                track_number=1,
                track_total=2,
                title="BLOOD.",
                artist="Kendrick Lamar",
                album="DAMN.",
                album_artist="Kendrick Lamar",
                duration=118,
            ),
        }
    }
    audit_svc.ensure_index = AsyncMock()

    # Мокаем DownloadService
    mock_download_service = MagicMock()
    mock_job = DownloadJob(
        job_id="job123",
        url="https://www.deezer.com/track/361204781",
        source="deezer",
    )
    mock_job.status = JobStatus.QUEUED
    mock_download_service.create_job = AsyncMock(return_value=mock_job)

    authenticated_client.app.dependency_overrides[get_library_audit_service] = lambda: audit_svc
    authenticated_client.app.dependency_overrides[get_download_service] = lambda: mock_download_service
    try:
        # 1. Запрос списка неполных релизов
        resp = authenticated_client.get("/files/audit/incomplete-albums")
        assert resp.status_code == 200
        data = resp.json()
        assert data["total_incomplete_albums"] == 1
        assert data["albums"][0]["missing_track_numbers"] == [2]

        # 2. Запрос сверки с Deezer
        match_resp = authenticated_client.get(
            "/files/audit/incomplete-albums/catalog-match",
            params={"path": "Kendrick Lamar/DAMN"},
        )
        assert match_resp.status_code == 200
        match_data = match_resp.json()
        assert match_data["is_catalog_matched"] is True
        assert match_data["deezer_album_id"] == "302127"
        assert len(match_data["missing_tracks"]) == 1
        missing_t = match_data["missing_tracks"][0]
        assert missing_t["title"] == "PRIDE."
        assert missing_t["deezer_url"] == "https://www.deezer.com/track/361204781"

        # 3. Запрос на дозагрузку недостающих треков
        dl_resp = authenticated_client.post(
            "/files/audit/incomplete-albums/download-missing",
            json={"path": "Kendrick Lamar/DAMN"},
        )
        assert dl_resp.status_code == 200
        jobs = dl_resp.json()
        assert len(jobs) == 1
        assert jobs[0]["id"] == "job123"
        mock_download_service.create_job.assert_called_once_with(
            url="https://www.deezer.com/track/361204781",
            source="auto",
        )
    finally:
        authenticated_client.app.dependency_overrides.pop(get_library_audit_service, None)
        authenticated_client.app.dependency_overrides.pop(get_download_service, None)


@pytest.mark.asyncio
async def test_match_album_catalog_without_explicit_track_position_field(
    authenticated_client: TestClient,
    tmp_path: Path,
):
    """Проверка корректного вычисления dz_pos по порядку в списке, если track_position отсутствует."""
    music_dir = tmp_path / "music"
    album_dir = music_dir / "Kendrick Lamar" / "DAMN"
    album_dir.mkdir(parents=True, exist_ok=True)

    # Локально есть треки 1 и 3, пропущен 2
    f1 = album_dir / "01. BLOOD..mp3"
    f1.write_bytes(b"dummy1")
    f3 = album_dir / "03. YAH..mp3"
    f3.write_bytes(b"dummy3")

    mock_tag_service = MagicMock()
    mock_tag_service.read_tags.side_effect = lambda p, rel_path: TrackTags(
        path=rel_path,
        filename=p.name,
        title=p.stem.split(". ", 1)[-1],
        artist="Kendrick Lamar",
        album="DAMN.",
        track_number=int(p.stem[:2]),
        track_total=3,
        disc_number=1,
        disc_total=1,
    )

    mock_dz = MagicMock()
    # Deezer возвращает треки БЕЗ поля 'track_position'
    mock_dz.search_albums = AsyncMock(return_value=[{"id": 39949511, "title": "DAMN.", "artist": {"name": "Kendrick Lamar"}}])
    mock_dz.get_album = AsyncMock(
        return_value={
            "id": 39949511,
            "title": "DAMN.",
            "artist": {"name": "Kendrick Lamar"},
            "tracks": {
                "data": [
                    {"id": 101, "title": "BLOOD.", "duration": 118},  # pos 1
                    {"id": 102, "title": "DNA.", "duration": 185},    # pos 2 (отсутствует локально!)
                    {"id": 103, "title": "YAH.", "duration": 160},    # pos 3
                ]
            },
        }
    )

    sandbox = PathSandbox(music_dir)
    audit_svc = LibraryAuditService(
        music_sandbox=sandbox,
        tag_service=mock_tag_service,
        deezer_client=mock_dz,
    )
    audit_svc.ensure_index = AsyncMock()

    mock_download_service = MagicMock()
    mock_job = DownloadJob(job_id="job_dna", url="https://www.deezer.com/track/102", source="deezer")
    mock_download_service.create_job = AsyncMock(return_value=mock_job)

    authenticated_client.app.dependency_overrides[get_library_audit_service] = lambda: audit_svc
    authenticated_client.app.dependency_overrides[get_download_service] = lambda: mock_download_service

    try:
        # Проверяем сопоставление
        match_res = authenticated_client.get(
            "/files/audit/incomplete-albums/catalog-match",
            params={"path": "Kendrick Lamar/DAMN"},
        )
        assert match_res.status_code == 200
        matched = match_res.json()
        assert len(matched["missing_tracks"]) == 1
        assert matched["missing_tracks"][0]["title"] == "DNA."
        assert matched["missing_tracks"][0]["track_number"] == 2
        assert matched["missing_tracks"][0]["deezer_url"] == "https://www.deezer.com/track/102"

        # Проверяем скачивание конкретного недостающего трека по track_numbers
        dl_res = authenticated_client.post(
            "/files/audit/incomplete-albums/download-missing",
            json={"path": "Kendrick Lamar/DAMN", "track_numbers": [2]},
        )
        assert dl_res.status_code == 200
        jobs = dl_res.json()
        assert len(jobs) == 1
        mock_download_service.create_job.assert_called_once_with(
            url="https://www.deezer.com/track/102",
            source="auto",
        )
    finally:
        authenticated_client.app.dependency_overrides.pop(get_library_audit_service, None)
        authenticated_client.app.dependency_overrides.pop(get_download_service, None)


@pytest.mark.asyncio
async def test_consensus_track_total_ignores_rogue_tag(tmp_path: Path):
    """Проверка, что единичный сбойный тег track_total=35 в альбоме из 13 треков не ломает аудит."""
    sandbox = PathSandbox(tmp_path)
    mock_tag_svc = MagicMock()
    audit_svc = LibraryAuditService(music_sandbox=sandbox, tag_service=mock_tag_svc)

    tracks = []
    for i in range(1, 14):
        # Трек 13 имеет ошибочный тег track_total=35, остальные track_total=13
        t_total = 35 if i == 13 else 13
        tracks.append(
            TrackTags(
                path=f"Kamikaze/{i:02d}. Track {i}.mp3",
                track_number=i,
                track_total=t_total,
                disc_number=1,
                disc_total=1,
                title=f"Track {i}",
                artist="Eminem",
                album="Kamikaze",
            )
        )

    audit_svc._cache = {
        t.path: {"mtime": 1000.0, "size": 1000, "track": t}
        for t in tracks
    }
    audit_svc.ensure_index = AsyncMock()

    res = await audit_svc.get_incomplete_albums()
    # Альбом не должен считаться неполным!
    assert res.total_incomplete_albums == 0
    assert len(res.albums) == 0


@pytest.mark.asyncio
async def test_download_missing_with_deezer_album_id(
    authenticated_client: TestClient,
    tmp_path: Path,
):
    """Проверка передачи deezer_album_id в запрос на дозагрузку недостающих треков."""
    sandbox = PathSandbox(tmp_path)
    mock_dz = MagicMock()
    mock_dz.get_album = AsyncMock(
        return_value={
            "id": 999999,
            "title": "Special Album",
            "tracks": {
                "data": [
                    {"id": 1, "title": "T1", "track_position": 1, "disk_number": 1},
                    {"id": 2, "title": "T2", "track_position": 2, "disk_number": 1},
                ]
            },
        }
    )

    (tmp_path / "Artist" / "Album").mkdir(parents=True, exist_ok=True)
    f1 = tmp_path / "Artist" / "Album" / "01. T1.mp3"
    f1.write_bytes(b"dummy")

    mock_tag_svc = MagicMock()
    mock_tag_svc.read_tags = MagicMock(return_value=TrackTags(
        path="Artist/Album/01. T1.mp3",
        track_number=1,
        track_total=2,
        disc_number=1,
        disc_total=1,
        title="T1",
        artist="Artist",
        album="Album",
    ))
    audit_svc = LibraryAuditService(music_sandbox=sandbox, tag_service=mock_tag_svc, deezer_client=mock_dz)
    audit_svc._cache = {
        "Artist/Album/01. T1.mp3": {
            "mtime": 1000.0,
            "size": 1000,
            "track": TrackTags(
                path="Artist/Album/01. T1.mp3",
                track_number=1,
                track_total=2,
                disc_number=1,
                disc_total=1,
                title="T1",
                artist="Artist",
                album="Album",
            ),
        }
    }
    audit_svc.ensure_index = AsyncMock()

    mock_download_service = MagicMock()
    mock_download_service.create_job = AsyncMock(return_value=DownloadJob(job_id="job_t2", url="https://www.deezer.com/track/2", source="deezer"))

    authenticated_client.app.dependency_overrides[get_library_audit_service] = lambda: audit_svc
    authenticated_client.app.dependency_overrides[get_download_service] = lambda: mock_download_service

    try:
        res = authenticated_client.post(
            "/files/audit/incomplete-albums/download-missing",
            json={
                "path": "Artist/Album",
                "deezer_album_id": "999999",
            },
        )
        assert res.status_code == 200
        jobs = res.json()
        assert len(jobs) == 1
        mock_dz.get_album.assert_called_once_with("999999", fetch_track_details=True)
        mock_download_service.create_job.assert_called_once_with(
            url="https://www.deezer.com/track/2",
            source="auto",
        )
    finally:
        authenticated_client.app.dependency_overrides.pop(get_library_audit_service, None)
        authenticated_client.app.dependency_overrides.pop(get_download_service, None)


@pytest.mark.asyncio
async def test_incomplete_albums_auto_match_deezer_filters_false_positives(tmp_path: Path):
    """Проверка, что ложно-неполный альбом (25 треков из 25 в Deezer, но track_total=30) отсеивается."""
    from app.core.sandbox import PathSandbox

    sandbox = PathSandbox(tmp_path)
    album_dir = tmp_path / "Eminem" / "The Slim Shady LP"
    album_dir.mkdir(parents=True, exist_ok=True)

    mock_dz = MagicMock()
    # Deezer возвращает 25 треков для этого альбома
    dz_tracks = [{"id": i, "title": f"Song {i}", "track_position": i, "disk_number": 1} for i in range(1, 26)]
    mock_dz.search_albums = AsyncMock(return_value=[{"id": 1111, "title": "The Slim Shady LP", "artist": {"name": "Eminem"}}])
    mock_dz.get_album = AsyncMock(return_value={"id": 1111, "title": "The Slim Shady LP", "tracks": {"data": dz_tracks}})

    mock_tag_svc = MagicMock()
    audit_svc = LibraryAuditService(music_sandbox=sandbox, tag_service=mock_tag_svc, deezer_client=mock_dz)

    # 25 локальных треков, но у каждого track_total=30
    cache = {}
    for i in range(1, 26):
        rel = f"Eminem/The Slim Shady LP/{i:02d}. Song {i}.mp3"
        cache[rel] = {
            "mtime": 1000.0,
            "size": 1000,
            "track": TrackTags(
                path=rel,
                track_number=i,
                track_total=30,  # Завышенный track_total
                title=f"Song {i}",
                artist="Eminem",
                album="The Slim Shady LP",
            ),
        }
    audit_svc._cache = cache
    audit_svc.ensure_index = AsyncMock()

    res = await audit_svc.get_incomplete_albums(auto_match=True)
    # Так как в каталоге Deezer всего 25 треков и все 25 есть на диске, альбом не должен возвращаться как неполный!
    assert res.total_incomplete_albums == 0
    assert len(res.albums) == 0
    # Проверяем, что результат сохранился в кэше каталога
    assert "Eminem/The Slim Shady LP" in audit_svc._catalog_cache


@pytest.mark.asyncio
async def test_incomplete_albums_single_folder_ignored(tmp_path: Path):
    """Проверка, что папка сингла с 1 треком и track_number=13 не попадает в неполные релизы."""
    from app.core.sandbox import PathSandbox

    sandbox = PathSandbox(tmp_path)
    mock_tag_svc = MagicMock()
    audit_svc = LibraryAuditService(music_sandbox=sandbox, tag_service=mock_tag_svc)

    rel = "Eminem/From The D 2 The LBC - Single/01. Song.mp3"
    audit_svc._cache = {
        rel: {
            "mtime": 1000.0,
            "size": 1000,
            "track": TrackTags(
                path=rel,
                track_number=13,  # Например, 13-й трек сборника, скачанный как сингл
                track_total=20,
                title="From The D 2 The LBC",
                artist="Eminem",
                album="From The D 2 The LBC - Single",
            ),
        }
    }
    audit_svc.ensure_index = AsyncMock()

    res = await audit_svc.get_incomplete_albums()
    assert res.total_incomplete_albums == 0
    assert len(res.albums) == 0


def test_audit_reorganize_endpoint(authenticated_client: TestClient, tmp_path: Path):
    """Проверка POST /files/audit/reorganize."""
    from app.dependencies import get_organizer_service
    from app.services.organizer_service import OrganizerService
    from app.services.tag_service import TagService

    sandbox = PathSandbox(tmp_path)
    tag_svc = TagService(music_sandbox=sandbox)
    audit_svc = LibraryAuditService(music_sandbox=sandbox, tag_service=tag_svc)
    org_svc = OrganizerService(music_sandbox=sandbox, tag_service=tag_svc)

    # Создаем фиктивный трек
    sub = tmp_path / "OldArtist" / "OldAlbum - 2020"
    sub.mkdir(parents=True, exist_ok=True)
    f = sub / "01. Song.mp3"
    f.write_bytes(b"\xff\xfb\x90\x00" + b"\x00" * 300)

    audit_svc._cache = {
        "OldArtist/OldAlbum - 2020/01. Song.mp3": {
            "mtime": 1000.0,
            "size": 304,
            "track": TrackTags(
                path="OldArtist/OldAlbum - 2020/01. Song.mp3",
                track_number=1,
                title="Song",
                artist="OldArtist",
                album="OldAlbum",
            ),
        }
    }
    audit_svc.ensure_index = AsyncMock()

    authenticated_client.app.dependency_overrides[get_library_audit_service] = lambda: audit_svc
    authenticated_client.app.dependency_overrides[get_organizer_service] = lambda: org_svc
    try:
        resp = authenticated_client.post("/files/audit/reorganize")
        assert resp.status_code == 200
        data = resp.json()
        assert data["success"] is True
        assert data["total_tracks"] == 1
        assert data["moved_tracks"] == 1
        assert data["total_albums"] == 1
        assert (tmp_path / "OldAlbum/01. Song.mp3").exists()
    finally:
        authenticated_client.app.dependency_overrides.pop(get_library_audit_service, None)
        authenticated_client.app.dependency_overrides.pop(get_organizer_service, None)




