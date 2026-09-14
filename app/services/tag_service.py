"""Сервис работы с метаданными и ID3v2.4 тегами аудиофайлов (Metadata & Tag Service).

Предоставляет строгую реализацию чтения и записи тегов стандарта ID3v2.4 через mutagen,
поддержку множественных значений артистов и жанров (null-separated в ID3, \\\\ в UI),
встраивание и извлечение фронтальных обложек (APIC type=3), вычисление длительности
и проверку внешних текстов песен (.lrc/.txt) рядом с MP3.
Все операции строго изолированы через PathSandbox.
"""

import asyncio
import logging
import re
import urllib.parse
from pathlib import Path

import httpx
from mutagen import MutagenError
from mutagen.id3 import (
    APIC,
    ID3,
    TALB,
    TCON,
    TDRC,
    TIT2,
    TPE1,
    TPE2,
    TPOS,
    TRCK,
    PictureType,
)
from mutagen.id3._util import ID3NoHeaderError
from mutagen.mp3 import MP3

from app.core.exceptions import (
    NotFoundError,
    PathTraversalError,
    StorageConfigurationError,
    TagError,
    TagWriteError,
)
from app.core.sandbox import PathSandbox
from app.schemas.tag import (
    AlbumDetailsResponse,
    AlbumTags,
    CoverSearchResultItem,
    TrackTags,
)
from app.services.deezer_client import DeezerClient
from app.services.yandex_client import YandexMusicClient

logger = logging.getLogger(__name__)


class TagService:
    """Сервис для чтения, обновления и агрегации тегов ID3v2.4."""

    def __init__(
        self,
        music_sandbox: PathSandbox,
        tmp_sandbox: PathSandbox | None = None,
        http_client: httpx.AsyncClient | None = None,
    ) -> None:
        """Инициализирует сервис песочницами медиатеки и временного буфера."""
        self._music_sandbox = music_sandbox
        self._tmp_sandbox = tmp_sandbox
        self._http_client = http_client

    def parse_multivalue(self, raw: str | None) -> list[str]:
        """Разбивает строку с несколькими значениями на список строк.

        Поддерживает разделители '\\\\', '\\' и null-байт '\\x00'.
        Очищает ведущие/замыкающие пробелы и отфильтровывает пустые элементы.
        """
        if not raw:
            return []
        return [part.strip() for part in re.split(r"\\+|\x00", str(raw)) if part.strip()]

    def format_multivalue(self, values: list[str]) -> str:
        """Объединяет список значений в строку для UI с разделителем '\\\\'.

        Пропускает пустые значения и пробелы.
        """
        if not values:
            return ""
        cleaned = [v.strip() for v in values if v and v.strip()]
        return "\\\\".join(cleaned)

    def resolve_path(self, rel_path: str | Path, allow_root: bool = False) -> Path:
        """Разрешает относительный путь через подходящую песочницу (music или tmp).

        Предотвращает Path Traversal и обеспечивает корректную маршрутизацию
        путей из tmp_downloads/{job_id} и постоянной медиатеки MUSIC_DIR.
        """
        if rel_path is None:
            raise PathTraversalError("Путь не может быть пустым (None)")

        rel_str = str(rel_path).strip()

        # Маршрутизация во временный буфер tmp_sandbox
        if self._tmp_sandbox is not None:
            if rel_str == "tmp_downloads" or rel_str.startswith("tmp_downloads/"):
                sub_path = rel_str[len("tmp_downloads") :].lstrip("/")
                return self._tmp_sandbox.resolve(sub_path, allow_root=allow_root)

            if rel_str == "tmp" or rel_str.startswith("tmp/"):
                sub_path = rel_str[len("tmp") :].lstrip("/")
                try:
                    cand = self._tmp_sandbox.resolve(sub_path, allow_root=allow_root)
                    if cand.exists():
                        return cand
                except PathTraversalError:
                    raise
                except (StorageConfigurationError, OSError):
                    pass

            if rel_str.startswith("tmp_"):
                try:
                    candidate = self._tmp_sandbox.resolve(rel_str, allow_root=allow_root)
                    if candidate.exists():
                        return candidate
                except PathTraversalError:
                    raise
                except (StorageConfigurationError, OSError) as exc:
                    logger.debug("Сбой проверки tmp кандидата '%s': %s", rel_str, exc)

            # Если путь существует в tmp_sandbox, но не в music_sandbox
            try:
                candidate = self._tmp_sandbox.resolve(rel_str, allow_root=allow_root)
                music_candidate = self._music_sandbox.resolve(rel_str, allow_root=allow_root)
                if candidate.exists() and not music_candidate.exists():
                    return candidate
            except PathTraversalError:
                raise
            except (StorageConfigurationError, OSError) as exc:
                logger.debug("Сбой сопоставления песочниц для '%s': %s", rel_str, exc)

        return self._music_sandbox.resolve(rel_str, allow_root=allow_root)

    def read_tags(self, file_path: Path, rel_path: str = "") -> TrackTags:
        """Считывает метаданные из MP3-файла в схему TrackTags.

        Корректно обрабатывает отсутствие ID3-заголовка, извлекает встроенную
        обложку APIC (type=3), считывает длительность аудио и проверяет
        наличие внешних текстов песен (.lrc/.txt).
        """
        if not file_path.exists():
            raise NotFoundError(f"Аудиофайл не найден: {file_path.name}")

        try:
            tags = ID3(str(file_path))
        except ID3NoHeaderError:
            tags = ID3()
        except Exception as exc:
            raise TagError(f"Ошибка чтения ID3-тегов из {file_path.name}: {exc}") from exc

        # 1. Название трека
        title = ""
        if "TIT2" in tags and tags["TIT2"].text:
            title = str(tags["TIT2"].text[0]).strip()

        # 2. Исполнители трека (TPE1)
        artist_parts: list[str] = []
        if "TPE1" in tags and tags["TPE1"].text:
            for item in tags["TPE1"].text:
                artist_parts.extend(self.parse_multivalue(str(item)))
        artist = self.format_multivalue(artist_parts)

        # 3. Альбом (TALB)
        album = ""
        if "TALB" in tags and tags["TALB"].text:
            album = str(tags["TALB"].text[0]).strip()

        # 4. Исполнитель альбома (TPE2)
        album_artist_parts: list[str] = []
        if "TPE2" in tags and tags["TPE2"].text:
            for item in tags["TPE2"].text:
                album_artist_parts.extend(self.parse_multivalue(str(item)))
        album_artist = self.format_multivalue(album_artist_parts)

        # 5. Номер дорожки (TRCK)
        track_number = 1
        track_total: int | None = None
        if "TRCK" in tags and tags["TRCK"].text:
            raw_trck = str(tags["TRCK"].text[0]).strip()
            match = re.match(r"^(\d+)(?:/(\d+))?", raw_trck)
            if match:
                try:
                    track_number = max(1, int(match.group(1)))
                except ValueError:
                    track_number = 1
                if match.group(2):
                    try:
                        track_total = max(1, int(match.group(2)))
                    except ValueError:
                        track_total = None

        # 5.1 Номер диска (TPOS)
        disc_number = 1
        disc_total: int | None = None
        if "TPOS" in tags and tags["TPOS"].text:
            raw_tpos = str(tags["TPOS"].text[0]).strip()
            m_tpos = re.match(r"^(\d+)(?:/(\d+))?", raw_tpos)
            if m_tpos:
                try:
                    disc_number = max(1, int(m_tpos.group(1)))
                except ValueError:
                    disc_number = 1
                if m_tpos.group(2):
                    try:
                        disc_total = max(1, int(m_tpos.group(2)))
                    except ValueError:
                        disc_total = None

        # 6. Год релиза (TDRC или TYER)
        year: int | None = None
        for frame_id in ("TDRC", "TYER"):
            if frame_id in tags and tags[frame_id].text:
                raw_val = str(tags[frame_id].text[0]).strip()
                match = re.search(r"\b(\d{4})\b", raw_val)
                if match:
                    try:
                        parsed_year = int(match.group(1))
                        if 1000 <= parsed_year <= 2999:
                            year = parsed_year
                            break
                    except ValueError:
                        pass

        # 7. Жанр (TCON)
        genre_parts: list[str] = []
        if "TCON" in tags and tags["TCON"].text:
            for item in tags["TCON"].text:
                genre_parts.extend(self.parse_multivalue(str(item)))
        genre = self.format_multivalue(genre_parts) if genre_parts else None

        # 8. Наличие встроенной обложки (APIC)
        has_cover = False
        for frame in tags.getall("APIC"):
            if getattr(frame, "data", None):
                has_cover = True
                break

        # 9. Проверка внешних текстов (.lrc / .txt)
        lyrics_status = "none"
        lrc_path = file_path.with_suffix(".lrc")
        txt_path = file_path.with_suffix(".txt")
        if lrc_path.exists() and lrc_path.is_file():
            lyrics_status = "lrc"
        elif txt_path.exists() and txt_path.is_file():
            lyrics_status = "txt"

        # 10. Длительность звучания через mutagen MP3
        duration: int | None = None
        try:
            mp3_obj = MP3(str(file_path))
            if mp3_obj.info and mp3_obj.info.length is not None:
                duration = round(mp3_obj.info.length)
        except (MutagenError, OSError) as exc:
            logger.debug("Не удалось прочитать длину аудиопотока из %s: %s", file_path.name, exc)
            duration = None

        norm_path = rel_path or str(file_path)

        return TrackTags(
            path=norm_path,
            track_number=track_number,
            track_total=track_total,
            disc_number=disc_number,
            disc_total=disc_total,
            title=title,
            artist=artist,
            album=album,
            album_artist=album_artist,
            year=year,
            genre=genre,
            duration=duration,
            lyrics_status=lyrics_status,
            has_cover=has_cover,
        )

    def write_tags(
        self,
        file_path: Path,
        tags: TrackTags,
        cover_bytes: bytes | None = None,
        cover_mime: str = "image/jpeg",
    ) -> None:
        """Записывает строго ID3v2.4 теги в MP3-файл.

        Множественные артисты и жанры сохраняются как null-separated (\\x00)
        список значений во фреймах TPE1, TPE2, TCON.
        Кадры USLT и SYLT безусловно удаляются в соответствии с инвариантом
        внешнего хранения текстов песен.
        """
        try:
            try:
                id3_tags = ID3(str(file_path))
            except ID3NoHeaderError:
                id3_tags = ID3()

            # Инвариант: удаление любых фреймов текстов песен из MP3
            id3_tags.delall("USLT")
            id3_tags.delall("SYLT")

            # 1. Название трека
            if tags.title:
                id3_tags.add(TIT2(encoding=3, text=[tags.title]))
            else:
                id3_tags.delall("TIT2")

            # 2. Исполнители трека (разделенные null-байтом в ID3v2.4)
            artist_list = self.parse_multivalue(tags.artist)
            if artist_list:
                id3_tags.add(TPE1(encoding=3, text=artist_list))
            else:
                id3_tags.delall("TPE1")

            # 3. Альбом
            if tags.album:
                id3_tags.add(TALB(encoding=3, text=[tags.album]))
            else:
                id3_tags.delall("TALB")

            # 4. Исполнитель альбома
            album_artist_list = self.parse_multivalue(tags.album_artist)
            if album_artist_list:
                id3_tags.add(TPE2(encoding=3, text=album_artist_list))
            else:
                id3_tags.delall("TPE2")

            # 5. Номер дорожки
            if tags.track_number:
                trck_val = f"{tags.track_number}/{tags.track_total}" if tags.track_total else str(tags.track_number)
                id3_tags.add(TRCK(encoding=3, text=[trck_val]))
            else:
                id3_tags.delall("TRCK")

            # 5.1 Номер диска (TPOS)
            if tags.disc_number:
                tpos_val = f"{tags.disc_number}/{tags.disc_total}" if tags.disc_total else str(tags.disc_number)
                id3_tags.add(TPOS(encoding=3, text=[tpos_val]))
            else:
                id3_tags.delall("TPOS")

            # 6. Год релиза (TDRC по стандарту ID3v2.4)
            if tags.year:
                id3_tags.add(TDRC(encoding=3, text=[str(tags.year)]))
                id3_tags.delall("TYER")
            else:
                id3_tags.delall("TDRC")
                id3_tags.delall("TYER")

            # 7. Жанр
            genre_list = self.parse_multivalue(tags.genre)
            if genre_list:
                id3_tags.add(TCON(encoding=3, text=genre_list))
            else:
                id3_tags.delall("TCON")

            # 8. Встраивание обложки при наличии
            if cover_bytes is not None:
                id3_tags.delall("APIC")
                id3_tags.add(
                    APIC(
                        encoding=3,
                        mime=cover_mime or "image/jpeg",
                        type=3,  # PictureType.COVER_FRONT
                        desc="Cover",
                        data=cover_bytes,
                    )
                )

            id3_tags.save(str(file_path), v2_version=4, v1=0)
        except Exception as exc:
            raise TagWriteError(f"Не удалось записать теги в {file_path.name}: {exc}") from exc

    def read_cover(self, file_path: Path) -> tuple[bytes, str] | None:
        """Извлекает фронтальную обложку (APIC type 3) из MP3-файла или каталога."""
        # 1. Если передан MP3-файл
        if file_path.is_file():
            if file_path.suffix.lower() == ".mp3":
                try:
                    tags = ID3(str(file_path))
                    target_frame = None
                    for frame in tags.getall("APIC"):
                        frame_type = getattr(frame, "type", None)
                        if frame_type == 3 or frame_type == PictureType.COVER_FRONT:
                            target_frame = frame
                            break
                    if not target_frame:
                        apics = tags.getall("APIC")
                        if apics:
                            target_frame = apics[0]
                    if target_frame and target_frame.data:
                        data = target_frame.data
                        if data.startswith(b"\x89PNG"):
                            mime = "image/png"
                        elif data.startswith(b"\xff\xd8\xff"):
                            mime = "image/jpeg"
                        elif data.startswith(b"RIFF") and len(data) >= 12 and data[8:12] == b"WEBP":
                            mime = "image/webp"
                        else:
                            mime = target_frame.mime or "image/jpeg"
                        return (data, mime)
                except (MutagenError, OSError) as exc:
                    logger.debug("Не удалось прочитать APIC из %s: %s", file_path.name, exc)
            check_dir = file_path.parent
        elif file_path.is_dir():
            check_dir = file_path
        else:
            return None

        # 2. Поиск файлов изображений в каталоге
        if check_dir.is_dir():
            candidates = (
                "cover.jpg",
                "cover.jpeg",
                "cover.png",
                "cover.webp",
                "folder.jpg",
                "folder.jpeg",
                "folder.png",
                "folder.webp",
                "front.jpg",
                "front.png",
                "front.webp",
            )
            for name in candidates:
                img_path = check_dir / name
                if img_path.is_file():
                    data = img_path.read_bytes()
                    if data.startswith(b"\x89PNG") or img_path.suffix.lower() == ".png":
                        mime = "image/png"
                    elif (
                        data.startswith(b"RIFF")
                        and len(data) >= 12
                        and data[8:12] == b"WEBP"
                    ) or img_path.suffix.lower() == ".webp":
                        mime = "image/webp"
                    else:
                        mime = "image/jpeg"
                    return (data, mime)

            try:
                for entry in check_dir.iterdir():
                    if entry.is_file() and entry.name.lower() in candidates:
                        data = entry.read_bytes()
                        if data.startswith(b"\x89PNG") or entry.suffix.lower() == ".png":
                            mime = "image/png"
                        elif (
                            data.startswith(b"RIFF")
                            and len(data) >= 12
                            and data[8:12] == b"WEBP"
                        ) or entry.suffix.lower() == ".webp":
                            mime = "image/webp"
                        else:
                            mime = "image/jpeg"
                        return (data, mime)
            except OSError as exc:
                logger.debug("Ошибка чтения каталога %s при поиске обложки: %s", check_dir, exc)

        return None

    def embed_cover(
        self,
        file_path: Path,
        cover_bytes: bytes,
        mime: str = "image/jpeg",
    ) -> None:
        """Встраивает обложку (APIC type 3) в MP3-файл с сохранением в ID3v2.4."""
        try:
            try:
                tags = ID3(str(file_path))
            except ID3NoHeaderError:
                tags = ID3()

            tags.delall("APIC")
            tags.add(
                APIC(
                    encoding=3,
                    mime=mime or "image/jpeg",
                    type=3,  # PictureType.COVER_FRONT
                    desc="Cover",
                    data=cover_bytes,
                )
            )
            tags.save(str(file_path), v2_version=4, v1=0)
        except Exception as exc:
            raise TagWriteError(f"Не удалось встроить обложку в {file_path.name}: {exc}") from exc

    def apply_album_defaults(
        self,
        tracks: list[TrackTags],
        album: AlbumTags,
    ) -> list[TrackTags]:
        """Применяет метаданные альбома к списку треков (двухуровневая модель редактирования)."""
        updated: list[TrackTags] = []
        for track in tracks:
            item = track.model_copy()
            if album.album:
                item.album = album.album
            if album.album_artist:
                item.album_artist = album.album_artist
            if album.year is not None:
                item.year = album.year
            if album.genre:
                item.genre = album.genre
            if album.disc_total is not None:
                item.disc_total = album.disc_total
            updated.append(item)
        return updated

    def read_album_details(self, rel_dir_path: str) -> AlbumDetailsResponse:
        """Сканирует каталог альбома, считывает теги всех MP3-треков и агрегирует метаданные."""
        dir_path = self.resolve_path(rel_dir_path, allow_root=False)
        if not dir_path.exists():
            raise NotFoundError(f"Каталог не найден: {rel_dir_path}")

        if dir_path.is_file():
            if dir_path.suffix.lower() == ".mp3":
                track = self.read_tags(dir_path, rel_path=rel_dir_path)
                album = AlbumTags(
                    album=track.album,
                    album_artist=track.album_artist or track.artist,
                    year=track.year,
                    genre=track.genre,
                    disc_total=track.disc_total,
                )
                cover_info = self.read_cover(dir_path)
                return AlbumDetailsResponse(
                    album=album,
                    tracks=[track],
                    has_cover=track.has_cover or (cover_info is not None),
                )
            raise TagError(f"Файл {dir_path.name} не является MP3-файлом")

        # Если это каталог: собираем MP3-файлы
        mp3_files = sorted(
            [p for p in dir_path.iterdir() if p.is_file() and p.suffix.lower() == ".mp3"],
            key=lambda p: p.name.lower(),
        )
        if not mp3_files:
            mp3_files = sorted(
                [p for p in dir_path.rglob("*.mp3") if p.is_file() and not p.name.startswith(".")],
                key=lambda p: (str(p.parent).lower(), p.name.lower()),
            )

        tracks: list[TrackTags] = []
        for mp3_file in mp3_files:
            if rel_dir_path == "tmp_downloads" or rel_dir_path.startswith("tmp_downloads/"):
                if self._tmp_sandbox:
                    rel_to_tmp = self._tmp_sandbox.to_relative(mp3_file)
                    track_rel_path = f"tmp_downloads/{rel_to_tmp}".replace("\\", "/")
                else:
                    track_rel_path = f"{rel_dir_path.rstrip('/')}/{mp3_file.name}".replace("\\", "/")
            elif self._tmp_sandbox and mp3_file.is_relative_to(self._tmp_sandbox.root):
                track_rel_path = str(self._tmp_sandbox.to_relative(mp3_file)).replace("\\", "/")
            else:
                track_rel_path = str(self._music_sandbox.to_relative(mp3_file)).replace("\\", "/")

            track_tags = self.read_tags(mp3_file, rel_path=track_rel_path)
            tracks.append(track_tags)

        # Сортировка по номеру диска, номеру дорожки, затем имени файла/названию
        tracks.sort(key=lambda t: (t.disc_number, t.track_number, t.title.lower(), t.path))

        album_artist = ""
        album_title = ""
        year: int | None = None
        genre: str | None = None

        for t in tracks:
            if not album_artist and t.album_artist:
                album_artist = t.album_artist
            if not album_title and t.album:
                album_title = t.album
            if year is None and t.year is not None:
                year = t.year
            if not genre and t.genre:
                genre = t.genre

        if not album_artist and tracks:
            album_artist = tracks[0].artist

        disc_totals = [t.disc_total for t in tracks if t.disc_total]
        disc_numbers = [t.disc_number for t in tracks if t.disc_number]
        album_disc_total = (
            max(disc_totals)
            if disc_totals
            else (max(disc_numbers) if (disc_numbers and max(disc_numbers) > 1) else None)
        )

        cover_info = self.read_cover(dir_path)
        has_cover = (cover_info is not None) or any(t.has_cover for t in tracks)

        cover_path: str | None = None
        candidates = (
            "cover.jpg",
            "cover.jpeg",
            "cover.png",
            "folder.jpg",
            "folder.jpeg",
            "folder.png",
            "front.jpg",
            "front.png",
        )
        for cand in candidates:
            cand_file = dir_path / cand
            if cand_file.is_file():
                cover_path = f"{rel_dir_path.rstrip('/')}/{cand}".lstrip("/")
                break

        album_tags = AlbumTags(
            album_artist=album_artist,
            album=album_title,
            year=year,
            genre=genre,
            cover_path=cover_path,
            disc_total=album_disc_total,
        )

        return AlbumDetailsResponse(
            album=album_tags,
            tracks=tracks,
            has_cover=has_cover,
            cover_path=cover_path,
        )

    @classmethod
    def clean_title(cls, title: str, current_artist: str = "") -> tuple[str, str]:
        """Интеллектуальная очистка названия трека от мусора и извлечение фитов (Clean Titles & Feat Extractor).

        - Удаляет мусорные суффиксы в скобках [ ... ] и ( ... ):
          Official Video, Official Music Video, Official Audio, Audio, Video, Lyric Video,
          Lyrics, Clip Officiel, Visualizer, HD, HQ, 4K, 1080p, 720p, Prod. by ..., Remastered и т.д.
        - Извлекает фит-исполнителей (feat., ft., featuring), разбивает по разделителям
          (&, ,, and, и, x) и дополняет список исполнителей через \\\\ без дубликатов.
        - Удаляет ведущие порядковые номера: "01. Title", "01 - Title" -> "Title".
        - Удаляет дублирование артиста в начале названия: "Artist - Title" -> "Title",
          а если исполнитель не был задан — устанавливает его из префикса.

        Returns:
            Кортеж (cleaned_title, updated_artist).
        """
        raw_title = str(title or "").strip()
        if not raw_title:
            return "", current_artist or ""

        # Список артистов (чистим и разбиваем существующий current_artist)
        artists_list: list[str] = [
            a.strip()
            for a in DeezerClient.split_artist_string(str(current_artist or ""))
            if a.strip()
        ]

        # 1. Удаление ведущих порядковых номеров: "01. ", "01 - ", "01 "
        raw_title = re.sub(r"^\s*\d{1,3}\s*[\.\-–—]\s*", "", raw_title)
        raw_title = re.sub(r"^\s*\d{1,3}\s+", "", raw_title)

        # 2. Проверка дублирования артиста в начале названия: "Artist - Title"
        m_artist = re.match(r"^([^\-–—]+?)\s*[-–—]\s*(.+)$", raw_title)
        if m_artist:
            cand_artist = m_artist.group(1).strip()
            rest_title = m_artist.group(2).strip()
            if artists_list:
                existing_lower = [a.lower() for a in artists_list]
                if cand_artist.lower() in existing_lower:
                    raw_title = rest_title
            else:
                # Если исполнитель не был задан — берём его из префикса
                for part in DeezerClient.split_artist_string(cand_artist):
                    if part and part.lower() not in [a.lower() for a in artists_list]:
                        artists_list.append(part)
                raw_title = rest_title

        # 3. Извлечение feat / ft / featuring
        # 3.1. Внутри скобок (...) или [...]
        feat_bracket_regex = re.compile(
            r"[\(\[]\s*(?:feat\.?|ft\.?|featuring|with)\s+([^\]\)]+?)[\)\]]",
            re.IGNORECASE,
        )
        for m in list(feat_bracket_regex.finditer(raw_title)):
            feat_content = m.group(1).strip()
            raw_title = raw_title[: m.start()] + " " + raw_title[m.end() :]
            parts = DeezerClient.split_artist_string(feat_content)
            for p in parts:
                p_clean = p.strip()
                if p_clean and p_clean.lower() not in [a.lower() for a in artists_list]:
                    artists_list.append(p_clean)

        # 3.2. Без скобок в конце строки или перед разделителем
        feat_plain_regex = re.compile(
            r"\b(?:feat\.?|ft\.?|featuring|with)\s+([^\[\]\(\)\-]+)$",
            re.IGNORECASE,
        )
        m_plain = feat_plain_regex.search(raw_title)
        if m_plain:
            feat_content = m_plain.group(1).strip()
            raw_title = raw_title[: m_plain.start()].strip()
            parts = DeezerClient.split_artist_string(feat_content)
            for p in parts:
                p_clean = p.strip()
                if p_clean and p_clean.lower() not in [a.lower() for a in artists_list]:
                    artists_list.append(p_clean)

        # 4. Удаление мусорных суффиксов в скобках [ ... ] и ( ... )
        junk_pattern = re.compile(
            r"[\(\[]\s*(?:"
            r"(?:(?:official|music|4k|hd|hq|1080p|720p)\s+)*(?:video|audio|visualizer|clip\s*officiel|lyric\s*video|lyrics)|"
            r"official|"
            r"audio|"
            r"video|"
            r"visualizer|"
            r"lyrics|"
            r"clip\s*officiel|"
            r"hd|"
            r"hq|"
            r"4k|"
            r"1080p|"
            r"720p|"
            r"prod\.?\s*by[^\)\]]+|"
            r"remaster(?:ed)?(?:\s*\d{4})?"
            r")\s*[\)\]]",
            re.IGNORECASE,
        )
        raw_title = junk_pattern.sub("", raw_title)

        # 5. Очистка пустых скобок, лишних пробелов и дефисов
        raw_title = re.sub(r"[\(\[]\s*[\)\]]", "", raw_title)
        raw_title = re.sub(r"\s+", " ", raw_title).strip(" \t-–—._")

        if not raw_title:
            raw_title = title.strip()

        updated_artist = "\\\\".join(artists_list)
        return raw_title, updated_artist

    async def download_cover(self, url: str) -> tuple[bytes, str]:
        """Скачивает изображение обложки по внешнему URL и определяет MIME-тип."""
        client = self._http_client
        should_close = False
        if client is None:
            client = httpx.AsyncClient(
                timeout=httpx.Timeout(15.0, connect=10.0),
                headers={"User-Agent": "MusicManager/1.0"},
                follow_redirects=True,
            )
            should_close = True

        try:
            resp = await client.get(url)
            if resp.status_code != 200:
                raise TagError(f"Не удалось скачать обложку: HTTP {resp.status_code}")
            content = resp.content
            if not content:
                raise TagError("Получен пустой ответ при скачивании обложки")

            # Сигнатуры форматов изображений
            if content.startswith(b"\x89PNG"):
                mime = "image/png"
            elif content.startswith(b"\xff\xd8\xff"):
                mime = "image/jpeg"
            elif content.startswith(b"RIFF") and len(content) >= 12 and content[8:12] == b"WEBP":
                mime = "image/webp"
            else:
                ct = resp.headers.get("content-type", "").lower()
                if "png" in ct:
                    mime = "image/png"
                elif "webp" in ct:
                    mime = "image/webp"
                else:
                    mime = "image/jpeg"
            return content, mime
        finally:
            if should_close:
                await client.aclose()

    async def _search_apple_covers(
        self, client: httpx.AsyncClient, query: str
    ) -> list[CoverSearchResultItem]:
        """Поиск обложек в Apple Music / iTunes Search API."""
        try:
            url = "https://itunes.apple.com/search"
            resp = await client.get(url, params={"term": query, "entity": "album", "limit": 8})
            if resp.status_code != 200:
                return []
            data = resp.json()
            results: list[CoverSearchResultItem] = []
            for item in data.get("results", []):
                art100 = item.get("artworkUrl100")
                if not art100:
                    continue
                # Замена 100x100bb на 1400x1400bb
                cover_url = re.sub(r"\d+x\d+bb(?:\.jpg|\.png)?", "1400x1400bb.jpg", art100)
                if not cover_url.endswith((".jpg", ".png", ".jpeg")):
                    cover_url = f"{cover_url}.jpg"
                title = item.get("collectionName") or item.get("trackName") or ""
                artist = item.get("artistName") or ""
                year = None
                rel_date = item.get("releaseDate")
                if rel_date:
                    m = re.match(r"^(\d{4})", str(rel_date))
                    if m:
                        year = int(m.group(1))
                results.append(
                    CoverSearchResultItem(
                        source="apple",
                        title=title,
                        artist=artist,
                        cover_url=cover_url,
                        width=1400,
                        height=1400,
                        year=year,
                    )
                )
            return results
        except Exception as exc:  # noqa: BLE001
            logger.debug("Сбой поиска обложек в Apple Music: %s", exc)
            return []

    async def _search_deezer_covers(
        self, client: httpx.AsyncClient, query: str
    ) -> list[CoverSearchResultItem]:
        """Поиск обложек в Deezer API."""
        try:
            url = "https://api.deezer.com/search/album"
            resp = await client.get(url, params={"q": query, "limit": 8})
            if resp.status_code != 200:
                return []
            data = resp.json()
            results: list[CoverSearchResultItem] = []
            for item in data.get("data", []):
                cover_url = (
                    item.get("cover_xl")
                    or item.get("cover_big")
                    or item.get("cover_medium")
                    or item.get("cover")
                )
                if not cover_url:
                    continue
                title = item.get("title") or ""
                artist_obj = item.get("artist", {})
                artist = artist_obj.get("name", "") if isinstance(artist_obj, dict) else ""
                results.append(
                    CoverSearchResultItem(
                        source="deezer",
                        title=title,
                        artist=artist,
                        cover_url=cover_url,
                        width=1400,
                        height=1400,
                        year=None,
                    )
                )
            return results
        except Exception as exc:  # noqa: BLE001
            logger.debug("Сбой поиска обложек в Deezer: %s", exc)
            return []

    async def _search_yandex_covers(
        self, client: httpx.AsyncClient, query: str
    ) -> list[CoverSearchResultItem]:
        """Поиск обложек в Яндекс Музыке."""
        try:
            url = "https://music.yandex.ru/handlers/music-search.jsx"
            resp = await client.get(url, params={"text": query, "type": "albums"})
            if resp.status_code != 200:
                return []
            data = resp.json()
            albums_data = data.get("albums", {})
            items = albums_data.get("items", []) if isinstance(albums_data, dict) else []
            results: list[CoverSearchResultItem] = []
            for item in items[:8]:
                cover_uri = item.get("ogImage") or item.get("coverUri")
                if not cover_uri:
                    continue
                cover_url = YandexMusicClient.format_cover(cover_uri, size="1000x1000")
                if not cover_url:
                    continue
                title = item.get("title") or ""
                artists_data = item.get("artists", [])
                artist = YandexMusicClient.format_artists(artists_data) if artists_data else ""
                year = item.get("year")
                results.append(
                    CoverSearchResultItem(
                        source="yandex",
                        title=title,
                        artist=artist,
                        cover_url=cover_url,
                        width=1000,
                        height=1000,
                        year=year,
                    )
                )
            return results
        except Exception as exc:  # noqa: BLE001
            logger.debug("Сбой поиска обложек в Яндекс Музыке: %s", exc)
            return []

    async def _search_spotify_covers(
        self, client: httpx.AsyncClient, query: str
    ) -> list[CoverSearchResultItem]:
        """Поиск обложек в Spotify."""
        try:
            url = "https://open.spotify.com/search/" + urllib.parse.quote(query)
            resp = await client.get(url)
            if resp.status_code != 200:
                return []
            covers = re.findall(r"https://i\.scdn\.co/image/([a-zA-Z0-9]+)", resp.text)
            results: list[CoverSearchResultItem] = []
            seen: set[str] = set()
            for cid in covers:
                if cid not in seen:
                    seen.add(cid)
                    results.append(
                        CoverSearchResultItem(
                            source="spotify",
                            title=query,
                            artist="",
                            cover_url=f"https://i.scdn.co/image/{cid}",
                            width=640,
                            height=640,
                            year=None,
                        )
                    )
                    if len(results) >= 4:
                        break
            return results
        except Exception as exc:  # noqa: BLE001
            logger.debug("Сбой поиска обложек в Spotify: %s", exc)
            return []

    async def search_covers(
        self,
        query: str,
        limit: int = 12,
    ) -> list[CoverSearchResultItem]:
        """Ищет обложки высокого разрешения в публичных сервисах (Apple Music, Deezer, Yandex, Spotify)."""
        clean_q = query.strip()
        if not clean_q:
            return []

        client = self._http_client
        should_close = False
        if client is None:
            client = httpx.AsyncClient(
                timeout=httpx.Timeout(12.0, connect=8.0),
                headers={
                    "User-Agent": (
                        "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "
                        "(KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36"
                    ),
                    "Accept": "application/json",
                },
                follow_redirects=True,
            )
            should_close = True

        try:
            tasks = [
                self._search_apple_covers(client, clean_q),
                self._search_deezer_covers(client, clean_q),
                self._search_yandex_covers(client, clean_q),
                self._search_spotify_covers(client, clean_q),
            ]
            responses = await asyncio.gather(*tasks, return_exceptions=True)

            all_items: list[CoverSearchResultItem] = []
            seen_urls: set[str] = set()

            valid_lists: list[list[CoverSearchResultItem]] = [
                r for r in responses if isinstance(r, list)
            ]
            max_len = max((len(r) for r in valid_lists), default=0)
            for i in range(max_len):
                for r in valid_lists:
                    if i < len(r):
                        item = r[i]
                        if item.cover_url not in seen_urls:
                            seen_urls.add(item.cover_url)
                            all_items.append(item)
                            if len(all_items) >= limit:
                                break
                if len(all_items) >= limit:
                    break

            return all_items[:limit]
        finally:
            if should_close:
                await client.aclose()
