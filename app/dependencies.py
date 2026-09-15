"""Внедрение зависимостей (Dependency Injection) для FastAPI.

Предоставляет фабрики и провайдеры зависимостей для эндпоинтов и сервисов,
включая валидацию сессий и извлечение текущего пользователя.
"""

from functools import lru_cache
from typing import Annotated

from fastapi import Depends, Request

from app.config import Settings
from app.core.exceptions import AuthenticationError
from app.core.sandbox import PathSandbox
from app.services.auth_service import AuthService
from app.services.deezer_client import DeezerClient
from app.services.download_service import DownloadService
from app.services.file_manager_service import FileManagerService
from app.services.library_audit_service import LibraryAuditService
from app.services.lyrics_service import GeniusClient, LrclibClient, LyricsService
from app.services.native_scanner import NativeScannerService
from app.services.organizer_service import OrganizerService
from app.services.source_resolver import SourceResolver
from app.services.tag_service import TagService
from app.services.torrent_service import TorrentService


@lru_cache(maxsize=1)
def get_settings() -> Settings:
    """Возвращает кэшированный экземпляр конфигурации приложения."""
    return Settings()


# Типизированные аннотации зависимостей (FastAPI idiomatic)
SettingsDep = Annotated[Settings, Depends(get_settings)]


def get_auth_service(settings: SettingsDep) -> AuthService:
    """Провайдер экземпляра сервиса аутентификации AuthService."""
    return AuthService(settings)


AuthServiceDep = Annotated[AuthService, Depends(get_auth_service)]


def extract_token_from_request(request: Request, settings: Settings) -> str | None:
    """Извлекает сессионный токен из cookie или заголовка Authorization.

    Поддерживает:
    1. Сессионный cookie (settings.SESSION_COOKIE_NAME) для браузера.
    2. Заголовок 'Authorization: Bearer <token>' для API-клиентов и тестов.
    """
    # 1. Проверяем наличие сессионного cookie
    token = request.cookies.get(settings.SESSION_COOKIE_NAME)
    if token:
        return token

    # 2. Проверяем заголовок Authorization: Bearer <token>
    auth_header = request.headers.get("Authorization")
    if auth_header and auth_header.startswith("Bearer "):
        bearer_token = auth_header[7:].strip()
        if bearer_token:
            return bearer_token

    return None


def get_current_user(
    request: Request,
    auth_service: AuthServiceDep,
    settings: SettingsDep,
) -> str:
    """Зависимость FastAPI для проверки аутентификации пользователя.

    Извлекает токен сессии, проверяет его подпись и срок жизни через AuthService.
    Возвращает логин пользователя или выбрасывает AuthenticationError (HTTP 401).
    """
    token = extract_token_from_request(request, settings)
    if not token:
        raise AuthenticationError("Требуется аутентификация")

    return auth_service.verify_session_token(token)


CurrentUserDep = Annotated[str, Depends(get_current_user)]


def get_music_sandbox(settings: SettingsDep) -> PathSandbox:
    """Провайдер экземпляра PathSandbox для постоянной медиатеки MUSIC_DIR."""
    return PathSandbox(settings.MUSIC_DIR)


def get_tmp_sandbox(settings: SettingsDep) -> PathSandbox:
    """Провайдер экземпляра PathSandbox для временного буфера загрузок TMP_DIR."""
    return PathSandbox(settings.TMP_DIR)


MusicSandboxDep = Annotated[PathSandbox, Depends(get_music_sandbox)]
TmpSandboxDep = Annotated[PathSandbox, Depends(get_tmp_sandbox)]


_deezer_client: DeezerClient | None = None
_source_resolver: SourceResolver | None = None
_download_service: DownloadService | None = None


def get_deezer_client() -> DeezerClient:
    """Провайдер синглтона DeezerClient."""
    global _deezer_client
    if _deezer_client is None:
        _deezer_client = DeezerClient()
    return _deezer_client


DeezerClientDep = Annotated[DeezerClient, Depends(get_deezer_client)]


def get_source_resolver(
    deezer_client: DeezerClientDep,
    settings: SettingsDep,
) -> SourceResolver:
    """Провайдер синглтона SourceResolver."""
    global _source_resolver
    if _source_resolver is None:
        _source_resolver = SourceResolver(
            deezer_client=deezer_client,
            cobalt_api_url=settings.COBALT_API_URL,
            allowed_domains=settings.ALLOWED_DOWNLOAD_DOMAINS,
            cookies_path=settings.cookies_file,
            settings=settings,
        )
    return _source_resolver


SourceResolverDep = Annotated[SourceResolver, Depends(get_source_resolver)]


def get_download_service(
    settings: SettingsDep,
    tmp_sandbox: TmpSandboxDep,
    source_resolver: SourceResolverDep,
    deezer_client: DeezerClientDep,
) -> DownloadService:
    """Провайдер синглтона DownloadService (хранилище задач в памяти)."""
    global _download_service
    if _download_service is None:
        torrent_svc: TorrentService | None = None
        if settings.TORRENT_ENABLED:
            torrent_svc = TorrentService(settings=settings)
        _download_service = DownloadService(
            settings=settings,
            tmp_sandbox=tmp_sandbox,
            source_resolver=source_resolver,
            deezer_client=deezer_client,
            torrent_service=torrent_svc,
        )
    return _download_service


DownloadServiceDep = Annotated[DownloadService, Depends(get_download_service)]


def get_tag_service(
    music_sandbox: MusicSandboxDep,
    tmp_sandbox: TmpSandboxDep,
) -> TagService:
    """Провайдер сервиса работы с тегами TagService."""
    return TagService(music_sandbox=music_sandbox, tmp_sandbox=tmp_sandbox)


TagServiceDep = Annotated[TagService, Depends(get_tag_service)]


def get_file_manager_service(
    music_sandbox: MusicSandboxDep,
    tag_service: TagServiceDep,
) -> FileManagerService:
    """Провайдер сервиса файлового менеджера FileManagerService."""
    return FileManagerService(music_sandbox=music_sandbox, tag_service=tag_service)


FileManagerServiceDep = Annotated[FileManagerService, Depends(get_file_manager_service)]


def get_organizer_service(
    music_sandbox: MusicSandboxDep,
    tmp_sandbox: TmpSandboxDep,
    tag_service: TagServiceDep,
) -> OrganizerService:
    """Провайдер сервиса организации медиатеки OrganizerService."""
    return OrganizerService(
        music_sandbox=music_sandbox,
        tmp_sandbox=tmp_sandbox,
        tag_service=tag_service,
    )


OrganizerServiceDep = Annotated[OrganizerService, Depends(get_organizer_service)]


def get_lrclib_client() -> LrclibClient:
    """Провайдер клиента LRCLIB."""
    return LrclibClient()


def get_genius_client(settings: SettingsDep) -> GeniusClient:
    """Провайдер клиента Genius API."""
    return GeniusClient(api_token=settings.GENIUS_API_TOKEN)


def get_lyrics_service(
    music_sandbox: MusicSandboxDep,
    tmp_sandbox: TmpSandboxDep,
    settings: SettingsDep,
) -> LyricsService:
    """Провайдер сервиса управления текстами песен LyricsService."""
    return LyricsService(
        music_sandbox=music_sandbox,
        tmp_sandbox=tmp_sandbox,
        lrclib_client=get_lrclib_client(),
        genius_client=get_genius_client(settings),
    )


LyricsServiceDep = Annotated[LyricsService, Depends(get_lyrics_service)]

_native_scanner: NativeScannerService | None = None


def get_native_scanner(
    music_sandbox: MusicSandboxDep,
    settings: SettingsDep,
) -> NativeScannerService:
    """Провайдер синглтона нативного сканера NativeScannerService."""
    global _native_scanner
    if _native_scanner is None or _native_scanner._sandbox.root != music_sandbox.root:
        _native_scanner = NativeScannerService(
            music_sandbox=music_sandbox,
            bin_path=settings.MUSIC_SCANNER_BIN_PATH,
        )
    return _native_scanner


NativeScannerDep = Annotated[NativeScannerService, Depends(get_native_scanner)]

_library_audit_service: LibraryAuditService | None = None


def get_library_audit_service(
    music_sandbox: MusicSandboxDep,
    tag_service: TagServiceDep,
    lyrics_service: LyricsServiceDep,
    deezer_client: DeezerClientDep,
    native_scanner: NativeScannerDep,
    settings: SettingsDep,
    tmp_sandbox: TmpSandboxDep,
) -> LibraryAuditService:
    """Провайдер синглтона сервиса аудита и глобального поиска медиатеки."""
    global _library_audit_service
    if _library_audit_service is None or _library_audit_service._music_sandbox.root != music_sandbox.root:
        _library_audit_service = LibraryAuditService(
            music_sandbox=music_sandbox,
            tag_service=tag_service,
            lyrics_service=lyrics_service,
            deezer_client=deezer_client,
            native_scanner=native_scanner,
            db_path=settings.DATABASE_PATH,
            tmp_sandbox=tmp_sandbox,
        )
    return _library_audit_service


LibraryAuditServiceDep = Annotated[LibraryAuditService, Depends(get_library_audit_service)]


