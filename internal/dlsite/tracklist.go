package dlsite

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const (
	trackListSelector  = "ul.c-productContent__trackList"
	trackItemSelector  = "li.c-productContent__trackListItem"
	trackTitleSelector = ".c-productContent__trackItemTitle"
	trackTimeSelector  = ".c-productContent__trackItemTime"
	trackTimeFormat    = `[0-9]{1,4}:(?:[0-5][0-9]:)?[0-5][0-9]`
)

var (
	trackDurationPattern = regexp.MustCompile(`^` + trackTimeFormat + `$`)
	trackTextRowPattern  = regexp.MustCompile(`^([0-9]{1,3})[ .．、_　\t]*(\D.*)$`)
	trackTextTimePattern = regexp.MustCompile(`(?:[(（]` + trackTimeFormat + `[)）]|` + trackTimeFormat + `)$`)
)

// TrackListEntry is a track name and whole-second duration published on DLsite.
type TrackListEntry struct {
	Title           string
	DurationSeconds float64
}

// FetchTrackList reads the public work page for a complete timed track list
// in its track table or a numbered description. It returns an empty list
// when the page lacks an unambiguous timed list.
func (c *Client) FetchTrackList(ctx context.Context, workno string) ([]TrackListEntry, error) {
	workno = NormalizeWorkno(workno)
	if workno == "" {
		return nil, errors.New("fetch DLsite track list requires a valid RJ number")
	}
	body, err := c.getWorkHTML(ctx, workno, "ja_jp")
	if err != nil {
		return nil, err
	}
	return parseTrackListHTML(body)
}

// IsTrackListAccessError reports whether DLsite restricted the requests.
func IsTrackListAccessError(err error) bool {
	var response *httpStatusError
	if !errors.As(err, &response) {
		return false
	}
	switch response.StatusCode {
	case http.StatusForbidden, http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return true
	default:
		return false
	}
}

func parseTrackListHTML(body []byte) ([]TrackListEntry, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse DLsite work page for track list: %w", err)
	}

	var tracks []TrackListEntry
	for _, list := range doc.Find(trackListSelector).EachIter() {
		entries := parseTrackList(list)
		if len(entries) < 2 {
			continue
		}
		if tracks != nil {
			return nil, nil
		}
		tracks = entries
	}
	if tracks != nil {
		return tracks, nil
	}
	for _, description := range doc.Find(".c-productContent__text").Nodes {
		entries := parseNumberedTrackList(description)
		if len(entries) < 2 {
			continue
		}
		if tracks != nil {
			return nil, nil
		}
		tracks = entries
	}
	return tracks, nil
}

func parseTrackList(list *goquery.Selection) []TrackListEntry {
	items := list.ChildrenFiltered(trackItemSelector)
	var entries []TrackListEntry
	for index, item := range items.EachIter() {
		title := strings.TrimSpace(item.Find(trackTitleSelector).Text())
		if isTrackListTotal(title) && index == items.Length()-1 {
			continue
		}
		duration := parseTrackDuration(strings.TrimSpace(item.Find(trackTimeSelector).Text()))
		if title == "" || duration == 0 {
			return nil
		}
		entries = append(entries, TrackListEntry{Title: title, DurationSeconds: duration})
	}
	return entries
}

func parseNumberedTrackList(description *html.Node) []TrackListEntry {
	var text strings.Builder
	writeTrackListText(&text, description)

	var entries []TrackListEntry
	firstNumber := 0
	for line := range strings.SplitSeq(text.String(), "\n") {
		row := trackTextRowPattern.FindStringSubmatch(strings.TrimSpace(line))
		if row == nil {
			continue
		}
		number, _ := strconv.Atoi(row[1])
		if len(entries) == 0 {
			if number > 1 {
				return nil
			}
			firstNumber = number
		}
		if number != firstNumber+len(entries) {
			return nil
		}
		entry, ok := parseNumberedTrack(row[2])
		if !ok {
			return nil
		}
		entries = append(entries, entry)
	}
	return entries
}

func writeTrackListText(text *strings.Builder, node *html.Node) {
	if node.Type == html.TextNode {
		text.WriteString(node.Data)
	} else if node.DataAtom == atom.Br {
		text.WriteByte('\n')
	}
	for child := range node.ChildNodes() {
		writeTrackListText(text, child)
	}
}

func parseNumberedTrack(label string) (TrackListEntry, bool) {
	match := trackTextTimePattern.FindStringIndex(label)
	if match == nil {
		return TrackListEntry{}, false
	}
	title := strings.TrimRight(strings.TrimSpace(label[:match[0]]), "・…")
	title = strings.TrimSpace(title)
	if title == "" || strings.TrimRight(title, "約~～〜-–－:：") != title {
		return TrackListEntry{}, false
	}
	seconds := parseTrackDuration(strings.Trim(label[match[0]:], "()（）"))
	return TrackListEntry{Title: title, DurationSeconds: seconds}, seconds > 0
}

func isTrackListTotal(title string) bool {
	switch strings.TrimRight(title, ":：") {
	case "総収録時間", "合計収録時間", "合計時間", "総再生時間", "合計":
		return true
	default:
		return false
	}
}

func parseTrackDuration(label string) float64 {
	if !trackDurationPattern.MatchString(label) {
		return 0
	}
	seconds := 0
	for component := range strings.SplitSeq(label, ":") {
		// The pattern limits components to at most four ASCII digits.
		value, _ := strconv.Atoi(component)
		seconds = seconds*60 + value
	}
	return float64(seconds)
}
