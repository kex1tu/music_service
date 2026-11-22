#!/usr/bin/env python3
"""
E2E User Flow Tester for Music Manager (Python + Playwright).
Тестирует интерфейс глазами реального пользователя:
1. Перехватывает ВСЕ необработанные ошибки JavaScript (pageerror и console.error).
2. Проверяет переключение ключевых вкладок (Загрузки, Медиатека, Радар).
3. Проверяет активность кнопок (отсутствие ошибочных :disabled блокировок).
4. Проверяет рендеринг списков и отсутствие фантомных дубликатов.
"""

import sys
import os
import time
import subprocess
import argparse

try:
    from playwright.sync_api import sync_playwright, expect
except ImportError:
    print("\n[INFO] Playwright для Python не установлен.")
    print("Для запуска браузерных тестов выполните:")
    print("  pip install playwright && playwright install chromium\n")
    sys.exit(0)

def run_e2e_audit(base_url="http://127.0.0.1:8080", password="admin"):
    print(f"\n========================================================")
    print(f"  Запуск E2E User Flow тестирования: {base_url}")
    print(f"========================================================\n")

    js_errors = []

    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        context = browser.new_context(viewport={"width": 1440, "height": 900})
        page = context.new_page()

        # Слушаем консоль браузера на наличие ошибок скриптов
        page.on("pageerror", lambda err: js_errors.append(f"[PageError] {err}"))
        page.on("console", lambda msg: js_errors.append(f"[ConsoleError] {msg.text}") if msg.type == "error" else None)

        try:
            # 1. Открываем главную страницу
            print("[1/5] Проверка загрузки страницы и авторизации...")
            page.goto(base_url, timeout=10000)
            page.wait_for_load_state("networkidle")

            # Если открыто модальное окно логина, выполняем вход
            login_input = page.locator('input[type="password"]')
            if login_input.is_visible():
                login_input.fill(password)
                page.keyboard.press("Enter")
                page.wait_for_load_state("networkidle")
                time.sleep(1)

            # Проверяем инициализацию Alpine стора
            store_ready = page.evaluate("() => typeof window.Alpine !== 'undefined' && typeof window.Alpine.store('app') !== 'undefined'")
            assert store_ready, "Alpine.store('app') не инициализирован!"
            print("  ✓ Главная страница загружена, Alpine.js стор активен.")

            # 2. Вкладка Радар: проверка интерфейса и списков
            print("[2/5] Проверка вкладки «Радар»...")
            radar_btn = page.locator('button:has-text("Радар"), [data-tab="radar"]').first
            if radar_btn.is_visible():
                radar_btn.click()
                time.sleep(0.5)

                # Проверка кнопок действий в Радаре
                scan_btn = page.locator('button:has-text("Проверить новинки"), button:has-text("Сканировать")').first
                if scan_btn.is_visible():
                    expect(scan_btn).to_be_enabled()

                # Проверка отсутствия дубликатов среди отображаемых карточек
                cards = page.locator('[data-release-id]').all()
                if len(cards) > 0:
                    release_ids = [c.get_attribute("data-release-id") for c in cards]
                    assert len(release_ids) == len(set(release_ids)), f"Обнаружены дубликаты карточек в Радаре: {release_ids}"
                print("  ✓ Вкладка «Радар» интерактивна, дубликаты отсутствуют.")

            # 3. Вкладка Загрузчик и проверка кнопок очереди
            print("[3/5] Проверка Загрузчика и карточек очереди...")
            downloader_btn = page.locator('button:has-text("Загрузки"), [data-tab="downloader"]').first
            if downloader_btn.is_visible():
                downloader_btn.click()
                time.sleep(0.5)

                url_input = page.locator('input[placeholder*="ссылк"], input[placeholder*="URL"]').first
                expect(url_input).to_be_visible()

                # Проверка карточек в статусе READY (если есть)
                ready_cards = page.locator('.job-card:has-text("READY"), [data-status="READY"]').all()
                for rc in ready_cards:
                    accept_btn = rc.locator('button:has-text("Принять в медиатеку")')
                    if accept_btn.is_visible():
                        expect(accept_btn).to_be_enabled()
                print("  ✓ Форма загрузчика и кнопки очереди в активном состоянии.")

            # 4. Файловый менеджер и поиск
            print("[4/5] Проверка Файлового менеджера и поиска...")
            files_btn = page.locator('button:has-text("Медиатека"), [data-tab="files"]').first
            if files_btn.is_visible():
                files_btn.click()
                time.sleep(0.5)
                search_box = page.locator('input[placeholder*="Поиск"]').first
                if search_box.is_visible():
                    expect(search_box).to_be_enabled()
                print("  ✓ Файловый менеджер и поисковая строка доступны.")

            # 5. Аудиоплеер в нижней панели
            print("[5/5] Проверка панели аудиоплеера...")
            player_panel = page.locator('[x-data*="audioPlayerComponent"], [x-data*="player"]').first
            expect(player_panel).to_be_attached()
            print("  ✓ Аудиоплеер присутствует в DOM.")

        except Exception as e:
            # Снимок экрана при сбое для отладки
            os.makedirs("scripts/playwright/artifacts", exist_ok=True)
            screenshot_path = "scripts/playwright/artifacts/failure_screenshot.png"
            page.screenshot(path=screenshot_path, full_page=True)
            print(f"\n[FAIL] Тест упал с ошибкой: {e}")
            print(f"Снимок экрана сохранен в: {screenshot_path}")
            browser.close()
            sys.exit(1)

        browser.close()

    # Финальная проверка пойманных JS-ошибок
    if js_errors:
        print("\n[CRITICAL] Обнаружены ошибки JavaScript в консоли браузера:")
        for err in js_errors:
            print(f"  • {err}")
        sys.exit(1)

    print("\n========================================================")
    print("  ✓ Все пользовательские E2E проверки успешно пройдены!")
    print("  ✓ 0 JS-ошибок в консоли браузера.")
    print("========================================================\n")

if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="E2E User Flow Tester")
    parser.add_argument("--url", default=os.getenv("BASE_URL", "http://127.0.0.1:8080"), help="Base URL сервиса")
    parser.add_argument("--password", default=os.getenv("MM_PASSWORD", "admin"), help="Пароль администратора")
    args = parser.parse_args()

    run_e2e_audit(args.url, args.password)
