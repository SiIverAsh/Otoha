package dlsite

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// AgeCategory constants (§3.4):
// 1 = All-ages (GEN / 全年齢), 2 = R-15, 3 = R-18.
const (
	AgeUnknown = 0
	AgeGeneral = 1
	AgeR15     = 2
	AgeR18     = 3
)

// Metadata represents unified work metadata extracted from DLsite or asmr.one (§3.4).
type Metadata struct {
	Workno               string   `json:"workno"`
	AsmrOneID            int      `json:"asmrone_id,omitempty"`
	Title                string   `json:"title"`
	CircleID             string   `json:"circle_id,omitempty"`
	CircleName           string   `json:"circle_name,omitempty"`
	ReleaseDate          string   `json:"release_date,omitempty"`
	CreateDate           string   `json:"create_date,omitempty"`
	AgeCategory          int      `json:"age_category"`
	WorkType             string   `json:"work_type,omitempty"`
	Price                int      `json:"price,omitempty"`
	Rating               float64  `json:"rating,omitempty"`
	RateCount            int      `json:"rate_count,omitempty"`
	ReviewCount          int      `json:"review_count,omitempty"`
	DLCount              int      `json:"dl_count,omitempty"`
	FileSize             int64    `json:"file_size,omitempty"`
	Duration             int      `json:"duration,omitempty"`
	HasSubtitle          bool     `json:"has_subtitle,omitempty"`
	SeriesID             string   `json:"series_id,omitempty"`
	SeriesName           string   `json:"series_name,omitempty"`
	CoverURL             string   `json:"cover_url,omitempty"`
	SamCoverURL          string   `json:"sam_cover_url,omitempty"`
	ThumbnailCoverURL    string   `json:"thumbnail_cover_url,omitempty"`
	SourceType           string   `json:"source_type,omitempty"`
	SourceURL            string   `json:"source_url,omitempty"`
	WorkAttributes       string   `json:"work_attributes,omitempty"`
	LanguageEditionsJSON string   `json:"language_editions_json,omitempty"`
	OtherEditionsJSON    string   `json:"other_editions_json,omitempty"`
	TranslationInfoJSON  string   `json:"translation_info_json,omitempty"`
	RateDetailJSON       string   `json:"rate_detail_json,omitempty"`
	RankJSON             string   `json:"rank_json,omitempty"`
	VAs                  []string `json:"vas,omitempty"`
	Scenarios            []string `json:"scenarios,omitempty"`
	Illusts              []string `json:"illusts,omitempty"`
	Musics               []string `json:"musics,omitempty"`
	Tags                 []string `json:"tags,omitempty"`
	OriginalWorkno       string   `json:"original_workno,omitempty"`
	Locale               string   `json:"locale,omitempty"`
	Source               string   `json:"source,omitempty"`
	NotFound             bool     `json:"not_found,omitempty"`
	FetchedAt            int64    `json:"fetched_at"`
}

// FormatLocaleParam returns the underscore locale required by product.json (?locale=zh_cn).
func FormatLocaleParam(locale string) string {
	l := strings.ToLower(strings.TrimSpace(locale))
	l = strings.ReplaceAll(l, "-", "_")
	switch l {
	case "ja_jp", "zh_cn", "zh_tw", "en_us", "ko_kr":
		return l
	default:
		return "zh_cn"
	}
}

// FormatLocaleCookie returns the hyphen locale required by DLsite HTML Cookie (locale=zh-cn).
func FormatLocaleCookie(locale string) string {
	return "locale=" + strings.ReplaceAll(FormatLocaleParam(locale), "_", "-")
}

type rawProductItem struct {
	Workno           string          `json:"workno"`
	WorkName         string          `json:"work_name"`
	MakerID          string          `json:"maker_id"`
	MakerName        string          `json:"maker_name"`
	RegistDate       string          `json:"regist_date"`
	AgeCategory      int             `json:"age_category"`
	WorkType         string          `json:"work_type"`
	Price            flexInt         `json:"price"`
	OfficialPrice    flexInt         `json:"official_price"`
	DLCount          flexInt         `json:"dl_count"`
	RateAverage2dp   flexFloat       `json:"rate_average_2dp"`
	RateCount        flexInt         `json:"rate_count"`
	ReviewCount      flexInt         `json:"review_count"`
	ContentsSize     flexInt64       `json:"contents_file_size"`
	SeriesID         string          `json:"series_id"`
	SeriesName       string          `json:"series_name"`
	ImageMain        rawImage        `json:"image_main"`
	Genres           []rawNamedItem  `json:"genres"`
	GenresReplaced   []rawNamedItem  `json:"genres_replaced"`
	Creaters         json.RawMessage `json:"creaters"` // PHP json_encode outputs [] when empty!
	TranslationInfo  json.RawMessage `json:"translation_info"`
	Duration         flexInt         `json:"duration"`
	HasSubtitle      bool            `json:"has_subtitle"`
	ImageThum        rawImage        `json:"image_thum"`
	ImageThumb       string          `json:"image_thumb"`
	SiteID           string          `json:"site_id"`
	RateDetail       json.RawMessage `json:"rate_count_detail"`
	Rank             json.RawMessage `json:"rank"`
	LanguageEditions json.RawMessage `json:"language_editions"`
	OtherEditions    json.RawMessage `json:"other_language_editions_in_db"`
	WorkAttributes   string          `json:"work_attributes"`
	RankTotal        flexInt         `json:"rank_total"`
	RankYear         flexInt         `json:"rank_year"`
	RankMonth        flexInt         `json:"rank_month"`
	RankWeek         flexInt         `json:"rank_week"`
	RankDay          flexInt         `json:"rank_day"`
	RankTotalDate    string          `json:"rank_total_date"`
	RankYearDate     string          `json:"rank_year_date"`
	RankMonthDate    string          `json:"rank_month_date"`
	RankWeekDate     string          `json:"rank_week_date"`
	RankDayDate      string          `json:"rank_day_date"`
}

type rawImage struct {
	URL string `json:"url"`
}

type rawNamedItem struct {
	Name string `json:"name"`
}

type rawCreaters struct {
	VoiceBy    []rawNamedItem `json:"voice_by"`
	ScenarioBy []rawNamedItem `json:"scenario_by"`
	IllustBy   []rawNamedItem `json:"illust_by"`
	MusicBy    []rawNamedItem `json:"music_by"`
	CreatedBy  []rawNamedItem `json:"created_by"`
}

type rawTransInfo struct {
	IsTranslationAgree bool   `json:"is_translation_agree"`
	OriginalWorkno     string `json:"original_workno"`
	ParentWorkno       string `json:"parent_workno"`
}

type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `" `)
	if s == "" || s == "null" || s == "false" {
		*f = 0
		return nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		if fv, ferr := strconv.ParseFloat(s, 64); ferr == nil {
			*f = flexInt(int(fv))
			return nil
		}
		*f = 0
		return nil
	}
	*f = flexInt(v)
	return nil
}

type flexInt64 int64

func (f *flexInt64) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `" `)
	if s == "" || s == "null" || s == "false" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		*f = 0
		return nil
	}
	*f = flexInt64(v)
	return nil
}

type flexFloat float64

func (f *flexFloat) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `" `)
	if s == "" || s == "null" || s == "false" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		*f = 0
		return nil
	}
	*f = flexFloat(v)
	return nil
}

func normalizeCoverURL(u string) string {
	u = strings.TrimSpace(u)
	if strings.HasPrefix(u, "//") {
		return "https:" + u
	}
	return u
}

func dedupeStrings(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	var out []string
	for _, it := range items {
		it = strings.TrimSpace(it)
		if it == "" {
			continue
		}
		if _, ok := seen[it]; ok {
			continue
		}
		seen[it] = struct{}{}
		out = append(out, it)
	}
	return out
}

func extractNames(items []rawNamedItem) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if n := strings.TrimSpace(item.Name); n != "" {
			out = append(out, n)
		}
	}
	return dedupeStrings(out)
}

// ParseProductJSON parses DLsite's product.json array response.
// Returns ErrWorkNotFound if the JSON array is empty.
func ParseProductJSON(body []byte, expectedWorkno string) (*Metadata, error) {
	var arr []rawProductItem
	if err := json.Unmarshal(body, &arr); err != nil {
		return nil, fmt.Errorf("parse product.json: %w", err)
	}
	if len(arr) == 0 {
		return nil, ErrWorkNotFound
	}
	item := arr[0]
	expected := NormalizeWorkno(expectedWorkno)
	if expected != "" {
		for _, candidate := range arr {
			if NormalizeWorkno(candidate.Workno) == expected {
				item = candidate
				break
			}
		}
	}
	wno := NormalizeWorkno(item.Workno)
	if wno == "" {
		wno = NormalizeWorkno(expectedWorkno)
	}
	if expected := NormalizeWorkno(expectedWorkno); expected != "" && wno != expected {
		return nil, fmt.Errorf("product.json returned %s for %s", wno, expected)
	}
	releaseDate := parseReleaseDate(item.RegistDate)
	price := int(item.Price)
	if price == 0 {
		price = int(item.OfficialPrice)
	}
	genres := item.Genres
	if len(item.GenresReplaced) > 0 {
		genres = item.GenresReplaced
	}

	m := &Metadata{
		Workno:               wno,
		Title:                strings.TrimSpace(item.WorkName),
		CircleID:             strings.TrimSpace(item.MakerID),
		CircleName:           strings.TrimSpace(item.MakerName),
		ReleaseDate:          releaseDate,
		AgeCategory:          item.AgeCategory,
		WorkType:             strings.TrimSpace(item.WorkType),
		Price:                price,
		Rating:               float64(item.RateAverage2dp),
		RateCount:            int(item.RateCount),
		ReviewCount:          int(item.ReviewCount),
		DLCount:              int(item.DLCount),
		FileSize:             int64(item.ContentsSize),
		Duration:             int(item.Duration),
		HasSubtitle:          item.HasSubtitle,
		SeriesID:             strings.TrimSpace(item.SeriesID),
		SeriesName:           strings.TrimSpace(item.SeriesName),
		CoverURL:             normalizeCoverURL(item.ImageMain.URL),
		SamCoverURL:          normalizeCoverURL(item.ImageThum.URL),
		ThumbnailCoverURL:    normalizeCoverURL(item.ImageThumb),
		SourceType:           "DLSITE",
		SourceURL:            fmt.Sprintf("https://www.dlsite.com/maniax/work/=/product_id/%s.html", wno),
		WorkAttributes:       strings.TrimSpace(item.WorkAttributes),
		LanguageEditionsJSON: compactJSON(item.LanguageEditions),
		OtherEditionsJSON:    compactJSON(item.OtherEditions),
		RateDetailJSON:       compactJSON(item.RateDetail),
		RankJSON:             compactJSON(item.Rank),
		TranslationInfoJSON:  compactJSON(item.TranslationInfo),
		Tags:                 extractNames(genres),
		Source:               "product.json",
	}

	if item.SiteID != "" && regexp.MustCompile(`^[a-z]+$`).MatchString(item.SiteID) {
		m.SourceURL = fmt.Sprintf("https://www.dlsite.com/%s/work/=/product_id/%s.html", item.SiteID, wno)
	}
	if m.RankJSON == "" {
		ranks := make(map[string]any)
		for _, rank := range []struct {
			name  string
			value flexInt
			date  string
		}{
			{"total", item.RankTotal, item.RankTotalDate}, {"year", item.RankYear, item.RankYearDate},
			{"month", item.RankMonth, item.RankMonthDate}, {"week", item.RankWeek, item.RankWeekDate}, {"day", item.RankDay, item.RankDayDate},
		} {
			if rank.value > 0 {
				ranks[rank.name] = map[string]any{"rank": int(rank.value), "date": rank.date}
			}
		}
		if len(ranks) > 0 {
			raw, _ := json.Marshal(ranks)
			m.RankJSON = string(raw)
		}
	}
	if len(item.TranslationInfo) > 0 {
		var translation rawTransInfo
		_ = json.Unmarshal(item.TranslationInfo, &translation)
		if orig := NormalizeWorkno(translation.OriginalWorkno); orig != "" && orig != wno {
			m.OriginalWorkno = orig
		} else if parent := NormalizeWorkno(translation.ParentWorkno); parent != "" && parent != wno {
			m.OriginalWorkno = parent
		}
	}

	trimmedCreaters := bytes.TrimSpace(item.Creaters)
	if len(trimmedCreaters) > 0 && trimmedCreaters[0] == '{' {
		var cr rawCreaters
		if err := json.Unmarshal(trimmedCreaters, &cr); err == nil {
			m.VAs = extractNames(cr.VoiceBy)
			m.Scenarios = extractNames(cr.ScenarioBy)
			m.Illusts = extractNames(cr.IllustBy)
			m.Musics = extractNames(cr.MusicBy)
		}
	}

	return m, nil
}

// MergeFallbackMetadata fills missing fields from the same work. Original editions
// may contribute shared credits. Publishing details belong to each edition.
func MergeFallbackMetadata(dst, src *Metadata) {
	if dst == nil || src == nil {
		return
	}
	if dst.Workno != "" && src.Workno != "" && NormalizeWorkno(dst.Workno) != NormalizeWorkno(src.Workno) {
		mergeSharedCredits(dst, src)
		return
	}
	if dst.Title == "" {
		dst.Title = src.Title
	}
	if dst.CircleID == "" {
		dst.CircleID = src.CircleID
	}
	if dst.CircleName == "" {
		dst.CircleName = src.CircleName
	}
	if dst.ReleaseDate == "" {
		dst.ReleaseDate = src.ReleaseDate
	}
	if dst.AgeCategory == AgeUnknown {
		dst.AgeCategory = src.AgeCategory
	}
	if dst.WorkType == "" {
		dst.WorkType = src.WorkType
	}
	if dst.Price == 0 {
		dst.Price = src.Price
	}
	if dst.Rating == 0 {
		dst.Rating = src.Rating
	}
	if dst.RateCount == 0 {
		dst.RateCount = src.RateCount
	}
	if dst.ReviewCount == 0 {
		dst.ReviewCount = src.ReviewCount
	}
	if dst.DLCount == 0 {
		dst.DLCount = src.DLCount
	}
	if dst.FileSize == 0 {
		dst.FileSize = src.FileSize
	}
	if dst.SeriesID == "" {
		dst.SeriesID = src.SeriesID
	}
	if dst.SeriesName == "" {
		dst.SeriesName = src.SeriesName
	}
	if dst.CoverURL == "" {
		dst.CoverURL = src.CoverURL
	}
	if len(dst.VAs) == 0 {
		dst.VAs = append([]string(nil), src.VAs...)
	}
	if len(dst.Scenarios) == 0 {
		dst.Scenarios = append([]string(nil), src.Scenarios...)
	}
	if len(dst.Illusts) == 0 {
		dst.Illusts = append([]string(nil), src.Illusts...)
	}
	if len(dst.Musics) == 0 {
		dst.Musics = append([]string(nil), src.Musics...)
	}
	if len(dst.Tags) == 0 {
		dst.Tags = append([]string(nil), src.Tags...)
	}
	if dst.AsmrOneID == 0 {
		dst.AsmrOneID = src.AsmrOneID
	}
	if dst.CreateDate == "" {
		dst.CreateDate = src.CreateDate
	}
	if dst.Duration == 0 {
		dst.Duration = src.Duration
	}
	if !dst.HasSubtitle && src.HasSubtitle {
		dst.HasSubtitle = true
	}
	if dst.SamCoverURL == "" {
		dst.SamCoverURL = src.SamCoverURL
	}
	if dst.ThumbnailCoverURL == "" {
		dst.ThumbnailCoverURL = src.ThumbnailCoverURL
	}
	if dst.SourceType == "" {
		dst.SourceType = src.SourceType
	}
	if dst.SourceURL == "" {
		dst.SourceURL = src.SourceURL
	}
	if dst.WorkAttributes == "" {
		dst.WorkAttributes = src.WorkAttributes
	}
	if dst.LanguageEditionsJSON == "" {
		dst.LanguageEditionsJSON = src.LanguageEditionsJSON
	}
	if dst.OtherEditionsJSON == "" {
		dst.OtherEditionsJSON = src.OtherEditionsJSON
	}
	if dst.TranslationInfoJSON == "" {
		dst.TranslationInfoJSON = src.TranslationInfoJSON
	}
	if dst.RateDetailJSON == "" {
		dst.RateDetailJSON = src.RateDetailJSON
	}
	if dst.RankJSON == "" {
		dst.RankJSON = src.RankJSON
	}
	if dst.OriginalWorkno == "" {
		dst.OriginalWorkno = src.OriginalWorkno
	}
}

func mergeSharedCredits(dst, src *Metadata) {
	credits := &Metadata{
		Workno: dst.Workno, SeriesID: src.SeriesID, SeriesName: src.SeriesName,
		VAs: src.VAs, Scenarios: src.Scenarios, Illusts: src.Illusts,
		Musics: src.Musics, Tags: src.Tags,
	}
	MergeFallbackMetadata(dst, credits)
}

func compactJSON(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "[]" || string(raw) == "{}" {
		return ""
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return ""
	}
	return compact.String()
}

var releaseDateRe = regexp.MustCompile(`(\d{4})\D+(\d{1,2})\D+(\d{1,2})`)

func parseReleaseDate(value string) string {
	m := releaseDateRe.FindStringSubmatch(value)
	if len(m) != 4 {
		return ""
	}
	year, _ := strconv.Atoi(m[1])
	month, _ := strconv.Atoi(m[2])
	day, _ := strconv.Atoi(m[3])
	d := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if d.Year() != year || int(d.Month()) != month || d.Day() != day {
		return ""
	}
	return d.Format("2006-01-02")
}

type rawAjaxInfo struct {
	DLCount        flexInt   `json:"dl_count"`
	RateAverage2dp flexFloat `json:"rate_average_2dp"`
	RateCount      flexInt   `json:"rate_count"`
	ReviewCount    flexInt   `json:"review_count"`
	Price          flexInt   `json:"price"`
	OfficialPrice  flexInt   `json:"official_price"`
	WorkName       string    `json:"work_name"`
	MakerID        string    `json:"maker_id"`
	AgeCategory    flexInt   `json:"age_category"`
	WorkImage      string    `json:"work_image"`
}

// ApplyAjaxDynamic merges dynamic stats (dl_count, rating, price) from maniax-touch/product/info/ajax.
func ApplyAjaxDynamic(dst *Metadata, body []byte, workno string) error {
	var m map[string]rawAjaxInfo
	if err := json.Unmarshal(body, &m); err != nil {
		return fmt.Errorf("parse info/ajax: %w", err)
	}
	info, ok := m[strings.ToUpper(workno)]
	if !ok {
		return nil
	}
	if int(info.DLCount) > 0 {
		dst.DLCount = int(info.DLCount)
	}
	if float64(info.RateAverage2dp) > 0 {
		dst.Rating = float64(info.RateAverage2dp)
	}
	if int(info.RateCount) > 0 {
		dst.RateCount = int(info.RateCount)
	}
	if int(info.ReviewCount) > 0 {
		dst.ReviewCount = int(info.ReviewCount)
	}
	if dst.Price == 0 {
		if int(info.Price) > 0 {
			dst.Price = int(info.Price)
		} else if int(info.OfficialPrice) > 0 {
			dst.Price = int(info.OfficialPrice)
		}
	}
	if dst.Title == "" && info.WorkName != "" {
		dst.Title = strings.TrimSpace(info.WorkName)
	}
	if dst.CircleID == "" && info.MakerID != "" {
		dst.CircleID = strings.TrimSpace(info.MakerID)
	}
	if dst.AgeCategory == AgeUnknown && int(info.AgeCategory) > 0 {
		dst.AgeCategory = int(info.AgeCategory)
	}
	if dst.CoverURL == "" && info.WorkImage != "" {
		dst.CoverURL = normalizeCoverURL(info.WorkImage)
	}
	if dst.Source == "product.json" {
		dst.Source = "product.json+ajax"
	}
	return nil
}

var (
	ogTitleSuffixRe = regexp.MustCompile(`\s*\[[^\]]+\]\s*\|\s*DLsite.*$`)
	digitsOnlyRe    = regexp.MustCompile(`\d+`)
)

// ParseWorkHTML parses DLsite's work HTML page via goquery as the Tier-3 fallback (§3.2).
func ParseWorkHTML(body []byte, workno string) (*Metadata, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse work html: %w", err)
	}

	title := strings.TrimSpace(doc.Find("#work_name").First().Text())
	if title == "" {
		if og, ok := doc.Find(`meta[property="og:title"]`).Attr("content"); ok {
			title = strings.TrimSpace(ogTitleSuffixRe.ReplaceAllString(og, ""))
		}
	}
	if title == "" {
		return nil, fmt.Errorf("work html missing title for %s", workno)
	}

	m := &Metadata{
		Workno:     NormalizeWorkno(workno),
		Title:      title,
		Source:     "html",
		SourceType: "DLSITE",
		SourceURL:  fmt.Sprintf("https://www.dlsite.com/maniax/work/=/product_id/%s.html", NormalizeWorkno(workno)),
	}

	circleLink := doc.Find("span.maker_name a").First()
	if circleLink.Length() > 0 {
		m.CircleName = strings.TrimSpace(circleLink.Text())
		if href, ok := circleLink.Attr("href"); ok {
			m.CircleID = ExtractCircleID(href)
		}
	}

	if ogImg, ok := doc.Find(`meta[property="og:image"]`).Attr("content"); ok {
		m.CoverURL = normalizeCoverURL(ogImg)
	}

	doc.Find("#work_outline tr").Each(func(_ int, tr *goquery.Selection) {
		th := strings.TrimSpace(tr.Find("th").First().Text())
		td := tr.Find("td").First()
		tdText := strings.TrimSpace(td.Text())

		switch th {
		case "年齢指定", "年龄指定", "年齡指定", "Age", "연령 지정":
			switch {
			case strings.Contains(tdText, "18"):
				m.AgeCategory = AgeR18
			case strings.Contains(tdText, "15"):
				m.AgeCategory = AgeR15
			case strings.Contains(tdText, "全") || strings.Contains(strings.ToUpper(tdText), "ALL") || strings.Contains(strings.ToUpper(tdText), "GEN"):
				m.AgeCategory = AgeGeneral
			}
		case "販売日", "贩卖日", "販賣日", "Release date", "판매일":
			m.ReleaseDate = parseReleaseDate(tdText)
		case "シリーズ名", "系列名", "Series name", "시리즈명":
			a := td.Find("a").First()
			if a.Length() > 0 {
				m.SeriesName = strings.TrimSpace(a.Text())
				if href, ok := a.Attr("href"); ok {
					m.SeriesID = ExtractSeriesID(href)
				}
			}
		case "作品形式", "Product format", "작품 형식":
			m.WorkType = strings.TrimSpace(td.Text())
		case "声優", "声优", "聲優", "Voice Actor", "성우":
			td.Find("a").Each(func(_ int, a *goquery.Selection) {
				m.VAs = append(m.VAs, strings.TrimSpace(a.Text()))
			})
		case "シナリオ", "剧本", "劇本", "Scenario", "시나리오":
			td.Find("a").Each(func(_ int, a *goquery.Selection) {
				m.Scenarios = append(m.Scenarios, strings.TrimSpace(a.Text()))
			})
		case "イラスト", "插画", "插畫", "Illustration", "일러스트":
			td.Find("a").Each(func(_ int, a *goquery.Selection) {
				m.Illusts = append(m.Illusts, strings.TrimSpace(a.Text()))
			})
		case "音楽", "音乐", "音樂", "Music", "음악":
			td.Find("a").Each(func(_ int, a *goquery.Selection) {
				m.Musics = append(m.Musics, strings.TrimSpace(a.Text()))
			})
		case "ジャンル", "分类", "分類", "Genre", "장르":
			td.Find("a").Each(func(_ int, a *goquery.Selection) {
				m.Tags = append(m.Tags, strings.TrimSpace(a.Text()))
			})
		case "ファイル容量", "文件容量", "檔案容量", "File size":
			m.FileSize = parseHumanSize(tdText)
		}
	})

	m.VAs = dedupeStrings(m.VAs)
	m.Scenarios = dedupeStrings(m.Scenarios)
	m.Illusts = dedupeStrings(m.Illusts)
	m.Musics = dedupeStrings(m.Musics)
	m.Tags = dedupeStrings(m.Tags)
	return m, nil
}

var humanSizeRe = regexp.MustCompile(`(?i)([\d.]+)\s*(GB|MB|KB|B)`)

func parseHumanSize(s string) int64 {
	m := humanSizeRe.FindStringSubmatch(s)
	if len(m) != 3 {
		return 0
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0
	}
	switch strings.ToUpper(m[2]) {
	case "GB":
		return int64(v * 1024 * 1024 * 1024)
	case "MB":
		return int64(v * 1024 * 1024)
	case "KB":
		return int64(v * 1024)
	default:
		return int64(v)
	}
}
