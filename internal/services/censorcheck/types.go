package censorcheck

// SpotCategory классифицирует категорию триггерного слова.
type SpotCategory string

const (
	// CategoryDrugCoAP — вещества, прекурсоры, приспособления и нарко-сленг (ст. 6.13 КоАП РФ).
	CategoryDrugCoAP SpotCategory = "DRUG_COAP_6_13"
	// CategorySuspect — подозрительные маркеры цензуры и эвфемизмы (clean, radio edit, балдеет, дуреет).
	CategorySuspect SpotCategory = "SUSPECT_MARKER"
	// CategoryProfane — нецензурная брань и грубый мат (хуй, пизда, ебать, блять, сука).
	CategoryProfane SpotCategory = "PROFANITY"
)

// QuickListenSpot представляет одну контрольную точку для прослушивания аудиофрагмента.
type QuickListenSpot struct {
	TimeSec  float64      `json:"time_sec"`  // Время в секундах (например, 74.2)
	TimeStr  string       `json:"time_str"`  // Форматированное время для бейджа ("01:14")
	Word     string       `json:"word"`      // Целевое слово ("косяк")
	Category SpotCategory `json:"category"`  // DRUG_COAP_6_13 / SUSPECT_MARKER / PROFANITY
	LineText string       `json:"line_text"` // Текст строки из песни
}
