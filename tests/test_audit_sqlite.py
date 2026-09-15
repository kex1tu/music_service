"""Тесты интеграции подсистемы аудита на Go + SQLite."""

from pathlib import Path

import aiosqlite
import pytest

from app.core.database import get_db_connection, init_db
from app.core.sandbox import PathSandbox
from app.services.library_audit_service import LibraryAuditService
from app.services.tag_service import TagService


@pytest.fixture
def test_sandbox(tmp_path: Path) -> PathSandbox:
    music_dir = tmp_path / "music"
    music_dir.mkdir(parents=True, exist_ok=True)
    return PathSandbox(music_dir)


@pytest.mark.asyncio
async def test_init_db_creates_tables(tmp_path: Path):
    db_path = tmp_path / "library.db"
    await init_db(db_path)
    assert db_path.exists()

    async with get_db_connection(db_path) as conn:
        cursor = await conn.execute("SELECT name FROM sqlite_master WHERE type='table';")
        tables = {row[0] for row in await cursor.fetchall()}
        assert "artists" in tables
        assert "albums" in tables
        assert "tracks" in tables
        assert "genres" in tables
        assert "missing_tracks" in tables
        assert "audit_issues" in tables


@pytest.mark.asyncio
async def test_audit_service_reads_sqlite_summary(test_sandbox: PathSandbox, tmp_path: Path):
    db_path = tmp_path / "test_audit.db"
    await init_db(db_path)

    # Заполняем тестовыми данными
    async with aiosqlite.connect(str(db_path)) as conn:
        await conn.execute("INSERT INTO artists (id, name, clean_name) VALUES (1, 'Test Artist', 'test artist')")
        await conn.execute(
            """
            INSERT INTO albums (
                id, artist_id, title, clean_title, folder_path,
                has_cover, total_tracks, present_tracks_count, is_complete,
                deezer_id, spotify_id
            ) VALUES (1, 1, 'Test Album', 'test album', 'Test Album', 1, 10, 8, 0, '123', 'sp_456')
            """
        )
        await conn.execute(
            """
            INSERT INTO tracks (
                id, album_id, artist_id, title, clean_title, track_number,
                file_path, file_size, mtime, lyrics_status, is_path_valid,
                path_violation_reason, suggested_path
            ) VALUES (
                1, 1, 1, 'Track 01', 'track 01', 1,
                'WrongFolder/01. Track.mp3', 1024, 100.0, 'none', 0,
                'Несоответствие папки', 'Test Album/01. Track 01.mp3'
            )
            """
        )
        await conn.execute("INSERT INTO genres (id, name, is_valid_en, canonical_en_name) VALUES (1, 'Русский рэп', 0, 'Russian Hip-Hop')")
        await conn.execute("INSERT INTO track_genres (track_id, genre_id) VALUES (1, 1)")
        await conn.execute(
            """
            INSERT INTO audit_issues (entity_type, entity_id, issue_type, severity, description, is_auto_fixed, is_resolved)
            VALUES ('track', 1, 'non_en_genre', 'warning', 'Русскоязычный жанр', 1, 0)
            """
        )
        await conn.execute(
            """
            INSERT INTO missing_tracks (
                album_id, title, artist, track_number, disc_number,
                expected_duration, deezer_id, spotify_id, status
            ) VALUES (1, 'Missing Track 2', 'Test Artist', 2, 1, 210.0, 'dz_789', 'sp_789', 'missing')
            """
        )
        await conn.commit()

    tag_service = TagService(music_sandbox=test_sandbox)
    service = LibraryAuditService(
        music_sandbox=test_sandbox,
        tag_service=tag_service,
        db_path=db_path,
    )

    # 1. Summary
    summary = await service.get_summary()
    assert summary.total_tracks == 1
    assert summary.total_albums == 1
    assert summary.missing_lyrics_count == 1
    assert summary.incomplete_albums_count == 1
    assert summary.invalid_paths_count == 1
    assert summary.non_en_genres_count == 1

    # 2. Invalid paths
    invalid_paths = await service.get_invalid_paths()
    assert len(invalid_paths) == 1
    assert invalid_paths[0].path == "WrongFolder/01. Track.mp3"
    assert invalid_paths[0].suggested_path == "Test Album/01. Track 01.mp3"

    # 3. Non-EN genres
    non_en_genres = await service.get_non_en_genres()
    assert len(non_en_genres) == 1
    assert non_en_genres[0].current_genre == "Русский рэп"
    assert non_en_genres[0].canonical_genre == "Russian Hip-Hop"
    assert non_en_genres[0].is_auto_fixed is True

    # 4. Incomplete albums with missing tracks and platform IDs
    incomplete = await service.get_incomplete_albums()
    assert incomplete.total_incomplete_albums == 1
    assert len(incomplete.albums) == 1
    alb = incomplete.albums[0]
    assert alb.album == "Test Album"
    assert alb.deezer_album_id == "123"
    assert alb.spotify_id == "sp_456"
    assert len(alb.missing_tracks) == 1
    assert alb.missing_tracks[0].title == "Missing Track 2"
    assert alb.missing_tracks[0].deezer_id == "dz_789"
    assert alb.missing_tracks[0].spotify_id == "sp_789"


def test_audit_api_endpoints_sqlite(authenticated_client, client, test_sandbox: PathSandbox, tmp_path: Path):
    import asyncio

    from app.dependencies import get_library_audit_service

    db_path = tmp_path / "test_api_audit.db"
    asyncio.run(init_db(db_path))

    # Заполняем тестовыми данными
    async def seed_data():
        async with aiosqlite.connect(str(db_path)) as conn:
            await conn.execute("INSERT INTO artists (id, name, clean_name) VALUES (1, 'Art 1', 'art 1')")
            await conn.execute("INSERT INTO albums (id, artist_id, title, clean_title, folder_path) VALUES (1, 1, 'Alb 1', 'alb 1', 'Alb 1')")
            await conn.execute(
                """
                INSERT INTO tracks (id, album_id, artist_id, title, clean_title, file_path, file_size, mtime, is_path_valid, suggested_path, path_violation_reason)
                VALUES (1, 1, 1, 'Trk', 'trk', 'Bad/Path.mp3', 10, 1.0, 0, 'Alb 1/01. Trk.mp3', 'Неверный каталог')
                """
            )
            await conn.execute("INSERT INTO genres (id, name, is_valid_en, canonical_en_name) VALUES (1, 'Рок', 0, 'Rock')")
            await conn.execute("INSERT INTO track_genres (track_id, genre_id) VALUES (1, 1)")
            await conn.commit()

    asyncio.run(seed_data())

    tag_service = TagService(music_sandbox=test_sandbox)
    service = LibraryAuditService(
        music_sandbox=test_sandbox,
        tag_service=tag_service,
        db_path=db_path,
    )

    # 1. 401 Unauthorized check
    assert client.get("/files/audit/invalid-paths").status_code == 401
    assert client.get("/files/audit/non-en-genres").status_code == 401

    # 2. Authenticated check with dependency override
    authenticated_client.app.dependency_overrides[get_library_audit_service] = lambda: service
    try:
        resp_paths = authenticated_client.get("/files/audit/invalid-paths")
        assert resp_paths.status_code == 200
        data_paths = resp_paths.json()
        assert len(data_paths) == 1
        assert data_paths[0]["path"] == "Bad/Path.mp3"
        assert data_paths[0]["suggested_path"] == "Alb 1/01. Trk.mp3"

        resp_genres = authenticated_client.get("/files/audit/non-en-genres")
        assert resp_genres.status_code == 200
        data_genres = resp_genres.json()
        assert len(data_genres) == 1
        assert data_genres[0]["current_genre"] == "Рок"
        assert data_genres[0]["canonical_genre"] == "Rock"
    finally:
        authenticated_client.app.dependency_overrides.pop(get_library_audit_service, None)

