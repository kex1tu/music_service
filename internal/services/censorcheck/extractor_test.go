package censorcheck

import (
	"strings"
	"testing"
)

func TestExtractSpots_DrugWords(t *testing.T) {
	lrc := `[ti:Test Song]
[ar:Test Artist]
[00:15.30]Я иду по улице ночной
[00:25.00]Тут лежит закладка под сосной
[01:14.20]Закуривая паранойю в виде косяка
[02:05.50]В клубе экстази и мефедрон рекой
`
	spots := ExtractSpots(lrc, 3)
	if len(spots) == 0 {
		t.Fatalf("expected spots, got 0")
	}
	if len(spots) > 3 {
		t.Fatalf("expected <= 3 spots, got %d", len(spots))
	}

	foundWords := make(map[string]bool)
	for _, s := range spots {
		if s.Category != CategoryDrugCoAP {
			t.Errorf("expected CategoryDrugCoAP for word '%s', got %s", s.Word, s.Category)
		}
		foundWords[strings.ToLower(s.Word)] = true
	}

	// Должны найтись нарко-слова из текста
	if !foundWords["закладка"] && !foundWords["косяка"] && !foundWords["экстази"] && !foundWords["мефедрон"] {
		t.Errorf("expected drug words to be matched, found: %+v", foundWords)
	}
}

func TestExtractSpots_PlanContext(t *testing.T) {
	// Не-наркотический контекст: 0 точек
	safeLrc := `[00:10.00]У меня есть надежный план
[00:20.00]Все идет строго по плану
[00:30.00]Мы составили подробный бизнес-план
[00:40.00]Мой план действий безупречен
`
	safeSpots := ExtractSpots(safeLrc, 3)
	if len(safeSpots) != 0 {
		t.Errorf("expected 0 spots for non-drug 'plan' context, got %d: %+v", len(safeSpots), safeSpots)
	}

	// Наркотический контекст: 1+ точка
	drugLrc := `[01:05.00]Мы курим чистый план
[01:30.00]Забивай зеленый планчик
`
	drugSpots := ExtractSpots(drugLrc, 3)
	if len(drugSpots) == 0 {
		t.Fatalf("expected drug spots for smoking plan context, got 0")
	}
	for _, s := range drugSpots {
		if s.Category != CategoryDrugCoAP {
			t.Errorf("expected CategoryDrugCoAP for drug plan, got %s", s.Category)
		}
	}
}

func TestExtractSpots_SafeEbRoots(t *testing.T) {
	// Безопасные слова с корнем "еб" / "ёб": 0 точек
	safeLrc := `[00:10.00]Волшебная палочка в руке
[00:20.00]Птицы летают на небе высоко
[00:30.00]Свежий хлеб на столе лежит
[00:40.00]Ребята веселятся во дворе
`
	spots := ExtractSpots(safeLrc, 3)
	if len(spots) != 0 {
		t.Errorf("expected 0 spots for safe eb roots, got %d: %+v", len(spots), spots)
	}

	// Настоящий мат с корнем "еб": должны быть точки
	profaneLrc := `[01:10.00]Это полное наебалово кругом
[01:40.00]Мне поебать на все проблемы
`
	pSpots := ExtractSpots(profaneLrc, 3)
	if len(pSpots) == 0 {
		t.Fatalf("expected profanity spots, got 0")
	}
	for _, s := range pSpots {
		if s.Category != CategoryProfane {
			t.Errorf("expected CategoryProfane, got %s", s.Category)
		}
	}
}

func TestExtractSpots_MaxSpotsLimitAndPriority(t *testing.T) {
	// Текст с 6 матами и 1 словом по КоАП 6.13
	lrc := `[00:10.00]Сука блять пиздец нахуй
[00:30.00]Тут опять хуйня какая-то
[01:00.00]А вот здесь чистый мефедрон
[01:30.00]Ебать колотить бляха
[02:00.00]Заебали все подряд
`
	spots := ExtractSpots(lrc, 3)
	if len(spots) != 3 {
		t.Fatalf("expected exactly 3 spots, got %d", len(spots))
	}

	// Проверяем, что мефедрон (КоАП 6.13) обязательно попал в топ-3 благодаря высшему приоритету
	hasDrug := false
	for _, s := range spots {
		if s.Category == CategoryDrugCoAP && strings.EqualFold(s.Word, "мефедрон") {
			hasDrug = true
		}
	}
	if !hasDrug {
		t.Errorf("expected 'мефедрон' (priority 1) to be included in top-3 spots: %+v", spots)
	}

	// Проверяем хронологическую сортировку
	for i := 1; i < len(spots); i++ {
		if spots[i].TimeSec < spots[i-1].TimeSec {
			t.Errorf("spots not sorted chronologically: spot[%d]=%f < spot[%d]=%f", i, spots[i].TimeSec, i-1, spots[i-1].TimeSec)
		}
	}
}

func TestExtractSpots_SuspectMarkers(t *testing.T) {
	lrc := `[00:15.00]This is the radio edit version of track
[00:45.00]Он балдеет и дуреет каждый день
`
	spots := ExtractSpots(lrc, 3)
	if len(spots) == 0 {
		t.Fatalf("expected spots for suspect markers, got 0")
	}
	hasSuspect := false
	for _, s := range spots {
		if s.Category == CategorySuspect {
			hasSuspect = true
		}
	}
	if !hasSuspect {
		t.Errorf("expected CategorySuspect in spots, got %+v", spots)
	}
}

func TestExtractSpots_EmptyOrPlainLrc(t *testing.T) {
	// Пустой текст
	if len(ExtractSpots("", 3)) != 0 {
		t.Errorf("expected 0 spots for empty text")
	}

	// Текст без таймкодов (plain lyrics)
	plain := "Просто текст песни без таймкодов\nСука мефедрон косяк"
	if len(ExtractSpots(plain, 3)) != 0 {
		t.Errorf("expected 0 spots for plain lyrics without timestamps")
	}
}
