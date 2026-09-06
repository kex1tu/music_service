"""Интеграционные тесты для роутера аутентификации и зависимости get_current_user."""

from fastapi import Depends
from fastapi.testclient import TestClient

from app.config import Settings
from app.dependencies import get_current_user
from app.main import create_app
from tests.conftest import TEST_ADMIN_PASSWORD


def test_login_success(client: TestClient, test_settings: Settings):
    """Проверка успешного логина, формата ответа и атрибутов сессионного cookie."""
    response = client.post(
        "/login",
        json={"login": "admin", "password": TEST_ADMIN_PASSWORD},
    )
    assert response.status_code == 200
    data = response.json()
    assert data["success"] is True
    assert "Авторизация успешна" in data["message"]

    # Проверяем заголовок Set-Cookie
    set_cookie = response.headers.get("set-cookie", "").lower()
    assert test_settings.SESSION_COOKIE_NAME.lower() in set_cookie
    assert "httponly" in set_cookie
    assert "samesite=strict" in set_cookie


def test_login_api_alias(client: TestClient):
    """Проверка алиаса POST /api/auth/login."""
    response = client.post(
        "/api/auth/login",
        json={"login": "admin", "password": TEST_ADMIN_PASSWORD},
    )
    assert response.status_code == 200
    assert response.json()["success"] is True


def test_login_wrong_password(client: TestClient):
    """Проверка возврата HTTP 401 при неверном пароле."""
    response = client.post(
        "/login",
        json={"login": "admin", "password": "wrong_password"},
    )
    assert response.status_code == 401
    data = response.json()
    assert data["error"] == "UNAUTHORIZED"
    assert "Неверный логин или пароль" in data["message"]


def test_login_wrong_username(client: TestClient):
    """Проверка возврата HTTP 401 при несуществующем логине."""
    response = client.post(
        "/login",
        json={"login": "intruder", "password": TEST_ADMIN_PASSWORD},
    )
    assert response.status_code == 401
    data = response.json()
    assert data["error"] == "UNAUTHORIZED"


def test_login_validation_error(client: TestClient):
    """Проверка возврата HTTP 422 при некорректном теле запроса."""
    response = client.post("/login", json={"login": ""})
    assert response.status_code == 422
    data = response.json()
    assert data["error"] == "VALIDATION_ERROR"


def test_logout(client: TestClient, test_settings: Settings):
    """Проверка выхода и очистки сессионного cookie."""
    # Сначала выполняем логин
    login_resp = client.post(
        "/login",
        json={"login": "admin", "password": TEST_ADMIN_PASSWORD},
    )
    assert login_resp.status_code == 200

    # Выполняем logout
    logout_resp = client.post("/logout")
    assert logout_resp.status_code == 200
    assert logout_resp.json()["success"] is True

    # Проверяем удаление cookie
    set_cookie = logout_resp.headers.get("set-cookie", "")
    assert test_settings.SESSION_COOKIE_NAME in set_cookie
    # Браузер удаляет cookie при max-age=0 или expires в прошлом
    assert "max-age=0" in set_cookie.lower() or "expires=" in set_cookie.lower()


def test_get_current_user_unauthorized(client: TestClient):
    """Проверка отказа в доступе без сессионного cookie."""
    response = client.get("/api/auth/me")
    assert response.status_code == 401
    data = response.json()
    assert data["error"] == "UNAUTHORIZED"
    assert "Требуется аутентификация" in data["message"]


def test_get_current_user_with_cookie(authenticated_client: TestClient):
    """Проверка успешного получения данных текущего пользователя по cookie."""
    response = authenticated_client.get("/api/auth/me")
    assert response.status_code == 200
    data = response.json()
    assert data["login"] == "admin"
    assert data["authenticated"] is True


def test_get_current_user_with_bearer_token(
    client: TestClient, session_token: str
):
    """Проверка авторизации через заголовок Authorization: Bearer <token>."""
    headers = {"Authorization": f"Bearer {session_token}"}
    response = client.get("/api/auth/me", headers=headers)
    assert response.status_code == 200
    data = response.json()
    assert data["login"] == "admin"
    assert data["authenticated"] is True


def test_get_current_user_tampered_cookie(client: TestClient, test_settings: Settings):
    """Проверка отказа при модифицированном сессионном cookie."""
    client.cookies.set(test_settings.SESSION_COOKIE_NAME, "tampered_invalid_token_value")
    response = client.get("/api/auth/me")
    assert response.status_code == 401
    data = response.json()
    assert data["error"] == "UNAUTHORIZED"


def test_protected_route_dependency(test_settings: Settings, session_token: str):
    """Проверка защиты кастомного маршрута через Depends(get_current_user)."""
    app = create_app(test_settings)

    @app.get("/api/protected-resource")
    async def protected_endpoint(user: str = Depends(get_current_user)):
        return {"data": "confidential", "user": user}

    with TestClient(app) as test_client:
        # Без токена
        resp_unauth = test_client.get("/api/protected-resource")
        assert resp_unauth.status_code == 401

        # С валидным cookie
        test_client.cookies.set(test_settings.SESSION_COOKIE_NAME, session_token)
        resp_auth = test_client.get("/api/protected-resource")
        assert resp_auth.status_code == 200
        assert resp_auth.json() == {"data": "confidential", "user": "admin"}
