package asmrone

import "encoding/json"

// workInfoResponse maps GET /api/workInfo/{workno} and items in GET /api/works.
type workInfoResponse struct {
	ID                    int             `json:"id"`
	Title                 string          `json:"title"`
	CircleID              int             `json:"circle_id"`
	Name                  string          `json:"name"`
	Nsfw                  bool            `json:"nsfw"`
	Release               string          `json:"release"`
	CreateDate            string          `json:"create_date"`
	DLCount               int             `json:"dl_count"`
	Price                 int             `json:"price"`
	ReviewCount           int             `json:"review_count"`
	RateCount             int             `json:"rate_count"`
	RateAverage2dp        float64         `json:"rate_average_2dp"`
	RateCountDetail       json.RawMessage `json:"rate_count_detail"`
	Rank                  json.RawMessage `json:"rank"`
	HasSubtitle           bool            `json:"has_subtitle"`
	Duration              int             `json:"duration"`
	AgeCategoryString     string          `json:"age_category_string"` // "general" | "r15" | "adult"
	SourceID              string          `json:"source_id"`           // "RJ243448" / "RJ01683351"
	SourceType            string          `json:"source_type"`
	SourceURL             string          `json:"source_url"`
	OriginalWorkno        string          `json:"original_workno"`
	WorkAttributes        string          `json:"work_attributes"`
	LanguageEditions      json.RawMessage `json:"language_editions"`
	OtherLanguageEditions json.RawMessage `json:"other_language_editions_in_db"`
	TranslationInfo       json.RawMessage `json:"translation_info"`
	MainCoverURL          string          `json:"mainCoverUrl"`
	ThumbnailCoverURL     string          `json:"thumbnailCoverUrl"`
	SamCoverURL           string          `json:"samCoverUrl"`
	Series                *seriesDTO      `json:"series"`
	Circle                *circle         `json:"circle"`
	VAs                   []va            `json:"vas"`
	Tags                  []tagDTO        `json:"tags"`
}

type seriesDTO struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	SourceID string `json:"source_id"` // "SRI0000012345"
}

type circle struct {
	ID       int    `json:"id"`        // internal autoincrement ID — never use as DLsite RG code
	Name     string `json:"name"`
	SourceID string `json:"source_id"` // "RG12345" or "RG01005704"
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

// PaginationInfo maps pagination metadata returned by GET /api/works.
type PaginationInfo struct {
	CurrentPage int `json:"currentPage"`
	PageSize    int `json:"pageSize"`
	TotalCount  int `json:"totalCount"`
}

type worksPageResponse struct {
	Works      []workInfoResponse `json:"works"`
	Pagination PaginationInfo     `json:"pagination"`
}

// RemoteNode represents an element in the recursive tree returned by GET /api/tracks/{id}.
type RemoteNode struct {
	Type                string       `json:"type"` // "folder" | "audio" | "text" | "image" | "other"
	Hash                string       `json:"hash,omitempty"`
	Title               string       `json:"title"`
	MediaStreamURL      string       `json:"mediaStreamUrl,omitempty"`
	MediaDownloadURL    string       `json:"mediaDownloadUrl,omitempty"`
	StreamLowQualityURL string       `json:"streamLowQualityUrl,omitempty"`
	Duration            float64      `json:"duration,omitempty"`
	Size                int64        `json:"size,omitempty"`
	Children            []RemoteNode `json:"children,omitempty"`
}

// RemoteAudioTrack is a flattened audio leaf extracted from /api/tracks/{id} with its paired subtitle URL.
type RemoteAudioTrack struct {
	RelPath      string  `json:"rel_path"`
	Size         int64   `json:"size"`
	Duration     float64 `json:"duration"`
	Ord          int     `json:"ord"`
	RemoteHash   string  `json:"remote_hash"`
	StreamURL    string  `json:"stream_url"`
	DownloadURL  string  `json:"download_url"`
	StreamLowURL string  `json:"stream_low_url,omitempty"`
	SubtitleURL  string  `json:"subtitle_url,omitempty"`
}

// RemoteAttachment represents a non-audio bonus/script/image file in /api/tracks/{id}.
type RemoteAttachment struct {
	Type        string `json:"type"` // "image" | "text" | "other"
	RelPath     string `json:"rel_path"`
	Size        int64  `json:"size"`
	StreamURL   string `json:"stream_url,omitempty"`
	DownloadURL string `json:"download_url,omitempty"`
}

// TracksBundle contains extracted audio tracks (paired with subtitles) and bonus attachments.
type TracksBundle struct {
	AudioTracks []RemoteAudioTrack `json:"audio_tracks"`
	Attachments []RemoteAttachment `json:"attachments"`
}

type errorResponse struct {
	Error string `json:"error"`
}
