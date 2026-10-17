# Спецификация архитектуры: Vertical Feature Slices & Zero-Friction Testkit

- **Дата:** 2026-10-15
- **Статус:** `DRAFT` (Готов к ревью)
- **Целевые директории:** `internal/core/`, `internal/features/`, `internal/testkit/`, `web/static/js/`
- **Соответствие инвариантам (AGENTS.md):** Pure Go (Chi), Zero-Node.js / Zero-npm, Sandbox Mandatory (`PathSandbox.Resolve()`), SQLite WAL, ID3v2.4 UTF-8.

---

## 1. Проблематика и контекст

### 1.1. Текущие архитектурные ограничения
1. **Слоистый монолит (Layered Monolith) и God-объекты:**
   - Пакет `internal/api/handlers/` разросся до 49 файлов.
   - `FileHandler` зависит от 11 сервисов и клиентов (`MusicSandbox`, `TmpSandbox`, `Database`, `Downloader`, `Organizer`, `Deezer`, `Lyrics`, `Scanner`, `Yandex`, `YTMusic`, `ITunes`).
   - `TagHandler` конфигурируется через мутирующие сеттеры (`SetConfig`, `SetMusicBrainz`, `SetShazam`).
   - Добавление новой фичи или изменение существующей требует одновременной модификации 5-6 файлов в разных частях дерева: `api/handlers/`, `services/`, `db/queries.go`, `cmd/server/main.go`, `web/static/js/`, `web/templates/`.

2. **Высокая когнитивная нагрузка для человека и AI (Vibecoding):**
   - При постановке задачи агенту необходимо передавать десятки файлов в контекст, что увеличивает время ответа, расход токенов и риск регрессий/галлюцинаций.
   - Отсутствие строгой локализации бизнес-логики приводит к случайному нарушению неявных контрактов между слоями.

3. **Многословность и трудоемкость тестирования:**
   - Каждый интеграционный тест вручную поднимает SQLite, создаёт временные песочницы, запускает мок-серверы и делает десятки проверок `assert.Equal` по каждому полю DTO.
   - Отсутствие универсального снапшот-тестирования (Golden Files) приводит к разрастанию бойлерплейта в тестах.

4. **Фронтенд-реактивность (Alpine.js No-Build):**
   - В шаблонах присутствуют прямые вызовы `fetch()` вперемешку с логикой UI.
   - Состояние частично размазано по глобальному `Alpine.store('app')` и локальным переменным компонентов, что создаёт риск рассинхронизации при обновлении очередей или тегов.

---

## 2. Цели и не-цели (Goals & Non-Goals)

### 2.1. Цели
- **Вертикальные слайсы (Vertical Slices):** Перевести ключевые домены в `internal/features/{downloader, tageditor, filemanager, audit, radio, radar}`. Каждая фича инкапсулирует свои HTTP-хендлеры, сервисную логику, DTO и профильные SQL-запросы к БД.
- **Саморегистрация маршрутов:** Каждая фича предоставляет метод `RegisterRoutes(r chi.Router)`. Сборка в `main.go` сводится к простому циклу вызовов.
- **Слабая связность:** Межфичевое взаимодействие строится через интерфейсы по месту потребления (*«consumer-driven interfaces»*). Полное исключение циклических импортов.
- **Хелперы `internal/core/httputil`:** Унификация парсинга JSON, сериализации ответов и обработки HTTP-ошибок.
- **Zero-Friction Testkit (`internal/testkit`):** Фабрика окружения `testkit.NewEnv(t)` с in-memory SQLite, временной песочницей и автомокающим транспортом для внешних каталогов (Deezer, iTunes, SoundCloud, MusicBrainz), плюс Golden Snapshot ассерты.
- **Модульный фронтенд:** Вынос обращений к серверу в типизированный клиент `web/static/js/api/client.js` и разделение сторов Alpine.js по фичам (`queue`, `puddletag`, `audit`, `radio`).

### 2.2. Не-цели
- Не переписывать парсер ID3v2.4 (`internal/id3`) — он полностью автономен, протестирован и стабилен.
- Не менять публичные HTTP API контракты — обратная совместимость со всеми существующими шаблонами и клиентами сохраняется на 100%.
- Не внедрять Node.js, npm, Webpack или Docker (строгий инвариант `AGENTS.md`).

---

## 3. Детальный дизайн компонентов

### 3.1. Структура фичи (Vertical Slice Blueprint)

Каждая фича в `internal/features/<name>/` организуется по единообразному шаблону:

```
internal/features/<name>/
├── feature.go          # Фабрика модуля, RegisterRoutes(r chi.Router), DI
├── handlers.go         # HTTP-обработчики (чистые контроллеры на httputil)
├── service.go          # Доменная бизнес-логика фичи
├── repository.go       # SQL-запросы к SQLite для сущностей этой фичи
├── types.go            # Входящие/исходящие DTO и доменные модели
└── feature_test.go     # Компактные тесты на базе testkit
```

#### Контракт регистрации маршрутов:
```go
package downloader

import (
    "github.com/go-chi/chi/v5"
)

type Module struct {
    service *Service
}

func NewModule(svc *Service) *Module {
    return &Module{service: svc}
}

func (m *Module) RegisterRoutes(r chi.Router) {
    r.Route("/downloads", func(r chi.Router) {
        r.Post("/", m.CreateJob)
        r.Get("/", m.ListJobs)
        r.Get("/{id}", m.GetJob)
        r.Post("/{id}/retry", m.RetryJob)
        r.Delete("/{id}", m.DeleteJob)
    })
}
```

### 3.2. Хелперы `internal/core/httputil`

Устраняют бойлерплейт и ошибки в HTTP-хендлерах:

```go
package httputil

import (
    "encoding/json"
    "net/http"
)

type ErrorResponse struct {
    Error string `json:"error"`
    Code  int    `json:"code,omitempty"`
}

func DecodeJSON[T any](r *http.Request, target *T) error {
    defer r.Body.Close()
    return json.NewDecoder(r.Body).Decode(target)
}

func RespondJSON(w http.ResponseWriter, status int, data any) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    if data != nil {
        _ = json.NewEncoder(w).Encode(data)
    }
}

func RespondError(w http.ResponseWriter, status int, message string) {
    RespondJSON(w, status, ErrorResponse{Error: message, Code: status})
}
```

### 3.3. Zero-Friction Testkit (`internal/testkit`)

Пакет `internal/testkit` предоставляет универсальный контекст для тестирования:

```go
package testkit

import (
    "bytes"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "os"
    "testing"

    "music-manager/internal/core/sandbox"
    "music-manager/internal/db"
)

type TestEnv struct {
    T            *testing.T
    DB           *db.DB
    MusicSandbox *sandbox.PathSandbox
    TmpSandbox   *sandbox.PathSandbox
    HTTPClient   *http.Client
    Server       *httptest.Server
}

func NewEnv(t *testing.T) *TestEnv {
    // 1. In-memory SQLite с накатом актуальной schema.sql
    // 2. Временные каталоги для Sandbox с базовыми фикстурами
    // 3. Автомокающий RoundTripper для внешних API
    ...
}

// AssertGoldenJSON сравнивает JSON с эталонным файлом testdata/*.golden.json
func AssertGoldenJSON(t *testing.T, actual []byte, goldenPath string) {
    t.Helper()
    ...
}
```

### 3.4. Модульный фронтенд (Alpine Stores & API Client)

1. **`web/static/js/api/client.js`**:
   Единая точка входа для сетевых запросов:
   ```javascript
   export const api = {
       downloader: {
           create: (url, source) => fetchJson('/downloads', { method: 'POST', body: { url, source } }),
           retry: (id) => fetchJson(`/downloads/${id}/retry`, { method: 'POST' }),
           list: () => fetchJson('/downloads'),
       },
       tags: {
           get: (path) => fetchJson(`/api/tags?path=${encodeURIComponent(path)}`),
           save: (payload) => fetchJson('/api/tags', { method: 'POST', body: payload }),
       },
       audit: {
           status: () => fetchJson('/api/audit/status'),
           fix: (ruleId, payload) => fetchJson(`/api/audit/fix/${ruleId}`, { method: 'POST', body: payload }),
       }
   };
   ```

2. **Выделенные Alpine Stores**:
   - `queue`: состояние фоновых закачек, прогресс-бары, авто-обновление по таймеру.
   - `puddletag`: выделенные дорожки, режим группировки, буфер изменений (dirty state), стек отмены (undo/redo).
   - `audit`: сводка проблем, списки альбомов на бэкфилл.

---

## 4. План поэтапной миграции (Migration Roadmap)

Миграция выполняется инкрементально, с сохранением 100% прохождения тестов на каждом шаге:

1. **Этап 1: Инфраструктурный фундамент (Core & Testkit)**
   - Реализация `internal/core/httputil`.
   - Реализация `internal/testkit` и базовых фикстур.
   - Валидация через юнит-тесты.

2. **Этап 2: Выделение первого вертикального слайса — Downloader**
   - Создание `internal/features/downloader/`.
   - Перенос логики из `services/downloader/` и `handlers/download_handlers.go`.
   - Вынос запросов задач в `downloader/repository.go`.
   - Подключение `downloader.NewModule().RegisterRoutes(r)` в роутере.
   - Прогон `go test ./...` и e2e тестов.

3. **Этап 3: Выделение слайса Audit & Backfill**
   - Создание `internal/features/audit/`.
   - Перенос `audit_handlers.go`, `tag_audit_*.go`, `backfill_handlers.go`.
   - Проверка изоляции.

4. **Этап 4: Выделение Tag Editor (Puddletag & Metadata)**
   - Создание `internal/features/tageditor/`.
   - Ликвидация сеттеров `TagHandler`, внедрение локальных интерфейсов.

5. **Этап 5: Фронтенд-рефакторинг (API Client & Stores)**
   - Добавление `web/static/js/api/client.js`.
   - Перевод вызовов в компонентах на сторы и `api.client`.
   - Проверка через `web/template_validator_test.go` и Playwright.

---

## 5. Риски и их минимизация

| Риск | Вероятность | Влияние | Стратегия минимизации |
|---|---|---|---|
| **Регрессия в путях API** | Средняя | Высокая | Все роуты сохраняются неизменными. Запуск `e2e_user_journey_test.go` на каждом этапе. |
| **Циклические зависимости в Go** | Низкая | Высокая | Использование локальных интерфейсов потребителей (*«accept interfaces, return structs»*). |
| **Поломка Alpine.js биндингов** | Средняя | Средняя | Проверка через статический валидатор шаблонов `web/template_validator_test.go` при каждом билде. |
| **Рассинхронизация SQLite WAL** | Низкая | Высокая | Транзакционный раннер остаётся в `internal/db`, репозитории фич работают через единый пул `*sql.DB`. |

---

## 6. Критерии приемки (Acceptance Criteria)

1. Все 13 Go-пакетов проекта собираются без ошибок (`go build -ldflags="-s -w" -o bin/music-manager cmd/server/main.go`).
2. `go test -race ./...` проходит на 100% с 0 data races.
3. Валидатор шаблонов `web/template_validator_test.go` возвращает 0 ошибок.
4. E2E сценарии (`internal/api/e2e_user_journey_test.go` и `scripts/test_user_flows.sh`) успешно выполняются.
5. `PROGRESS.md` актуализирован.
