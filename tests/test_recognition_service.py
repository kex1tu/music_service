"""Модульные тесты сервиса акустического распознавания треков RecognitionService."""

from pathlib import Path
from unittest.mock import AsyncMock, patch

import pytest

from app.core.exceptions import NotFoundError, PathTraversalError
from app.core.sandbox import PathSandbox
from app.services.recognition_service import RecognitionService


@pytest.fixture
def recognition_service(tmp_path: Path) -> tuple[RecognitionService, Path, Path]:
    """Создает тестовый экземпляр RecognitionService с изолированными песочницами."""
    music_dir = tmp_path / "music"
    tmp_dir = tmp_path / "tmp"
    music_dir.mkdir(parents=True, exist_ok=True)
    tmp_dir.mkdir(parents=True, exist_ok=True)

    music_sandbox = PathSandbox(music_dir)
    tmp_sandbox = PathSandbox(tmp_dir)

    service = RecognitionService(
        music_sandbox=music_sandbox,
        tmp_sandbox=tmp_sandbox,
    )
    return service, music_dir, tmp_dir


@pytest.mark.asyncio
async def test_recognize_audio_success(recognition_service: tuple[RecognitionService, Path, Path]):
    """Проверка успешного распознавания метаданных трека через Shazam."""
    service, music_dir, _ = recognition_service
    track_file = music_dir / "test_track.mp3"
    track_file.write_bytes(b"dummy audio bytes")

    mock_shazam_response = {
        "matches": [{"id": "12345"}],
        "track": {
            "title": "One More Time",
            "subtitle": "Daft Punk",
            "genres": {"primary": "House"},
            "sections": [
                {
                    "type": "SONG",
                    "metadata": [
                        {"title": "Album", "text": "Discovery"},
                        {"title": "Released", "text": "2001"},
                    ],
                }
            ],
            "images": {
                "coverarthq": "https://is1-ssl.mzstatic.com/image/thumb/Music123/400x400cc.jpg",
            },
        },
    }

    with patch.object(service.shazam, "recognize", new=AsyncMock(return_value=mock_shazam_response)):
        res = await service.recognize_audio("test_track.mp3")
        assert res.recognized is True
        assert res.title == "One More Time"
        assert res.artist == "Daft Punk"
        assert res.album == "Discovery"
        assert res.year == 2001
        assert res.genre == "House"
        # Проверяем автоматическое повышение разрешения обложки Apple/Shazam до 1000x1000
        assert "1000x1000cc.jpg" in res.cover_url


@pytest.mark.asyncio
async def test_recognize_audio_no_match(recognition_service: tuple[RecognitionService, Path, Path]):
    """Проверка обработки случая, когда трек не найден в базе Shazam."""
    service, music_dir, _ = recognition_service
    track_file = music_dir / "unknown.mp3"
    track_file.write_bytes(b"dummy unknown bytes")

    mock_shazam_response = {"matches": []}

    with patch.object(service.shazam, "recognize", new=AsyncMock(return_value=mock_shazam_response)):
        res = await service.recognize_audio("unknown.mp3")
        assert res.recognized is False
        assert res.title is None


@pytest.mark.asyncio
async def test_recognize_audio_file_not_found(recognition_service: tuple[RecognitionService, Path, Path]):
    """Проверка выброса NotFoundError, если файл не существует на диске."""
    service, _, _ = recognition_service
    with pytest.raises(NotFoundError):
        await service.recognize_audio("non_existent_file.mp3")


@pytest.mark.asyncio
async def test_recognize_audio_path_traversal(recognition_service: tuple[RecognitionService, Path, Path]):
    """Проверка защиты от path traversal атак."""
    service, _, _ = recognition_service
    with pytest.raises(PathTraversalError):
        await service.recognize_audio("../../../etc/passwd")


def test_shazam_russian_defaults(tmp_path: Path):
    """Проверка конфигурации языка Shazam по умолчанию для предотвращения транслитерации."""
    music_sandbox = PathSandbox(tmp_path / "music")
    service = RecognitionService(music_sandbox=music_sandbox)
    assert service.shazam.language == "ru-RU"
    assert service.shazam.endpoint_country == "RU"

