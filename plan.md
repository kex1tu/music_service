# Комплексный генеральный план архитектурного рефакторинга: Vertical Feature Slices, Core-каркас и Zero-Friction Testkit

- **Дата составления:** 2026-10-15  
- **Статус плана:** `COMPLETED & VERIFIED (100% DONE)`  
- **Целевые директории:** `internal/core/`, `internal/testkit/`, `internal/features/`, `internal/api/`, `cmd/server/`, `web/static/js/`  
- **Архитектурные инварианты (AGENTS.md):** Pure Go (Chi), Zero-Python в серверной части, Zero Node.js / Zero npm, Zero Docker, Sandbox Mandatory (`PathSandbox.Resolve()`), ID3v2.4 UTF-8 (null-separated), SQLite WAL, No-Build Frontend (Tailwind CDN + Alpine.js CDN).

---

## 1. Цель и границы архитектурной трансформации (Objective & Scope)

### 1.1. Проблематика кодовой базы
1. **Эрозия слоев и разрастание монолитных пакетов:**
   - Пакет `internal/api/handlers/` содержит **49 файлов**, собранных в единый неймспейс.
   - `FileHandler` превратился в God-объект с **11 зависимостями** в конструкторе (`MusicSandbox`, `TmpSandbox`, `Database`, `Downloader`, `Organizer`, `Deezer`, `Lyrics`, `Scanner`, `Yandex`, `YTMusic`, `ITunes`).
   - `TagHandler` конфигурируется через мутирующие сеттеры (`SetConfig`, `SetMusicBrainz`, `SetShazam`), что нарушает иммутабельность сервисного слоя и усложняет тестирование.
   - Файл `internal/db/queries.go` содержит свыше **1500 строк** разнородных SQL-запросов (от очередей загрузки до аудита дубликатов и радиоволны).
2. **Высокая когнитивная сложность для AI-вайбкодинга и разработчиков:**
   - Любое изменение функционала (например, добавление поля в задачу загрузки) требует правок в 5–6 несвязанных местах: в DTO хендлера, в `Service`, в `JobManager`, в `queries.go`, в роутере и в JavaScript-шаблонах.
   - Из-за большого числа затронутых файлов контекст агента перегружается, что повышает риск опечаток и скрытых регрессий.
3. **Трудоемкость тестирования (Boilerplate Overhead):**
   - Написание интеграционных тестов для каждого хендлера требует 50–100 строк подготовки: создание временных файлов, запуск локального `httptest.Server`, мокирование внешних HTTP-серверов и десятки проверок `if got != want` по каждому полю ответа.
4. **Реактивные риски на фронтенде (No-Build Alpine.js):**
   - Прямые вызовы `fetch()` с инлайн-парсингом ошибок разбросаны по 20+ шаблонам компонентов.
   - Состояние частично дублируется между глобальным `Alpine.store('app')` и локальными скоупами компонентов.

### 1.2. Целевой результат
1. **Переход к вертикальным слайсам (Vertical Feature Slices):**
   - Разбиение функционала на 6 самодостаточных пакетов в `internal/features/`:
     - `downloader`: пайплайн yt-dlp/ffmpeg, SoundCloud-First matcher, управление очередью задач.
     - `tageditor`: Puddletag табличный редактор, пакетное тегирование, обложки APIC, MusicBrainz OAuth, Shazam.
     - `filemanager`: файловое дерево, переименование, организация медиатеки, потоковое вещание (Audio Stream).
     - `audit`: инспекция медиатеки, чекер неполных альбомов, бэкфилл Deezer, нормализация жанров, дедупликация.
     - `radio`: «Моя Волна», генерация M3U, ICY стриминг с метаданными, интеграция с Navidrome/Subsonic.
     - `radar`: сканирование дискографий любимых исполнителей, агрегация новых релизов.
2. **Саморегистрирующиеся модули (`RegisterRoutes`):**
   - Каждая фича реализует единый интерфейс модуля и регистрирует свои маршруты в переданном Chi-роутере. Роутер в `main.go` больше не знает о деталях отдельных хендлеров.
3. **Изоляция зависимостей через локальные интерфейсы потребителей:**
   - Никаких перекрестных импортов между фичами. Если фича `radar` инициирует загрузку, она объявляет локальный интерфейс `JobStarter`, реализуемый модулем `downloader`.
4. **Инфраструктурный пакет `internal/core/httputil`:**
   - Типобезопасный `DecodeJSON[T](r, &req)` с защитой от переполнения буфера (10 МБ лимит) и гарантированным закрытием `r.Body`.
   - `RespondJSON(w, code, data)` и стандартизированный `RespondError(w, code, message)`.
   - Хелперы для извлечения query-параметров (`QueryString`, `QueryInt`, `QueryBool`).
5. **Zero-Friction Testkit (`internal/testkit`):**
   - `testkit.NewEnv(t)`: создание изолированного окружения за 2 мс (in-memory SQLite, временная песочница с фикстурами MP3, автомокающий транспорт внешних каталогов).
   - `testkit.AssertGoldenJSON(t, got, "fixtures/expected.golden.json")`: сокращение кода тестов с 50 до 5 строк.
6. **Типизированный клиент API и модульные сторы Alpine.js:**
   - Создание `web/static/js/api/client.js` со строгой структуризацией запросов (`api.downloader.*`, `api.tags.*`, `api.audit.*`).
   - Изоляция реактивных сторов: `$store.queue`, `$store.puddletag`, `$store.audit`.

### 1.3. Границы проекта (Scope)
- **Входит в объем (In-Scope):**
  - Создание `internal/core/httputil` с полным набором unit-тестов.
  - Создание `internal/testkit` (фабрика тестового окружения, автомокирование HTTP, Golden File snapshot engine).
  - Поэтапный перенос 6 вертикальных слайсов (`downloader`, `tageditor`, `filemanager`, `audit`, `radio`, `radar`).
  - Миграция SQL-запросов из `internal/db/queries.go` в локальные `repository.go` внутри соответствующих фич.
  - Адаптация точки входа `cmd/server/main.go` и маршрутизатора `internal/api/router.go`.
  - Создание `web/static/js/api/client.js` и переход шаблонов на вызовы сторов и клиента.
  - Обеспечение 100% прохождения `go test -race ./...` и Playwright E2E проверок на каждом этапе.
- **Не входит в объем (Out-of-Scope):**
  - Изменение бинарного парсера ID3v2.4 в `internal/id3` (пакет полностью отлажен и стабилен).
  - Изменение схемы таблиц SQLite (структура БД остаётся неизменной).
  - Добавление Node.js/npm зависимостей или Docker.

---

## 2. Анализ текущего состояния системы (Current State Analysis)

### 2.1. Инвентаризация монолитных хендлеров
В текущем пакете [`internal/api/handlers/`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/) сосредоточено 49 файлов:
- **Загрузка и очереди:** `download_handlers.go`, `download_preview_handler.go`, `queue_handlers.go`, `torrent_handlers.go`.
- **Теги и обложки:** `tag_handlers.go`, `tag_save_handlers.go`, `tag_cover_handlers.go`, `batch_covers_handlers.go`, `batch_lyrics_handlers.go`, `musicbrainz_oauth_handlers.go`, `recognize_handlers.go`.
- **Аудит и целостность:** `audit_handlers.go`, `backfill_handlers.go`, `duplicate_handlers.go`, `tag_audit_handlers.go`, `tag_audit_analyzer.go`, `tag_audit_fix_handlers.go`, `tag_audit_album_handlers.go`, `path_fix_handlers.go`.
- **Файловые операции:** `file_handlers.go`, `file_rename_handlers.go`, `reorganize_handlers.go`, `stream_handlers.go`, `stream_search_handlers.go`, `catalog_match_handlers.go`, `catalog_unified_handlers.go`, `deezer_search_handlers.go`, `discography_handlers.go`.
- **Радио и радар:** `radio_handlers.go`, `radar_handlers.go`.

### 2.2. Карта перекрестных зависимостей
```
cmd/server/main.go ──► FileHandler (11 зависимостей)
                    ──► TagHandler (5 зависимостей + 3 сеттера)
                    ──► DownloadHandler (2 зависимости)
                    ──► RadioHandler (7 зависимостей)
                    ──► RadarHandler (2 зависимости)
```
*Проблема:* `FileHandler` выполняет функции файлового менеджера, стримера аудио, каталожного поисковика, менеджера дубликатов и дискографий. Любое изменение сигнатуры в сервисе требует перекомпиляции и перенастройки огромного объекта.

---

## 3. Архитектурный проект целевого состояния (Target Architecture Blueprint)

### 3.1. Общая топология модулей
```
music-manager/
├── cmd/
│   └── server/
│       └── main.go                 # Composition Root: инициализация Core, запуск Feature Modules
├── internal/
│   ├── config/                     # Настройки (.env)
│   ├── core/                       # Платформенный базис (Shared Kernel)
│   │   ├── auth/                   # Сессии HMAC-SHA256, bcrypt
│   │   ├── sandbox/                # PathSandbox (изоляция путей)
│   │   └── httputil/               # DecodeJSON, RespondJSON, RespondError, Query-хелперы
│   ├── db/                         # Базовый пул SQLite WAL, schema.sql, RunInTx
│   ├── id3/                        # ID3v2.4 Engine (парсер/генератор, genres, tags)
│   ├── testkit/                    # Тестовый полигон (In-memory DB, Sandboxed TempDir, Golden Snapshots)
│   └── features/                   # Вертикальные слайсы (Vertical Feature Slices)
│       ├── downloader/             # Слайс 1: Загрузка и очередь
│       ├── tageditor/              # Слайс 2: Редактор тегов Puddletag, обложки, MusicBrainz
│       ├── filemanager/            # Слайс 3: Навигация, перемещение, стриминг
│       ├── audit/                  # Слайс 4: Аудит целостности, бэкфилл, дубликаты
│       ├── radio/                  # Слайс 5: «Моя Волна», M3U, ICY стриминг
│       └── radar/                  # Слайс 6: Сканер дискографий и новых релизов
├── web/
│   ├── static/
│   │   ├── js/
│   │   │   ├── api/client.js       # Единый типизированный API-клиент
│   │   │   └── store/              # Изолированные сторы ($store.queue, $store.puddletag, ...)
│   │   └── css/
│   └── templates/                  # Модульные HTML-шаблоны без сборки
```

### 3.2. Спецификация контракта фиче-модуля
Каждый вертикальный слайс обязан реализовать контракт:
```go
package features

import "github.com/go-chi/chi/v5"

// Module представляет саморегистрирующийся вертикальный слайс системы.
type Module interface {
    Name() string
    RegisterRoutes(r chi.Router)
}
```

---

## 4. Пошаговая декомпозиция реализации (Detailed Implementation Roadmap)

Реализация разделена на 6 последовательных фаз. На каждом шаге гарантируется непрерывная компиляция и зелёный статус всех тестов (`go test -race ./...`).

---

### Фаза 1: Инфраструктурный фундамент (Core & Testkit)

#### 1.1. Пакет `internal/core/httputil`
- **[NEW] `internal/core/httputil/httputil.go`**
  - Константа `DefaultMaxBodyBytes = 10 << 20` (10 МБ).
  - Структура `ErrorResponse { Error string, Code int }`.
  - Дженерик-функция `DecodeJSON[T any](r *http.Request, target *T) error`:
    - Проверка `r.Body != nil`.
    - Ограничение чтения через `io.LimitReader`.
    - Гарантированный `defer r.Body.Close()`.
  - `RespondJSON(w http.ResponseWriter, status int, data any)`: установка заголовка `Content-Type: application/json`, запись статуса и сериализация.
  - `RespondError(w http.ResponseWriter, status int, message string)`: формирование единого JSON-ответа об ошибке.
  - Хелперы: `QueryString`, `QueryInt`, `QueryBool`.
- **[NEW] `internal/core/httputil/httputil_test.go`**
  - Тесты: валидный парсинг, битый JSON, превышение лимита, nil-body, извлечение query-параметров с fallback-значениями.

#### 1.2. Пакет `internal/testkit`
- **[NEW] `internal/testkit/testenv.go`**
  - Структура `TestEnv`:
    ```go
    type TestEnv struct {
        T            *testing.T
        DB           *db.DB
        MusicSandbox *sandbox.PathSandbox
        TmpSandbox   *sandbox.PathSandbox
        Server       *httptest.Server
        Client       *http.Client
        MockHTTP     *MockTransport
    }
    ```
  - Метод `NewEnv(t *testing.T) *TestEnv`:
    - Создание уникального `t.TempDir()`.
    - Инициализация SQLite базы `db.Open(filepath.Join(tempDir, "test.db"))`.
    - Инициализация песочниц `MusicDir` и `TmpDir`.
    - Настройка мок-транспорта `http.RoundTripper` для перехвата сетевых запросов к Deezer, SoundCloud, iTunes.
  - Хелперы для тестов:
    - `env.CreateDummyMP3(relPath string, title, artist, album string) string`: генерация валидного MP3-файла с ID3v2.4 фреймами.
    - `env.PostJSON(path string, body any) *http.Response`.
    - `env.Get(path string) *http.Response`.
- **[NEW] `internal/testkit/golden.go`**
  - Функция `AssertGoldenJSON(t *testing.T, actual []byte, goldenPath string)`:
    - Форматирование JSON с отступами (`json.Indent`).
    - Сравнение с содержимым файла `goldenPath`.
    - Если файл отсутствует или задана переменная `UPDATE_GOLDEN=true`, автоматическая запись эталона.
- **[NEW] `internal/testkit/mock_transport.go`**
  - Перехват внешних HTTP-запросов и возврат детерминированных заготовленных фикстур по шаблону URL.
- **[NEW] `internal/testkit/testenv_test.go`**
  - Верификация фабрики окружения, песочницы и механизма Golden Files.

---

### Фаза 2: Слайс 1 — `internal/features/downloader`

#### 2.1. Структура слайса
```
internal/features/downloader/
├── feature.go          # Фабрика NewModule, RegisterRoutes
├── handlers.go         # HTTP хендлеры
├── service.go          # Воркер-пул, yt-dlp/ffmpeg пайплайн
├── repository.go       # SQL-запросы к таблице download_jobs
├── types.go            # Request/Response DTO
└── feature_test.go     # Интеграционные тесты на testkit
```

#### 2.2. Детальная спецификация файлов
- **[NEW] `internal/features/downloader/types.go`**
  ```go
  type CreateJobRequest struct {
      URL    string `json:"url"`
      Source string `json:"source"`
  }
  type BatchJobRequest struct {
      URLs   []string `json:"urls"`
      Source string   `json:"source"`
  }
  type JobResponse struct {
      ID          int64   `json:"id"`
      URL         string  `json:"url"`
      Source      string  `json:"source"`
      Status      string  `json:"status"`
      Title       string  `json:"title"`
      Artist      string  `json:"artist"`
      Progress    float64 `json:"progress"`
      Error       string  `json:"error,omitempty"`
      CreatedAt   string  `json:"created_at"`
  }
  ```
- **[NEW] `internal/features/downloader/repository.go`**
  - Перенос SQL-запросов из `internal/db/queries.go`:
    - `CreateJob(ctx context.Context, job *Job) (int64, error)`
    - `GetJob(ctx context.Context, id int64) (*Job, error)`
    - `ListJobs(ctx context.Context, limit, offset int) ([]*Job, error)`
    - `UpdateJobProgress(ctx context.Context, id int64, progress float64, status string) error`
    - `DeleteJob(ctx context.Context, id int64) error`
    - `ResetInterruptedJobs(ctx context.Context) error`
- **[NEW] `internal/features/downloader/service.go`**
  - Перенос логики из `internal/services/downloader/`:
    - Ограничение конкурентных загрузок через семафор `cfg.MaxConcurrentDownloads`.
    - Асинхронный запуск `yt-dlp` и `ffmpeg` с контролем контекста.
    - SoundCloud-First AudioMatcher и fallback на YouTube Music.
    - Автоматическая очистка каталога `tmp_downloads/{job_id}/` после завершения или сбоя.
- **[NEW] `internal/features/downloader/handlers.go`**
  - Методы модуля:
    - `CreateJob(w http.ResponseWriter, r *http.Request)`
    - `CreateBatch(w http.ResponseWriter, r *http.Request)`
    - `ListJobs(w http.ResponseWriter, r *http.Request)`
    - `GetJob(w http.ResponseWriter, r *http.Request)`
    - `RetryJob(w http.ResponseWriter, r *http.Request)`
    - `DeleteJob(w http.ResponseWriter, r *http.Request)`
    - `PreviewJob(w http.ResponseWriter, r *http.Request)`
- **[NEW] `internal/features/downloader/feature.go`**
  - Реализация `Module`:
    ```go
    func (m *Module) RegisterRoutes(r chi.Router) {
        r.Route("/downloads", func(r chi.Router) {
            r.Post("/", m.CreateJob)
            r.Get("/", m.ListJobs)
            r.Post("/batch", m.CreateBatch)
            r.Get("/preview", m.PreviewJob)
            r.Route("/{id}", func(r chi.Router) {
                r.Get("/", m.GetJob)
                r.Post("/retry", m.RetryJob)
                r.Delete("/", m.DeleteJob)
            })
        })
    }
    ```
- **[NEW] `internal/features/downloader/feature_test.go`**
  - Тестирование всего жизненного цикла задачи через `testkit.NewEnv(t)`:
    - Создание задачи -> проверка ответа -> повтор упавшей задачи -> удаление задачи -> Golden JSON снапшот.

---

### Фаза 3: Слайс 2 — `internal/features/tageditor`

#### 3.1. Структура слайса
```
internal/features/tageditor/
├── feature.go          # Регистрация маршрутов /api/tags
├── handlers.go         # ReadTags, SaveTags, BatchApply
├── puddletag.go        # Логика табличного редактирования и валидации ID3
├── covers.go           # Извлечение APIC, галерея, сжатие cover.jpg
├── musicbrainz.go      # Авторизация OAuth и получение метаданных
├── shazam.go           # Распознавание треков по сигнатуре
├── types.go            # DTO тегов и альбомов
└── feature_test.go     # Тестирование чтения и записи тегов
```

#### 3.2. Ключевые изменения и ликвидация God-объекта
- Устранение сеттеров `TagHandler.SetConfig`, `TagHandler.SetMusicBrainz`.
- Конструктор `NewModule` принимает неизменяемую конфигурацию через интерфейсы:
  ```go
  type MetadataProvider interface {
      SearchReleases(ctx context.Context, query string) ([]Release, error)
  }
  type FingerprintProvider interface {
      Recognize(ctx context.Context, audioPath string) (*TrackMetadata, error)
  }
  ```
- Строгая валидация ID3v2.4 UTF-8 с null-разделителями (`\x00`) для полей `Artist` и `Genre`.
- Запрет вшивания текстов в MP3 (инвариант сохранен).

---

### Фаза 4: Слайс 3 — `internal/features/filemanager`

#### 4.1. Структура слайса
```
internal/features/filemanager/
├── feature.go          # Регистрация /api/files, /api/stream
├── handlers.go         # ListFiles, RenameFile, DeleteFile, StreamTrack
├── organizer.go        # Раскладка {Album} - {Artist}/01. {Title}.mp3
├── companions.go       # Перемещение сопутствующих .lrc и .jpg
├── streamer.go         # Поддержка HTTP 206 Partial Content (Seek)
└── feature_test.go     # Тесты переименования, песочницы и стриминга
```

#### 4.2. Безопасность и Sandbox Guard
- Каждая файловая операция проходит валидацию `PathSandbox.Resolve()`.
- Потоковое воспроизведение (`StreamTrack`) использует `http.ServeContent` с проверкой расширения `.mp3` и заголовками `Content-Range`.

---

### Фаза 5: Слайсы 4, 5, 6 — `audit`, `radio`, `radar`

1. **`internal/features/audit`:**
   - Инспекция медиатеки на соответствие стандартам (битрейт 320 kbps, наличие обложки, текстов `.lrc`).
   - Сверка неполных альбомов с официальным каталогом Deezer.
   - Механизм бэкфилла недостающих дорожек в один клик.
   - Дедупликация файлов на основе сверки ISRC и аудио-отпечатков.
2. **`internal/features/radio`:**
   - Алгоритм формирования очереди «Моя Волна» на основе истории прослушиваний Navidrome.
   - ICY-стриминг с трансляцией заголовков треков в Shoutcast/Icecast клиенты.
   - Ежедневный генератор плейлиста `Моя Волна.m3u` в 05:00.
3. **`internal/features/radar`:**
   - Периодический мониторинг релизов артистов из медиатеки через Deezer и MusicBrainz.
   - Управление списком отклоненных/скрытых карточек релизов.

---

### Фаза 6: Фронтенд-архитектура и типизированный клиент

#### 6.1. Модуль `web/static/js/api/client.js`
Единый слой сетевых обращений с обработкой ошибок:
```javascript
async function request(url, options = {}) {
    const res = await fetch(url, {
        headers: { 'Content-Type': 'application/json', ...(options.headers || {}) },
        ...options,
    });
    if (!res.ok) {
        const errorData = await res.json().catch(() => ({ error: res.statusText }));
        throw new Error(errorData.error || `HTTP ${res.status}`);
    }
    return res.json();
}

export const api = {
    downloader: {
        list: () => request('/downloads'),
        create: (url, source) => request('/downloads', { method: 'POST', body: JSON.stringify({ url, source }) }),
        retry: (id) => request(`/downloads/${id}/retry`, { method: 'POST' }),
        delete: (id) => request(`/downloads/${id}`, { method: 'DELETE' }),
    },
    tags: {
        get: (path) => request(`/api/tags?path=${encodeURIComponent(path)}`),
        save: (payload) => request('/api/tags', { method: 'POST', body: JSON.stringify(payload) }),
    },
    files: {
        tree: (dir) => request(`/api/files?dir=${encodeURIComponent(dir || '')}`),
        rename: (oldPath, newPath) => request('/api/files/rename', { method: 'POST', body: JSON.stringify({ oldPath, newPath }) }),
    },
    audit: {
        status: () => request('/api/audit/status'),
        fix: (ruleId, payload) => request(`/api/audit/fix/${ruleId}`, { method: 'POST', body: JSON.stringify(payload) }),
    }
};
```

#### 6.2. Рефакторинг сторов Alpine.js
- Вынос очереди в выделенный модуль `web/static/js/store/queue.js`:
  ```javascript
  Alpine.store('queue', {
      jobs: [],
      loading: false,
      async fetch() {
          this.jobs = await api.downloader.list();
      },
      async retry(id) {
          await api.downloader.retry(id);
          await this.fetch();
      }
  });
  ```
- В HTML-шаблонах заменяются инлайн-скрипты: вместо `@click="fetch('/downloads/retry/' + job.id)"` строго `@click="$store.queue.retry(job.id)"`.

---

## 5. Анализ рисков, краевых случаев и защитные меры (Risks & Guardrails)

| Риск | Уровень | Меры предотвращения и защиты |
|---|---|---|
| **Регрессия маршрутов API** | Высокий | Строгая проверка путей в `internal/api/e2e_user_journey_test.go`. URL эндпоинтов не меняются ни на один символ. |
| **Утечки горутин и Data Races** | Высокий | Запуск всех тестов под флагом `go test -race ./...`. Проверка корректного закрытия контекстов воркеров загрузки. |
| **Ошибки в No-Build шаблонах (опечатки)** | Средний | Валидатор `web/template_validator_test.go` проверяет совпадение всех вызовов `@click` с методами сторов и функций. |
| **Нарушение изоляции песочницы** | Критический | Строгий аудит: любая операция с путями обязана начинаться с `PathSandbox.Resolve()`. Прямой вызов `os.*` с пользовательскими путями запрещен. |
| **Блокировка базы SQLite WAL** | Средний | Использование пула с ограничением соединений и `busy_timeout(5000)`. Транзакции оборачиваются в `RunInTx`. |

---

## 6. Матрица верификации (Verification Matrix)

Каждый шаг плана сопровождается набором проверок:

```bash
# 1. Быстрый прогон изолированных юнит-тестов (httputil и testkit)
go test -v -race ./internal/core/httputil/...
go test -v -race ./internal/testkit/...

# 2. Тестирование вертикальных слайсов
go test -v -race ./internal/features/...

# 3. Полный регрессионный прогон всего Go-бэкенда
go test -v -race ./...

# 4. Проверка целостности HTML-шаблонов Alpine.js (статический Go-линтер)
go test -v ./web/...

# 5. Сборка продакшен-бинарника (проверка компилятора и линковщика)
go build -ldflags="-s -w" -o bin/music-manager cmd/server/main.go

# 6. Комплексное тестирование пользовательских сценариев (E2E)
./scripts/test_user_flows.sh
```

---

## 7. Критерии приемки (Definition of Done)

- [x] Пакет `internal/core/httputil` реализован с полным тестовым покрытием.
- [x] Пакет `internal/testkit` предоставляет in-memory окружение и Golden JSON снапшоты.
- [x] Все 6 вертикальных слайсов (`downloader`, `tageditor`, `filemanager`, `audit`, `radio`, `radar`) выделены в `internal/features/` и саморегистрируются в Chi-роутере.
- [x] Монолитный файл `internal/db/queries.go` разгружен: запросы перенесены в соответствующие `repository.go`.
- [x] Создан типизированный API-клиент `web/static/js/api/client.js`, прямые вызовы `fetch()` удалены из шаблонов.
- [x] Статический валидатор `template_validator_test.go` возвращает 0 ошибок.
- [x] `go test -race ./...` отрабатывает с результатом 100% PASS и 0 data races.
- [x] Бинарник `bin/music-manager` собирается и запускается без предупреждений.
- [x] Файл трекинга `PROGRESS.md` актуализирован.
