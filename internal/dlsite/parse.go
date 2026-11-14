package dlsite

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

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

// Metadata represents unified work metadata extracted from DLsite (§3.4).
type Metadata struct {
	Workno             string   `json:"workno"`
	Title              string   `json:"title"`
	CircleID           string   `json:"circle_id,omitempty"`
	CircleName         string   `json:"circle_name,omitempty"`
	ReleaseDate        string   `json:"release_date,omitempty"`
	AgeCategory        int      `json:"age_category"`
	WorkType           string   `json:"work_type,omitempty"`
	Price              int      `json:"price,omitempty"`
	Rating             float64  `json:"rating,omitempty"`
	RateCount          int      `json:"rate_count,omitempty"`
	ReviewCount        int      `json:"review_count,omitempty"`
	DLCount            int      `json:"dl_count,omitempty"`
	FileSize           int64    `json:"file_size,omitempty"`
	SeriesID           string   `json:"series_id,omitempty"`
	SeriesName         string   `json:"series_name,omitempty"`
	CoverURL           string   `json:"cover_url,omitempty"`
	VAs                []string `json:"vas,omitempty"`
	Scenarios          []string `json:"scenarios,omitempty"`
	Illusts            []string `json:"illusts,omitempty"`
	Musics             []string `json:"musics,omitempty"`
	Tags               []string `json:"tags,omitempty"`
	OriginalWorkno     string   `json:"original_workno,omitempty"`
	Locale             string   `json:"locale,omitempty"`
	Source             string   `json:"source,omitempty"`
	NotFound           bool     `json:"not_found,omitempty"`
	FetchedAt          int64    `json:"fetched_at"`
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
	Workno          string          `json:"workno"`
	WorkName        string          `json:"work_name"`
	MakerID         string          `json:"maker_id"`
	MakerName       string          `json:"maker_name"`
	RegistDate      string          `json:"regist_date"`
	AgeCategory     int             `json:"age_category"`
	WorkType        string          `json:"work_type"`
	Price           flexInt         `json:"price"`
	OfficialPrice   flexInt         `json:"official_price"`
	DLCount         flexInt         `json:"dl_count"`
	RateAverage2dp  flexFloat       `json:"rate_average_2dp"`
	RateCount       flexInt         `json:"rate_count"`
	ReviewCount     flexInt         `json:"review_count"`
	ContentsSize    flexInt64       `json:"contents_file_size"`
	SeriesID        string          `json:"series_id"`
	SeriesName      string          `json:"series_name"`
	ImageMain       rawImage        `json:"image_main"`
	Genres          []rawNamedItem  `json:"genres"`
	GenresReplaced  []rawNamedItem  `json:"genres_replaced"`
	Creaters        json.RawMessage `json:"creaters"` // PHP json_encode outputs [] when empty!
	TranslationInfo *rawTransInfo   `json:"translation_info"`
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
	wno := NormalizeWorkno(item.Workno)
	if wno == "" {
		wno = NormalizeWorkno(expectedWorkno)
	}
	releaseDate := strings.TrimSpace(item.RegistDate)
	if len(releaseDate) >= 10 {
		releaseDate = releaseDate[:10]
	}
	price := int(item.Price)
	if price == 0 {
		price = int(item.OfficialPrice)
	}
	genres := item.Genres
	if len(item.GenresReplaced) > 0 {
		genres = item.GenresReplaced
	}

	m := &Metadata{
		Workno:      wno,
		Title:       strings.TrimSpace(item.WorkName),
		CircleID:    strings.TrimSpace(item.MakerID),
		CircleName:  strings.TrimSpace(item.MakerName),
		ReleaseDate: releaseDate,
		AgeCategory: item.AgeCategory,
		WorkType:    strings.TrimSpace(item.WorkType),
		Price:       price,
		Rating:      float64(item.RateAverage2dp),
		RateCount:   int(item.RateCount),
		ReviewCount: int(item.ReviewCount),
		DLCount:     int(item.DLCount),
		FileSize:    int64(item.ContentsSize),
		SeriesID:    strings.TrimSpace(item.SeriesID),
		SeriesName:  strings.TrimSpace(item.SeriesName),
		CoverURL:    normalizeCoverURL(item.ImageMain.URL),
		Tags:        extractNames(genres),
		Source:      "product.json",
	}

	if item.TranslationInfo != nil {
		if orig := NormalizeWorkno(item.TranslationInfo.OriginalWorkno); orig != "" && orig != wno {
			m.OriginalWorkno = orig
		} else if parent := NormalizeWorkno(item.TranslationInfo.ParentWorkno); parent != "" && parent != wno {
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

// MergeFallbackMetadata fills missing fields in dst from fallback src (used for
// translation_info.original_workno and ja_jp fallback, §3.2 & §13).
func MergeFallbackMetadata(dst, src *Metadata) {
	if dst == nil || src == nil {
		return
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
		for _, v := range m {
			info = v
			ok = true
			break
		}
	}
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
		Workno: NormalizeWorkno(workno),
		Title:  title,
		Source: "html",
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
			nums := strings.Join(digitsOnlyRe.FindAllString(tdText, -1), "")
			if len(nums) >= 8 {
				m.ReleaseDate = fmt.Sprintf("%s-%s-%s", nums[0:4], nums[4:6], nums[6:8])
			}
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
