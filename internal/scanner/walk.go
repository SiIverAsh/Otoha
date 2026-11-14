package scanner

import (
	"context"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/internal/dlsite"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// DiscoveredWork represents a work folder identified during streaming DFS traversal.
type DiscoveredWork struct {
	Workno        string
	FallbackTitle string
	AgeHint       int
	RelPath       string // slash-normalized path relative to storage root
	AbsPath       string
	LocalCover    string
	Tracks        []db.ScannedTrackInput
}

// SkippedPath records a directory or file skipped due to permissions, encoding, or symlink loop (§4.2).
type SkippedPath struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// ScanStats summarizes a completed (or cancelled) storage scan.
type ScanStats struct {
	WorksFound  int           `json:"works_found"`
	TracksFound int           `json:"tracks_found"`
	DirsVisited int           `json:"dirs_visited"`
	Skipped     []SkippedPath `json:"skipped"`
}

// Options configures the media library scanner (§4.1 & §4.2).
type Options struct {
	MaxDepth       int
	IgnorePatterns []string
	Logger         *log.Logger
	// ReadDirFunc allows deterministic testing of os.ErrPermission on Windows.
	ReadDirFunc func(dir string) ([]os.DirEntry, error)
}

func (o *Options) readDir(dir string) ([]os.DirEntry, error) {
	if o.ReadDirFunc != nil {
		return o.ReadDirFunc(dir)
	}
	return os.ReadDir(dir)
}

func (o *Options) shouldIgnore(name string) bool {
	for _, pat := range o.IgnorePatterns {
		pat = strings.TrimSpace(pat)
		if pat == "" {
			continue
		}
		if matched, err := filepath.Match(pat, name); err == nil && matched {
			return true
		}
		if strings.EqualFold(pat, name) {
			return true
		}
	}
	return false
}

// SanitizeDisplayString ensures invalid GBK/Shift-JIS byte remnants become valid UTF-8 (§4.2).
func SanitizeDisplayString(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	raw := []byte(s)
	if decoded, err := decodeWith(raw, japanese.ShiftJIS.NewDecoder()); err == nil && utf8.ValidString(decoded) {
		return decoded
	}
	if decoded, err := decodeWith(raw, simplifiedchinese.GB18030.NewDecoder()); err == nil && utf8.ValidString(decoded) {
		return decoded
	}
	return strings.ToValidUTF8(s, "\uFFFD")
}

func decodeWith(raw []byte, dec transform.Transformer) (string, error) {
	out, err := io.ReadAll(transform.NewReader(strings.NewReader(string(raw)), dec))
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// InferAgeHintFromFolder extracts an explicit age hint from folder prefix if present (e.g. GEN[...] or R18[...]).
func InferAgeHintFromFolder(folderName string) int {
	upper := strings.ToUpper(folderName)
	switch {
	case strings.Contains(upper, "R18") || strings.Contains(upper, "18禁"):
		return dlsite.AgeR18
	case strings.Contains(upper, "R15") || strings.Contains(upper, "R-15"):
		return dlsite.AgeR15
	case strings.HasPrefix(upper, "GEN[") || strings.Contains(folderName, "全年龄") || strings.Contains(folderName, "全年齢"):
		return dlsite.AgeGeneral
	default:
		return dlsite.AgeUnknown
	}
}

// ScanRoot performs a streaming depth-first scan of rootDir (§4.1, §4.2, §7).
// Each discovered work is emitted immediately to onWork without buffering the whole library in RAM.
func ScanRoot(ctx context.Context, rootDir string, opts Options, onWork func(DiscoveredWork) error) (*ScanStats, error) {
	if opts.MaxDepth <= 0 {
		opts.MaxDepth = 6
	}
	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, err
	}

	stats := &ScanStats{}
	activeRealAncestors := make(map[string]bool)
	if realRoot, err := filepath.EvalSymlinks(absRoot); err == nil {
		activeRealAncestors[normalizeCasePath(realRoot)] = true
	} else {
		activeRealAncestors[normalizeCasePath(absRoot)] = true
	}

	var walkDir func(currentAbs string, depth int) error
	walkDir = func(currentAbs string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		stats.DirsVisited++

		entries, err := opts.readDir(currentAbs)
		if err != nil {
			// §4.2: permission / IO error on directory MUST skip and log, never abort scan!
			reason := err.Error()
			stats.Skipped = append(stats.Skipped, SkippedPath{Path: currentAbs, Reason: reason})
			if opts.Logger != nil {
				opts.Logger.Printf("[scanner] skip unreadable directory %s: %v", currentAbs, err)
			}
			return nil
		}

		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			rawName := entry.Name()
			if opts.shouldIgnore(rawName) {
				continue
			}

			childAbs := filepath.Join(currentAbs, rawName)
			isDir := entry.IsDir()
			var realTarget string

			// Symlink handling & cycle detection (§4.2)
			if entry.Type()&fs.ModeSymlink != 0 {
				resolved, symErr := filepath.EvalSymlinks(childAbs)
				if symErr != nil {
					stats.Skipped = append(stats.Skipped, SkippedPath{Path: childAbs, Reason: "broken symlink: " + symErr.Error()})
					if opts.Logger != nil {
						opts.Logger.Printf("[scanner] skip broken symlink %s: %v", childAbs, symErr)
					}
					continue
				}
				st, statErr := os.Stat(resolved)
				if statErr != nil {
					stats.Skipped = append(stats.Skipped, SkippedPath{Path: childAbs, Reason: statErr.Error()})
					continue
				}
				isDir = st.IsDir()
				realTarget = normalizeCasePath(resolved)
				if isDir && activeRealAncestors[realTarget] {
					stats.Skipped = append(stats.Skipped, SkippedPath{Path: childAbs, Reason: "symlink cycle detected"})
					if opts.Logger != nil {
						opts.Logger.Printf("[scanner] skip symlink cycle %s -> %s", childAbs, resolved)
					}
					continue
				}
			} else if isDir {
				if resolved, symErr := filepath.EvalSymlinks(childAbs); symErr == nil {
					realTarget = normalizeCasePath(resolved)
				} else {
					realTarget = normalizeCasePath(childAbs)
				}
				if activeRealAncestors[realTarget] {
					stats.Skipped = append(stats.Skipped, SkippedPath{Path: childAbs, Reason: "directory cycle detected"})
					continue
				}
			}

			if !isDir {
				continue
			}

			cleanName := SanitizeDisplayString(rawName)
			if wno := dlsite.ExtractWorkno(cleanName); wno != "" {
				// §4.1: Work root identified -> collect all audio tracks inside, do NOT look for nested works.
				work, collectSkipped := collectWorkDirectory(ctx, absRoot, childAbs, wno, cleanName, opts, activeRealAncestors, realTarget)
				stats.Skipped = append(stats.Skipped, collectSkipped...)
				stats.WorksFound++
				stats.TracksFound += len(work.Tracks)
				if onWork != nil {
					if err := onWork(work); err != nil {
						return err
					}
				}
				continue
			}

			// Otherwise descend if within MaxDepth (§4.1)
			if depth+1 <= opts.MaxDepth {
				if realTarget != "" {
					activeRealAncestors[realTarget] = true
				}
				err := walkDir(childAbs, depth+1)
				if realTarget != "" {
					delete(activeRealAncestors, realTarget)
				}
				if err != nil {
					return err
				}
			}
		}
		return nil
	}

	// Also support rootDir itself being a single work directory
	rootBase := SanitizeDisplayString(filepath.Base(absRoot))
	if wno := dlsite.ExtractWorkno(rootBase); wno != "" {
		work, collectSkipped := collectWorkDirectory(ctx, filepath.Dir(absRoot), absRoot, wno, rootBase, opts, activeRealAncestors, "")
		stats.Skipped = append(stats.Skipped, collectSkipped...)
		stats.WorksFound++
		stats.TracksFound += len(work.Tracks)
		if onWork != nil {
			if err := onWork(work); err != nil {
				return stats, err
			}
		}
		return stats, nil
	}

	if err := walkDir(absRoot, 0); err != nil {
		return stats, err
	}
	return stats, nil
}

func collectWorkDirectory(
	ctx context.Context,
	storageRoot string,
	workAbs string,
	workno string,
	folderDisplayName string,
	opts Options,
	parentAncestors map[string]bool,
	workReal string,
) (DiscoveredWork, []SkippedPath) {
	relWork, err := filepath.Rel(storageRoot, workAbs)
	if err != nil {
		relWork = filepath.Base(workAbs)
	}
	relWorkSlash := filepath.ToSlash(relWork)

	var tracks []db.ScannedTrackInput
	var skipped []SkippedPath
	var bestCoverPath string
	var bestCoverScore int64 = -1 << 60

	ancestors := make(map[string]bool, len(parentAncestors)+2)
	for k, v := range parentAncestors {
		ancestors[k] = v
	}
	if workReal != "" {
		ancestors[workReal] = true
	}

	var walkSub func(dirAbs string, depth int)
	walkSub = func(dirAbs string, depth int) {
		if ctx.Err() != nil || depth > 12 {
			return
		}
		entries, err := opts.readDir(dirAbs)
		if err != nil {
			skipped = append(skipped, SkippedPath{Path: dirAbs, Reason: err.Error()})
			if opts.Logger != nil {
				opts.Logger.Printf("[scanner] skip unreadable subdir %s: %v", dirAbs, err)
			}
			return
		}

		for _, entry := range entries {
			if ctx.Err() != nil {
				return
			}
			name := entry.Name()
			if opts.shouldIgnore(name) {
				continue
			}
			full := filepath.Join(dirAbs, name)
			isDir := entry.IsDir()
			var subReal string
			var fileSize int64

			if entry.Type()&fs.ModeSymlink != 0 {
				resolved, symErr := filepath.EvalSymlinks(full)
				if symErr != nil {
					skipped = append(skipped, SkippedPath{Path: full, Reason: "broken symlink: " + symErr.Error()})
					continue
				}
				st, statErr := os.Stat(resolved)
				if statErr != nil {
					skipped = append(skipped, SkippedPath{Path: full, Reason: statErr.Error()})
					continue
				}
				isDir = st.IsDir()
				fileSize = st.Size()
				subReal = normalizeCasePath(resolved)
				if isDir && ancestors[subReal] {
					skipped = append(skipped, SkippedPath{Path: full, Reason: "symlink cycle inside work"})
					continue
				}
			} else if isDir {
				if resolved, symErr := filepath.EvalSymlinks(full); symErr == nil {
					subReal = normalizeCasePath(resolved)
				} else {
					subReal = normalizeCasePath(full)
				}
				if ancestors[subReal] {
					skipped = append(skipped, SkippedPath{Path: full, Reason: "directory cycle inside work"})
					continue
				}
			} else {
				info, infoErr := entry.Info()
				if infoErr != nil {
					skipped = append(skipped, SkippedPath{Path: full, Reason: infoErr.Error()})
					continue
				}
				fileSize = info.Size()
			}

			if isDir {
				if subReal != "" {
					ancestors[subReal] = true
				}
				walkSub(full, depth+1)
				if subReal != "" {
					delete(ancestors, subReal)
				}
				continue
			}

			relTrack, relErr := filepath.Rel(workAbs, full)
			if relErr != nil {
				continue
			}
			relTrackSlash := SanitizeDisplayString(filepath.ToSlash(relTrack))

			if IsAudioFile(name) {
				dur := ProbeAudioDuration(full)
				tracks = append(tracks, db.ScannedTrackInput{
					RelPath:  relTrackSlash,
					Size:     fileSize,
					Duration: dur,
				})
			} else if IsImageFile(name) && fileSize > 0 {
				sc := ScoreCoverCandidate(relTrackSlash, fileSize)
				if sc > bestCoverScore {
					bestCoverScore = sc
					bestCoverPath = full
				}
			}
		}
	}

	walkSub(workAbs, 0)

	sort.Slice(tracks, func(i, j int) bool {
		return tracks[i].RelPath < tracks[j].RelPath
	})
	for i := range tracks {
		tracks[i].Ord = i + 1
	}

	return DiscoveredWork{
		Workno:        workno,
		FallbackTitle: cleanFallbackTitle(folderDisplayName, workno),
		AgeHint:       InferAgeHintFromFolder(folderDisplayName),
		RelPath:       relWorkSlash,
		AbsPath:       workAbs,
		LocalCover:    bestCoverPath,
		Tracks:        tracks,
	}, skipped
}

func cleanFallbackTitle(folderName, workno string) string {
	s := strings.TrimSpace(folderName)
	if s == "" || strings.EqualFold(s, workno) {
		return workno
	}
	return s
}

func normalizeCasePath(p string) string {
	p = filepath.Clean(p)
	if filepath.Separator == '\\' {
		return strings.ToLower(p)
	}
	return p
}
