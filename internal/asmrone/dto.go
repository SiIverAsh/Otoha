package asmrone

// workInfoResponse maps GET /api/workInfo/{workno}.
// Field names verified against asmr.one production responses.
type workInfoResponse struct {
	ID                int      `json:"id"`
	Title             string   `json:"title"`
	Name              string   `json:"name"`
	Nsfw              bool     `json:"nsfw"`
	Release           string   `json:"release"`
	DLCount           int      `json:"dl_count"`
	Price             int      `json:"price"`
	ReviewCount       int      `json:"review_count"`
	RateCount         int      `json:"rate_count"`
	RateAverage2dp    float64  `json:"rate_average_2dp"`
	HasSubtitle       bool     `json:"has_subtitle"`
	Duration          int      `json:"duration"`
	AgeCategoryString string   `json:"age_category_string"` // "general" | "r15" | "adult"
	SourceID          string   `json:"source_id"`           // "RJ243448"
	SourceType        string   `json:"source_type"`
	OriginalWorkno    string   `json:"original_workno"`
	MainCoverURL      string   `json:"mainCoverUrl"`
	ThumbnailCoverURL string   `json:"thumbnailCoverUrl"`
	SamCoverURL       string   `json:"samCoverUrl"`
	Circle            *circle  `json:"circle"`
	VAs               []va     `json:"vas"`
	Tags              []tagDTO `json:"tags"`
}

type circle struct {
	ID       int    `json:"id"`        // internal autoincrement ID — never use as DLsite RG code
	Name     string `json:"name"`
	SourceID string `json:"source_id"` // "RG12345"
}

type va struct {
	ID   string `json:"id"` // UUID v5
	Name string `json:"name"`
}

type tagDTO struct {
	ID   int                  `json:"id"`
	Name string               `json:"name"`
	I18n map[string]tagI18nDT `json:"i18n"` // key: "zh-cn" / "ja-jp" / "en-us"
}

type tagI18nDT struct {
	Name string `json:"name"`
}

type errorResponse struct {
	Error string `json:"error"`
}
