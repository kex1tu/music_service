# Детекция цензурированных треков в медиатеке

Архитектурный план добавления автоматического обнаружения уже загруженных цензурированных версий треков в систему аудита тегов (`audit_tags`) с возможностью их удаления и последующей перезагрузки.

---

## 1. Цель и границы задачи (Objective & Scope)

### 1.1. Проблема
Проект уже имеет мощную защиту от скачивания цензурированных версий при загрузке (через штрафы за `badKeywords` в `resolver/score.go` и SoundCloud-First резолвер в `uncensored_matcher.go`), но не умеет обнаруживать цензурированные треки, которые уже загружены в медиатеку. Пользователь не знает, какие треки в его библиотеке являются цензурированными версиями ("Clean", "Radio Edit", "Censored").

### 1.2. Решение
Добавить детекцию цензурированных треков в существующую систему аудита тегов (`audit_tags`):
- **Бэкенд:** новый тип дефекта `censored_version` в анализаторе тегов [`tag_audit_analyzer.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/tag_audit_analyzer.go) на базе локального regex `badRegexAudit`.
- **Фронтенд:** отображение бейджа "Цензурированная версия" в таблице аудита [`audit_tags.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/audit_tags.html) и кнопка фильтрации "Цензурированные".
- **UI:** кнопка «Удалить» для удаления цензурированного файла с подтверждением, вызовом `DELETE /files` и обновлением интерфейса.
- **Поддержка контрактов API:** поддержка парсинга пути как из query-параметра `?path=...`, так и из тела JSON-запроса `{"path": "..."}` в [`file_handlers.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/file_handlers.go).
- **Документация:** секция 4c конфигурации резолвера нецензурированных версий в [`.env.example`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/.env.example).

### 1.3. Out-of-Scope
- Автоматическая перезагрузка трека (требует отдельной фоновой интеграции с очередью).
- Изменение существующей логики скачивания (она уже работает корректно).
- Модификация схемы базы данных SQLite (дефекты тегов определяются динамически в рантайме аудита).

---

## 2. Анализ текущего состояния (Current State Analysis)

| Компонент / Файл | Текущее состояние | Выявленный недостаток | Требуемое изменение |
|---|---|---|---|
| [`internal/api/handlers/tag_audit_analyzer.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/tag_audit_analyzer.go) | Анализ 8 типов дефектов (клипы, фиты, каналы, номер в названии, дубли артиста, год, альбом, `\\`) | Цензурированные треки не помечались и не попадали в аудит | Внедрен `badRegexAudit`, дефект `censored_version` и серьезность `review_needed` |
| [`web/templates/components/audit_tags.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/audit_tags.html) | Кнопки фильтрации дефектов, бейджи и действия «Исправить», «Сверка» | Отсутствовал фильтр цензуры и действие удаления цензурированного трека | Добавлены фильтр «Цензурированные», бейдж `bg-red-600/10 text-red-400` и кнопка «Удалить» |
| [`web/static/js/audit/tag_audit.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/audit/tag_audit.js) | Методы `fixTagIssue`, `fixSafeTagIssues`, `fixSelectedTagIssues` | Нет метода удаления трека прямо из таблицы аудита | Добавлен `deleteCensoredTrack(item)` с диалогом подтверждения и вызовом `DELETE /files` |
| [`internal/api/handlers/file_handlers.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/file_handlers.go) | `DeleteFile` ожидал путь строго в `r.URL.Query().Get("path")` | Запросы с телом `{ "path": "..." }` приводили к 400 Bad Request | Добавлен парсинг JSON-тела с сохранением обратной совместимости |

---

## 3. Архитектурное решение и обоснование (Architecture & Rationale)

### 3.1. Метод детекции цензуры (Локальный Regex)
Используется локальный регулярный выразитель `badRegexAudit` в [`tag_audit_analyzer.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/tag_audit_analyzer.go):
```go
badRegexAudit = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(clean|radio\s+edit|censored|censored version|radio version|tv edit|с\s+цензурой|цензурная)(?:$|[^\p{L}\p{N}])`)
```
- **Почему именно так:**
  - **Словные границы (`[^\p{L}\p{N}]`):** предотвращают ложные срабатывания на нормальные слова вроде "cleaner", "cleaning" и т.п.
  - **Без внешних HTTP-запросов:** проверка выполняется мгновенно в оперативной памяти во время сканирования и пагинации SQLite.
  - **Специфика аудита:** в аудите медиатеки отсутствует внешний эталон, поэтому Anti-Collision проверка сравнивает слова внутри названия трека. Имена артистов вроде "Clean Bandit" при нормальном названии ("Rather Be") не триггерят дефект, так как проверяется именно название трека (`title`).

### 3.2. Классификация серьезности (Severity: `review_needed`)
- Автоисправление тегов для цензурированной версии невозможно, поскольку аудиодорожка физически содержит запиканные/вырезанные фрагменты.
- Поэтому дефект классифицируется как `review_needed` (amber бейдж «Требует внимания»), требующий ручного решения пользователя.

### 3.3. Безопасность и каскадная очистка при удалении
- Запрос на удаление передается в `DELETE /files`, который верифицирует путь через `PathSandbox.Resolve()`.
- При удалении `.mp3` файла автоматически удаляются сопутствующие `.lrc`, `.txt`, поквартирные `.jpg` и каскадно обновляются таблицы `tracks`, `track_genres`, `audit_issues`, `albums` и `artists` в SQLite WAL.

---

## 4. Пошаговая декомпозиция реализации (Step-by-Step Implementation)

### Этап 1: Бэкенд — детекция цензуры в анализаторе тегов
**Файл:** [`internal/api/handlers/tag_audit_analyzer.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/tag_audit_analyzer.go) `[MODIFY]`
- Объявлен `badRegexAudit`.
- Добавлена проверка шага 9:
  ```go
  badMatches := badRegexAudit.FindAllStringSubmatchIndex(title, -1)
  if len(badMatches) > 0 {
      issues = append(issues, "censored_version")
  }
  ```
- В классификацию серьезности добавлен `case "censored_version": hasReviewNeeded = true`.

### Этап 2: Бэкенд — поддержка JSON-тела в `DeleteFile`
**Файл:** [`internal/api/handlers/file_handlers.go`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/internal/api/handlers/file_handlers.go) `[MODIFY]`
- В метод `DeleteFile` добавлен парсинг JSON-структуры `{ "path": "..." }` при отсутствии query-параметра.

### Этап 3: Фронтенд — визуализация и фильтрация
**Файл:** [`web/templates/components/audit_tags.html`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/templates/components/audit_tags.html) `[MODIFY]`
- Кнопка фильтра `filterTagIssues('censored_version')`.
- Бейдж с текстом «Цензурированная версия» и стилями `bg-red-600/10 text-red-400 border border-red-600/20`.
- Кнопка «Удалить» в колонке действий с директивой `<template x-if="item.issues.includes('censored_version')">`.

### Этап 4: JavaScript — удаление трека
**Файл:** [`web/static/js/audit/tag_audit.js`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/web/static/js/audit/tag_audit.js) `[MODIFY]`
- Метод `deleteCensoredTrack(item)` с подтверждением `confirm()`, отправкой запроса `DELETE /files`, тостом уведомления и оптимистичным удалением из `this.tagIssues`.

### Этап 5: Документация и конфигурация
**Файл:** [`.env.example`](file:///home/kex1tu/Рабочий%20стол/music%20download/music-manager/.env.example) `[MODIFY]`
- Актуализирована секция 4c параметров резолвера нецензурированных версий.

---

## 5. Анализ рисков и краевых случаев (Risks, Edge Cases & Guardrails)

1. **Ложные срабатывания (False Positives):**
   - *Риск:* Название трека содержит подстроку "clean" в составе другого слова (например, "Cleaner").
   - *Митигация:* Использование словных границ `(?:^|[^\p{L}\p{N}])` в регулярном выражении.
2. **Артисты со словом Clean (кейс Clean Bandit):**
   - *Риск:* Треки группы Clean Bandit без маркеров цензуры могут быть ошибочно помечены.
   - *Митигация:* Регулярное выражение применяется исключительно к строке названия трека (`title`), а не к исполнителю (`artist`).
3. **Удаление файла с диска:**
   - *Риск:* Случайный клик пользователя приведет к немедленной потере файла.
   - *Митигация:* Нативный диалог `confirm(...)` с предупреждением и указанием, как восстановить трек через Downloader.

---

## 6. План тестирования и верификации (Verification Matrix)

| Тест / Сценарий | Уровень | Проверяемый аспект | Результат |
|---|---|---|---|
| `TestAnalyzeTrackIssues_CensoredDetection` | Unit (Go) | Проверка распознавания Clean, Radio Edit, Censored, маркеров на кириллице и кейса Clean Bandit | PASS (10/10 сценариев) |
| `TestFileHandler_DeleteFile_JSONBody` | Integration (Go HTTP) | Удаление файла через `DELETE /files` с телом JSON, проверка удаления MP3/LRC и очистки записей SQLite | PASS |
| `go test -race ./...` | Concurrency | Проверка отсутствия состояний гонки во всех пакетах репозитория | PASS (0 data races) |
| `go build` | Build | Компиляция единого статического бинарника `bin/music-manager` | PASS |

---

## 7. Критерии приемки (Definition of Done)

- [x] Цензурированные треки автоматически детектируются в аудите тегов.
- [x] Бейдж "Цензурированная версия" отображается в таблице аудита (красный цвет).
- [x] Фильтр "Цензурированные" работает корректно.
- [x] Серьезность дефекта = "Требует внимания" (`review_needed` / amber).
- [x] Кнопка "Удалить" видна только для цензурированных треков.
- [x] Удаление файла работает корректно через существующий `DELETE /files` endpoint (поддерживает и query, и JSON body).
- [x] Unit-тесты проходят (`go test -v ./internal/api/handlers -run TestAnalyzeTrackIssues_CensoredDetection`).
- [x] Тесты удаления проходят (`go test -v ./internal/api/handlers -run TestFileHandler_DeleteFile_JSONBody`).
- [x] Все тесты с детектором гонок проходят (`go test -race ./...`).
- [x] Бинарник собирается без ошибок (`go build -ldflags="-s -w" -o bin/music-manager cmd/server/main.go`).
- [x] `.env.example` обновлен с параметрами резолвера.
