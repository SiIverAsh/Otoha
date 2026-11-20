package views

import (
	"bytes"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"

	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/internal/dlsite"
)

func TestDLsiteURLResolvesProductOrSearch(t *testing.T) {
	for _, testCase := range []struct {
		name string
		work db.Work
		want string
	}{
		{
			name: "reuse matching product link",
			work: db.Work{Workno: "rj123456", SourceURL: "https://dlsite.com/home/work/%3D/product_id/RJ123456.html?lang=jp"},
			want: "https://dlsite.com/home/work/%3D/product_id/RJ123456.html?lang=jp",
		},
		{
			name: "recover from nonmatching product",
			work: db.Work{Workno: "RJ123456", SourceURL: "https://www.dlsite.com/home/work/=/product_id/RJ654321.html", AgeCategory: dlsite.AgeGeneral},
			want: "https://www.dlsite.com/home/work/=/product_id/RJ123456.html",
		},
		{
			name: "use original work number for untrusted external link",
			work: db.Work{Workno: "A18-001", OriginalWorkno: "BJ12345678", SourceURL: "https://media.example/home/work/=/product_id/BJ12345678.html"},
			want: "https://www.dlsite.com/books/work/=/product_id/BJ12345678.html",
		},
		{
			name: "escape search path",
			work: db.Work{Title: "音葉 A/B"},
			want: "https://www.dlsite.com/home/fsr/=/keyword/%E9%9F%B3%E8%91%89%20A%2FB",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := DLsiteURL(testCase.work); got != testCase.want {
				t.Fatalf("DLsiteURL = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestDrawerHeaderShowsTranslationAndOriginal(t *testing.T) {
	const (
		translatedWorkno = "RJ123456"
		originalWorkno   = "RJ654321"
		originalTitle    = "原作 <em>雨</em> & 海"
		originalCircle   = "原作社团 & 海岸"
		translatedCircle = "译者社团"
	)
	for _, testCase := range []struct {
		name     string
		original *db.OriginalWork
	}{
		{
			name: "local original",
			original: &db.OriginalWork{
				Workno: originalWorkno, Title: originalTitle, CircleName: originalCircle,
			},
		},
		{name: "external original"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			detail := &db.WorkDetail{
				Work: db.Work{
					Workno: translatedWorkno, Title: "译版", CircleName: translatedCircle,
					OriginalWorkno: originalWorkno, AgeCategory: dlsite.AgeGeneral,
				},
				OriginalWork: testCase.original,
				LanguageEditionsJSON: `{
					"10": {"workno": "RJ123456", "label": "简体中文"},
					"2": {"workno": "RJ654321", "label": "日本語"}
				}`,
			}

			var output bytes.Buffer
			if err := drawerHeader(detail).Render(t.Context(), &output); err != nil {
				t.Fatal(err)
			}
			document, err := goquery.NewDocumentFromReader(&output)
			if err != nil {
				t.Fatal(err)
			}
			credits := make(map[string]*goquery.Selection)
			document.Find(".drawer-meta > div").Each(func(_ int, row *goquery.Selection) {
				credits[row.Find("dt").Text()] = row.Find("dd")
			})
			if row := credits["译版社团"]; row == nil || row.Text() != translatedCircle {
				t.Fatal("missing translated circle credit")
			}
			original := credits["原作"]
			if original == nil {
				t.Fatal("missing original work credit")
			}
			if testCase.original != nil {
				button := original.Find(`button[data-action="open-work"][data-workno="` + originalWorkno + `"]`)
				if !strings.Contains(button.Text(), originalTitle) || button.Find("em").Length() != 0 {
					t.Fatalf("local original button did not display the escaped title: %q", button.Text())
				}
				if row := credits["原作社团"]; row == nil || row.Text() != originalCircle {
					t.Fatal("missing original circle credit")
				}
			} else {
				link := original.Find("a")
				href, _ := link.Attr("href")
				if href != "https://www.dlsite.com/home/work/=/product_id/"+originalWorkno+".html" {
					t.Fatalf("original DLsite link = %q", href)
				}
				if credits["原作社团"] != nil {
					t.Fatal("found an original circle credit for a missing original")
				}
			}
			editions := document.Find(`.drawer-editions [data-action="jump-edition"]`)
			if editions.Length() != 2 {
				t.Fatalf("found %d numeric-key language editions, want 2", editions.Length())
			}
			if first := editions.First(); first.AttrOr("data-workno", "") != originalWorkno ||
				first.Find(".drawer-edition-language").Text() != "日本語" {
				t.Fatalf("first language edition = %q", first.Text())
			}
			if last := editions.Last(); last.AttrOr("data-workno", "") != translatedWorkno ||
				last.Find(".drawer-edition-language").Text() != "简体中文" {
				t.Fatalf("second language edition = %q", last.Text())
			}
		})
	}
}
