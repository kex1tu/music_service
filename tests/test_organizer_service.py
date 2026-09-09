"""Модульные тесты для сервиса организации библиотеки OrganizerService."""

from pathlib import Path

import pytest

from app.core.exceptions import NotFoundError
from app.core.sandbox import PathSandbox
from app.schemas.tag import AlbumTags, TrackTags
from app.services.organizer_service import OrganizerService
from app.services.tag_service import TagService

MINIMAL_MP3 = b"\xff\xfb\x90\x00" + b"\x00" * 300
DUMMY_JPEG = b"\xff\xd8\xff\xe0\x00\x10JFIF" + b"\x00" * 64


@pytest.fixture
def organizer(
    temp_storage: dict[str, Path],
) -> tuple[OrganizerService, TagService, Path, Path]:
    """Фикстура сервиса OrganizerService с песочницами медиатеки и временного хранилища."""
    music_dir = temp_storage["music_dir"]
    tmp_dir = temp_storage["tmp_dir"]

    music_sandbox = PathSandbox(music_dir)
    tmp_sandbox = PathSandbox(tmp_dir)
    tag_service = TagService(music_sandbox=music_sandbox, tmp_sandbox=tmp_sandbox)
    org_svc = OrganizerService(
        music_sandbox=music_sandbox,
        tmp_sandbox=tmp_sandbox,
        tag_service=tag_service,
    )
    return org_svc, tag_service, music_dir, tmp_dir


def test_sanitize_component(organizer: tuple[OrganizerService, TagService, Path, Path]):
    """Проверка очистки и санитизации компонентов пути."""
    org, _, _, _ = organizer

    # Замена опасных символов
    assert org.sanitize_component('AC/DC: "Back*In*Black"?') == "AC_DC_ _Back_In_Black__"

    # Обработка двойных слэшей множественных артистов
    assert org.sanitize_component("Daft Punk\\\\Romanthony") == "Daft Punk & Romanthony"

    # Отсечение ведущих/замыкающих точек и пробелов
    assert org.sanitize_component("...Album Title...   ") == "Album Title"

    # Пустая строка -> fallback
    assert org.sanitize_component("", fallback="Unknown Artist") == "Unknown Artist"
    assert org.sanitize_component("   ", fallback="Fallback") == "Fallback"


def test_compute_target_paths(organizer: tuple[OrganizerService, TagService, Path, Path]):
    """Проверка генерации пути по строгому ТЗ шаблону:
    MUSIC_DIR/{AlbumArtist}/{Album} - {Year}/{TrackNumber:02d}. {Title}.mp3
    """
    org, _, _, _ = organizer

    album = AlbumTags(
        album_artist="Daft Punk",
        album="Discovery",
        year=2001,
    )
    track = TrackTags(
        path="dummy.mp3",
        track_number=1,
        title="One More Time",
        artist="Daft Punk",
    )

    album_dir, track_path = org.compute_target_paths(album, track)
    assert album_dir == "Daft Punk/Discovery - 2001"
    assert track_path == "Daft Punk/Discovery - 2001/01. One More Time.mp3"

    # Без года
    album_no_year = AlbumTags(album_artist="Modjo", album="Modjo", year=None)
    track2 = TrackTags(path="dummy.mp3", track_number=5, title="Lady")
    album_dir2, track_path2 = org.compute_target_paths(album_no_year, track2)
    assert album_dir2 == "Modjo/Modjo"
    assert track_path2 == "Modjo/Modjo/05. Lady.mp3"

    # Альбом уже содержит год в конце названия
    album_with_year = AlbumTags(album_artist="Artist", album="Album - 2020", year=2020)
    album_dir3, _ = org.compute_target_paths(album_with_year, track2)
    assert album_dir3 == "Artist/Album - 2020"


def test_move_from_tmp_to_music_dir(
    organizer: tuple[OrganizerService, TagService, Path, Path],
):
    """Проверка перемещения треков из временного каталога (TMP_DIR) в постоянную медиатеку."""
    org, _, music_dir, tmp_dir = organizer

    # Создаем фиктивную задачу загрузки в TMP_DIR
    job_id = "job_12345"
    job_converted = tmp_dir / job_id / "converted"
    job_converted.mkdir(parents=True, exist_ok=True)

    raw_mp3 = job_converted / "01. draft.mp3"
    raw_mp3.write_bytes(MINIMAL_MP3)
    raw_lrc = job_converted / "01. draft.lrc"
    raw_lrc.write_text("[00:01.00] Synced line", encoding="utf-8")

    cover_file = tmp_dir / job_id / "cover.jpg"
    cover_file.write_bytes(DUMMY_JPEG)

    album = AlbumTags(
        album_artist="Kavinsky",
        album="OutRun",
        year=2013,
        genre="Synthwave",
    )
    track = TrackTags(
        path=f"{job_id}/converted/01. draft.mp3",
        track_number=1,
        title="Nightcall",
        artist="Kavinsky",
    )

    result = org.organize_album(
        album=album,
        tracks=[track],
        source_dir=f"{job_id}/converted",
        cover_path=f"{job_id}/cover.jpg",
    )

    expected_album_dir = "Kavinsky/OutRun - 2013"
    expected_track_rel = f"{expected_album_dir}/01. Nightcall.mp3"

    assert result.destination_dir == expected_album_dir
    assert len(result.tracks) == 1
    assert result.tracks[0].path == expected_track_rel

    # Проверяем файлы на диске в MUSIC_DIR
    final_mp3 = music_dir / "Kavinsky" / "OutRun - 2013" / "01. Nightcall.mp3"
    final_lrc = music_dir / "Kavinsky" / "OutRun - 2013" / "01. Nightcall.lrc"
    final_cover = music_dir / "Kavinsky" / "OutRun - 2013" / "cover.jpg"

    assert final_mp3.exists()
    assert final_lrc.exists()
    assert final_cover.exists()
    assert final_cover.read_bytes() == DUMMY_JPEG

    # Временный каталог задачи должен быть очищен
    assert not (tmp_dir / job_id).exists()


def test_reorganize_existing_library_tracks_and_cleanup(
    organizer: tuple[OrganizerService, TagService, Path, Path],
):
    """Проверка реорганизации существующих файлов в медиатеке и удаления опустевших папок."""
    org, _, music_dir, _ = organizer

    # Создаем существующий трек в старой папке
    old_album_dir = music_dir / "OldArtist" / "OldAlbum - 1999"
    old_album_dir.mkdir(parents=True, exist_ok=True)
    old_mp3 = old_album_dir / "01. OldSong.mp3"
    old_mp3.write_bytes(MINIMAL_MP3)
    old_txt = old_album_dir / "01. OldSong.txt"
    old_txt.write_text("plain lyrics", encoding="utf-8")

    album = AlbumTags(
        album_artist="NewArtist",
        album="NewAlbum",
        year=2024,
    )
    track = TrackTags(
        path="OldArtist/OldAlbum - 1999/01. OldSong.mp3",
        track_number=2,
        title="RenamedSong",
        artist="NewArtist",
    )

    # 1. Если reorganize=False: пути не меняются
    res_no_reorg = org.organize_album(
        album=album,
        tracks=[track],
        source_dir="OldArtist/OldAlbum - 1999",
        reorganize=False,
    )
    assert res_no_reorg.destination_dir == "OldArtist/OldAlbum - 1999"
    assert res_no_reorg.tracks[0].path == "OldArtist/OldAlbum - 1999/01. OldSong.mp3"
    assert old_mp3.exists()

    # 2. Если reorganize=True: файлы перемещаются в новую структуру
    res_reorg = org.organize_album(
        album=album,
        tracks=[track],
        source_dir="OldArtist/OldAlbum - 1999",
        reorganize=True,
    )

    new_album_dir = "NewArtist/NewAlbum - 2024"
    new_track_rel = f"{new_album_dir}/02. RenamedSong.mp3"
    assert res_reorg.destination_dir == new_album_dir
    assert res_reorg.tracks[0].path == new_track_rel

    new_mp3 = music_dir / "NewArtist" / "NewAlbum - 2024" / "02. RenamedSong.mp3"
    new_txt = music_dir / "NewArtist" / "NewAlbum - 2024" / "02. RenamedSong.txt"
    assert new_mp3.exists()
    assert new_txt.exists()

    # Старые директории должны быть полностью удалены, так как они опустели
    assert not old_album_dir.exists()
    assert not (music_dir / "OldArtist").exists()


def test_organize_missing_track_raises_not_found(
    organizer: tuple[OrganizerService, TagService, Path, Path],
):
    """Проверка ошибки NotFoundError при отсутствии исходного файла."""
    org, _, _, _ = organizer
    album = AlbumTags(album="A", album_artist="B")
    track = TrackTags(path="missing/track.mp3", track_number=1, title="Missing")

    with pytest.raises(NotFoundError):
        org.organize_album(album=album, tracks=[track], source_dir="missing", reorganize=True)


def test_track_swap_in_place_no_data_loss(
    organizer: tuple[OrganizerService, TagService, Path, Path],
):
    """Защита от потери данных: перестановка номеров треков местами в рамках одного альбома."""
    org, _, music_dir, _ = organizer

    album_dir = music_dir / "Artist" / "Album - 2020"
    album_dir.mkdir(parents=True, exist_ok=True)
    t1 = album_dir / "01. Song A.mp3"
    t2 = album_dir / "02. Song B.mp3"
    t1.write_bytes(b"CONTENT_A")
    t2.write_bytes(b"CONTENT_B")

    # Меняем треки местами: Track 1 становится номером 2 с названием Song B, Track 2 - номером 1 с Song A
    album = AlbumTags(album_artist="Artist", album="Album", year=2020)
    track1 = TrackTags(
        path="Artist/Album - 2020/01. Song A.mp3",
        track_number=2,
        title="Song B",
        artist="Artist",
    )
    track2 = TrackTags(
        path="Artist/Album - 2020/02. Song B.mp3",
        track_number=1,
        title="Song A",
        artist="Artist",
    )

    result = org.organize_album(
        album=album,
        tracks=[track1, track2],
        source_dir="Artist/Album - 2020",
        reorganize=True,
    )

    assert len(result.tracks) == 2
    final_01 = music_dir / "Artist" / "Album - 2020" / "01. Song A.mp3"
    final_02 = music_dir / "Artist" / "Album - 2020" / "02. Song B.mp3"

    assert final_01.exists()
    assert final_02.exists()
    assert final_01.read_bytes() == b"CONTENT_B"
    assert final_02.read_bytes() == b"CONTENT_A"


def test_reorganize_library_with_cover_cleans_up_old_dir(
    organizer: tuple[OrganizerService, TagService, Path, Path],
):
    """Проверка перемещения cover.jpg и полного удаления старого каталога."""
    org, _, music_dir, _ = organizer

    old_album_dir = music_dir / "OldArtist" / "OldAlbum - 2000"
    old_album_dir.mkdir(parents=True, exist_ok=True)
    t_old = old_album_dir / "01. Song.mp3"
    t_old.write_bytes(MINIMAL_MP3)
    c_old = old_album_dir / "cover.jpg"
    c_old.write_bytes(DUMMY_JPEG)

    album = AlbumTags(album_artist="NewArtist", album="NewAlbum", year=2024)
    track = TrackTags(
        path="OldArtist/OldAlbum - 2000/01. Song.mp3",
        track_number=1,
        title="Song",
        artist="NewArtist",
    )

    res = org.organize_album(
        album=album,
        tracks=[track],
        source_dir="OldArtist/OldAlbum - 2000",
        cover_path="OldArtist/OldAlbum - 2000/cover.jpg",
        reorganize=True,
    )

    assert res.destination_dir == "NewArtist/NewAlbum - 2024"
    # Старый каталог и его родитель должны быть полностью удалены
    assert not old_album_dir.exists()
    assert not (music_dir / "OldArtist").exists()

    # Новый каталог должен содержать перенесенные трек и обложку
    new_dir = music_dir / "NewArtist" / "NewAlbum - 2024"
    assert (new_dir / "01. Song.mp3").exists()
    assert (new_dir / "cover.jpg").exists()
    assert (new_dir / "cover.jpg").read_bytes() == DUMMY_JPEG


def test_compilation_album_tracks_stay_in_single_directory(
    organizer: tuple[OrganizerService, TagService, Path, Path],
):
    """Сборники (Various Artists): все треки альбома сохраняются в единую папку."""
    org, _, music_dir, _ = organizer

    draft_dir = music_dir / "Drafts"
    draft_dir.mkdir(parents=True, exist_ok=True)
    (draft_dir / "d1.mp3").write_bytes(MINIMAL_MP3)
    (draft_dir / "d2.mp3").write_bytes(MINIMAL_MP3)

    album = AlbumTags(album="Greatest Hits 2024", year=2024)  # no album_artist
    tr1 = TrackTags(path="Drafts/d1.mp3", track_number=1, title="Song 1", artist="Artist Alpha")
    tr2 = TrackTags(path="Drafts/d2.mp3", track_number=2, title="Song 2", artist="Artist Beta")

    res = org.organize_album(album=album, tracks=[tr1, tr2], source_dir="Drafts", reorganize=True)

    expected_dir = "Various Artists/Greatest Hits 2024 - 2024"
    assert res.destination_dir == expected_dir
    assert res.tracks[0].path == f"{expected_dir}/01. Song 1.mp3"
    assert res.tracks[1].path == f"{expected_dir}/02. Song 2.mp3"
    assert (music_dir / res.tracks[0].path).exists()
    assert (music_dir / res.tracks[1].path).exists()


def test_duplicate_target_paths_deduplicated_safely(
    organizer: tuple[OrganizerService, TagService, Path, Path],
):
    """Защита от перезаписи: два трека с одинаковыми номерами дорожек и названиями получают уникальные имена."""
    org, _, music_dir, _ = organizer

    draft_dir = music_dir / "Dupes"
    draft_dir.mkdir(parents=True, exist_ok=True)
    (draft_dir / "f1.mp3").write_bytes(b"DATA_1")
    (draft_dir / "f2.mp3").write_bytes(b"DATA_2")

    album = AlbumTags(album_artist="Artist", album="Album", year=2020)
    tr1 = TrackTags(path="Dupes/f1.mp3", track_number=1, title="Intro", artist="Artist")
    tr2 = TrackTags(path="Dupes/f2.mp3", track_number=1, title="Intro", artist="Artist")

    res = org.organize_album(album=album, tracks=[tr1, tr2], source_dir="Dupes", reorganize=True)

    assert len(res.tracks) == 2
    assert res.tracks[0].path != res.tracks[1].path

    file1 = music_dir / res.tracks[0].path
    file2 = music_dir / res.tracks[1].path
    assert file1.exists()
    assert file2.exists()
    contents = {file1.read_bytes(), file2.read_bytes()}
    assert contents == {b"DATA_1", b"DATA_2"}


def test_organize_preserves_preexisting_unrelated_file_on_disk(
    organizer: tuple[OrganizerService, TagService, Path, Path],
):
    """Защита от потери данных: предсуществующий файл на диске не перезаписывается новым треком."""
    org, _, music_dir, tmp_dir = organizer

    # Создаем существующий трек в целевом альбоме медиатеки
    album_dir = music_dir / "Artist" / "Album - 2020"
    album_dir.mkdir(parents=True, exist_ok=True)
    existing_file = album_dir / "01. Intro.mp3"
    existing_file.write_bytes(b"EXISTING_TRACK_DATA")

    # Организуем новый трек из временного хранилища с совпадающим именем
    job_dir = tmp_dir / "job_new" / "converted"
    job_dir.mkdir(parents=True, exist_ok=True)
    new_track_file = job_dir / "download.mp3"
    new_track_file.write_bytes(b"NEW_DOWNLOAD_DATA")

    album = AlbumTags(album_artist="Artist", album="Album", year=2020)
    new_track = TrackTags(
        path="job_new/converted/download.mp3",
        track_number=1,
        title="Intro",
        artist="Artist",
    )

    res = org.organize_album(album=album, tracks=[new_track], source_dir="job_new/converted")

    # Проверяем, что исходный файл не был уничтожен
    assert existing_file.exists()
    assert existing_file.read_bytes() == b"EXISTING_TRACK_DATA"

    # Новый трек должен получить уникальное имя (2)
    expected_new_path = "Artist/Album - 2020/01. Intro (2).mp3"
    assert len(res.tracks) == 1
    assert res.tracks[0].path == expected_new_path
    new_final_file = music_dir / expected_new_path
    assert new_final_file.exists()
    assert new_final_file.read_bytes() == b"NEW_DOWNLOAD_DATA"


def test_organize_saves_cover_when_reorganize_false(
    organizer: tuple[OrganizerService, TagService, Path, Path],
):
    """Проверка сохранения новой обложки в папку альбома даже при reorganize=False."""
    org, _, music_dir, tmp_dir = organizer

    album_dir = music_dir / "Modjo" / "Modjo"
    album_dir.mkdir(parents=True, exist_ok=True)
    track_file = album_dir / "01. Lady.mp3"
    track_file.write_bytes(MINIMAL_MP3)

    new_cover = tmp_dir / "new_cover.jpg"
    new_cover.write_bytes(DUMMY_JPEG)

    album = AlbumTags(album_artist="Modjo", album="Modjo")
    track = TrackTags(path="Modjo/Modjo/01. Lady.mp3", track_number=1, title="Lady", artist="Modjo")

    res = org.organize_album(
        album=album,
        tracks=[track],
        source_dir="Modjo/Modjo",
        cover_path="new_cover.jpg",
        reorganize=False,
    )

    assert res.destination_dir == "Modjo/Modjo"
    dest_cover = album_dir / "cover.jpg"
    assert dest_cover.exists()
    assert dest_cover.read_bytes() == DUMMY_JPEG


def test_organize_handles_case_only_rename(
    organizer: tuple[OrganizerService, TagService, Path, Path],
):
    """Проверка переименования регистра имени файла через промежуточный стейджинг."""
    org, _, music_dir, _ = organizer

    album_dir = music_dir / "Artist" / "Album"
    album_dir.mkdir(parents=True, exist_ok=True)
    track_file = album_dir / "01. song.mp3"
    track_file.write_bytes(MINIMAL_MP3)

    album = AlbumTags(album_artist="Artist", album="Album")
    track = TrackTags(path="Artist/Album/01. song.mp3", track_number=1, title="Song", artist="Artist")

    res = org.organize_album(album=album, tracks=[track], source_dir="Artist/Album", reorganize=True)

    expected_rel = "Artist/Album/01. Song.mp3"
    assert res.tracks[0].path == expected_rel
    assert (music_dir / expected_rel).exists()


def test_cleanup_empty_directories_with_non_substantive_subdirectories(
    organizer: tuple[OrganizerService, TagService, Path, Path],
):
    """Проверка удаления директории, содержащей служебные подпапки (.stage_...)."""
    org, _, music_dir, _ = organizer

    old_dir = music_dir / "OldArtist" / "OldAlbum"
    old_dir.mkdir(parents=True, exist_ok=True)

    # Создаем служебную подпапку со стейджингом
    junk_dir = old_dir / ".stage_abc123_temp"
    junk_dir.mkdir()
    (junk_dir / ".DS_Store").write_bytes(b"garbage")

    org.cleanup_empty_directories(old_dir)
    assert not old_dir.exists()
    assert not (music_dir / "OldArtist").exists()


def test_concurrent_album_reorganization_serialized(
    organizer: tuple[OrganizerService, TagService, Path, Path],
):
    """Проверка потокобезопасности при параллельной реорганизации треков одного альбома."""
    import concurrent.futures

    org, _, music_dir, tmp_dir = organizer

    # Создаем 4 трека в разных источниках
    tracks: list[TrackTags] = []
    for i in range(1, 5):
        j_dir = tmp_dir / f"job_{i}" / "converted"
        j_dir.mkdir(parents=True, exist_ok=True)
        t_file = j_dir / f"{i}.mp3"
        t_file.write_bytes(f"DATA_{i}".encode())
        tracks.append(
            TrackTags(
                path=f"job_{i}/converted/{i}.mp3",
                track_number=i,
                title=f"Track {i}",
                artist="SyncArtist",
            )
        )

    album = AlbumTags(album_artist="SyncArtist", album="SyncAlbum", year=2025)

    def run_reorg(t: TrackTags) -> str:
        res = org.organize_album(album=album, tracks=[t], source_dir=str(Path(t.path).parent))
        return res.tracks[0].path

    with concurrent.futures.ThreadPoolExecutor(max_workers=4) as executor:
        futures = [executor.submit(run_reorg, t) for t in tracks]
        results = [f.result() for f in futures]

    assert len(results) == 4
    for t_rel in results:
        assert (music_dir / t_rel).exists()

