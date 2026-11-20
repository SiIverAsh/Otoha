package views

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/a-h/templ"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// CatalogQuery is the state shared by catalog routes and controls.
type CatalogQuery struct {
	Query   string
	View    string
	Options db.ListWorksOptions
}

// HasFilters reports whether the browser has narrowed the catalog.
func (query CatalogQuery) HasFilters() bool {
	opts := query.Options
	return query.Query != "" || opts.Source != db.AllWorkSources ||
		opts.Circle != "" || opts.VA != "" || opts.Tag != "" ||
		opts.Progress != "" || opts.SubtitleOnly || opts.StorageID > 0
}

// Values serializes the catalog controls for links and forms.
func (query CatalogQuery) Values() url.Values {
	opts := query.Options
	values := url.Values{
		"age":       {opts.AgeMode},
		"view":      {query.View},
		"sort_by":   {opts.SortBy},
		"sort_desc": {strconv.FormatBool(opts.SortDesc)},
	}
	for key, value := range map[string]string{
		"q": query.Query, "source": string(opts.Source), "circle": opts.Circle,
		"va": opts.VA, "tag": opts.Tag, "progress": opts.Progress,
	} {
		if value != "" {
			values.Set(key, value)
		}
	}
	if opts.Page > 1 {
		values.Set("page", strconv.Itoa(opts.Page))
	}
	if opts.PageSize > 0 {
		values.Set("page_size", strconv.Itoa(opts.PageSize))
	}
	if opts.StorageID > 0 {
		values.Set("storage_id", strconv.FormatInt(opts.StorageID, 10))
	}
	if opts.SubtitleOnly {
		values.Set("subtitle_only", "1")
	}
	return values
}

// URL builds a link that preserves the catalog controls.
func (query CatalogQuery) URL(path string) string {
	return path + "?" + query.Values().Encode()
}

// Reset clears the search and optional filters.
func (query CatalogQuery) Reset() CatalogQuery {
	opts := &query.Options
	query.Query = ""
	opts.Page = 1
	opts.Source = db.AllWorkSources
	opts.Circle, opts.VA, opts.Tag, opts.Progress = "", "", "", ""
	opts.SubtitleOnly = false
	opts.StorageID = 0
	return query
}

// BrowseURL builds a link to another library section, retaining browsing preferences.
func (query CatalogQuery) BrowseURL(path string) string {
	browse := query.Reset()
	browse.Options.Source = query.Options.Source
	return browse.URL(path)
}

// WithoutSearch clears the search field and returns to the first page.
func (query CatalogQuery) WithoutSearch() CatalogQuery {
	query.Query = ""
	query.Options.Page = 1
	return query
}

// ReverseSort changes the ordering direction and returns to the first page.
func (query CatalogQuery) ReverseSort() CatalogQuery {
	query.Options.SortDesc = !query.Options.SortDesc
	query.Options.Page = 1
	return query
}

func (query CatalogQuery) facetURL(kind, value string) string {
	facet := query.Reset()
	facet.Options.Source = query.Options.Source
	switch kind {
	case "circle":
		facet.Options.Circle = value
	case "va":
		facet.Options.VA = value
	case "tag":
		facet.Options.Tag = value
	}
	return facet.URL("/")
}

func (query CatalogQuery) pageURL(page int) string {
	query.Options.Page = page
	return query.URL("/")
}

func (query CatalogQuery) viewURL(view string) string {
	query.View = view
	return query.URL("/")
}

func (query CatalogQuery) subtitleURL() string {
	query.Options.Page = 1
	query.Options.SubtitleOnly = !query.Options.SubtitleOnly
	return query.URL("/")
}

// CatalogOption pairs a catalog setting with its label.
type CatalogOption struct {
	Value string
	Label string
}

// CatalogSortOptions returns the available catalog orderings.
func CatalogSortOptions() []CatalogOption {
	return []CatalogOption{
		{Value: "release_date", Label: "发售日期"},
		{Value: "created_at", Label: "入库时间"},
		{Value: "rating", Label: "评分"},
		{Value: "dl_count", Label: "销量"},
		{Value: "workno", Label: "作品号"},
	}
}

// IsCatalogSort reports whether the sort value is available in the web controls.
func IsCatalogSort(value string) bool {
	return containsOption(CatalogSortOptions(), value)
}

func catalogAgeOptions() []CatalogOption {
	return []CatalogOption{
		{Value: "general", Label: "仅全年龄 (SFW)"},
		{Value: "sfw_unrated", Label: "全年龄 + 未分级"},
		{Value: "r15", Label: "R15 及以下"},
		{Value: "r18", Label: "仅 R18"},
		{Value: "all", Label: "全部分级"},
	}
}

// IsCatalogAge reports whether the age value is available in the web controls.
func IsCatalogAge(value string) bool {
	return containsOption(catalogAgeOptions(), value)
}

func containsOption(options []CatalogOption, value string) bool {
	return slices.ContainsFunc(options, func(option CatalogOption) bool {
		return option.Value == value
	})
}

// CatalogProgressOptions returns the user progress choices with their labels.
func CatalogProgressOptions() []CatalogOption {
	return []CatalogOption{
		{Value: "", Label: "未标记"},
		{Value: "marked", Label: "想听"},
		{Value: "listening", Label: "在听"},
		{Value: "listened", Label: "听过"},
		{Value: "replay", Label: "重听"},
		{Value: "postponed", Label: "搁置"},
	}
}

// ProgressLabel returns a label for the stored listening progress.
func ProgressLabel(progress string) string {
	for _, option := range CatalogProgressOptions() {
		if option.Value == progress {
			return option.Label
		}
	}
	return "-"
}

// CatalogData holds the rendered works, facets, and playback history.
type CatalogData struct {
	Query          CatalogQuery
	Works          []db.Work
	Recent         []db.HistoryEntry
	Circles        []db.FacetCount
	VAs            []db.FacetCount
	Tags           []db.FacetCount
	Total          int
	SearchDegraded bool
	SearchWarning  string
}

func (data CatalogData) totalPages() int {
	return max(1, (data.Total+data.Query.Options.PageSize-1)/data.Query.Options.PageSize)
}

// CoverURL returns a same-origin URL for the specified work's cover.
func CoverURL(workno string) string {
	return "/api/cover/" + url.PathEscape(workno)
}

// CatalogLinkAttributes enables fragment navigation with a full-page link fallback.
func CatalogLinkAttributes(href string) templ.Attributes {
	return templ.Attributes{
		"href":        href,
		"hx-boost":    "true",
		"hx-target":   "#page-content",
		"hx-swap":     "innerHTML",
		"hx-push-url": "true",
	}
}

func catalogCount(total int) string {
	return message.NewPrinter(language.SimplifiedChinese).Sprintf("%d 部作品", total)
}

func catalogWorkCreators(work db.Work) string {
	creators := make([]string, 0, len(work.VAs)+1)
	if work.CircleName != "" {
		creators = append(creators, work.CircleName)
	}
	for _, name := range work.VAs {
		if name != "" && !slices.Contains(creators, name) {
			creators = append(creators, name)
		}
	}
	return strings.Join(creators, " · ")
}

func tagNames(tags []db.Tag) string {
	names := make([]string, 0, len(tags))
	for _, tag := range tags {
		names = append(names, tag.Name)
	}
	return strings.Join(names, ", ")
}

func displayOr(value, placeholder string) string {
	if value == "" {
		return placeholder
	}
	return value
}

func tableWorkTooltip(work db.Work) string {
	rating := "-"
	if work.Rating != 0 {
		rating = strconv.FormatFloat(work.Rating, 'f', -1, 64)
	}
	return fmt.Sprintf("%s · %s | 评分:%s 销量:%d", work.Workno, work.Title, rating, work.DLCount)
}

type favoriteWork struct {
	Workno         string   `json:"workno"`
	Title          string   `json:"title"`
	CircleName     string   `json:"circle_name"`
	AgeCategory    int      `json:"age_category"`
	ASMRoneID      int      `json:"asmrone_id,omitempty"`
	SourceURL      string   `json:"source_url,omitempty"`
	OriginalWorkno string   `json:"original_workno,omitempty"`
	VAs            []string `json:"vas"`
	Tags           []db.Tag `json:"tags"`
	HasSubtitle    bool     `json:"has_subtitle"`
	ReleaseDate    string   `json:"release_date"`
	CreatedAt      int64    `json:"created_at"`
	DLCount        int      `json:"dl_count"`
	Rating         float64  `json:"rating"`
	Progress       string   `json:"progress"`
	TrackCount     int      `json:"track_count"`
}

func favoriteWorkJSON(work db.Work) (string, error) {
	return templ.JSONString(favoriteWork{
		Workno: work.Workno, Title: work.Title, CircleName: work.CircleName,
		AgeCategory: work.AgeCategory, ASMRoneID: work.AsmrOneID,
		SourceURL: work.SourceURL, OriginalWorkno: work.OriginalWorkno,
		VAs: work.VAs, Tags: work.Tags, HasSubtitle: work.HasSubtitle,
		ReleaseDate: work.ReleaseDate, CreatedAt: work.CreatedAt, DLCount: work.DLCount,
		Rating: work.Rating, Progress: work.Progress, TrackCount: work.TrackCount,
	})
}
