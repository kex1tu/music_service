"""Роутер аутентификации администратора."""

from fastapi import APIRouter, Response

from app.core.exceptions import AuthenticationError
from app.dependencies import AuthServiceDep, CurrentUserDep, SettingsDep
from app.schemas.auth import (
    CurrentUserResponse,
    LoginRequest,
    LoginResponse,
    LogoutResponse,
)

router = APIRouter(tags=["auth"])


@router.post("/login", response_model=LoginResponse)
@router.post("/api/auth/login", response_model=LoginResponse)
async def login(
    payload: LoginRequest,
    response: Response,
    auth_service: AuthServiceDep,
    settings: SettingsDep,
) -> LoginResponse:
    """Аутентифицирует администратора и устанавливает защищенный сессионный cookie."""
    is_valid = auth_service.verify_credentials(payload.login, payload.password)
    if not is_valid:
        raise AuthenticationError("Неверный логин или пароль")

    token = auth_service.create_session_token(payload.login)

    response.set_cookie(
        key=settings.SESSION_COOKIE_NAME,
        value=token,
        max_age=settings.SESSION_TTL,
        httponly=True,
        secure=settings.SESSION_COOKIE_SECURE,
        samesite=settings.SESSION_COOKIE_SAMESITE,
        path="/",
    )

    return LoginResponse(success=True, message="Авторизация успешна")


@router.post("/logout", response_model=LogoutResponse)
@router.post("/api/auth/logout", response_model=LogoutResponse)
async def logout(
    response: Response,
    settings: SettingsDep,
) -> LogoutResponse:
    """Завершает сессию пользователя и удаляет сессионный cookie."""
    response.delete_cookie(
        key=settings.SESSION_COOKIE_NAME,
        path="/",
        httponly=True,
        samesite=settings.SESSION_COOKIE_SAMESITE,
        secure=settings.SESSION_COOKIE_SECURE,
    )
    return LogoutResponse(success=True)


@router.get("/api/auth/me", response_model=CurrentUserResponse)
async def get_current_user_info(
    current_user: CurrentUserDep,
) -> CurrentUserResponse:
    """Возвращает информацию о текущем авторизованном пользователе."""
    return CurrentUserResponse(login=current_user, authenticated=True)
