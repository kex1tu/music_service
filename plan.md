# Архитектурный план: Исправление аудита медиатеки, отображения нешаблонных путей, автозамены жанров и автоопределения total_tracks

**Дата:** 15 сентября 2026  
**Статус:** `PROPOSED` (Ожидает подтверждения)  
**Область:** `app/templates/index.html`, `app/static/js/library_audit.js`, `app/services/library_audit_service.py`, `tools/music-scanner/`  
**Целевые серверы деплоя:** `local dev`, `nl_vpn` (144.31.51.105), `nl_music` (194.76.168.20)  

---

## 1. Цель и границы задачи (Objective & Scope)

### 1.1. Цель
Устранить все выявленные визуальные и функциональные дефекты на странице аудита медиатеки (включая критический баг пустой таблицы в разделе «Некорректные пути» с ложным сообщением «Отлично! Все файлы разложены по стандарту» при 2826 нешаблонных путях, рассинхрон бейджей табов с карточками), внедрить правила автозамены русскоязычных жанров на международные стандарты (включая `Поп\Рок` -> `Pop \Rock\Pop-Rock`, `Альтернативная музыка` -> `Alternative`, `Поп` -> `Pop`, `Танцевальная музыка` -> `Dance Music`), обеспечить автоопределение `total_tracks` через Deezer API при их отсутствии в локальных тегах ID3, гарантировать 100% точность отображения метрик и надежность параллельных запросов SQLite.

### 1.2. Границы задачи (Scope)
- **In-Scope:**
  1. **Устранение бага раздела «Некорректные пути»:**
     - Добавление индикатора загрузки `isLoading` во вкладку нешаблонных путей.
     - Добавление `timeout=30.0` в `aiosqlite.connect()` для исключения ошибок `database is locked`.
     - In-memory fallback в `get_invalid_paths()` при сбое чтения базы.
     - Синхронизация бейджей на кнопках вкладок (`x-text="invalidPaths.length || summary.invalid_paths_count || 0"`).
  2. **Автоопределение `total_tracks` через Deezer API:**
     - Если у альбома `total_tracks == 0` или `NULL`, запрашивать официальный релиз в Deezer API (`/search/album`), считывать `nb_tracks` и обновлять запись альбома в SQLite.
  3. **Автозамена и нормализация русскоязычных жанров:**
     - `Поп\Рок` / `Поп/Рок` / `Поп-Рок` $\rightarrow$ `Pop \Rock\Pop-Rock`
     - `Альтернативная музыка` / `Альтернатива` $\rightarrow$ `Alternative`
     - `Поп` / `Поп-музыка` $\rightarrow$ `Pop`
     - `Танцевальная музыка` / `Танцевальная` $\rightarrow$ `Dance Music`
  4. **Исправление отображения нулей на карточках и вкладках:** устранение бага с пустыми числами для метрик «Не по шаблону» и «Жанры (не-EN)».
  5. **Коррекция алгоритма сверки с каталогом в Go-сканере:** передача реального списка локальных треков в `catalog.FindMissingTracks()`, исключение ложных пометок «Отсутствуют все треки».
  6. **Калибровка классификации «Неполные релизы»:** исключение синглов и цельных релизов из категории неполных (исправление SQL-запросов и логики триггера `is_complete`).
  7. **Унификация терминологии и счетчиков дубликатов:** явное разделение числа групп дубликатов и общего числа лишних копий файлов.
  8. **Синхронизация счетчиков релизов:** выравнивание формулы подсчета альбомов между SQLite-индексом и in-memory fallback.
  9. **Адаптивная верстка дашборда:** адаптация 7 карточек под экраны 1080p/2K без визуальных дыр и пустых ячеек.
  10. **Сборка и развертывание:** кросс-компиляция Go-демона `music-scanner`, прогон тестов Go и Python (554+ тестов), коммит и деплой на оба продакшн-сервера (`nl_vpn`, `nl_music`).
- **Out-of-Scope:**
  - Изменение структуры тегов ID3v2.4 в существующих аудиофайлах без явного запуска автозамены.
  - Внедрение внешних СУБД (сохраняется строгий Zero-Database инвариант: SQLite — локальный кэш-индекс).

---

## 2. Анализ текущего состояния и первопричин дефектов (Current State Analysis)

| # | Выявленный дефект | Затронутые файлы | Первопричина (Root Cause) |
|---|---|---|---|
| **1** | В разделе «Некорректные пути» отображается «Отлично! Все файлы разложены по стандарту» при 2826 нешаблонных путях | [index.html](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/templates/index.html#L1907), [library_audit_service.py](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/services/library_audit_service.py#L521) | 1) `index.html` сразу проверяет `invalidPaths.length === 0` без проверки `isLoading`.<br>2) Параллельный вызов 7 эндпоинтов в `Promise.all` приводил к `sqlite3.OperationalError: database is locked` из-за отсутствия таймаута, и `get_invalid_paths()` возвращал пустой `[]`. |
| **2** | Рассинхрон бейджей табов («Треки без текстов 0», «Неполные релизы 0») с карточками («782», «920») | [index.html](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/templates/index.html#L1445) | Табы привязаны к `missingLyrics.length`, а карточки — к `summary.missing_lyrics_count`. Пока массивы подгружаются или при сетевой задержке бейдж показывал 0. |
| **3** | Пустые цифры на карточках «Не по шаблону» и «Жанры (не-EN)» | [index.html](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/templates/index.html#L1413) | `x-text="invalidPaths.length || summary.invalid_paths_count"`: когда массив пуст (`length === 0`) и поле отсутствует в `summary`, вычисляется `0 || undefined` = `undefined`, отображая пустую строку. |
| **4** | Помечены отсутствующими все треки релиза (например, «Отсутствуют: #1, #2, #3, #4, #5»), когда часть уже есть на диске | [main.go](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/tools/music-scanner/main.go#L187), [matcher.go](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/tools/music-scanner/catalog/matcher.go#L20) | В фоновом цикле `main.go` в функцию `FindMissingTracks(album.ID, nil, remoteAlbum)` передавался `nil` вместо списка локальных треков. |
| **5** | Раздутое количество неполных релизов (151 / 920 альбомов) | [sqlite.go](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/tools/music-scanner/db/sqlite.go#L293), [library_audit_service.py](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/services/library_audit_service.py#L834) | Синглы без тега `track_total` имеют `total_tracks = 0`. В SQL `is_complete` выставлялся в `0`, а выборка `WHERE al.is_complete = 0 OR al.total_tracks = 0` возвращала все синглы как неполные альбомы. |
| **6** | Несоответствие счетчика дубликатов («50 копий» на карточке vs «46» на табе) | [index.html](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/templates/index.html#L1375), [library_audit.js](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/static/js/library_audit.js#L128) | Отсутствие поясняющего контекста: 46 — это количество уникальных групп дубликатов, а 50 — суммарное число удаляемых копий файлов. |
| **7** | Расхождение общего числа релизов (381 в базе vs 256 по папкам) | [library_audit_service.py](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/services/library_audit_service.py#L502) | В SQLite считаются записи в таблице `albums` (группировка `artist_id + album_title`), а во fallback-режиме считались только уникальные папки на диске. |
| **8** | Неровная сетка из 7 карточек (пустая 8-я ячейка при 4 колонках) | [index.html](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/templates/index.html#L1306) | Класс `xl:grid-cols-4 2xl:grid-cols-7` на экранах от 1280px до 1535px создает 2 ряда по 4 ячейки, оставляя последнюю ячейку пустой. |

---

## 3. Архитектурное решение и обоснование (Architecture & Rationale)

### 3.1. Архитектурные решения
1. **Надежность SQLite и защита от блокировок:** Подключение `aiosqlite.connect(str(self._db_path), timeout=30.0)` предотвращает падения при параллельных запросах со стороны FastAPI. При сбое чтения базы задействуется fallback на in-memory анализ `self._cache`.
2. **UI Loading States (Спиннеры вместо ложных сообщений):** Секция некорректных путей проверяет состояние `isLoading`. Пока данные загружаются, отображается аккуратный спиннер «Анализ структуры файлов...», а сообщение «Отлично!» появляется только при подтвержденном нуле нарушений.
3. **Deezer API как авторитетный источник `total_tracks`:** Если в ID3-тегах не прописан `TRACKTOTAL`, обращение к Deezer API по `artist + album` позволяет узнать официальное число треков (`nb_tracks`) и сохранить его в `albums.total_tracks`.
4. **Двухуровневый сопоставитель жанров:** Сначала нормализуется вся строка целиком (чтобы поймать `Поп\Рок`, `Альтернативная музыка`, `Танцевальная музыка`). Если прямого совпадения нет, строка делится по разделителям (`\\`, `/`, `,`, `;`) и маппится поэлементно.
5. **Go SQLite Data Integrity:** Сканер в Go читает существующие треки альбома (`GetAlbumTracks(albumID)`) перед вызовом Deezer API.

---

## 4. Пошаговая декомпозиция реализации (Step-by-Step Implementation)

### Этап 1. FastAPI сервис аудита и SQLite стабильность
- `[MODIFY]` [`app/services/library_audit_service.py`](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/services/library_audit_service.py)
  - Добавить `timeout=30.0` ко всем вызовам `aiosqlite.connect()`.
  - В `get_invalid_paths()` добавить in-memory fallback по `self._cache` при ошибке базы данных.
  - В `match_album_catalog()`: при отсутствии `total_tracks` заполнять `expected_count` из `deezer_album.nb_tracks` и обновлять SQLite.
  - В `get_summary()`: скорректировать SQL-запрос неполных альбомов `WHERE (is_complete = 0 AND total_tracks > 1) OR (SELECT COUNT(*) FROM missing_tracks WHERE album_id = albums.id) > 0`.

### Этап 2. Frontend шаблоны и отображение списков
- `[MODIFY]` [`app/templates/index.html`](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/templates/index.html)
  - Секция «Некорректные пути»: добавить блок `<template x-if="isLoading">` со спиннером.
  - Исправить привязки бейджей на кнопках табов: `x-text="invalidPaths.length || summary.invalid_paths_count || 0"` (и аналогично для других табов).
  - Сетка дашборда: `grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 2xl:grid-cols-7 gap-4`.
- `[MODIFY]` [`app/static/js/library_audit.js`](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/app/static/js/library_audit.js)
  - Обработка ошибок и гарантированное обновление состояния при завершении запросов.

### Этап 3. Go Validator, Scanner & SQLite
- `[MODIFY]` [`tools/music-scanner/validator/genre.go`](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/tools/music-scanner/validator/genre.go)
  - Добавить автозамену жанров (`Поп\Рок` $\rightarrow$ `Pop \Rock\Pop-Rock`, `Альтернативная музыка` $\rightarrow$ `Alternative`, `Поп` $\rightarrow$ `Pop`, `Танцевальная музыка` $\rightarrow$ `Dance Music`).
- `[MODIFY]` [`tools/music-scanner/validator/validator_test.go`](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/tools/music-scanner/validator/validator_test.go)
  - Модульные тесты правил жанров.
- `[MODIFY]` [`tools/music-scanner/db/sqlite.go`](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/tools/music-scanner/db/sqlite.go)
  - Добавить методы `GetAlbumTracks(albumID int64)` и `UpdateAlbumCatalogInfo(albumID int64, totalTracks int, deezerID string, coverURL string)`.
  - Скорректировать расчет `is_complete`.
- `[MODIFY]` [`tools/music-scanner/main.go`](file:///home/kex1tu/%D0%A0%D0%B0%D0%B1%D0%BE%D1%87%D0%B8%D0%B9%20%D1%81%D1%82%D0%BE%D0%BB/music%20download/music-manager/tools/music-scanner/main.go)
  - Передавать `localTracks` в `catalog.FindMissingTracks()` и обновлять `total_tracks` из Deezer.

### Этап 4. Сборка, верификация и деплой
- Сборка Go: `cd tools/music-scanner && go test ./... && go build -o ../../bin/music-scanner main.go`.
- Тесты Python: `./venv/bin/python -m pytest tests/`.
- Линтер: `./venv/bin/ruff check app tests`.
- Деплой на `nl_vpn` (144.31.51.105) и `nl_music` (194.76.168.20), перезапуск сервисов `music-manager` и `music-auditor`.

---

## 5. План тестирования и верификации (Verification Matrix)

1. **Юнит-тесты Go и Python:**
   - Тестирование нормализации жанров (`validator_test.go`).
   - Тестирование `catalog.FindMissingTracks()` с частичным совпадением локальных треков.
   - Тестирование `LibraryAuditService.get_invalid_paths()`, `get_summary()`, `get_incomplete_albums()`.
2. **Ручная проверка веб-интерфейса:**
   - Переход во вкладку «Некорректные пути»: проверка отображения реального списка нешаблонных файлов с причиной и кнопкой «В папку», ложная плашка «Отлично!» не появляется при `invalidPaths.length > 0`.
   - Проверка спиннеров загрузки во всех табах.
   - Проверка синхронности чисел между карточками и кнопками табов.
3. **Проверка работы на серверах (`nl_vpn`, `nl_music`):**
   - Запуск `systemctl status music-manager` и `systemctl status music-auditor`.
   - Проверка ответа `/files/audit/invalid-paths` и `/files/audit/summary` по curl.

---

## 6. Критерии приемки (Definition of Done)

- [ ] Во вкладке «Некорректные пути» корректно рендерится список нарушений, ложная надпись «Отлично!» отсутствует при наличии нешаблонных файлов.
- [ ] При загрузке отображаются анимированные спиннеры во всех секциях.
- [ ] Бейджи на кнопках табов строго соответствуют числам на карточках дашборда.
- [ ] Автоопределение `total_tracks` через Deezer API работает в Go-сканере и бэкенде при отсутствии тега `TRACKTOTAL`.
- [ ] Автозамена жанров (`Поп\Рок` $\rightarrow$ `Pop \Rock\Pop-Rock`, `Альтернативная музыка` $\rightarrow$ `Alternative`, `Поп` $\rightarrow$ `Pop`, `Танцевальная музыка` $\rightarrow$ `Dance Music`) протестирована и активна.
- [ ] 100% тестов `pytest` и `go test` проходят успешно.
- [ ] Обновления развернуты на `nl_vpn` и `nl_music`.
