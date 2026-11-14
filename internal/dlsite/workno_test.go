package dlsite

import (
	"bufio"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestWorknoCorpus(t *testing.T) {
	corpusPath := filepath.Join("..", "..", "testdata", "worknos.txt")
	f, err := os.Open(corpusPath)
	if err != nil {
		t.Fatalf("open corpus: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	lineNo := 0
	tested := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "=>")
		if len(parts) != 2 {
			t.Fatalf("line %d: malformed entry %q", lineNo, line)
		}
		input := strings.TrimSpace(parts[0])
		want := strings.TrimSpace(parts[1])
		if want == "NONE" {
			want = ""
		}
		got := ExtractWorknoFromPath(input)
		if got != want {
			t.Errorf("line %d: ExtractWorknoFromPath(%q) = %q, want %q", lineNo, input, got, want)
		}
		tested++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan corpus: %v", err)
	}
	if tested < 25 {
		t.Fatalf("expected >= 25 corpus cases, got %d", tested)
	}
}

func TestAdjacentWorknosNotSwallowed(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"RJ123456+RJ01234567", []string{"RJ123456", "RJ01234567"}},
		{"[RJ123456][BJ01234567][VJ654321]", []string{"RJ123456", "BJ01234567", "VJ654321"}},
		{`D:\VOICES\RJ111111\RJ01234567`, []string{"RJ111111", "RJ01234567"}},
	}
	for _, tc := range cases {
		got := ExtractAllFromPath(tc.in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ExtractAllFromPath(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestCircleAndSeriesExtraction(t *testing.T) {
	s := "[240510][RG54321][SRI0000012345][RJ01199887] 作品タイトル"
	if got := ExtractCircleID(s); got != "RG54321" {
		t.Errorf("ExtractCircleID = %q, want RG54321", got)
	}
	if got := ExtractCircleID("RG01005704"); got != "RG01005704" {
		t.Errorf("ExtractCircleID 8-digit = %q, want RG01005704", got)
	}
	if got := ExtractSeriesID(s); got != "SRI0000012345" {
		t.Errorf("ExtractSeriesID = %q, want SRI0000012345", got)
	}
	if got := ExtractCircleID("ARG54321 RG123456"); got != "" {
		t.Errorf("ExtractCircleID invalid = %q, want empty", got)
	}
	if got := NumericIDFromWorkno("RJ01683351"); got != 1683351 {
		t.Errorf("NumericIDFromWorkno = %d, want 1683351", got)
	}
}

func TestWorknoFromKikoeruID(t *testing.T) {
	cases := []struct {
		id   int
		want string
	}{
		{12345, "RJ012345"},
		{334212, "RJ334212"},
		{999999, "RJ999999"},
		{1000000, "RJ01000000"},
		{1234567, "RJ01234567"},
		{12345678, "RJ12345678"},
	}
	for _, tc := range cases {
		if got := WorknoFromKikoeruID(tc.id); got != tc.want {
			t.Errorf("WorknoFromKikoeruID(%d) = %q, want %q", tc.id, got, tc.want)
		}
	}
}
