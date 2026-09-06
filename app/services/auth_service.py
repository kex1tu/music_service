"""Сервис аутентификации администратора приложения."""

import secrets

from itsdangerous import BadSignature, SignatureExpired

from app.config import Settings
from app.core.exceptions import AuthenticationError
from app.core.security import (
    DUMMY_BCRYPT_HASH,
    create_session_token,
    decode_session_token,
    verify_password,
)


class AuthService:
    """Доменный сервис проверки учетных данных и управления сессиями."""

    def __init__(self, settings: Settings) -> None:
        self.settings = settings

    def verify_credentials(self, login: str, password: str) -> bool:
        """Сверяет логин и пароль с настройками приложения.

        Использует secrets.compare_digest и фиктивный хэш DUMMY_BCRYPT_HASH для
        гарантированного выравнивания времени отклика (Timing Attack Defense).
        """
        login_matches = secrets.compare_digest(login, self.settings.ADMIN_LOGIN)

        if login_matches:
            return verify_password(password, self.settings.ADMIN_PASSWORD_HASH)

        # Выполняем проверку пароля против фиктивного хэша, чтобы время ответа
        # было неотличимо от проверки реального пользователя
        verify_password(password, DUMMY_BCRYPT_HASH)
        return False

    def create_session_token(self, user: str) -> str:
        """Генерирует криптографически подписанный сессионный токен."""
        payload = {"sub": user}
        return create_session_token(payload, self.settings.SECRET_KEY)

    def verify_session_token(self, token: str) -> str:
        """Валидирует сессионный токен и возвращает имя пользователя.

        При нарушении подписи или истечении срока действия токена
        выбрасывает AuthenticationError (401).
        """
        try:
            payload = decode_session_token(
                token=token,
                secret_key=self.settings.SECRET_KEY,
                max_age=self.settings.SESSION_TTL,
            )
            user = payload.get("sub")
            if not user or not isinstance(user, str):
                raise AuthenticationError("Токен сессии не содержит корректного идентификатора пользователя")
            return user
        except SignatureExpired as exc:
            raise AuthenticationError("Срок действия сессии истек") from exc
        except BadSignature as exc:
            raise AuthenticationError("Недействительная цифровая подпись сессии") from exc
