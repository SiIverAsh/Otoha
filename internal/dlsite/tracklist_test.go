package dlsite

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

const timedTrackList = `<ul class="c-productContent__trackList">
  <li class="c-productContent__trackListItem">
    <div class="c-productContent__trackItemTitle">冒頭 &amp; ご案内</div>
    <div class="c-productContent__trackItemTime">5:04</div>
  </li>
  <li class="c-productContent__trackListItem">
    <div class="c-productContent__trackItemTitle">本編</div>
    <div class="c-productContent__trackItemTime">1:02:03</div>
  </li>
  <li class="c-productContent__trackListItem">
    <div class="c-productContent__trackItemTitle">総収録時間</div>
    <div class="c-productContent__trackItemTime">1:07:07</div>
  </li>
</ul>`

type trackListTestTransport func(*http.Request) (*http.Response, error)

func (transport trackListTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestFetchTrackListReadsTimedRowsInPageOrder(t *testing.T) {
	transport := trackListTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/maniax/work/=/product_id/RJ01211108.html" {
			t.Errorf("request path = %s", r.URL.Path)
		}
		locale, err := r.Cookie("locale")
		if err != nil || locale.Value != "ja-jp" {
			t.Errorf("expected Japanese work description, cookie = %v, err = %v", locale, err)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(timedTrackList))}, nil
	})

	client, err := NewClient(ClientOptions{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.FetchTrackList(t.Context(), "rj01211108")
	want := []TrackListEntry{
		{Title: "冒頭 & ご案内", DurationSeconds: 304},
		{Title: "本編", DurationSeconds: 3723},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("track list = %+v, err = %v, want %+v", got, err, want)
	}
}

func TestParseTrackListReadsNumberedTextWithDescriptionLines(t *testing.T) {
	body := `<div class="c-productContent__text">
01ご案内・・・・・・05:04<br>
最初のトラックの説明<br>
02. <b>お話</b>（前半）(1:02:03)<br>
03. エンディング0:08<br>
合計　約1時間
</div>`
	want := []TrackListEntry{
		{Title: "ご案内", DurationSeconds: 304},
		{Title: "お話（前半）", DurationSeconds: 3723},
		{Title: "エンディング", DurationSeconds: 8},
	}
	got, err := parseTrackListHTML([]byte(body))
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("track list = %+v, err = %v, want %+v", got, err, want)
	}
}

func TestParseTrackListRejectsIncompleteOrAmbiguousData(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "multiple timed versions",
			body: timedTrackList + timedTrackList,
		},
		{
			name: "description only",
			body: `<p>冒頭は5:04。本編は1:02:03。</p>`,
		},
		{
			name: "missing time in numbered text",
			body: `<div class="c-productContent__text">1. Track 1:04<br>2. No timing<br>3. Ending 2:09</div>`,
		},
		{
			name: "approximate or ranged times in numbered text",
			body: `<div class="c-productContent__text">1. Intro 1:04<br>2. Main 約2:08<br>3. Ending 2:09～2:50</div>`,
		},
		{
			name: "competing lists in numbered text",
			body: `<div class="c-productContent__text">0. Intro 1:04<br>1. Main 2:08<br>Bonus tracks<br>1. Extra 1:03<br>2. Closing 3:21</div>`,
		},
		{
			name: "missing time",
			body: `<ul class="c-productContent__trackList">
  <li class="c-productContent__trackListItem">
    <div class="c-productContent__trackItemTitle">Track 1</div>
    <div class="c-productContent__trackItemTime">3:14</div>
  </li>
  <li class="c-productContent__trackListItem">
    <div class="c-productContent__trackItemTitle">Track 2</div>
  </li>
  <li class="c-productContent__trackListItem">
    <div class="c-productContent__trackItemTitle">Track 3</div>
    <div class="c-productContent__trackItemTime">7:06</div>
  </li>
</ul>`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseTrackListHTML([]byte(test.body))
			if err != nil || len(got) != 0 {
				t.Fatalf("track list = %+v, err = %v, want no usable list", got, err)
			}
		})
	}
}

func TestParseTrackDurationRequiresWholeSecondTimes(t *testing.T) {
	for _, label := range []string{"", "約5:04", "5", "0:00", "3:67", "1:70:00", "1:2", "-1:02", "+1:02", "1:04.5"} {
		t.Run(label, func(t *testing.T) {
			if seconds := parseTrackDuration(label); seconds != 0 {
				t.Fatalf("parseTrackDuration(%q) = %v, want 0", label, seconds)
			}
		})
	}
}

func TestTrackListAccessErrorRecognizesUpstreamLimits(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		err := fmt.Errorf("track list fetch: %w", &httpStatusError{StatusCode: status})
		if !IsTrackListAccessError(err) {
			t.Errorf("expected HTTP %d to stop the batch", status)
		}
	}
	if IsTrackListAccessError(ErrWorkNotFound) || IsTrackListAccessError(errors.New("unrelated error")) {
		t.Fatal("non-restriction errors must not stop the batch")
	}
}
