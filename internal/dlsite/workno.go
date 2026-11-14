package dlsite

import (
	"fmt"
	"regexp"
	"strings"
)

// §3.1: 8-digit branch MUST come before 6-digit branch.
// Go uses RE2 (no negative lookahead), so (?:\D|$) enforces the right digit boundary.
var (
	worknoRe = regexp.MustCompile(`[RBV]J(\d{8}|\d{6})(?:\D|$)`)
	circleRe = regexp.MustCompile(`RG(\d{5})(?:\D|$)`)
	seriesRe = regexp.MustCompile(`SRI(\d{10})(?:\D|$)`)
)

func isASCIIAlpha(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

// ExtractAllFromSegment extracts all valid worknos ([RBV]J + 6 or 8 digits) from a single
// path segment without allowing the trailing (?:\D|$) delimiter to swallow an adjacent match
// and without matching embedded ASCII words such as "PROJ123456".
func ExtractAllFromSegment(segment string) []string {
	upper := strings.ToUpper(segment)
	var out []string
	pos := 0
	for pos < len(upper) {
		loc := worknoRe.FindStringSubmatchIndex(upper[pos:])
		if loc == nil {
			break
		}
		matchStart := pos + loc[0]
		digitsEnd := pos + loc[3] // end of (\d{8}|\d{6}), before trailing \D

		// Reject if preceded by an ASCII letter (e.g., PROJ123456 or ARJ01234567).
		if matchStart > 0 && isASCIIAlpha(upper[matchStart-1]) {
			pos = matchStart + 1
			continue
		}

		out = append(out, upper[matchStart:digitsEnd])
		pos = digitsEnd
	}
	return out
}

// ExtractWorkno extracts the first valid workno from a single directory/file name segment.
func ExtractWorkno(segment string) string {
	matches := ExtractAllFromSegment(segment)
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}

// ExtractWorknoFromPath splits a Windows or POSIX path by '/' and '\' and extracts the
// first valid workno segment-by-segment (§3.1).
func ExtractWorknoFromPath(path string) string {
	all := ExtractAllFromPath(path)
	if len(all) == 0 {
		return ""
	}
	return all[0]
}

// ExtractAllFromPath extracts all valid worknos across all path segments in order.
func ExtractAllFromPath(path string) []string {
	segments := strings.FieldsFunc(path, func(r rune) bool {
		return r == '/' || r == '\\'
	})
	var out []string
	for _, seg := range segments {
		out = append(out, ExtractAllFromSegment(seg)...)
	}
	return out
}

// IsValidWorkno reports whether s is an exact canonical workno (e.g. RJ123456 or RJ01234567).
func IsValidWorkno(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) != 8 && len(s) != 10 {
		return false
	}
	m := ExtractWorkno(s)
	return m != "" && strings.EqualFold(m, s)
}

// NormalizeWorkno validates s and returns the uppercase workno, or "" if invalid.
func NormalizeWorkno(s string) string {
	s = strings.TrimSpace(s)
	if !IsValidWorkno(s) {
		return ""
	}
	return strings.ToUpper(s)
}

// ExtractCircleID extracts an RG\d{5} circle identifier from s.
func ExtractCircleID(s string) string {
	upper := strings.ToUpper(s)
	pos := 0
	for pos < len(upper) {
		loc := circleRe.FindStringSubmatchIndex(upper[pos:])
		if loc == nil {
			return ""
		}
		matchStart := pos + loc[0]
		digitsEnd := pos + loc[3]
		if matchStart > 0 && isASCIIAlpha(upper[matchStart-1]) {
			pos = matchStart + 1
			continue
		}
		return upper[matchStart:digitsEnd]
	}
	return ""
}

// ExtractSeriesID extracts an SRI\d{10} series identifier from s.
func ExtractSeriesID(s string) string {
	upper := strings.ToUpper(s)
	pos := 0
	for pos < len(upper) {
		loc := seriesRe.FindStringSubmatchIndex(upper[pos:])
		if loc == nil {
			return ""
		}
		matchStart := pos + loc[0]
		digitsEnd := pos + loc[3]
		if matchStart > 0 && isASCIIAlpha(upper[matchStart-1]) {
			pos = matchStart + 1
			continue
		}
		return upper[matchStart:digitsEnd]
	}
	return ""
}

// WorknoFromKikoeruID converts a numeric kikoeru t_work.id to an RJ workno (§14.2).
// 6-digit codes top out at RJ999999; 8-digit codes start at RJ01000000 (id = 1000000).
func WorknoFromKikoeruID(id int) string {
	if id < 1000000 {
		return fmt.Sprintf("RJ%06d", id)
	}
	return fmt.Sprintf("RJ%08d", id)
}
