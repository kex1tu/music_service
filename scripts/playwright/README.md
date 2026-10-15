# Playwright Frontend Audit Tests

Внешний инструмент E2E тестирования фронтенда (Alpine.js, Tailwind CDN, Audio Player, Downloader, Radar).

> [!NOTE]
> Согласно `AGENTS.md`, Playwright хранится строго изолированно в `scripts/playwright/` и не является зависимостью сервиса. В `go.mod` и `web/` зависимости npm не добавляются.

## Установка

```bash
cd scripts/playwright
npm init -y
npm install --save-dev @playwright/test
npx playwright install chromium
```

## Запуск

1. Запустите backend-сервис:
```bash
./bin/music-manager
```

2. В отдельном терминале запустите E2E тесты:
```bash
cd scripts/playwright
BASE_URL="http://localhost:8080" MM_PASSWORD="yourpassword" npx playwright test
```
