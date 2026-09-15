"""Модульные тесты для TagService (ID3v2.4, обложки, мультитеги, песочница)."""

from pathlib import Path

import pytest
from mutagen.id3 import (
    ID3,
    SYLT,
    TIT2,
    USLT,
    PictureType,
)

from app.core.exceptions import (
    NotFoundError,
    PathTraversalError,
    TagError,
    TagWriteError,
)
from app.core.sandbox import PathSandbox
from app.schemas.tag import AlbumTags, TrackTags
from app.services.tag_service import TagService

MINIMAL_MP3 = b"\xff\xfb\x90\x00" + b"\x00" * 300
DUMMY_JPEG = b"\xff\xd8\xff\xe0\x00\x10JFIF" + b"\x00" * 64
DUMMY_PNG = b"\x89PNG\r\n\x1a\n" + b"\x00" * 64


@pytest.fixture
def service_and_paths(temp_storage: dict[str, Path]):
    """Создает песочницы и сервис TagService для тестов."""
    music_sb = PathSandbox(temp_storage["music_dir"])
    tmp_sb = PathSandbox(temp_storage["tmp_dir"])
    svc = TagService(music_sandbox=music_sb, tmp_sandbox=tmp_sb)
    return svc, temp_storage["music_dir"], temp_storage["tmp_dir"]


def test_parse_and_format_multivalue(service_and_paths):
    """Проверка парсинга и форматирования множественных значений тегов."""
    svc, _, _ = service_and_paths

    # Граничные случаи парсинга
    assert svc.parse_multivalue("") == []
    assert svc.parse_multivalue(None) == []
    assert svc.parse_multivalue("   ") == []
    assert svc.parse_multivalue("  \\\\  ") == []
    assert svc.parse_multivalue("Single Artist") == ["Single Artist"]

    # Двойной слэш
    assert svc.parse_multivalue("Daft Punk\\\\Romanthony") == ["Daft Punk", "Romanthony"]
    assert svc.parse_multivalue("  Artist 1  \\\\  Artist 2  ") == ["Artist 1", "Artist 2"]

    # Одинарный слэш и null-байты
    assert svc.parse_multivalue("Artist 1\\Artist 2") == ["Artist 1", "Artist 2"]
    assert svc.parse_multivalue("Artist 1\x00Artist 2") == ["Artist 1", "Artist 2"]

    # Ведущие и завершающие разделители
    assert svc.parse_multivalue("\\\\Artist 1\\\\Artist 2\\\\") == ["Artist 1", "Artist 2"]

    # Форматирование
    assert svc.format_multivalue([]) == ""
    assert svc.format_multivalue(["Single"]) == "Single"
    assert svc.format_multivalue(["Daft Punk", "Romanthony"]) == "Daft Punk\\\\Romanthony"
    assert svc.format_multivalue(["  Artist A  ", " Artist B "]) == "Artist A\\\\Artist B"
    assert svc.format_multivalue(["Artist A", "", "Artist B"]) == "Artist A\\\\Artist B"


def test_read_tags_no_header_fallback(service_and_paths):
    """Проверка считывания файла без ID3 заголовка (fallback ID3NoHeaderError)."""
    svc, music_dir, _ = service_and_paths
    test_file = music_dir / "untagged.mp3"
    test_file.write_bytes(MINIMAL_MP3)

    tags = svc.read_tags(test_file, rel_path="untagged.mp3")
    assert tags.title == ""
    assert tags.artist == ""
    assert tags.album == ""
    assert tags.album_artist == ""
    assert tags.track_number == 1
    assert tags.year is None
    assert tags.genre is None
    assert tags.has_cover is False
    assert tags.lyrics_status == "none"


def test_write_and_read_tags_strict_id3v24(service_and_paths):
    """Проверка записи и чтения тегов стандарта ID3v2.4.0."""
    svc, music_dir, _ = service_and_paths
    test_file = music_dir / "test_track.mp3"
    test_file.write_bytes(MINIMAL_MP3)

    track_in = TrackTags(
        path="test_track.mp3",
        track_number=3,
        title="Harder, Better, Faster, Stronger",
        artist="Daft Punk\\\\Guy-Manuel",
        album="Discovery",
        album_artist="Daft Punk",
        year=2001,
        genre="French House\\\\Electronic",
    )

    svc.write_tags(test_file, track_in)

    # 1. Проверяем заголовок ID3v2.4 на уровне сырых байтов
    raw_bytes = test_file.read_bytes()
    header_5 = list(raw_bytes[:5])
    assert header_5 == [0x49, 0x44, 0x33, 0x04, 0x00], f"Заголовок не ID3v2.4.0: {header_5}"

    # 2. Проверяем через mutagen, что фреймы сохранены списком
    id3 = ID3(str(test_file))
    assert id3["TIT2"].text == ["Harder, Better, Faster, Stronger"]
    assert id3["TPE1"].text == ["Daft Punk", "Guy-Manuel"]
    assert id3["TALB"].text == ["Discovery"]
    assert id3["TPE2"].text == ["Daft Punk"]
    assert id3["TRCK"].text == ["3"]
    assert [str(x) for x in id3["TDRC"].text] == ["2001"]
    assert id3["TCON"].text == ["French House", "Electronic"]

    # 3. Считываем обратно через TagService.read_tags
    track_out = svc.read_tags(test_file, rel_path="test_track.mp3")
    assert track_out.title == "Harder, Better, Faster, Stronger"
    assert track_out.artist == "Daft Punk\\\\Guy-Manuel"
    assert track_out.album == "Discovery"
    assert track_out.album_artist == "Daft Punk"
    assert track_out.track_number == 3
    assert track_out.year == 2001
    assert track_out.genre == "French House\\\\Electronic"


def test_strip_lyrics_frames(service_and_paths):
    """Инвариант: кадры текстов песен (USLT, SYLT) безусловно удаляются из MP3."""
    svc, music_dir, _ = service_and_paths
    test_file = music_dir / "with_lyrics.mp3"
    test_file.write_bytes(MINIMAL_MP3)

    # Внедряем USLT и SYLT вручную
    id3 = ID3()
    id3.add(TIT2(encoding=3, text=["Track with lyrics"]))
    id3.add(USLT(encoding=3, lang="eng", desc="", text="Unsynced lyrics content"))
    id3.add(SYLT(encoding=3, lang="eng", format=1, type=1, desc="", text=[("synced line", 1000)]))
    id3.save(str(test_file), v2_version=4)

    # Убеждаемся, что они присутствуют
    check_id3 = ID3(str(test_file))
    assert len(check_id3.getall("USLT")) > 0
    assert len(check_id3.getall("SYLT")) > 0

    # Перезаписываем теги через TagService
    track = TrackTags(path="with_lyrics.mp3", title="Sanitized Track", artist="Artist")
    svc.write_tags(test_file, track)

    # Проверяем, что тексты были вычищены
    sanitized_id3 = ID3(str(test_file))
    assert len(sanitized_id3.getall("USLT")) == 0
    assert len(sanitized_id3.getall("SYLT")) == 0


def test_embed_and_read_cover(service_and_paths):
    """Проверка встраивания и чтения фронтальной обложки (APIC type=3)."""
    svc, music_dir, _ = service_and_paths
    test_file = music_dir / "cover_test.mp3"
    test_file.write_bytes(MINIMAL_MP3)

    # Встраиваем JPEG через write_tags
    track = TrackTags(path="cover_test.mp3", title="Track with Cover")
    svc.write_tags(test_file, track, cover_bytes=DUMMY_JPEG, cover_mime="image/jpeg")

    # Проверяем кадр APIC в mutagen
    id3 = ID3(str(test_file))
    apics = id3.getall("APIC")
    assert len(apics) == 1
    assert int(apics[0].type) == int(PictureType.COVER_FRONT)
    assert apics[0].mime == "image/jpeg"
    assert apics[0].data == DUMMY_JPEG

    # Читаем обложку через read_cover
    cover_res = svc.read_cover(test_file)
    assert cover_res is not None
    data, mime = cover_res
    assert data == DUMMY_JPEG
    assert mime == "image/jpeg"

    # Проверяем embed_cover с PNG
    svc.embed_cover(test_file, DUMMY_PNG, mime="image/png")
    png_res = svc.read_cover(test_file)
    assert png_res is not None
    p_data, p_mime = png_res
    assert p_data == DUMMY_PNG
    assert p_mime == "image/png"


def test_read_cover_from_directory(service_and_paths):
    """Проверка извлечения обложки из каталога (cover.jpg, folder.png)."""
    svc, music_dir, _ = service_and_paths
    album_dir = music_dir / "Daft Punk" / "Discovery"
    album_dir.mkdir(parents=True, exist_ok=True)

    # Без файла обложки
    assert svc.read_cover(album_dir) is None

    # С файлом cover.jpg
    cover_file = album_dir / "cover.jpg"
    cover_file.write_bytes(DUMMY_JPEG)

    res = svc.read_cover(album_dir)
    assert res is not None
    data, mime = res
    assert data == DUMMY_JPEG
    assert mime == "image/jpeg"

    # Проверка вызова read_cover на MP3-файле без встроенного APIC
    mp3_file = album_dir / "01. One More Time.mp3"
    mp3_file.write_bytes(MINIMAL_MP3)
    res_from_mp3 = svc.read_cover(mp3_file)
    assert res_from_mp3 is not None
    assert res_from_mp3[0] == DUMMY_JPEG


def test_apply_album_defaults(service_and_paths):
    """Проверка двухуровневого наследования тегов альбома треками."""
    svc, _, _ = service_and_paths

    album = AlbumTags(
        album_artist="Daft Punk",
        album="Discovery",
        year=2001,
        genre="Electronic",
    )

    tracks = [
        TrackTags(path="t1.mp3", track_number=1, title="One More Time", artist="Daft Punk"),
        TrackTags(path="t2.mp3", track_number=2, title="Aerodynamic", artist="Daft Punk", genre="Rock"),
    ]

    updated = svc.apply_album_defaults(tracks, album)
    assert len(updated) == 2

    # Трек 1 получил все поля альбома
    assert updated[0].album == "Discovery"
    assert updated[0].album_artist == "Daft Punk"
    assert updated[0].year == 2001
    assert updated[0].genre == "Electronic"

    # Трек 2 получил поля альбома
    assert updated[1].album == "Discovery"
    assert updated[1].album_artist == "Daft Punk"
    assert updated[1].year == 2001
    assert updated[1].genre == "Electronic"


def test_read_album_details(service_and_paths):
    """Проверка агрегации альбома и сканирования треков (read_album_details)."""
    svc, music_dir, _ = service_and_paths
    album_dir = music_dir / "Justice" / "Cross"
    album_dir.mkdir(parents=True, exist_ok=True)

    t1 = album_dir / "02. D.A.N.C.E.mp3"
    t2 = album_dir / "01. Genesis.mp3"
    t1.write_bytes(MINIMAL_MP3)
    t2.write_bytes(MINIMAL_MP3)

    svc.write_tags(
        t1,
        TrackTags(
            path="Justice/Cross/02. D.A.N.C.E.mp3",
            track_number=2,
            title="D.A.N.C.E.",
            artist="Justice",
            album="Cross",
            album_artist="Justice",
            year=2007,
            genre="Electro House",
        ),
    )
    svc.write_tags(
        t2,
        TrackTags(
            path="Justice/Cross/01. Genesis.mp3",
            track_number=1,
            title="Genesis",
            artist="Justice",
            album="Cross",
            album_artist="Justice",
            year=2007,
            genre="Electro House",
        ),
    )

    # Добавляем внешний текст для первого трека
    lrc_file = album_dir / "01. Genesis.lrc"
    lrc_file.write_text("[00:01.00] Intro", encoding="utf-8")

    # Добавляем обложку
    cover_file = album_dir / "cover.png"
    cover_file.write_bytes(DUMMY_PNG)

    details = svc.read_album_details("Justice/Cross")
    assert details.album.album == "Cross"
    assert details.album.album_artist == "Justice"
    assert details.album.year == 2007
    assert details.album.genre == "Electro House"
    assert details.has_cover is True
    assert details.cover_path == "Justice/Cross/cover.png"

    # Треки должны быть отсортированы по track_number
    assert len(details.tracks) == 2
    assert details.tracks[0].track_number == 1
    assert details.tracks[0].title == "Genesis"
    assert details.tracks[0].lyrics_status == "lrc"
    assert details.tracks[1].track_number == 2
    assert details.tracks[1].title == "D.A.N.C.E."
    assert details.tracks[1].lyrics_status == "none"


def test_read_album_details_single_file(service_and_paths):
    """Проверка вызова read_album_details для одиночного MP3 файла."""
    svc, music_dir, _ = service_and_paths
    single = music_dir / "single.mp3"
    single.write_bytes(MINIMAL_MP3)
    svc.write_tags(
        single,
        TrackTags(
            path="single.mp3",
            track_number=1,
            title="Solo",
            artist="Soloist",
            album="Single Album",
            year=2020,
        ),
    )

    res = svc.read_album_details("single.mp3")
    assert res.album.album == "Single Album"
    assert res.album.album_artist == "Soloist"
    assert len(res.tracks) == 1
    assert res.tracks[0].title == "Solo"


def test_read_album_details_errors(service_and_paths):
    """Проверка ошибок при сканировании несуществующих или неверных путей."""
    svc, music_dir, _ = service_and_paths

    with pytest.raises(NotFoundError):
        svc.read_album_details("non_existent_folder")

    txt_file = music_dir / "notes.txt"
    txt_file.write_text("not audio", encoding="utf-8")
    with pytest.raises(TagError):
        svc.read_album_details("notes.txt")


def test_resolve_path_sandbox_protection(service_and_paths):
    """Проверка защиты песочницы при разрешении путей в TagService."""
    svc, music_dir, tmp_dir = service_and_paths

    # Корректный путь в MUSIC_DIR
    resolved = svc.resolve_path("sub/folder/file.mp3")
    assert resolved == music_dir / "sub" / "folder" / "file.mp3"

    # Корректный путь в TMP_DIR с префиксом tmp_downloads/
    tmp_resolved = svc.resolve_path("tmp_downloads/job123/track.mp3")
    assert tmp_resolved == tmp_dir / "job123" / "track.mp3"

    # Попытки выхода за пределы песочницы
    with pytest.raises(PathTraversalError):
        svc.resolve_path("../../etc/passwd")

    with pytest.raises(PathTraversalError):
        svc.resolve_path("tmp_downloads/../../etc/passwd")

    with pytest.raises(PathTraversalError):
        svc.resolve_path(None)


def test_tag_write_error_on_invalid_file(service_and_paths):
    """Проверка выброса TagWriteError при ошибке записи в файл."""
    svc, music_dir, _ = service_and_paths
    bad_dir = music_dir / "directory_as_file.mp3"
    bad_dir.mkdir()

    track = TrackTags(path="directory_as_file.mp3", title="Fail")
    with pytest.raises(TagWriteError):
        svc.write_tags(bad_dir, track)

    with pytest.raises(TagWriteError):
        svc.embed_cover(bad_dir, DUMMY_JPEG)


def test_read_tags_file_not_found(service_and_paths):
    """Проверка выброса NotFoundError при отсутствии файла в read_tags."""
    svc, music_dir, _ = service_and_paths
    with pytest.raises(NotFoundError):
        svc.read_tags(music_dir / "ghost.mp3")


def test_read_album_details_discovers_nested_multi_disc_tracks(service_and_paths):
    """Проверка обнаружения треков во вложенных поддиректориях альбомов (например, CD1 / CD2)."""
    svc, music_dir, _ = service_and_paths

    album_dir = music_dir / "Pink Floyd" / "The Wall - 1979"
    cd1 = album_dir / "CD1"
    cd2 = album_dir / "CD2"
    cd1.mkdir(parents=True, exist_ok=True)
    cd2.mkdir(parents=True, exist_ok=True)

    t1 = cd1 / "01. In the Flesh.mp3"
    t2 = cd2 / "01. Hey You.mp3"
    t1.write_bytes(MINIMAL_MP3)
    t2.write_bytes(MINIMAL_MP3)

    svc.write_tags(
        t1,
        TrackTags(
            path="Pink Floyd/The Wall - 1979/CD1/01. In the Flesh.mp3",
            track_number=1,
            title="In the Flesh",
            album="The Wall",
            album_artist="Pink Floyd",
            year=1979,
        ),
    )
    svc.write_tags(
        t2,
        TrackTags(
            path="Pink Floyd/The Wall - 1979/CD2/01. Hey You.mp3",
            track_number=1,
            title="Hey You",
            album="The Wall",
            album_artist="Pink Floyd",
            year=1979,
        ),
    )

    res = svc.read_album_details("Pink Floyd/The Wall - 1979")
    assert len(res.tracks) == 2
    assert res.album.album == "The Wall"
    assert res.album.album_artist == "Pink Floyd"
    assert res.album.year == 1979
    titles = {t.title for t in res.tracks}
    assert titles == {"In the Flesh", "Hey You"}


def test_tag_service_read_cover_webp(service_and_paths):
    """R2, R3: Проверка корректного распознавания WebP обложки из APIC и из директории альбома."""
    svc, music_dir, _ = service_and_paths
    album_dir = music_dir / "WebP Album"
    album_dir.mkdir(parents=True, exist_ok=True)

    dummy_webp = b"RIFF\x20\x00\x00\x00WEBPVP8 " + b"\x00" * 32

    # 1. Поиск cover.webp в каталоге
    webp_cover_path = album_dir / "cover.webp"
    webp_cover_path.write_bytes(dummy_webp)

    res = svc.read_cover(album_dir)
    assert res is not None
    data, mime = res
    assert data == dummy_webp
    assert mime == "image/webp"

    # 2. Чтение WebP из APIC фрейма MP3 файла
    mp3_path = album_dir / "track.mp3"
    mp3_path.write_bytes(MINIMAL_MP3)
    svc.embed_cover(mp3_path, dummy_webp, mime="image/webp")

    track_cover_res = svc.read_cover(mp3_path)
    assert track_cover_res is not None
    t_data, t_mime = track_cover_res
    assert t_data == dummy_webp
    assert t_mime == "image/webp"


def test_tag_service_save_strips_id3v1(service_and_paths):
    """R3: Запись тегов и обложек через TagService обязана удалять устаревшие ID3v1 теги (v1=0)."""
    svc, music_dir, _ = service_and_paths
    track_file = music_dir / "id3v1_strip_test.mp3"
    track_file.write_bytes(MINIMAL_MP3)

    # Присоединяем ID3v1 заголовок в конец файла
    v1_tag = b"TAG" + b"Legacy Title".ljust(30, b"\x00") + b"Legacy Artist".ljust(30, b"\x00") + b"\x00" * 65
    track_file.write_bytes(track_file.read_bytes() + v1_tag)
    assert track_file.read_bytes()[-128:-125] == b"TAG"

    # Записываем теги через TagService
    svc.write_tags(
        track_file,
        TrackTags(
            path="id3v1_strip_test.mp3",
            title="Clean ID3v2.4 Title",
            artist="Clean Artist",
        ),
    )

    # ID3v1 должен быть удален
    data = track_file.read_bytes()
    assert not data.endswith(v1_tag)
    assert data[-128:-125] != b"TAG"


def test_clean_title_junk_removal():
    """Проверка очистки мусорных суффиксов из названий треков."""
    samples = [
        ("Song Name [Official Video]", "Song Name"),
        ("Track Title (1080p)", "Track Title"),
        ("Hit Single [Prod. by Metro Boomin]", "Hit Single"),
        ("Beautiful Melody (Lyric Video)", "Beautiful Melody"),
        ("Greatest Hits [Remastered 2021]", "Greatest Hits"),
        ("Album Version (Official Audio)", "Album Version"),
        ("Music Clip [Official Music Video]", "Music Clip"),
        ("Clip [Clip Officiel]", "Clip"),
        ("Cool Track [4K Visualizer]", "Cool Track"),
    ]
    for raw, expected in samples:
        cleaned, _ = TagService.clean_title(raw, "")
        assert cleaned == expected


def test_clean_title_feat_extractor():
    """Проверка извлечения соисполнителей (feat., ft., featuring) в поле артистов."""
    # 1. В скобках с разделителем &
    title, artists = TagService.clean_title("Song Title (feat. Drake & Future)", "Kendrick Lamar")
    assert title == "Song Title"
    assert artists == "Kendrick Lamar\\\\Drake\\\\Future"

    # 2. Квадратные скобки [ft. ...]
    title, artists = TagService.clean_title("Creepin' [ft. The Weeknd, 21 Savage]", "Metro Boomin")
    assert title == "Creepin'"
    assert artists == "Metro Boomin\\\\The Weeknd\\\\21 Savage"

    # 3. Без скобок в конце строки
    title, artists = TagService.clean_title("Where Are Ü Now feat. Justin Bieber", "Skrillex\\\\Diplo")
    assert title == "Where Are Ü Now"
    assert artists == "Skrillex\\\\Diplo\\\\Justin Bieber"

    # 4. Дедупликация артистов (не добавлять повторно)
    title, artists = TagService.clean_title("Hotline Bling (feat. Drake)", "Drake")
    assert title == "Hotline Bling"
    assert artists == "Drake"

    # 5. Разделение запятых в current_artist (например, "jay-z, eminem")
    title, artists = TagService.clean_title("Renegade", "jay-z, eminem")
    assert title == "Renegade"
    assert artists == "jay-z\\\\eminem"


def test_clean_title_artist_prefix_and_track_numbers():
    """Проверка удаления ведущих номеров треков и префикса артиста (Artist - Title)."""
    # Номера дорожек в начале
    assert TagService.clean_title("01. Get Lucky", "Daft Punk")[0] == "Get Lucky"
    assert TagService.clean_title("05 - Technologic", "Daft Punk")[0] == "Technologic"
    assert TagService.clean_title("12 Harder Better Faster Stronger", "Daft Punk")[0] == "Harder Better Faster Stronger"

    # Префикс совпадает с current_artist
    t, a = TagService.clean_title("Daft Punk - One More Time", "Daft Punk")
    assert t == "One More Time"
    assert a == "Daft Punk"

    # Префикс переносится в артиста, если поле артиста было пустым
    t, a = TagService.clean_title("Daft Punk - One More Time", "")
    assert t == "One More Time"
    assert a == "Daft Punk"


def test_id3v24_tpos_disc_number(service_and_paths):
    """Проверка записи и чтения номера и общего количества дисков (TPOS) в ID3v2.4."""
    svc, music_dir, _ = service_and_paths
    track_file = music_dir / "disc_test.mp3"
    track_file.write_bytes(MINIMAL_MP3)

    # 1. Запись с общим числом дисков: 1/2
    svc.write_tags(
        track_file,
        TrackTags(
            path="disc_test.mp3",
            title="Disc 1 Track",
            disc_number=1,
            disc_total=2,
            track_number=1,
        ),
    )
    tags_read = svc.read_tags(track_file, rel_path="disc_test.mp3")
    assert tags_read.disc_number == 1
    assert tags_read.disc_total == 2

    raw_id3 = ID3(str(track_file))
    assert "TPOS" in raw_id3
    assert raw_id3["TPOS"].text[0] == "1/2"

    # 2. Запись только номера диска без общего числа: 2
    svc.write_tags(
        track_file,
        TrackTags(
            path="disc_test.mp3",
            title="Disc 2 Track",
            disc_number=2,
            disc_total=None,
            track_number=1,
        ),
    )
    tags_read2 = svc.read_tags(track_file, rel_path="disc_test.mp3")
    assert tags_read2.disc_number == 2
    assert tags_read2.disc_total is None

    raw_id3_2 = ID3(str(track_file))
    assert raw_id3_2["TPOS"].text[0] == "2"


def test_id3v24_trck_total(service_and_paths):
    """Проверка записи и чтения номера дорожки с общим числом (TRCK, например 05/12)."""
    svc, music_dir, _ = service_and_paths
    track_file = music_dir / "track_total_test.mp3"
    track_file.write_bytes(MINIMAL_MP3)

    svc.write_tags(
        track_file,
        TrackTags(
            path="track_total_test.mp3",
            title="Track 5 of 12",
            track_number=5,
            track_total=12,
        ),
    )
    tags_read = svc.read_tags(track_file, rel_path="track_total_test.mp3")
    assert tags_read.track_number == 5
    assert tags_read.track_total == 12

    raw_id3 = ID3(str(track_file))
    assert "TRCK" in raw_id3
    assert raw_id3["TRCK"].text[0] == "5/12"


@pytest.mark.asyncio
async def test_download_cover(service_and_paths):
    """Проверка скачивания изображения обложки и определения MIME-типа."""
    svc, _, _ = service_and_paths
    from unittest.mock import AsyncMock, patch

    import httpx

    # 1. Скачивание валидного JPEG
    mock_resp_jpeg = AsyncMock()
    mock_resp_jpeg.status_code = 200
    mock_resp_jpeg.content = DUMMY_JPEG
    mock_resp_jpeg.headers = {"content-type": "image/jpeg"}

    with patch.object(httpx.AsyncClient, "get", new=AsyncMock(return_value=mock_resp_jpeg)):
        data, mime = await svc.download_cover("https://example.com/cover.jpg")
        assert data == DUMMY_JPEG
        assert mime == "image/jpeg"

    # 2. Скачивание валидного PNG
    mock_resp_png = AsyncMock()
    mock_resp_png.status_code = 200
    mock_resp_png.content = DUMMY_PNG
    mock_resp_png.headers = {"content-type": "image/png"}

    with patch.object(httpx.AsyncClient, "get", new=AsyncMock(return_value=mock_resp_png)):
        data, mime = await svc.download_cover("https://example.com/cover.png")
        assert data == DUMMY_PNG
        assert mime == "image/png"

    # 3. HTTP 404 вызывает TagError
    mock_resp_404 = AsyncMock()
    mock_resp_404.status_code = 404
    mock_resp_404.content = b""

    with (
        patch.object(httpx.AsyncClient, "get", new=AsyncMock(return_value=mock_resp_404)),
        pytest.raises(TagError, match="HTTP 404"),
    ):
        await svc.download_cover("https://example.com/missing.jpg")


@pytest.mark.asyncio
async def test_search_covers_aggregation(service_and_paths):
    """Проверка параллельного поиска обложек в мульти-источниках (Apple, Deezer, Yandex, Spotify)."""
    svc, _, _ = service_and_paths
    from unittest.mock import MagicMock, patch

    import httpx

    def mock_get_dispatch(url, *args, **kwargs):
        mock_resp = MagicMock()
        mock_resp.status_code = 200
        url_str = str(url)
        if "itunes.apple.com" in url_str:
            mock_resp.json.return_value = {
                "results": [
                    {
                        "collectionName": "Discovery",
                        "artistName": "Daft Punk",
                        "artworkUrl100": "https://is1-ssl.mzstatic.com/image/thumb/100x100bb.jpg",
                        "releaseDate": "2001-03-07T08:00:00Z",
                    }
                ]
            }
        elif "api.deezer.com" in url_str:
            mock_resp.json.return_value = {
                "data": [
                    {
                        "title": "Discovery",
                        "artist": {"name": "Daft Punk"},
                        "cover_xl": "https://e-cdns-images.dzcdn.net/images/cover/1400x1400.jpg",
                    }
                ]
            }
        elif "music.yandex.ru" in url_str:
            mock_resp.json.return_value = {
                "albums": {
                    "items": [
                        {
                            "title": "Discovery",
                            "artists": [{"name": "Daft Punk"}],
                            "coverUri": "avatars.yandex.net/get-music-content/123/%%",
                            "year": 2001,
                        }
                    ]
                }
            }
        elif "open.spotify.com" in url_str:
            mock_resp.text = '<img src="https://i.scdn.co/image/ab67616d0000b273discovery123"/>'
        else:
            mock_resp.status_code = 404
        return mock_resp

    with patch.object(httpx.AsyncClient, "get", side_effect=mock_get_dispatch):
        results = await svc.search_covers("Discovery Daft Punk", limit=10)
        assert len(results) >= 3
        sources = {r.source for r in results}
        assert "apple" in sources
        assert "deezer" in sources
        assert "yandex" in sources

        # Проверка апскейла Apple Music с 100x100bb до 1400x1400bb
        apple_item = next(r for r in results if r.source == "apple")
        assert "1400x1400bb.jpg" in apple_item.cover_url
        assert apple_item.width == 1400
        assert apple_item.year == 2001

        # Проверка форматирования Яндекс Музыки до 1000x1000
        yandex_item = next(r for r in results if r.source == "yandex")
        assert "1000x1000" in yandex_item.cover_url
        assert yandex_item.width == 1000


def test_genre_formatting_and_hip_hop_normalization():
    """Проверка разделения жанров по разделителям и канонической записи Hip-Hop."""
    from app.schemas.tag import format_genres, normalize_genre_item

    # Hip-Hop канонический вид через дефис
    assert normalize_genre_item("Hip Hop") == "Hip-Hop"
    assert normalize_genre_item("hip hop") == "Hip-Hop"
    assert normalize_genre_item("hip-hop") == "Hip-Hop"
    assert normalize_genre_item("HIP HOP") == "Hip-Hop"
    assert normalize_genre_item("hiphop") == "Hip-Hop"
    assert normalize_genre_item("хип хоп") == "Hip-Hop"
    assert normalize_genre_item("хип-хоп") == "Hip-Hop"
    assert normalize_genre_item("Russian Hip Hop") == "Russian Hip-Hop"

    # Не затрагивает сторонние жанры
    assert normalize_genre_item("Trip Hop") == "Trip Hop"
    assert normalize_genre_item("R&B") == "R&B"
    assert normalize_genre_item("Post-Punk") == "Post-Punk"

    # Разделение по слешам (Deezer: "Rap/Hip Hop")
    assert format_genres("Rap/Hip Hop") == "Rap\\\\Hip-Hop"
    assert format_genres("Hip-Hop/Rap") == "Hip-Hop\\\\Rap"

    # Разделение по запятым, точкам с запятой, пайпам, null-байтам и бэкслешам
    assert format_genres("Hip Hop, Rap; Trap | Drill") == "Hip-Hop\\\\Rap\\\\Trap\\\\Drill"
    assert format_genres("Rap\x00Hip Hop") == "Rap\\\\Hip-Hop"
    assert format_genres("Rap\\\\Hip Hop") == "Rap\\\\Hip-Hop"

    # Списки жанров (из внешних API)
    assert format_genres(["Rap/Hip Hop", "Trap"]) == "Rap\\\\Hip-Hop\\\\Trap"
    assert format_genres(["hip hop", "Rap"]) == "Hip-Hop\\\\Rap"

    # Дедупликация без учета регистра
    assert format_genres("Rap\\\\Hip-Hop\\\\rap\\\\hip hop") == "Rap\\\\Hip-Hop"

    # Пустые значения
    assert format_genres(None) is None
    assert format_genres("") is None
    assert format_genres("   ") is None


def test_genre_schema_validation_and_id3_roundtrip(service_and_paths):
    """Проверка автоматической нормализации в Pydantic и сохранения в ID3v2.4."""
    svc, music_dir, _ = service_and_paths
    test_file = music_dir / "genre_test.mp3"
    test_file.write_bytes(MINIMAL_MP3)

    # Pydantic schema validation: слеши и "Hip Hop" мгновенно приводятся к каноническому виду
    album = AlbumTags(album="Test", album_artist="Artist", genre="Rap/Hip Hop, Trap")
    assert album.genre == "Rap\\\\Hip-Hop\\\\Trap"

    track = TrackTags(
        path="genre_test.mp3",
        title="Test Title",
        artist="Artist",
        album="Test",
        genre="Hip Hop/Rap",
    )
    assert track.genre == "Hip-Hop\\\\Rap"

    # Проверка сохранения оригинального регистра не-Hip-Hop жанров
    track_lower = TrackTags(
        path="genre_test.mp3",
        title="Test Title",
        artist="Artist",
        album="Test",
        genre="hip hop/rap",
    )
    assert track_lower.genre == "Hip-Hop\\\\rap"

    # Запись в ID3v2.4
    svc.write_tags(test_file, track)

    # Проверка сырого TCON фрейма в mutagen: список ["Hip-Hop", "Rap"]
    id3 = ID3(str(test_file))
    assert id3["TCON"].text == ["Hip-Hop", "Rap"]

    # Чтение обратно через TagService
    read_back = svc.read_tags(test_file, rel_path="genre_test.mp3")
    assert read_back.genre == "Hip-Hop\\\\Rap"


def test_genre_canonicalize_rules():
    """Инвариант: проверка правил канонизации и схлопывания жанров."""
    from app.schemas.tag import canonicalize_genre

    # 1. Rock collapse: любой rock/рок схлопывается в Rock
    assert canonicalize_genre("Rock") == "Rock"
    assert canonicalize_genre("rock") == "Rock"
    assert canonicalize_genre("Alternative Rock") == "Rock"
    assert canonicalize_genre("Indie Rock") == "Rock"
    assert canonicalize_genre("Pop-Rock") == "Rock"
    assert canonicalize_genre("Alternative \\ Rock") == "Rock"
    assert canonicalize_genre("Alternative \\\\ Rock") == "Rock"
    assert canonicalize_genre("Alternative \\ Indie Rock \\ Rock") == "Rock"
    assert canonicalize_genre("Hard Rock") == "Rock"
    assert canonicalize_genre("Punk Rock") == "Rock"
    assert canonicalize_genre("Рок") == "Rock"
    assert canonicalize_genre("рок") == "Rock"
    assert canonicalize_genre("Русский рок") == "Rock"
    assert canonicalize_genre("Альтернативный рок") == "Rock"
    assert canonicalize_genre("панк-рок") == "Rock"
    assert canonicalize_genre("тяжелый рок") == "Rock"
    assert canonicalize_genre(r"Поп\\Рок") == "Rock"
    assert canonicalize_genre("Рок / Поп") == "Rock"

    # 2. Hip-Hop / Pop / Rap collapse: схлопывается в Hip-Hop
    assert canonicalize_genre("Hip-Hop") == "Hip-Hop"
    assert canonicalize_genre("hip hop") == "Hip-Hop"
    assert canonicalize_genre("хип-хоп") == "Hip-Hop"
    assert canonicalize_genre("хип хоп") == "Hip-Hop"
    assert canonicalize_genre("хипхоп") == "Hip-Hop"
    assert canonicalize_genre("Russian Hip-Hop") == "Hip-Hop"
    assert canonicalize_genre("русский хип-хоп") == "Hip-Hop"
    assert canonicalize_genre("Rap") == "Hip-Hop"
    assert canonicalize_genre("рэп") == "Hip-Hop"
    assert canonicalize_genre("Русский рэп") == "Hip-Hop"
    assert canonicalize_genre("Pop") == "Hip-Hop"
    assert canonicalize_genre("pop") == "Hip-Hop"
    assert canonicalize_genre("поп") == "Hip-Hop"
    assert canonicalize_genre("Поп-музыка") == "Hip-Hop"
    assert canonicalize_genre("русский поп") == "Hip-Hop"
    assert canonicalize_genre("Synthpop") == "Hip-Hop"
    assert canonicalize_genre("K-Pop") == "Hip-Hop"
    assert canonicalize_genre("Rap \\ Hip-Hop") == "Hip-Hop"
    assert canonicalize_genre("Pop \\ Hip-Hop") == "Hip-Hop"

    # 3. Остальные жанры: маппинг и удаление пробелов вокруг \\
    assert canonicalize_genre("Джаз") == "Jazz"
    assert canonicalize_genre("Электроника") == "Electronic"
    assert canonicalize_genre("Танцевальная музыка") == "Dance Music"
    assert canonicalize_genre("Electronic \\ Dance") == "Electronic\\\\Dance"
    assert canonicalize_genre("Electronic \\\\ Dance") == "Electronic\\\\Dance"
    assert canonicalize_genre(["Jazz", "Blues"]) == "Jazz\\\\Blues"

    # 4. Барокко не считается роком
    assert canonicalize_genre("Baroque") != "Rock"
    assert canonicalize_genre("Барокко") != "Rock"

    # 5. Пустые значения
    assert canonicalize_genre(None) is None
    assert canonicalize_genre("") is None
    assert canonicalize_genre("   ") is None


