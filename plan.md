# План: Аудит, актуализация и очистка Markdown-документации (*.md)

**Дата:** 17 сентября 2026  
**Статус:** `DONE` (Все задачи выполнены)  
**Целевые компоненты:** Корневая документация (`/`), временные логи мультиагентных сессий (`.agents/`), архитектурные спецификации.

---

## 1. Цель и границы задачи (Objective & Scope)

### 1.1. Проблема
В репозитории скопилось большое количество устаревших и мусорных файлов формата `*.md`:
1. Файлы `Architecture.md`, `README.md`, `PROJECT.md`, `TEST_INFRA.md` и `ORIGINAL_REQUEST.md` содержат устаревшую информацию о старой Python/FastAPI реализации проекта (с упоминаниями `uvicorn`, `pytest`, `mutagen`, `ruff`, `venv`, `Zero-Database`), тогда как проект полностью переведен на высокопроизводительный **Pure Go (Chi) + SQLite WAL + ID3v2.4 Engine + No-Build Alpine.js**.
2. В файле `AGENTS.md` присутствуют остаточные рудименты Python в примерах правил (`app/main.py`, `asyncio`, `shutil.move`).
3. В каталоге `.agents/` находится более 115 мусорных файлов отчетов, брифингов и диспетчеризации старых под-агентов (44 директории вида `worker_*`, `reviewer_*`, `challenger_*`, `teamwork_preview_*`), а также дубликат `ORIGINAL_REQUEST.md`.
4. Файл `plan.md` содержит выполненный план предыдущего блока задач (Блок 32).

### 1.2. Границы задачи
- **In-Scope:**
  - Полный аудит всех `*.md` файлов в репозитории на соответствие текущему коду на Go.
  - Удаление устаревших и дублирующих файлов в корне (`ORIGINAL_REQUEST.md`, `PROJECT.md`, `TEST_INFRA.md`).
  - Полная очистка мусорных временных каталогов старых под-агентов в `.agents/` (44 папки) и дубликата `.agents/ORIGINAL_REQUEST.md`.
  - Сохранение активных навыков в `.agents/skills/` (7 навыков).
  - Актуализация `README.md` (Go-сборка, запуск, архитектура, API, systemd).
  - Актуализация `Architecture.md` (актуальная Go-архитектура, пакеты, модули, SQLite WAL, No-Build Frontend).
  - Очистка `AGENTS.md` от Python-рудиментов.
  - Сохранение `PROGRESS.md` как актуального источника трекинга задач.
- **Out-of-Scope:**
  - Изменение исходного Go-кода, HTML-шаблонов или JS-скриптов.
  - Изменение навыков в `.agents/skills/`.

---

## 2. Анализ текущего состояния и матрица решений

| Файл / Каталог | Текущее состояние | Решение | Обоснование |
|---|---|---|---|
| [`AGENTS.md`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/AGENTS.md) | Актуален на 95%, но содержит единичные упоминания Python (`app/main.py`, `asyncio`, `shutil.move`) в секциях 5–6 | **[MODIFY]** | Очистить правила от рудиментов Python, синхронизировать с Pure Go стандартами |
| [`README.md`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/README.md) | Полностью устарел: описывает Python 3.11, `pip install`, `uvicorn`, `mutagen`, `Zero-Database` | **[MODIFY]** | Полностью переписать под Go 1.22+, `bin/music-manager`, SQLite WAL, актуальный функционал |
| [`Architecture.md`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/Architecture.md) | Полностью устарел: описывает FastAPI, `app/services/`, Jinja2, mutagen | **[MODIFY]** | Переписать под актуальную архитектуру Go (Chi router, `internal/id3`, `internal/services/`, SQLite WAL) |
| [`PROGRESS.md`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/PROGRESS.md) | Актуален (38 блоков, 100% готовность) | **[KEEP]** | Оставить без изменений как эталонный журнал выполненных работ |
| [`plan.md`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/plan.md) | Устарел: содержит выполненный план Блока 32 | **[MODIFY]** | Актуализировать текущим планом ревизии документации |
| `ORIGINAL_REQUEST.md` (корень) | Устарел: лог ТЗ на Python от 06.09.2026 | **[DELETE]** | Удалить — исторический мусор |
| `PROJECT.md` (корень) | Устарел: майлстоуны Python | **[DELETE]** | Удалить — дублирует устаревшую информацию |
| `TEST_INFRA.md` (корень) | Устарел: запуск pytest и ruff | **[DELETE]** | Удалить — тестовая инфраструктура на Go описана в `README.md` и `AGENTS.md` |
| `.agents/ORIGINAL_REQUEST.md` | Устаревший дубликат | **[DELETE]** | Удалить |
| `.agents/` (44 папки под-агентов) | 115+ файлов логов и брифингов старых сессий | **[DELETE]** | Удалить все 44 мусорные папки |
| `.agents/skills/*/SKILL.md` | 7 активных навыков для Antigravity | **[KEEP]** | Сохранить в полном объеме |

---

## 3. Архитектурное решение и обоснование («Почему именно так»)

### 3.1. Единый источник истины (Single Source of Truth)
- **`README.md`** — главная точка входа для человека/разработчика: описание возможностей, системные требования, сборка бинарника, конфигурация `.env`, запуск и деплой `systemd`.
- **`Architecture.md`** — исчерпывающая техническая спецификация системы на Go: структура пакетов, конвейер загрузки (SoundCloud-First, yt-dlp, ffmpeg), ID3v2.4 Engine (`\x00` multi-values), схема базы данных SQLite WAL, No-Build Frontend.
- **`AGENTS.md`** — правила, границы ответственности и жесткие инварианты для AI-агентов при работе с репозиторием.
- **`PROGRESS.md`** — полная история и матрица статусов реализации компонентов.

### 3.2. Удаление устаревших файлов
- Удаление `ORIGINAL_REQUEST.md`, `PROJECT.md`, `TEST_INFRA.md` и временных папок в `.agents/` исключает галлюцинации LLM и запутывание разработчиков устаревшими инструкциями по несуществующему Python-коду.

---

## 4. Пошаговый план реализации (Step-by-Step Implementation)

### Этап 1: Удаление мусорных и устаревших файлов
1. Удалить `ORIGINAL_REQUEST.md`, `PROJECT.md`, `TEST_INFRA.md` из корня проекта.
2. Удалить `.agents/ORIGINAL_REQUEST.md`.
3. Удалить все 44 временные директории под-агентов в `.agents/` (`auditor_*`, `challenger_*`, `reviewer_*`, `sentinel_*`, `survey_explorer_*`, `teamwork_preview_*`, `worker_*`), оставив только `.agents/skills/`.

### Этап 2: Актуализация `README.md`
1. Обновить заголовок и стек: Pure Go (Chi) + yt-dlp + ffmpeg + ID3v2.4 Engine + SQLite WAL + No-Build Frontend.
2. Описать все современные возможности:
   - SoundCloud-First Matcher & YouTube fallback.
   - Deezer Anti-403 & iTunes Search fallback.
   - Детерминированная персистентная очередь загрузок в SQLite и Retry механизм.
   - Поддержка синглов и каноническая структура `{Album} - {Artist}/{Track}.mp3`.
   - Сохранение обложек `{TrackBaseName}.jpg` и извлечение embedded APIC.
   - Внешние тексты `.lrc`/`.txt` (Zero ID3 Lyrics).
   - Аудит и автоисправление путей, сверка неполных релизов (Backfill).
3. Привести актуальные команды сборки и запуска:
   ```bash
   go build -ldflags="-s -w" -o bin/music-manager cmd/server/main.go
   ./bin/music-manager
   ```
4. Обновить инструкцию по `systemd` и переменным окружения.

### Этап 3: Актуализация `Architecture.md`
1. Описать актуальную компонентную схему на Go (HTTP Layer / Chi Router -> Service Layer -> Storage / Engine Layer).
2. Описать структуру пакетов (`cmd/server`, `internal/api`, `internal/core`, `internal/id3`, `internal/services`, `internal/db`, `web/`, `tools/`).
3. Задокументировать схему БД SQLite (`tracks`, `albums`, `artists`, `track_genres`, `download_jobs`, `audit_issues`, `missing_tracks`).
4. Описать модульную структуру No-Build фронтенда.

### Этап 4: Очистка `AGENTS.md`
1. Заменить остаточные упоминания `app/main.py` на `cmd/server/main.go`.
2. Заменить упоминания `asyncio` и `shutil` на Go-эквиваленты (`goroutines`, `os.Rename`, `os.RemoveAll`, `exec.CommandContext`).

---

## 5. Анализ рисков и меры защиты (Risks & Guardrails)

| Риск | Вероятность | Влияние | Стратегия митигации |
|---|---|---|---|
| Случайное удаление рабочих навыков `.agents/skills/` | Низкая | Высокое | Использовать точечные команды удаления только для папок под-агентов, не затрагивая `.agents/skills/` |
| Потеря важных архитектурных деталей при обновлении `Architecture.md` | Средняя | Среднее | Тщательно перенести все актуальные алгоритмы (ID3v2.4 UTF-8, SoundCloud-First, PathSandbox, Backfill) в новый документ |
| Нарушение инвариантов проекта | Низкая | Критическое | Все документы строго подтверждают инварианты из `AGENTS.md` (No Docker, Zero-Node.js, Pure Go, PathSandbox) |

---

## 6. План тестирования и верификации

1. Проверить дерево файлов: в корне должны остаться только `README.md`, `Architecture.md`, `AGENTS.md`, `PROGRESS.md`, `plan.md`.
2. Проверить каталог `.agents/`: в нем должен остаться только подкаталог `skills/` с 7 навыками.
3. Проверить, что все ссылки в документах кликабельны и ведут на существующие файлы.
4. Убедиться, что сборка проекта `go build -ldflags="-s -w" -o bin/music-manager cmd/server/main.go` и тесты `go test -race ./...` проходят успешно.

---

## 7. Критерии приемки (Definition of Done)

- [x] В корне репозитория удалены устаревшие файлы `ORIGINAL_REQUEST.md`, `PROJECT.md`, `TEST_INFRA.md`.
- [x] В `.agents/` удалены 44 мусорные папки под-агентов и `.agents/ORIGINAL_REQUEST.md`, сохранены все 7 навыков в `.agents/skills/`.
- [x] `README.md` на 100% отражает актуальный стек на Go, команды сборки, настройки `.env` и возможности системы.
- [x] `Architecture.md` на 100% отражает актуальную архитектуру Go-сервиса и SQLite WAL.
- [x] `AGENTS.md` очищен от рудиментов Python.
- [x] `PROGRESS.md` сохранен в актуальном состоянии.
- [x] Все тесты `go test -race ./...` — зеленые, бинарник успешно компилируется.
