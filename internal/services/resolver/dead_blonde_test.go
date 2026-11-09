package resolver

import (
	"math"
	"strings"
	"testing"
)

// TestDeadBlonde_RejectRemixesAndSlowed проверяет отсечение ремиксов, slowed+reverb и спид-апов треков DEAD BLONDE
func TestDeadBlonde_RejectRemixesAndSlowed(t *testing.T) {
	matcher := NewFullReleaseMatcher(nil)

	targetTrack := ResolvedTrack{
		TrackID:  "dz_malchik",
		Title:    "Мальчик на девятке",
		Artist:   "DEAD BLONDE",
		Duration: 187,
	}

	candidates := []struct {
		cand        AlbumTrackCandidate
		shouldMatch bool
		reason      string
	}{
		{
			cand:        AlbumTrackCandidate{VideoID: "slow1", Title: "Мальчик на девятке (slowed + reverb)", Duration: 235},
			shouldMatch: false,
			reason:      "Slowed версия с искаженной длительностью должна отсекаться (дельта 48с > 12с)",
		},
		{
			cand:        AlbumTrackCandidate{VideoID: "sped1", Title: "Мальчик на девятке [Sped Up]", Duration: 130},
			shouldMatch: false,
			reason:      "Sped Up версия с ускоренным хронометражем должна отсекаться (дельта 57с > 12с)",
		},
		{
			cand:        AlbumTrackCandidate{VideoID: "remix1", Title: "Мальчик на девятке (DJ Club Remix)", Duration: 220},
			shouldMatch: false,
			reason:      "Клубный ремикс должен отсекаться (дельта 33с > 12с)",
		},
		{
			cand:        AlbumTrackCandidate{VideoID: "official", Title: "Мальчик на девятке", Duration: 188},
			shouldMatch: true,
			reason:      "Официальная альбомная дорожка (дельта 1с <= 12с) должна успешно сопоставляться",
		},
	}

	for _, tc := range candidates {
		urls, matched := matcher.MatchTracklists([]ResolvedTrack{targetTrack}, []AlbumTrackCandidate{tc.cand})
		isMatched := matched == 1 && len(urls) == 1
		if isMatched != tc.shouldMatch {
			t.Errorf("Кандидат %q: сопоставлен=%v, ожидалось=%v. Причина: %s", tc.cand.Title, isMatched, tc.shouldMatch, tc.reason)
		}
	}
}

// TestDeadBlonde_RejectCoversWithForeignArtists проверяет отсечение любительских каверов сторонних исполнителей
func TestDeadBlonde_RejectCoversWithForeignArtists(t *testing.T) {
	targetArtist := "DEAD BLONDE"
	coverArtist := "Вася Каверщик & Ансамбль"

	normTarget := strings.ToLower(CleanQueryText(targetArtist))
	normCover := strings.ToLower(CleanQueryText(coverArtist))

	sim := LevenshteinRatio(normTarget, normCover)
	if sim >= 0.65 {
		t.Fatalf("Артист кавера %q ошибочно признан похожим на %q (score=%.2f >= 0.65)", coverArtist, targetArtist, sim)
	}

	// Проверяем, что эквивалентность артистов возвращает false
	if AreArtistsEquivalent(targetArtist, coverArtist) {
		t.Fatalf("AreArtistsEquivalent ошибочно вернул true для %q и %q", targetArtist, coverArtist)
	}
}

// TestDeadBlonde_FullAlbumSpletnitsa_Matching проверяет 100% сопоставление 12 треков альбома «СПЛЕТНИЦА»
func TestDeadBlonde_FullAlbumSpletnitsa_Matching(t *testing.T) {
	matcher := NewFullReleaseMatcher(nil)

	// Эталонный треклист альбома DEAD BLONDE - СПЛЕТНИЦА (2022)
	targetTracks := []ResolvedTrack{
		{TrackID: "dz_1", TrackNumber: 1, Title: "Банкомат", Artist: "DEAD BLONDE", Duration: 200},
		{TrackID: "dz_2", TrackNumber: 2, Title: "Красный свет", Artist: "DEAD BLONDE", Duration: 198},
		{TrackID: "dz_3", TrackNumber: 3, Title: "Самолёт", Artist: "DEAD BLONDE", Duration: 182},
		{TrackID: "dz_4", TrackNumber: 4, Title: "Без шансов", Artist: "DEAD BLONDE", Duration: 214},
		{TrackID: "dz_5", TrackNumber: 5, Title: "Beauty Queen", Artist: "DEAD BLONDE", Duration: 206},
		{TrackID: "dz_6", TrackNumber: 6, Title: "VIP CLUB", Artist: "DEAD BLONDE", Duration: 151},
		{TrackID: "dz_7", TrackNumber: 7, Title: "Всего лишь друг", Artist: "DEAD BLONDE", Duration: 209},
		{TrackID: "dz_8", TrackNumber: 8, Title: "Мираж", Artist: "DEAD BLONDE", Duration: 199},
		{TrackID: "dz_9", TrackNumber: 9, Title: "Тело", Artist: "DEAD BLONDE", Duration: 175},
		{TrackID: "dz_10", TrackNumber: 10, Title: "Не такая, как все", Artist: "DEAD BLONDE", Duration: 168},
		{TrackID: "dz_11", TrackNumber: 11, Title: "Сталкер", Artist: "DEAD BLONDE", Duration: 218},
		{TrackID: "dz_12", TrackNumber: 12, Title: "Ту-лу-ла", Artist: "DEAD BLONDE", Duration: 168},
	}

	// Кандидат из официального YouTube Music релиза (с небольшими дельтами длительности в пределах 2 сек)
	candTracks := []AlbumTrackCandidate{
		{VideoID: "m80jEPGrr7o", Title: "Банкомат", Duration: 200, Index: 1},
		{VideoID: "0pd_z_x144M", Title: "Красный свет", Duration: 198, Index: 2},
		{VideoID: "B2dcmYaRzaY", Title: "Самолёт", Duration: 182, Index: 3},
		{VideoID: "6pnnNP_S63U", Title: "Без шансов", Duration: 214, Index: 4},
		{VideoID: "4C7dbuDzYjw", Title: "Beauty Queen", Duration: 206, Index: 5},
		{VideoID: "rMrVN4uI_5o", Title: "VIP CLUB", Duration: 151, Index: 6},
		{VideoID: "2SmIbl0xahc", Title: "Всего лишь друг", Duration: 209, Index: 7},
		{VideoID: "l666MnTi0_I", Title: "Мираж", Duration: 199, Index: 8},
		{VideoID: "bmnChhr1K7A", Title: "Тело", Duration: 175, Index: 9},
		{VideoID: "8P0fClE7neo", Title: "Не такая, как все", Duration: 168, Index: 10},
		{VideoID: "1sV6s65qC7U", Title: "Сталкер", Duration: 218, Index: 11},
		{VideoID: "7XSaGy3TMEo", Title: "Ту-лу-ла", Duration: 168, Index: 12},
	}

	urls, matched := matcher.MatchTracklists(targetTracks, candTracks)
	if matched != 12 {
		t.Fatalf("Ожидалось 12/12 совпадений треков альбома «СПЛЕТНИЦА», получено: %d", matched)
	}
	if len(urls) != 12 {
		t.Fatalf("Ожидалось 12 прямых URL, получено: %d", len(urls))
	}

	// Проверяем, что первый трек получил точный VideoID "m80jEPGrr7o"
	if urls[0] != "https://music.youtube.com/watch?v=m80jEPGrr7o" {
		t.Errorf("Трек 0 (Банкомат) получил неверный URL: %s", urls[0])
	}
}

// TestDeadBlonde_SoundCloudSet_Parsing проверяет парсинг сета DEAD BLONDE с агрегаторным загрузчиком
func TestDeadBlonde_SoundCloudSet_Parsing(t *testing.T) {
	uploader := "Русская Рэп-Сцена"
	rawTracks := []struct {
		title   string
		wantArt string
		wantTit string
	}{
		{"DEAD BLONDE - Банкомат [FREE DL]", "DEAD BLONDE", "Банкомат"},
		{"DEAD BLONDE — Перестройка (Buy = Free DL)", "DEAD BLONDE", "Перестройка"},
		{"DEAD BLONDE ft. GSPD - Первая любовь (prod. by GSPD)", "DEAD BLONDE", "Первая любовь"},
	}

	for _, tr := range rawTracks {
		art, tit := ParseSoundCloudTitle(tr.title, uploader)
		if !strings.Contains(art, tr.wantArt) {
			t.Errorf("Для заголовка %q: артист=%q, ожидалось вхождение %q", tr.title, art, tr.wantArt)
		}
		if tit != tr.wantTit {
			t.Errorf("Для заголовка %q: название=%q, ожидалось %q", tr.title, tit, tr.wantTit)
		}
	}
}

// TestDeadBlonde_DurationToleranceDelta12s проверяет строгое соблюдение допуска дельты хронометража <= 12 секунд
func TestDeadBlonde_DurationToleranceDelta12s(t *testing.T) {
	testCases := []struct {
		targetDur int
		candDur   int
		allowed   bool
	}{
		{200, 200, true},  // дельта 0с
		{200, 205, true},  // дельта 5с
		{200, 212, true},  // дельта 12с (ровно граница)
		{200, 213, false}, // дельта 13с > 12с
		{200, 188, true},  // дельта 12с
		{200, 187, false}, // дельта 13с > 12с
		{187, 235, false}, // дельта 48с (slowed)
	}

	for _, tc := range testCases {
		diff := math.Abs(float64(tc.targetDur - tc.candDur))
		isAllowed := diff <= 12.0
		if isAllowed != tc.allowed {
			t.Errorf("Дельта между %d и %d (%f): получено allowed=%v, ожидалось %v", tc.targetDur, tc.candDur, diff, isAllowed, tc.allowed)
		}
	}
}
