"""Pydantic-схемы валидации данных модуля аутентификации."""

from pydantic import BaseModel, Field


class LoginRequest(BaseModel):
    """Схема запроса авторизации."""

    login: str = Field(
        ...,
        min_length=1,
        max_length=100,
        description="Логин администратора",
    )
    password: str = Field(
        ...,
        min_length=1,
        max_length=256,
        description="Пароль администратора",
    )


class LoginResponse(BaseModel):
    """Схема успешного ответа авторизации."""

    success: bool = Field(
        default=True,
        description="Флаг успешности выполнения операции",
    )
    message: str = Field(
        default="Авторизация успешна",
        description="Человекочитаемое сообщение о результате",
    )


class CurrentUserResponse(BaseModel):
    """Схема информации о текущем авторизованном пользователе."""

    login: str = Field(
        ...,
        description="Идентификатор (логин) текущего пользователя",
    )
    authenticated: bool = Field(
        default=True,
        description="Признак наличия активной аутентификации",
    )


class LogoutResponse(BaseModel):
    """Схема ответа при завершении сессии (logout)."""

    success: bool = Field(
        default=True,
        description="Флаг успешного завершения сессии",
    )
