package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"music-manager/internal/db"
	"music-manager/internal/id3"
	"music-manager/internal/services/downloader"
)

// TestUserJourney_RadarDeduplicationAndDownload:
// Пользовательский сценарий Радара:
// 1. Отслеживаются 2 артиста, выпустившие совместный сингл.
// 2. В ленте новинок (/api/radar/feed) карточка релиза отображается СТРОГО 1 РАЗ (дедупликация подтверждена).
// 3. Пользователь нажимает "Скачать" -> статус переходит в 'downloaded', релиз мгновенно ИСЧЕЗАЕТ из ленты новинок.
// 4. Пользователь нажимает "Скрыть" на другом релизе -> статус переходит в 'ignored', релиз ИСЧЕЗАЕТ из ленты.
func TestUserJourney_RadarDeduplicationAndDownload(t *testing.T) {
	it := setupIntegrationServer(t)
	cookie := loginForCookie(t, it.server.URL)
	client := it.server.Client()

	ctx := context.Background()

	// 1. Пользователь добавляет отслеживаемых артистов
	_ = it.database.UpsertTrackedArtist(ctx, "Big Baby Tape")
	_ = it.database.UpsertTrackedArtist(ctx, "Aarne")

	// 2. Радар находит совместный релиз обоих артистов
	jointRelease := &db.RadarReleaseEntity{
		DeezerID:    "999001",
		Title:       "Hoodak MP3",
		CleanTitle:  "hoodak mp3",
		ArtistName:  "Big Baby Tape, Aarne",
		ReleaseDate: "2026-09-01",
		ReleaseType: "single",
		CoverURL:    "http://example.com/cover.jpg",
		Status:      "new",
		IsRadar:     true,
		SourceURL:   "https://deezer.com/album/999001",
	}
	if err := it.database.UpsertRadarRelease(ctx, jointRelease); err != nil {
		t.Fatalf("failed to insert joint release: %v", err)
	}

	// 3. Пользователь открывает ленту новинок
	feedReq, _ := http.NewRequest("GET", it.server.URL+"/api/radar/feed?limit=50&offset=0", nil)
	feedReq.AddCookie(cookie)
	resp, err := client.Do(feedReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected feed status 200, got %d", resp.StatusCode)
	}

	var feedData struct {
		Items []db.RadarReleaseEntity `json:"items"`
		Total int                     `json:"total"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&feedData); err != nil {
		t.Fatalf("failed to decode feed: %v", err)
	}

	// КРИТИЧЕСКАЯ ПРОВЕРКА: Релиз должен быть ровно ОДИН, без дублирования карточек
	count := 0
	var foundRelease *db.RadarReleaseEntity
	for i := range feedData.Items {
		rel := &feedData.Items[i]
		if rel.DeezerID == "999001" {
			count++
			foundRelease = rel
		}
	}
	if count != 1 {
		t.Fatalf("ошибка дедупликации: ожидалась ровно 1 карточка совместного релиза, обнаружено %d", count)
	}
	if foundRelease.ArtistName != "Big Baby Tape, Aarne" {
		t.Errorf("ожидалось отображение обоих соавторов 'Big Baby Tape, Aarne', получено %q", foundRelease.ArtistName)
	}

	// 4. Пользователь нажимает "Скачать" на карточке релиза: клиент устанавливает статус 'downloaded'
	statusPayload, _ := json.Marshal(map[string]string{"status": "downloaded"})
	downloadReq, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/radar/releases/%d/status", it.server.URL, foundRelease.ID), bytes.NewReader(statusPayload))
	downloadReq.Header.Set("Content-Type", "application/json")
	downloadReq.AddCookie(cookie)
	dResp, err := client.Do(downloadReq)
	if err != nil {
		t.Fatal(err)
	}
	defer dResp.Body.Close()

	if dResp.StatusCode != http.StatusOK {
		t.Fatalf("expected download status 200, got %d", dResp.StatusCode)
	}

	// 5. Проверяем, что после скачивания релиз ИСЧЕЗ из ленты новинок (инбокс-логика)
	feedReq2, _ := http.NewRequest("GET", it.server.URL+"/api/radar/feed?limit=50&offset=0", nil)
	feedReq2.AddCookie(cookie)
	resp2, err := client.Do(feedReq2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()

	var feedData2 struct {
		Items []db.RadarReleaseEntity `json:"items"`
		Total int                     `json:"total"`
	}
	_ = json.NewDecoder(resp2.Body).Decode(&feedData2)

	for _, rel := range feedData2.Items {
		if rel.DeezerID == "999001" {
			t.Fatalf("релиз %q остался в ленте новинок после скачивания! Он должен быть скрыт (is_radar = 0)", rel.Title)
		}
	}

	// 6. Пользователь нажимает "Скрыть" на другом релизе
	otherRelease := &db.RadarReleaseEntity{
		DeezerID:    "999002",
		Title:       "Unwanted Single",
		CleanTitle:  "unwanted single",
		ArtistName:  "Some Artist",
		ReleaseType: "single",
		Status:      "new",
		IsRadar:     true,
		SourceURL:   "https://deezer.com/album/999002",
	}
	_ = it.database.UpsertRadarRelease(ctx, otherRelease)

	// Получаем реальный ID
	feedReq3, _ := http.NewRequest("GET", it.server.URL+"/api/radar/feed", nil)
	feedReq3.AddCookie(cookie)
	resp3, _ := client.Do(feedReq3)
	var feedData3 struct {
		Items []db.RadarReleaseEntity `json:"items"`
	}
	_ = json.NewDecoder(resp3.Body).Decode(&feedData3)
	resp3.Body.Close()

	var toIgnoreID int64
	for _, r := range feedData3.Items {
		if r.DeezerID == "999002" {
			toIgnoreID = r.ID
			break
		}
	}
	if toIgnoreID == 0 {
		t.Fatalf("не найден релиз 999002 для проверки скрытия")
	}

	// Отправляем скрытие
	ignorePayload, _ := json.Marshal(map[string]string{"status": "ignored"})
	ignoreReq, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/radar/releases/%d/status", it.server.URL, toIgnoreID), bytes.NewReader(ignorePayload))
	ignoreReq.Header.Set("Content-Type", "application/json")
	ignoreReq.AddCookie(cookie)
	ignResp, err := client.Do(ignoreReq)
	if err != nil {
		t.Fatal(err)
	}
	defer ignResp.Body.Close()
	if ignResp.StatusCode != http.StatusOK {
		t.Fatalf("expected ignore status 200, got %d", ignResp.StatusCode)
	}

	// Проверяем статус в БД: статус ignored, is_radar = false
	updatedRel, err := it.database.GetRadarReleaseByID(ctx, toIgnoreID)
	if err != nil {
		t.Fatal(err)
	}
	if updatedRel.Status != "ignored" || updatedRel.IsRadar {
		t.Errorf("expected status 'ignored' and is_radar false, got status=%q, isRadar=%v", updatedRel.Status, updatedRel.IsRadar)
	}
}

// TestUserJourney_PuddletagDownloaderModeAcceptance:
// Пользовательский сценарий Puddletag в режиме загрузчика:
// 1. Загрузка завершена в tmp_downloads/{job_id} в статусе READY.
// 2. Пользователь открывает редактор тегов и, НЕ меняя поля вручную (dirtyIndices пуст),
//    нажимает "Принять в медиатеку".
// 3. Сервер успешно переносит ВСЕ файлы релиза в целевую папку MUSIC_DIR/{destination_dir}/{Track}.mp3.
// 4. Временная директория tmp_downloads/{job_id} очищается, задача удаляется из очереди.
func TestUserJourney_PuddletagDownloaderModeAcceptance(t *testing.T) {
	it := setupIntegrationServer(t)
	cookie := loginForCookie(t, it.server.URL)
	client := it.server.Client()

	jobID := "job_user_puddletag_123"
	jobTmpDir := filepath.Join(it.tmpSb.Root(), jobID)
	_ = os.MkdirAll(jobTmpDir, 0755)

	// Создаем тестовый MP3 файл в папке загрузки
	mp3File := filepath.Join(jobTmpDir, "01. Ready Track.mp3")
	_ = os.WriteFile(mp3File, []byte("audio-payload-for-puddletag-downloader-acceptance"), 0644)
	_ = id3.WriteTags(mp3File, id3.TrackTags{
		Title:       "Ready Track",
		Artist:      "Electronic Masters",
		Album:       "Synthetics",
		AlbumArtist: "Electronic Masters",
		TrackNumber: 1,
		TrackTotal:  1,
		Year:        2026,
		Genre:       "Synthwave",
	}, nil)

	// Имитируем задачу в очереди загрузок
	artistName := "Electronic Masters"
	albumTitle := "Synthetics"
	it.downloader.Jobs().Add(&downloader.DownloadJob{
		ID:         jobID,
		Status:     downloader.StatusReady,
		ArtistName: &artistName,
		AlbumTitle: &albumTitle,
	})

	// Пользователь нажимает "Принять в медиатеку" в Puddletag (без dirty-изменений)
	trackRelPath := filepath.Join(jobID, "01. Ready Track.mp3")
	batchPayload := map[string]interface{}{
		"album": map[string]interface{}{
			"album_artist": "Electronic Masters",
			"album":        "Synthetics",
			"year":         2026,
			"genre":        "Synthwave",
		},
		"tracks": []map[string]interface{}{
			{
				"path":         trackRelPath,
				"title":        "Ready Track",
				"artist":       "Electronic Masters",
				"album":        "Synthetics",
				"album_artist": "Electronic Masters",
				"track_number": 1,
				"track_total":  1,
				"year":         2026,
				"genre":        "Synthwave",
			},
		},
		"source_dir": jobID,
		"reorganize": true, // Флаг переноса в библиотеку
	}
	body, _ := json.Marshal(batchPayload)

	req, _ := http.NewRequest("POST", it.server.URL+"/tags/batch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for batch tag save, got %d", resp.StatusCode)
	}

	var batchResp struct {
		Success        bool            `json:"success"`
		DestinationDir string          `json:"destination_dir"`
		Tracks         []id3.TrackTags `json:"tracks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&batchResp); err != nil {
		t.Fatalf("failed to decode batch response: %v", err)
	}
	if !batchResp.Success {
		t.Fatalf("expected batch save success to be true")
	}

	// ПРОВЕРКА 1: Файл перемещен в MUSIC_DIR по каноническому пути
	if len(batchResp.Tracks) == 0 {
		t.Fatalf("batch save returned 0 updated tracks")
	}
	destTrackFull, err := it.musicSb.Resolve(batchResp.Tracks[0].Path, false)
	if err != nil {
		t.Fatalf("failed to resolve track path in music sandbox: %v", err)
	}
	if _, err := os.Stat(destTrackFull); os.IsNotExist(err) {
		t.Fatalf("трек не найден в медиатеке по ожидаемому пути: %s (destination_dir: %s)", destTrackFull, batchResp.DestinationDir)
	}

	// ПРОВЕРКА 2: Исходный файл в tmp_downloads удален (не дублируется на диске)
	if _, err := os.Stat(mp3File); !os.IsNotExist(err) {
		t.Errorf("исходный файл в tmp_downloads не был перемещен и остался на диске!")
	}

	// Пользователь удаляет задачу из очереди
	delReq, _ := http.NewRequest("DELETE", it.server.URL+"/downloads/"+jobID, nil)
	delReq.AddCookie(cookie)
	delResp, err := client.Do(delReq)
	if err != nil {
		t.Fatal(err)
	}
	defer delResp.Body.Close()

	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for job delete, got %d", delResp.StatusCode)
	}

	// Проверяем, что запрос на получение задачи возвращает 404 (задача удалена из очереди)
	getReq, _ := http.NewRequest("GET", it.server.URL+"/downloads/"+jobID, nil)
	getReq.AddCookie(cookie)
	getResp, err := client.Do(getReq)
	if err != nil {
		t.Fatal(err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusNotFound {
		t.Errorf("ожидался статус 404 для удаленной задачи, получено %d", getResp.StatusCode)
	}
}

// TestUserJourney_DirectOneClickAccept:
// Пользовательский сценарий прямого принятия (1-Click Accept) из очереди загрузок:
// Без входа в редактор тегов, прямо по зеленой кнопке в карточке загрузки.
func TestUserJourney_DirectOneClickAccept(t *testing.T) {
	it := setupIntegrationServer(t)
	cookie := loginForCookie(t, it.server.URL)
	client := it.server.Client()

	jobID := "job_1click_accept_456"
	jobTmpDir := filepath.Join(it.tmpSb.Root(), jobID)
	_ = os.MkdirAll(jobTmpDir, 0755)

	mp3File := filepath.Join(jobTmpDir, "Track_01.mp3")
	_ = os.WriteFile(mp3File, []byte("audio-payload-for-direct-accept"), 0644)
	_ = id3.WriteTags(mp3File, id3.TrackTags{
		Title:       "Speed Light",
		Artist:      "Retro Wave",
		Album:       "Highway 80s",
		AlbumArtist: "Retro Wave",
		TrackNumber: 1,
		Year:        2024,
	}, nil)

	artistName := "Retro Wave"
	albumTitle := "Highway 80s"
	it.downloader.Jobs().Add(&downloader.DownloadJob{
		ID:         jobID,
		Status:     downloader.StatusReady,
		ArtistName: &artistName,
		AlbumTitle: &albumTitle,
	})

	// 1-Click Accept отправляет POST /tags/batch с reorganize: true
	trackRelPath := filepath.Join(jobID, "Track_01.mp3")
	batchPayload := map[string]interface{}{
		"album": map[string]interface{}{
			"album_artist": "Retro Wave",
			"album":        "Highway 80s",
			"year":         2024,
		},
		"tracks": []map[string]interface{}{
			{
				"path":         trackRelPath,
				"title":        "Speed Light",
				"artist":       "Retro Wave",
				"album":        "Highway 80s",
				"album_artist": "Retro Wave",
				"track_number": 1,
				"year":         2024,
			},
		},
		"source_dir": jobID,
		"reorganize": true,
	}
	body, _ := json.Marshal(batchPayload)

	req, _ := http.NewRequest("POST", it.server.URL+"/tags/batch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 1-click accept status 200, got %d", resp.StatusCode)
	}

	var batchResp struct {
		Success        bool            `json:"success"`
		DestinationDir string          `json:"destination_dir"`
		Tracks         []id3.TrackTags `json:"tracks"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&batchResp)

	if len(batchResp.Tracks) == 0 {
		t.Fatalf("1-click accept вернул 0 треков")
	}

	// Проверяем, что файл переехал в медиатеку по возвращенному каноническому пути
	destTrackFull, err := it.musicSb.Resolve(batchResp.Tracks[0].Path, false)
	if err != nil {
		t.Fatalf("failed to resolve track path in music sandbox: %v", err)
	}
	if _, err := os.Stat(destTrackFull); os.IsNotExist(err) {
		t.Fatalf("1-click accept не перенес файл в медиатеку: %s (destination_dir: %s)", destTrackFull, batchResp.DestinationDir)
	}
}

// TestUserJourney_CyrillicMultiArtistReconciliation:
// Пользовательский сценарий сверки кириллических совместных релизов:
// 1. В медиатеке пользователя уже лежит кириллический релиз "Metox & Паша Техник/Кипятильник/01. Трек.mp3".
// 2. Сканер медиатеки индексирует его в SQLite.
// 3. Радар запускает сверку ReconcileAllRadarReleases.
// 4. Релиз безошибочно распознается как in_library и НЕ отображается в ленте как ложная новинка.
func TestUserJourney_CyrillicMultiArtistReconciliation(t *testing.T) {
	it := setupIntegrationServer(t)
	cookie := loginForCookie(t, it.server.URL)
	client := it.server.Client()
	ctx := context.Background()

	// 1. Раскладываем альбом в медиатеке
	albumDir := filepath.Join(it.musicSb.Root(), "Metox & Паша Техник", "Кипятильник")
	_ = os.MkdirAll(albumDir, 0755)

	trackPath := filepath.Join(albumDir, "01. Трек.mp3")
	_ = os.WriteFile(trackPath, []byte("fake-audio-cyrillic-payload"), 0644)
	_ = id3.WriteTags(trackPath, id3.TrackTags{
		Title:       "Трек 1",
		Artist:      "Metox & Паша Техник",
		Album:       "Кипятильник",
		AlbumArtist: "Metox & Паша Техник",
		TrackNumber: 1,
		Year:        2024,
	}, nil)

	// 2. Сканируем медиатеку
	if _, err := it.scanner.ScanAll(ctx); err != nil {
		t.Fatalf("сканирование медиатеки не удалось: %v", err)
	}

	// 3. Создаем запись в радар-релизах со статусом new
	release := &db.RadarReleaseEntity{
		DeezerID:    "cyrillic_rel_777",
		Title:       "Кипятильник",
		CleanTitle:  "кипятильник",
		ArtistName:  "Metox, Паша Техник",
		ReleaseType: "album",
		Status:      "new",
		IsRadar:     true,
		SourceURL:   "https://deezer.com/album/cyrillic_777",
	}
	if err := it.database.UpsertRadarRelease(ctx, release); err != nil {
		t.Fatalf("failed to insert radar release: %v", err)
	}

	// 4. Запускаем пакетную реконсиляцию
	reconciled, err := it.database.ReconcileAllRadarReleases(ctx)
	if err != nil {
		t.Fatalf("ReconcileAllRadarReleases failed: %v", err)
	}
	t.Logf("Reconciled count: %d", reconciled)

	// 5. Запрашиваем ленту Радара через HTTP API
	req, _ := http.NewRequest("GET", it.server.URL+"/api/radar/feed", nil)
	req.AddCookie(cookie)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var feed struct {
		Items []db.RadarReleaseEntity `json:"items"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&feed)

	for _, rel := range feed.Items {
		if rel.DeezerID == "cyrillic_rel_777" {
			t.Fatalf("кириллический релиз %q ошибочно появился в ленте новинок, хотя он уже есть на диске в медиатеке!", rel.Title)
		}
	}
}

// TestUserJourney_FileManagerRootAndSubfoldersBrowsing:
// Пользовательский сценарий открытия раздела "Файлы":
// 1. Пользователь кликает раздел "Файлы", фронтенд отправляет GET /files?path=&recursive=false.
// 2. Сервер НЕ возвращает ошибку 500/400 "invalid file path".
// 3. Возвращается DirectoryListingResponse с хлебными крошками "Корень", списком подпапок (type: "directory") и треков.
// 4. Пользователь переходит в подпапку и включает рекурсивный режим.
func TestUserJourney_FileManagerRootAndSubfoldersBrowsing(t *testing.T) {
	it := setupIntegrationServer(t)
	cookie := loginForCookie(t, it.server.URL)
	client := it.server.Client()
	ctx := context.Background()

	// 1. Создаем структуру альбома в медиатеке
	albumDir := filepath.Join(it.musicSb.Root(), "Daft Punk - Discovery")
	_ = os.MkdirAll(albumDir, 0755)
	trackPath := filepath.Join(albumDir, "01. One More Time.mp3")
	_ = os.WriteFile(trackPath, []byte("fake-audio-payload-daft-punk"), 0644)
	_ = id3.WriteTags(trackPath, id3.TrackTags{
		Title:       "One More Time",
		Artist:      "Daft Punk",
		Album:       "Discovery",
		AlbumArtist: "Daft Punk",
		TrackNumber: 1,
		TrackTotal:  14,
		Year:        2001,
		Genre:       "House",
	}, nil)

	// Индексируем в базу данных
	_, _ = it.scanner.ScanAll(ctx)

	// 2. Открытие корня (как делает PuddletagWorkstation: /files?path=&recursive=false&_t=...)
	reqRoot, _ := http.NewRequest("GET", it.server.URL+"/files?path=&recursive=false", nil)
	reqRoot.AddCookie(cookie)
	respRoot, err := client.Do(reqRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer respRoot.Body.Close()

	if respRoot.StatusCode != http.StatusOK {
		t.Fatalf("ошибка открытия раздела 'Файлы' (корень): ожидался статус 200 OK, получен %d", respRoot.StatusCode)
	}

	var rootListing struct {
		CurrentPath string `json:"current_path"`
		Breadcrumbs []struct {
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"breadcrumbs"`
		Items []struct {
			Name       string `json:"name"`
			Path       string `json:"path"`
			Type       string `json:"type"`
			TrackCount *int   `json:"track_count"`
		} `json:"items"`
		TotalDirectories int `json:"total_directories"`
		TotalFiles       int `json:"total_files"`
	}

	if err := json.NewDecoder(respRoot.Body).Decode(&rootListing); err != nil {
		t.Fatalf("не удалось распарсить ответ корня медиатеки: %v", err)
	}

	if len(rootListing.Breadcrumbs) == 0 || rootListing.Breadcrumbs[0].Name != "Корень" {
		t.Errorf("хлебные крошки должны начинаться с 'Корень', получено: %+v", rootListing.Breadcrumbs)
	}

	foundAlbumFolder := false
	for _, item := range rootListing.Items {
		if item.Name == "Daft Punk - Discovery" && item.Type == "directory" {
			foundAlbumFolder = true
			if item.TrackCount == nil || *item.TrackCount != 1 {
				t.Errorf("ожидался track_count == 1 для каталога альбома, получено: %v", item.TrackCount)
			}
		}
	}
	if !foundAlbumFolder {
		t.Errorf("каталог 'Daft Punk - Discovery' не найден в выдаче корня файлов: %+v", rootListing.Items)
	}

	// 3. Переход в подпапку: GET /files?path=Daft Punk - Discovery&recursive=false
	reqSub, _ := http.NewRequest("GET", it.server.URL+"/files?path=Daft%20Punk%20-%20Discovery&recursive=false", nil)
	reqSub.AddCookie(cookie)
	respSub, err := client.Do(reqSub)
	if err != nil {
		t.Fatal(err)
	}
	defer respSub.Body.Close()

	if respSub.StatusCode != http.StatusOK {
		t.Fatalf("ошибка открытия подпапки: ожидался статус 200 OK, получен %d", respSub.StatusCode)
	}

	var subListing struct {
		CurrentPath string `json:"current_path"`
		Items       []struct {
			Name     string `json:"name"`
			Path     string `json:"path"`
			Type     string `json:"type"`
			Title    string `json:"title"`
			Artist   string `json:"artist"`
			Album    string `json:"album"`
			HasCover bool   `json:"has_cover"`
		} `json:"items"`
	}
	if err := json.NewDecoder(respSub.Body).Decode(&subListing); err != nil {
		t.Fatalf("не удалось распарсить ответ подпапки: %v", err)
	}

	if len(subListing.Items) != 1 || subListing.Items[0].Type != "file" || subListing.Items[0].Title != "One More Time" {
		t.Errorf("некорректные элементы в подпапке альбома: %+v", subListing.Items)
	}

	// 4. Рекурсивный режим всей медиатеки: GET /files?recursive=true
	reqRec, _ := http.NewRequest("GET", it.server.URL+"/files?recursive=true", nil)
	reqRec.AddCookie(cookie)
	respRec, err := client.Do(reqRec)
	if err != nil {
		t.Fatal(err)
	}
	defer respRec.Body.Close()

	if respRec.StatusCode != http.StatusOK {
		t.Fatalf("ошибка рекурсивного запроса: ожидался 200 OK, получен %d", respRec.StatusCode)
	}
}
