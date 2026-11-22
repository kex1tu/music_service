package resolver

import (
	"testing"
)

func TestExtractSaviorsAndCheckBad_Cases(t *testing.T) {
	// 1. Коллизии артистов и названий
	t.Run("Clean Bandit collision with savior", func(t *testing.T) {
		hasSavior, isBad := ExtractSaviorsAndCheckBad("Clean Bandit - Symphony (до цензуры)", "Clean Bandit", "Symphony")
		if !hasSavior {
			t.Errorf("expected hasSavior = true")
		}
		if isBad {
			t.Errorf("expected isBad = false, got true (Clean Bandit collision false positive)")
		}
	})

	t.Run("Track (Original Mix) with regular target", func(t *testing.T) {
		_, isBad := ExtractSaviorsAndCheckBad("Track (Original Mix)", "Artist", "Track")
		if isBad {
			t.Errorf("expected isBad = false for Original Mix, got true")
		}
	})

	t.Run("Track (Remix) when target is remix", func(t *testing.T) {
		_, isBad := ExtractSaviorsAndCheckBad("Track (Remix)", "Artist", "Track (Remix)")
		if isBad {
			t.Errorf("expected isBad = false when target is remix, got true")
		}
	})

	t.Run("Track (Remix) when target is regular track", func(t *testing.T) {
		_, isBad := ExtractSaviorsAndCheckBad("Track (Remix)", "Artist", "Track")
		if !isBad {
			t.Errorf("expected isBad = true for unrequested remix, got false")
		}
	})

	// 2. Anti-Glue разделители
	t.Run("Clean(old version)Clean glued markers", func(t *testing.T) {
		hasSavior, isBad := ExtractSaviorsAndCheckBad("Clean(old version)Clean", "Artist", "Track")
		if !hasSavior {
			t.Errorf("expected hasSavior = true")
		}
		if !isBad {
			t.Errorf("expected isBad = true due to Clean stop word on remainder")
		}
	})

	// 3. Регистр и спасители
	t.Run("Scally Milano - Заберу (ДО ЦЕНЗУРЫ) uppercase savior", func(t *testing.T) {
		hasSavior, isBad := ExtractSaviorsAndCheckBad("Scally Milano - Заберу (ДО ЦЕНЗУРЫ)", "Scally Milano", "Заберу")
		if !hasSavior {
			t.Errorf("expected hasSavior = true")
		}
		if isBad {
			t.Errorf("expected isBad = false")
		}
	})

	t.Run("Track [Old version uncut] adjacent savior phrases", func(t *testing.T) {
		hasSavior, isBad := ExtractSaviorsAndCheckBad("Track [Old version uncut]", "Artist", "Track")
		if !hasSavior {
			t.Errorf("expected hasSavior = true")
		}
		if isBad {
			t.Errorf("expected isBad = false")
		}
	})
}

func TestRankCandidates_StatusesAndDeltas(t *testing.T) {
	// 4. Хронометраж и статусы
	validDurations := []int{180}
	distributors := []string{"distrokid", "believe"}

	// Спаситель + совпадение <= 2с -> marked
	c1 := []YtCandidate{
		{ID: "1", Title: "Track (до цензуры)", Duration: 181, UploadDate: "20220101"},
	}
	r1 := RankCandidates(c1, "Artist", "Track", validDurations, 180, false, 2, 10, 2023, distributors)
	if len(r1) != 1 || r1[0].Status != "marked" {
		t.Fatalf("expected marked status, got %+v", r1)
	}

	// Спаситель + эталона нет -> marked
	c2 := []YtCandidate{
		{ID: "2", Title: "Track (до цензуры)", Duration: 250, UploadDate: "20220101"},
	}
	r2 := RankCandidates(c2, "Artist", "Track", nil, 0, true, 2, 10, 2023, distributors)
	if len(r2) != 1 || r2[0].Status != "marked" {
		t.Fatalf("expected marked status when reference missing, got %+v", r2)
	}

	// Спаситель + дельта 4с -> unverified (сохраняется в альтернативах)
	c3 := []YtCandidate{
		{ID: "3", Title: "Track (до цензуры)", Duration: 184, UploadDate: "20220101"},
	}
	r3 := RankCandidates(c3, "Artist", "Track", validDurations, 180, false, 2, 10, 2023, distributors)
	if len(r3) != 1 || r3[0].Status != "unverified" {
		t.Fatalf("expected unverified status for delta 4s with savior, got %+v", r3)
	}

	// Нет спасителя + совпадение <= 2с -> likely
	c4 := []YtCandidate{
		{ID: "4", Title: "Track", Duration: 182, UploadDate: "20220101"},
	}
	r4 := RankCandidates(c4, "Artist", "Track", validDurations, 180, false, 2, 10, 2023, distributors)
	if len(r4) != 1 || r4[0].Status != "likely" {
		t.Fatalf("expected likely status, got %+v", r4)
	}

	// Нет спасителя + дельта 4с -> unverified (сохраняется как близкая альтернатива)
	c5 := []YtCandidate{
		{ID: "5", Title: "Track", Duration: 184, UploadDate: "20220101"},
	}
	r5 := RankCandidates(c5, "Artist", "Track", validDurations, 180, false, 2, 10, 2023, distributors)
	if len(r5) != 1 || r5[0].Status != "unverified" {
		t.Fatalf("expected unverified status for delta 4s without savior, got %+v", r5)
	}

	// Нет спасителя + дельта 10с -> отбраковывается
	c6 := []YtCandidate{
		{ID: "6", Title: "Track", Duration: 190, UploadDate: "20220101"},
	}
	r6 := RankCandidates(c6, "Artist", "Track", validDurations, 180, false, 2, 10, 2023, distributors)
	if len(r6) != 0 {
		t.Fatalf("expected candidate with delta 10s and no savior to be dropped, got %+v", r6)
	}
}
