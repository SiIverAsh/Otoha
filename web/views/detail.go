package views

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/internal/dlsite"
)

const dlsiteOrigin = "https://www.dlsite.com"

var dlsiteProductPath = regexp.MustCompile(`(?i)^/[a-z][a-z-]*/work/=/product_id/([RBV]J(?:\d{6}|\d{8}))(?:\.html)?/?$`)

// DLsiteURL resolves an existing DLsite link or builds a product or search URL.
func DLsiteURL(work db.Work) string {
	workno := dlsite.NormalizeWorkno(work.Workno)
	if workno == "" {
		workno = dlsite.NormalizeWorkno(work.OriginalWorkno)
	}
	source, err := url.Parse(work.SourceURL)
	if err == nil && source.Scheme == "https" && source.User == nil &&
		(strings.EqualFold(source.Host, "www.dlsite.com") || strings.EqualFold(source.Host, "dlsite.com")) {
		matches := dlsiteProductPath.FindStringSubmatch(source.Path)
		if len(matches) > 0 && (workno == "" || strings.EqualFold(matches[1], workno)) {
			return source.String()
		}
	}
	if workno != "" {
		section := "maniax"
		switch workno[:2] {
		case "RJ":
			if work.AgeCategory == dlsite.AgeGeneral {
				section = "home"
			}
		case "BJ":
			section = "books"
		case "VJ":
			section = "pro"
			if work.AgeCategory == dlsite.AgeGeneral {
				section = "soft"
			}
		}
		return dlsiteOrigin + "/" + section + "/work/=/product_id/" + workno + ".html"
	}

	keyword := strings.TrimSpace(work.Title)
	if keyword == "" {
		keyword = strings.TrimSpace(work.Workno)
	}
	if keyword == "" {
		keyword = strings.TrimSpace(work.OriginalWorkno)
	}
	if keyword == "" {
		return dlsiteOrigin
	}
	return dlsiteOrigin + "/home/fsr/=/keyword/" + url.PathEscape(keyword)
}

func dlsiteLinkLabel(work db.Work) string {
	if work.Title != "" {
		return "在 DLsite 查看 " + work.Title
	}
	if work.Workno != "" {
		return "在 DLsite 查看 " + work.Workno
	}
	return "在 DLsite 查看 作品"
}

// FormatDuration formats the elapsed or total number of seconds for display.
func FormatDuration(seconds float64) string {
	if seconds <= 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return "--:--"
	}
	total := int(seconds)
	hours, minutes, remaining := total/3600, total/60%60, total%60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, remaining)
	}
	return fmt.Sprintf("%02d:%02d", minutes, remaining)
}

type drawerEdition struct {
	Lang   string `json:"lang"`
	Label  string `json:"label"`
	Workno string `json:"workno"`
}

func (edition drawerEdition) displayLanguage() string {
	if edition.Label != "" {
		return edition.Label
	}
	return edition.Lang
}

func drawerEditions(raw string) []drawerEdition {
	raw = strings.TrimSpace(raw)
	var editions []drawerEdition
	if strings.HasPrefix(raw, "{") {
		// Older entries may key language editions by numeric IDs or labels.
		var byLanguage map[string]drawerEdition
		if err := json.Unmarshal([]byte(raw), &byLanguage); err != nil {
			return nil
		}
		for _, key := range slices.SortedFunc(maps.Keys(byLanguage), compareEditionKeys) {
			editions = append(editions, byLanguage[key])
		}
	} else {
		editions = parseMetadataList[drawerEdition](raw)
	}

	valid := editions[:0]
	for _, edition := range editions {
		edition.Workno = dlsite.NormalizeWorkno(edition.Workno)
		if edition.Workno != "" {
			valid = append(valid, edition)
		}
	}
	return valid
}

func compareEditionKeys(left, right string) int {
	leftIndex, leftErr := strconv.ParseUint(left, 10, 32)
	rightIndex, rightErr := strconv.ParseUint(right, 10, 32)
	switch {
	case leftErr == nil && rightErr == nil && leftIndex != rightIndex:
		return cmp.Compare(leftIndex, rightIndex)
	case leftErr == nil && rightErr != nil:
		return -1
	case leftErr != nil && rightErr == nil:
		return 1
	default:
		return strings.Compare(left, right)
	}
}

func originalWorkno(detail *db.WorkDetail) string {
	original := dlsite.NormalizeWorkno(detail.OriginalWorkno)
	if original == detail.Workno {
		return ""
	}
	return original
}

type drawerAttachment struct {
	Kind        string `json:"type"`
	RelPath     string `json:"rel_path"`
	DownloadURL string `json:"download_url"`
}

func (attachment drawerAttachment) publicURL() string {
	target, err := url.Parse(attachment.DownloadURL)
	if err != nil || target.Host == "" || target.User != nil {
		return ""
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return ""
	}
	return target.String()
}

func parseMetadataList[T interface {
	drawerEdition | drawerAttachment | drawerRank
}](raw string) []T {
	if raw == "" {
		return nil
	}
	var items []T
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil
	}
	return items
}

type drawerRank struct {
	Rank int `json:"rank"`
}

type workStat struct {
	label string
	value string
}

func workStats(work *db.WorkDetail) []workStat {
	rating := "—"
	if work.Rating != 0 {
		rating = fmt.Sprintf("%.2f (%d)", work.Rating, work.RateCount)
	}
	stats := []workStat{
		{label: "评分", value: rating},
		{label: "销量", value: fmt.Sprint(work.DLCount)},
		{label: "价格", value: fmt.Sprintf("¥%d", work.Price)},
	}
	if work.Duration > 0 {
		stats = append(stats, workStat{label: "时长", value: FormatDuration(float64(work.Duration))})
	}
	const unavailableRank = 9999
	bestRank := unavailableRank
	for _, rank := range parseMetadataList[drawerRank](work.RankJSON) {
		if rank.Rank > 0 {
			bestRank = min(bestRank, rank.Rank)
		}
	}
	if bestRank < unavailableRank {
		stats = append(stats, workStat{label: "最高榜", value: fmt.Sprintf("#%d", bestRank)})
	}
	if work.SeriesName != "" {
		stats = append(stats, workStat{label: "系列", value: work.SeriesName})
	}
	return stats
}

func workCredits(work *db.WorkDetail) string {
	displayNames := func(names []string) string {
		if len(names) == 0 {
			return "-"
		}
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("剧本: %s | 插画: %s | 音乐: %s",
		displayNames(work.Scenarios), displayNames(work.Illusts), displayNames(work.Musics))
}

func drawerTagNames(tags []db.Tag, source string) string {
	var names []string
	for _, tag := range tags {
		if (tag.Source == "user") == (source == "user") {
			names = append(names, tag.Name)
		}
	}
	return strings.Join(names, ", ")
}

func hlsFolder(children []*db.TrackTreeNode) bool {
	if len(children) == 0 {
		return false
	}
	for _, child := range children {
		if child.Track == nil {
			return false
		}
		streamURL, _, _ := strings.Cut(child.Track.StreamURL, "?")
		streamURL, _, _ = strings.Cut(streamURL, "#")
		if !strings.HasSuffix(strings.ToLower(streamURL), ".m3u8") {
			return false
		}
	}
	return true
}

func trackDuration(track *db.Track) float64 {
	if track.Duration != nil {
		return *track.Duration
	}
	return 0
}

func trackProgressWidth(track *db.Track) string {
	duration := trackDuration(track)
	if duration <= 0 {
		return "0%"
	}
	percent := min(100, math.Round(track.PlayPosition/duration*100))
	return fmt.Sprintf("%.0f%%", percent)
}
