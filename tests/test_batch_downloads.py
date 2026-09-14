"""Тесты пакетной загрузки и управления очередью (POST /downloads/batch, /downloads/queue/*)."""

from unittest.mock import AsyncMock, patch

from fastapi.testclient import TestClient

from app.schemas.download import JobStatus, ResolvedTrack
from app.services.download_service import DownloadService


def test_batch_downloads_unauthorized(client: TestClient):
    """Проверка запрета доступа к пакетным эндпоинтам без аутентификации."""
    resp_post = client.post("/downloads/batch", json={"urls": ["https://www.deezer.com/album/1"]})
    assert resp_post.status_code == 401

    resp_list = client.get("/downloads")
    assert resp_list.status_code == 401

    resp_summary = client.get("/downloads/queue/summary")
    assert resp_summary.status_code == 401

    resp_clear = client.post("/downloads/queue/clear")
    assert resp_clear.status_code == 401


def test_create_batch_downloads_validation(authenticated_client: TestClient):
    """Проверка валидации входящих данных в пакетном режиме."""
    # Пустой список URL
    resp_empty = authenticated_client.post("/downloads/batch", json={"urls": []})
    assert resp_empty.status_code == 422

    # Список с неподдерживаемым доменом
    resp_invalid = authenticated_client.post(
        "/downloads/batch",
        json={"urls": ["https://www.deezer.com/album/1", "https://unsupported-site.com/audio"]},
    )
    assert resp_invalid.status_code == 400
    assert resp_invalid.json()["error"] == "UNSUPPORTED_SOURCE"


def test_create_batch_downloads_success(authenticated_client: TestClient):
    """Проверка успешной постановки нескольких URL в очередь."""
    with patch.object(DownloadService, "_execute_pipeline", new=AsyncMock()):
        resp = authenticated_client.post(
            "/downloads/batch",
            json={
                "urls": [
                    "https://www.deezer.com/album/100",
                    "https://soundcloud.com/artist/track",
                    "https://music.youtube.com/watch?v=12345",
                ],
                "source": "auto",
            },
        )
        assert resp.status_code == 202
        data = resp.json()
        assert data["total_queued"] == 3
        assert len(data["job_ids"]) == 3
        assert len(data["jobs"]) == 3
        for job in data["jobs"]:
            assert job["status"] == "QUEUED"


def test_queue_summary_and_clear_flow(authenticated_client: TestClient):
    """Проверка работы сводки очереди (GET /downloads/queue/summary) и очистки завершенных задач."""
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

    with patch.object(DownloadService, "_execute_pipeline", new=AsyncMock()):
        resp = authenticated_client.post(
            "/downloads/batch",
            json={
                "urls": [
                    "https://www.deezer.com/album/101",
                    "https://www.deezer.com/album/102",
                ],
            },
        )
        job_ids = resp.json()["job_ids"]
        jid1, jid2 = job_ids[0], job_ids[1]

        # Искусственно настроим состояния задач
        job1 = svc.get_job(jid1)
        job1.status = JobStatus.READY
        job1.progress = 100.0
        job1.tracks = [
            ResolvedTrack(
                track_id="t1",
                track_number=1,
                title="Song 1",
                artist="Artist 1",
                duration=180,
                status="ready",
            )
        ]
        job1.completed_tracks = 1

        job2 = svc.get_job(jid2)
        job2.status = JobStatus.DOWNLOADING
        job2.progress = 40.0
        job2.tracks = [
            ResolvedTrack(
                track_id="t2",
                track_number=1,
                title="Song 2",
                artist="Artist 2",
                duration=200,
                status="downloading",
            )
        ]

        # 1. Проверяем получение сводки
        summary_resp = authenticated_client.get("/downloads/queue/summary")
        assert summary_resp.status_code == 200
        s_data = summary_resp.json()
        assert s_data["active_jobs"] >= 1
        assert s_data["completed_jobs"] >= 1
        assert s_data["completed_tracks"] >= 1
        assert s_data["overall_progress"] > 0

        # 2. Проверяем список всех задач
        list_resp = authenticated_client.get("/downloads")
        assert list_resp.status_code == 200
        all_jobs = list_resp.json()
        assert len(all_jobs) >= 2

        # 3. Очищаем завершенные задачи
        clear_resp = authenticated_client.post("/downloads/queue/clear")
        assert clear_resp.status_code == 200
        assert clear_resp.json()["status"] == "cleared"

        # job1 (READY) должен быть удален, job2 (DOWNLOADING) должен остаться
        after_summary = authenticated_client.get("/downloads/queue/summary").json()
        assert after_summary["completed_jobs"] == 0
        assert svc.get_job(jid2).status == JobStatus.DOWNLOADING


@patch("asyncio.create_subprocess_exec")
def test_batch_semaphore_concurrency(mock_subproc, authenticated_client: TestClient):
    """Проверка, что семафор ограничивает количество одновременно работающих задач."""
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

    # Проверяем, что значение семафора соответствует настройке MAX_CONCURRENT_DOWNLOADS
    assert svc._semaphore._value <= settings.MAX_CONCURRENT_DOWNLOADS


def test_batch_downloads_with_magnet_and_torrents(authenticated_client: TestClient):
    """Проверка успешной постановки в очередь списка со ссылками на Magnet и Deezer."""
    with patch.object(DownloadService, "_execute_pipeline", new=AsyncMock()):
        resp = authenticated_client.post(
            "/downloads/batch",
            json={
                "urls": [
                    "https://www.deezer.com/album/200",
                    "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567",
                    "magnet:xt=urn:btih:fedcba9876543210fedcba9876543210fedcba98",
                    "https://trackers.org/album.torrent",
                ],
                "source": "auto",
            },
        )
        assert resp.status_code == 202
        data = resp.json()
        assert data["total_queued"] == 4
        assert len(data["job_ids"]) == 4
        assert len(data["jobs"]) == 4
        # Проверяем источники созданных задач
        jobs = data["jobs"]
        assert jobs[0]["source"] == "deezer"
        assert jobs[1]["source"] == "torrent"
        assert jobs[2]["source"] == "torrent"
        assert jobs[3]["source"] == "torrent"

