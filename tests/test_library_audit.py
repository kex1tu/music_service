"""Тесты аудита медиатеки (GET /files/audit/*, POST /files/audit/*)."""

from pathlib import Path
from unittest.mock import AsyncMock, MagicMock

import pytest
from fastapi.testclient import TestClient

from app.dependencies import get_library_audit_service
from app.schemas.lyrics import LyricsSearchResult
from app.schemas.tag import TrackTags
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
