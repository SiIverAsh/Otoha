package subtitle

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// SupportedExtensions in priority order (§8).
var SupportedExtensions = []string{".lrc", ".vtt", ".srt", ".ass"}

// Cue represents a single timestamped subtitle line.
type Cue struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

// Result contains the UTF-8 normalized subtitle content and parsed cues (§8).
type Result struct {
	Format   string `json:"format"`   // "lrc" | "vtt" | "srt" | "ass"
	Encoding string `json:"encoding"` // detected source encoding
	FileName string `json:"file_name"`
	Content  string `json:"content"`
	Cues     []Cue  `json:"cues"`
}

// FindMatchingSubtitle locates a subtitle file in the same directory with the same base stem
// (e.g. `01 本編.wav` <-> `01 本編.lrc` or `01 本編.wav.lrc`, §8).
func FindMatchingSubtitle(audioAbsPath string) string {
	dir := filepath.Dir(audioAbsPath)
	base := filepath.Base(audioAbsPath)
	stem := strings.TrimSuffix(base, filepath.Ext(base))

	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, wantExt := range SupportedExtensions {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			ext := strings.ToLower(filepath.Ext(name))
			if ext != wantExt {
				continue
			}
			nameStem := strings.TrimSuffix(name, filepath.Ext(name))
			if strings.EqualFold(nameStem, stem) || strings.EqualFold(nameStem, base) {
				return filepath.Join(dir, name)
			}
		}
	}
	return ""
}

// LoadAndParse reads a subtitle file, detects encoding (UTF-8 / BOM / Shift-JIS / GBK),
// normalizes to UTF-8, and parses structured timestamped cues (§8).
func LoadAndParse(subAbsPath string) (*Result, error) {
	raw, err := os.ReadFile(subAbsPath)
	if err != nil {
		return nil, err
	}
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(subAbsPath)), ".")
	return ParseBytes(raw, filepath.Base(subAbsPath), ext), nil
}

// ParseBytes decodes raw subtitle bytes into UTF-8 and parses cues according to format.
func ParseBytes(raw []byte, fileName, format string) *Result {
	text, encName := DetectAndDecodeUTF8(raw)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	format = strings.ToLower(strings.TrimPrefix(format, "."))
	var cues []Cue
	switch format {
	case "lrc":
		cues = parseLRC(text)
	case "vtt":
		cues = parseSRTOrVTT(text, true)
	case "srt":
		cues = parseSRTOrVTT(text, false)
	case "ass", "ssa":
		format = "ass"
		cues = parseASS(text)
	}
	if cues == nil {
		cues = []Cue{}
	}
	return &Result{
		Format:   format,
		Encoding: encName,
		FileName: fileName,
		Content:  text,
		Cues:     cues,
	}
}

// DetectAndDecodeUTF8 detects UTF-8 BOM, UTF-16 LE/BE BOM, plain UTF-8, Shift-JIS, or GBK/GB18030 (§8).
func DetectAndDecodeUTF8(raw []byte) (decoded string, encodingName string) {
	if len(raw) == 0 {
		return "", "utf-8"
	}
	// UTF-8 BOM
	if bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}) {
		body := raw[3:]
		if utf8.Valid(body) {
			return string(body), "utf-8-bom"
		}
	}
	// UTF-16 LE BOM
	if bytes.HasPrefix(raw, []byte{0xFF, 0xFE}) {
		if s, err := transformBytes(raw[2:], unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM).NewDecoder()); err == nil {
			return s, "utf-16le"
		}
	}
	// UTF-16 BE BOM
	if bytes.HasPrefix(raw, []byte{0xFE, 0xFF}) {
		if s, err := transformBytes(raw[2:], unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM).NewDecoder()); err == nil {
			return s, "utf-16be"
		}
	}
	// Valid UTF-8
	if utf8.Valid(raw) {
		return string(raw), "utf-8"
	}

	// Compare Shift-JIS vs GB18030/GBK
	sjisStr, sjisErr := transformBytes(raw, japanese.ShiftJIS.NewDecoder())
	gbkStr, gbkErr := transformBytes(raw, simplifiedchinese.GB18030.NewDecoder())

	sjisScore := scoreDecodedText(sjisStr, sjisErr, true)
	gbkScore := scoreDecodedText(gbkStr, gbkErr, false)

	if sjisScore >= gbkScore && sjisStr != "" {
		return sjisStr, "shift_jis"
	}
	if gbkStr != "" {
		return gbkStr, "gbk"
	}
	return strings.ToValidUTF8(string(raw), "\uFFFD"), "fallback-utf8"
}

func transformBytes(raw []byte, t transform.Transformer) (string, error) {
	out, err := io.ReadAll(transform.NewReader(bytes.NewReader(raw), t))
	return string(out), err
}

func scoreDecodedText(s string, err error, favorKana bool) int {
	if s == "" || !utf8.ValidString(s) {
		return -100000
	}
	score := 0
	if err != nil {
		score -= 500
	}
	for _, r := range s {
		switch {
		case r == utf8.RuneError:
			score -= 200
		case r >= 0x3040 && r <= 0x30FF: // Hiragana + Katakana
			if favorKana {
				score += 6
			} else {
				score += 1
			}
		case r >= 0x4E00 && r <= 0x9FFF: // CJK Unified Ideographs
			score += 3
		case r >= 0x20 && r <= 0x7E || r == '\n' || r == '\r' || r == '\t':
			score += 1
		case r >= 0xFF61 && r <= 0xFF9F: // Half-width katakana (common GBK false positive on SJIS or vice versa)
			score -= 4
		case r < 0x20:
			score -= 20
		}
	}
	return score
}

var (
	lrcTagRe    = regexp.MustCompile(`\[(\d{1,3}):(\d{2})(?:[.:](\d{1,3}))?\]`)
	lrcOffsetRe = regexp.MustCompile(`(?i)^\[offset:\s*([+-]?\d+)\s*\]$`)
)

func parseLRC(text string) []Cue {
	var cues []Cue
	var offsetSec float64
	lines := strings.Split(text, "\n")

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if m := lrcOffsetRe.FindStringSubmatch(trimmed); len(m) == 2 {
			if ms, err := strconv.Atoi(m[1]); err == nil {
				offsetSec = float64(ms) / 1000.0
			}
			continue
		}
		matches := lrcTagRe.FindAllStringSubmatchIndex(line, -1)
		if len(matches) == 0 {
			continue
		}
		lyric := strings.TrimSpace(line[matches[len(matches)-1][1]:])
		for _, loc := range matches {
			minStr := line[loc[2]:loc[3]]
			secStr := line[loc[4]:loc[5]]
			mins, _ := strconv.Atoi(minStr)
			secs, _ := strconv.Atoi(secStr)
			frac := 0.0
			if loc[6] >= 0 && loc[7] > loc[6] {
				fStr := line[loc[6]:loc[7]]
				fVal, _ := strconv.Atoi(fStr)
				switch len(fStr) {
				case 1:
					frac = float64(fVal) / 10.0
				case 2:
					frac = float64(fVal) / 100.0
				default:
					frac = float64(fVal) / 1000.0
				}
			}
			ts := float64(mins*60+secs) + frac + offsetSec
			if ts < 0 {
				ts = 0
			}
			cues = append(cues, Cue{Start: ts, Text: lyric})
		}
	}

	sort.SliceStable(cues, func(i, j int) bool {
		return cues[i].Start < cues[j].Start
	})
	for i := range cues {
		if i+1 < len(cues) {
			cues[i].End = cues[i+1].Start
		} else {
			cues[i].End = cues[i].Start + 5.0
		}
	}
	return cues
}

var srtTimeLineRe = regexp.MustCompile(`^\s*((?:\d{1,2}:)?\d{2}:\d{2}[,.]\d{1,3})\s*-->\s*((?:\d{1,2}:)?\d{2}:\d{2}[,.]\d{1,3})`)

func parseSRTOrVTT(text string, isVTT bool) []Cue {
	var cues []Cue
	lines := strings.Split(text, "\n")
	i := 0
	if isVTT && i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[0]), "WEBVTT") {
		i++
	}
	for i < len(lines) {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			i++
			continue
		}
		m := srtTimeLineRe.FindStringSubmatch(line)
		if len(m) != 3 {
			i++
			continue
		}
		start := parseClockTimestamp(m[1])
		end := parseClockTimestamp(m[2])
		i++
		var textLines []string
		for i < len(lines) && strings.TrimSpace(lines[i]) != "" {
			textLines = append(textLines, strings.TrimSpace(lines[i]))
			i++
		}
		if len(textLines) > 0 {
			cues = append(cues, Cue{
				Start: start,
				End:   end,
				Text:  strings.Join(textLines, "\n"),
			})
		}
	}
	return cues
}

func parseClockTimestamp(s string) float64 {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", ".")
	parts := strings.Split(s, ":")
	if len(parts) == 3 {
		h, _ := strconv.Atoi(parts[0])
		m, _ := strconv.Atoi(parts[1])
		sec, _ := strconv.ParseFloat(parts[2], 64)
		return float64(h*3600+m*60) + sec
	}
	if len(parts) == 2 {
		m, _ := strconv.Atoi(parts[0])
		sec, _ := strconv.ParseFloat(parts[1], 64)
		return float64(m*60) + sec
	}
	return 0
}

var assOverrideTagRe = regexp.MustCompile(`\{[^}]*\}`)

func parseASS(text string) []Cue {
	var cues []Cue
	inEvents := false
	textFieldIdx := 9
	startIdx := 1
	endIdx := 2

	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.EqualFold(line, "[Events]") {
			inEvents = true
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") && !strings.EqualFold(line, "[Events]") {
			inEvents = false
			continue
		}
		if !inEvents {
			continue
		}
		if strings.HasPrefix(strings.ToLower(line), "format:") {
			cols := strings.Split(strings.TrimSpace(line[len("format:"):]), ",")
			for idx, col := range cols {
				switch strings.ToLower(strings.TrimSpace(col)) {
				case "start":
					startIdx = idx
				case "end":
					endIdx = idx
				case "text":
					textFieldIdx = idx
				}
			}
			continue
		}
		if !strings.HasPrefix(strings.ToLower(line), "dialogue:") {
			continue
		}
		payload := strings.TrimSpace(line[len("dialogue:"):])
		fields := strings.SplitN(payload, ",", textFieldIdx+1)
		if len(fields) <= textFieldIdx || startIdx >= len(fields) || endIdx >= len(fields) {
			continue
		}
		start := parseClockTimestamp(fields[startIdx])
		end := parseClockTimestamp(fields[endIdx])
		clean := assOverrideTagRe.ReplaceAllString(fields[textFieldIdx], "")
		clean = strings.ReplaceAll(clean, `\N`, "\n")
		clean = strings.ReplaceAll(clean, `\n`, "\n")
		clean = strings.ReplaceAll(clean, `\h`, " ")
		clean = strings.TrimSpace(clean)
		if clean != "" {
			cues = append(cues, Cue{Start: start, End: end, Text: clean})
		}
	}

	sort.SliceStable(cues, func(i, j int) bool {
		return cues[i].Start < cues[j].Start
	})
	return cues
}
