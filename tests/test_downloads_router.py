"""Тесты API маршрутов загрузки (POST /downloads, GET /downloads/{job_id}, etc.)."""

from unittest.mock import AsyncMock, patch

from fastapi.testclient import TestClient

from app.schemas.download import JobStatus, ResolvedTrack
from app.services.download_service import DownloadService


def test_downloads_unauthorized(client: TestClient):
    """Проверка запрета доступа к эндпоинтам без аутентификации."""
    resp_post = client.post("/downloads", json={"url": "https://www.deezer.com/album/1"})
    assert resp_post.status_code == 401

    resp_get = client.get("/downloads/some_job_id")
    assert resp_get.status_code == 401

    resp_tracks = client.get("/downloads/some_job_id/tracks")
    assert resp_tracks.status_code == 401

    resp_del = client.delete("/downloads/some_job_id")
    assert resp_del.status_code == 401


def test_create_download_invalid_domain(authenticated_client: TestClient):
    """Проверка отклонения URL с неподдерживаемым доменом (HTTP 400)."""
    resp = authenticated_client.post(
        "/downloads",
        json={"url": "https://disallowed-service.com/audio.mp3"},
    )
    assert resp.status_code == 400
    data = resp.json()
    assert data["error"] == "UNSUPPORTED_SOURCE"


def test_create_and_poll_download(authenticated_client: TestClient):
    """Проверка создания задачи и последующего опроса статуса через GET /downloads/{job_id}."""
    with patch.object(DownloadService, "_execute_pipeline", new=AsyncMock()):
        # 1. Создание задачи
        resp = authenticated_client.post(
            "/downloads",
            json={"url": "https://www.deezer.com/album/302127", "source": "deezer"},
        )
        assert resp.status_code == 202
        data = resp.json()
        assert "id" in data
        assert data["status"] == "QUEUED"
        assert data["source"] == "deezer"

        job_id = data["id"]

        # 2. Опрос статуса
        poll_resp = authenticated_client.get(f"/downloads/{job_id}")
        assert poll_resp.status_code == 200
        poll_data = poll_resp.json()
        assert poll_data["id"] == job_id
        assert poll_data["status"] == "QUEUED"


def test_get_download_tracks_flow(authenticated_client: TestClient):
    """Проверка получения подготовленных тегов после готовности задачи."""
    with patch.object(DownloadService, "_execute_pipeline", new=AsyncMock()):
        resp = authenticated_client.post(
            "/downloads",
            json={"url": "https://www.deezer.com/album/302127"},
        )
        job_id = resp.json()["id"]

        # Пока задача в QUEUED - получение треков должно возвращать ошибку
        tracks_err = authenticated_client.get(f"/downloads/{job_id}/tracks")
        assert tracks_err.status_code == 502

        # Переводим задачу в READY искусственно для проверки ответа роутера
        from app.dependencies import (
            get_deezer_client,
            get_download_service,
            get_settings,
            get_source_resolver,
            get_tmp_sandbox,
        )

        settings = get_settings()
        dc = get_deezer_client()
        sr = get_source_resolver(dc, settings)
        sb = get_tmp_sandbox(settings)
        svc = get_download_service(settings, sb, sr, dc)

        job = svc.get_job(job_id)
        job.status = JobStatus.READY
        job.album_metadata = {
            "album": "Discovery",
            "album_artist": "Daft Punk",
            "year": 2001,
            "genre": "House",
            "cover_url": "https://cover.jpg",
        }
        job.tracks = [
            ResolvedTrack(
                track_id="1",
                track_number=1,
                title="One More Time",
                artist="Daft Punk\\\\Romanthony",
                album="Discovery",
                album_artist="Daft Punk",
                year=2001,
                genre="House",
                duration=320,
                converted_path=f"{job_id}/converted/01. One More Time.mp3",
                status="ready",
            )
        ]

        # Теперь запрос должен быть успешен
        tracks_ok = authenticated_client.get(f"/downloads/{job_id}/tracks")
        assert tracks_ok.status_code == 200
        t_data = tracks_ok.json()
        assert t_data["album"] == "Discovery"
        assert t_data["album_artist"] == "Daft Punk"
        assert len(t_data["tracks"]) == 1
        assert t_data["tracks"][0]["title"] == "One More Time"


def test_cancel_download(authenticated_client: TestClient):
    """Проверка отмены задачи и удаления временных файлов через DELETE."""
    with patch.object(DownloadService, "_execute_pipeline", new=AsyncMock()):
        resp = authenticated_client.post(
            "/downloads",
            json={"url": "https://www.deezer.com/album/302127"},
        )
        job_id = resp.json()["id"]

        del_resp = authenticated_client.delete(f"/downloads/{job_id}")
        assert del_resp.status_code == 200
        assert del_resp.json() == {"status": "deleted", "job_id": job_id}

        # После удаления задача не должна находиться
        get_resp = authenticated_client.get(f"/downloads/{job_id}")
        assert get_resp.status_code == 404


def test_create_magnet_download(authenticated_client: TestClient):
    """Проверка создания задачи по Magnet-ссылке (HTTP 202, source='torrent')."""
    with patch.object(DownloadService, "_execute_pipeline", new=AsyncMock()):
        magnet_url = "magnet:?xt=urn:btih:d2474e86c95b19b8bcfdb92bc12c9d44667cfa36&dn=TestAlbum"
        resp = authenticated_client.post(
            "/downloads",
            json={"url": magnet_url, "source": "auto"},
        )
        assert resp.status_code == 202
        data = resp.json()
        assert data["source"] == "torrent"
        assert data["status"] == "QUEUED"
        assert data["url"] == magnet_url


def test_upload_torrent_file(authenticated_client: TestClient):
    """Проверка загрузки файла .torrent через multipart/form-data (HTTP 202)."""
    with patch.object(DownloadService, "_execute_pipeline", new=AsyncMock()):
        torrent_bytes = b"d8:announce31:http://tracker.example.com/announcee"
        files = {"file": ("album.torrent", torrent_bytes, "application/x-bittorrent")}

        resp = authenticated_client.post("/downloads/torrent", files=files)
        assert resp.status_code == 202
        data = resp.json()
        assert data["source"] == "torrent"
        assert data["status"] == "QUEUED"
        assert "album.torrent" in data["url"]


def test_upload_torrent_invalid_extension(authenticated_client: TestClient):
    """Проверка отклонения загрузки файла с невалидным расширением (HTTP 400)."""
    files = {"file": ("song.mp3", b"FAKE_MP3", "audio/mpeg")}
    resp = authenticated_client.post("/downloads/torrent", files=files)
    assert resp.status_code == 400
    assert resp.json()["error"] == "UNSUPPORTED_SOURCE"


def test_get_artist_discography_endpoint(authenticated_client: TestClient):
    """Проверка эндпоинта GET /downloads/discography."""
    from app.schemas.download import ArtistDiscographyResponse, DiscographyReleaseItem
    from app.services.source_resolver import SourceResolver

    mock_resp = ArtistDiscographyResponse(
        artist_id="27",
        artist_name="Daft Punk",
        artist_avatar="https://img.com/avatar.jpg",
        source="deezer",
        total_releases=1,
        releases=[
            DiscographyReleaseItem(
                id="101",
                title="Discovery",
                type="album",
                year=2001,
                track_count=14,
                cover_url="https://img.com/discovery.jpg",
                url="https://www.deezer.com/album/101",
            )
        ],
    )

    with patch.object(SourceResolver, "resolve_artist_discography", new=AsyncMock(return_value=mock_resp)):
        resp = authenticated_client.get("/downloads/discography?url=https://www.deezer.com/artist/27")
        assert resp.status_code == 200
        data = resp.json()
        assert data["artist_name"] == "Daft Punk"
        assert data["total_releases"] == 1
        assert data["releases"][0]["title"] == "Discovery"
        assert data["releases"][0]["year"] == 2001


