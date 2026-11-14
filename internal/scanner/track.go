package scanner

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dhowden/tag"
	"github.com/mewkiz/flac"
)

var audioExtensions = map[string]bool{
	".wav":  true,
	".flac": true,
	".mp3":  true,
	".m4a":  true,
	".aac":  true,
	".ogg":  true,
	".opus": true,
}

var imageExtensions = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".webp": true,
}

// IsAudioFile reports whether filename has a supported audio extension (§4.3).
func IsAudioFile(name string) bool {
	return audioExtensions[strings.ToLower(filepath.Ext(name))]
}

// IsImageFile reports whether filename has a supported cover image extension.
func IsImageFile(name string) bool {
	return imageExtensions[strings.ToLower(filepath.Ext(name))]
}

// ProbeAudioDuration extracts track duration in seconds using pure-Go container/tag parsers
// without CGO or ffmpeg (§4.3). Returns nil if duration cannot be determined from headers.
func ProbeAudioDuration(path string) *float64 {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil || st.Size() <= 0 {
		return nil
	}

	// Invoke dhowden/tag first to validate tag header if present (§2 & §4.3).
	_, _ = tag.ReadFrom(f)
	_, _ = f.Seek(0, io.SeekStart)

	ext := strings.ToLower(filepath.Ext(path))
	var sec float64
	switch ext {
	case ".flac":
		sec = parseFLACDuration(f)
	case ".wav":
		sec = parseWAVDuration(f, st.Size())
	case ".mp3":
		sec = parseMP3Duration(f, st.Size())
	case ".m4a":
		sec = parseMP4Duration(f, st.Size())
	case ".ogg", ".opus":
		sec = parseOggDuration(f, st.Size())
	}

	if sec > 0 && sec < 86400*7 {
		return &sec
	}
	return nil
}

func parseFLACDuration(r io.Reader) float64 {
	stream, err := flac.New(r)
	if err != nil || stream == nil || stream.Info == nil || stream.Info.SampleRate == 0 || stream.Info.NSamples == 0 {
		return 0
	}
	_ = stream.Close()
	return float64(stream.Info.NSamples) / float64(stream.Info.SampleRate)
}

func parseWAVDuration(r io.ReadSeeker, fileSize int64) float64 {
	var hdr [12]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0
	}
	magic := string(hdr[0:4])
	if (magic != "RIFF" && magic != "RF64") || string(hdr[8:12]) != "WAVE" {
		return 0
	}

	var byteRate uint32
	var dataSize int64
	pos := int64(12)

	for pos+8 <= fileSize {
		if _, err := r.Seek(pos, io.SeekStart); err != nil {
			break
		}
		var ch [8]byte
		if _, err := io.ReadFull(r, ch[:]); err != nil {
			break
		}
		chunkID := string(ch[0:4])
		chunkLen := int64(binary.LittleEndian.Uint32(ch[4:8]))
		pos += 8

		switch chunkID {
		case "fmt ":
			if chunkLen >= 16 {
				var fmtBuf [16]byte
				if _, err := io.ReadFull(r, fmtBuf[:]); err == nil {
					byteRate = binary.LittleEndian.Uint32(fmtBuf[8:12])
				}
			}
		case "data":
			if chunkLen == 0xFFFFFFFF || pos+chunkLen > fileSize {
				dataSize = fileSize - pos
			} else {
				dataSize = chunkLen
			}
			if byteRate > 0 && dataSize > 0 {
				return float64(dataSize) / float64(byteRate)
			}
		}

		if chunkLen < 0 {
			break
		}
		advance := chunkLen
		if advance%2 != 0 {
			advance++
		}
		pos += advance
	}
	if byteRate > 0 && dataSize > 0 {
		return float64(dataSize) / float64(byteRate)
	}
	return 0
}

var mp3BitratesV1L3 = [16]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}
var mp3BitratesV2L3 = [16]int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0}
var mp3SampleRates = [4][3]int{
	{11025, 12000, 8000},  // MPEG 2.5
	{0, 0, 0},             // reserved
	{22050, 24000, 16000}, // MPEG 2
	{44100, 48000, 32000}, // MPEG 1
}

func parseMP3Duration(r io.ReadSeeker, fileSize int64) float64 {
	var id3 [10]byte
	startOffset := int64(0)
	if _, err := io.ReadFull(r, id3[:]); err == nil && string(id3[0:3]) == "ID3" {
		sz := (int64(id3[6]&0x7F) << 21) | (int64(id3[7]&0x7F) << 14) | (int64(id3[8]&0x7F) << 7) | int64(id3[9]&0x7F)
		startOffset = 10 + sz
	}
	if _, err := r.Seek(startOffset, io.SeekStart); err != nil {
		return 0
	}

	buf := make([]byte, 8192)
	n, err := r.Read(buf)
	if err != nil || n < 4 {
		return 0
	}
	buf = buf[:n]

	for i := 0; i+4 <= len(buf); i++ {
		if buf[i] != 0xFF || (buf[i+1]&0xE0) != 0xE0 {
			continue
		}
		verIdx := (buf[i+1] >> 3) & 0x03
		layerIdx := (buf[i+1] >> 1) & 0x03
		brIdx := (buf[i+2] >> 4) & 0x0F
		srIdx := (buf[i+2] >> 2) & 0x03
		if verIdx == 1 || layerIdx != 1 || brIdx == 0 || brIdx == 15 || srIdx == 3 {
			continue
		}
		sr := mp3SampleRates[verIdx][srIdx]
		if sr <= 0 {
			continue
		}
		samplesPerFrame := 1152
		br := mp3BitratesV1L3[brIdx]
		if verIdx != 3 {
			samplesPerFrame = 576
			br = mp3BitratesV2L3[brIdx]
		}
		if br <= 0 {
			continue
		}

		// Check Xing/Info VBR header inside first frame
		frameSlice := buf[i:]
		for _, marker := range [][]byte{[]byte("Xing"), []byte("Info")} {
			if idx := bytes.Index(frameSlice, marker); idx >= 0 && idx+12 <= len(frameSlice) {
				flags := binary.BigEndian.Uint32(frameSlice[idx+4 : idx+8])
				if flags&0x01 != 0 {
					frames := binary.BigEndian.Uint32(frameSlice[idx+8 : idx+12])
					if frames > 0 {
						return float64(frames) * float64(samplesPerFrame) / float64(sr)
					}
				}
			}
		}

		audioBytes := fileSize - (startOffset + int64(i))
		if audioBytes > 128 {
			return float64(audioBytes*8) / float64(br*1000)
		}
	}
	return 0
}

func parseMP4Duration(r io.ReadSeeker, fileSize int64) float64 {
	return scanMP4Boxes(r, 0, fileSize, 0)
}

func scanMP4Boxes(r io.ReadSeeker, start, end int64, depth int) float64 {
	if depth > 4 {
		return 0
	}
	pos := start
	for pos+8 <= end {
		if _, err := r.Seek(pos, io.SeekStart); err != nil {
			return 0
		}
		var hdr [16]byte
		if _, err := io.ReadFull(r, hdr[:8]); err != nil {
			return 0
		}
		sz := int64(binary.BigEndian.Uint32(hdr[0:4]))
		typ := string(hdr[4:8])
		hdrLen := int64(8)
		if sz == 1 {
			if _, err := io.ReadFull(r, hdr[8:16]); err != nil {
				return 0
			}
			sz = int64(binary.BigEndian.Uint64(hdr[8:16]))
			hdrLen = 16
		} else if sz == 0 {
			sz = end - pos
		}
		if sz < hdrLen || pos+sz > end {
			return 0
		}

		if typ == "moov" {
			if d := scanMP4Boxes(r, pos+hdrLen, pos+sz, depth+1); d > 0 {
				return d
			}
		} else if typ == "mvhd" {
			var body [32]byte
			if _, err := io.ReadFull(r, body[:24]); err == nil {
				ver := body[0]
				if ver == 0 {
					timescale := binary.BigEndian.Uint32(body[12:16])
					dur := binary.BigEndian.Uint32(body[16:20])
					if timescale > 0 {
						return float64(dur) / float64(timescale)
					}
				} else if ver == 1 {
					if _, err := io.ReadFull(r, body[24:32]); err == nil {
						timescale := binary.BigEndian.Uint32(body[20:24])
						dur := binary.BigEndian.Uint64(body[24:32])
						if timescale > 0 {
							return float64(dur) / float64(timescale)
						}
					}
				}
			}
		}
		pos += sz
	}
	return 0
}

func parseOggDuration(r io.ReadSeeker, fileSize int64) float64 {
	head := make([]byte, 512)
	n, err := r.Read(head)
	if err != nil || n < 36 || string(head[0:4]) != "OggS" {
		return 0
	}
	head = head[:n]
	sampleRate := 0
	var preSkip uint16
	if idx := bytes.Index(head, []byte("OpusHead")); idx >= 0 && idx+12 <= len(head) {
		sampleRate = 48000
		preSkip = binary.LittleEndian.Uint16(head[idx+10 : idx+12])
	} else if idx := bytes.Index(head, []byte("\x01vorbis")); idx >= 0 && idx+16 <= len(head) {
		sampleRate = int(binary.LittleEndian.Uint32(head[idx+12 : idx+16]))
	}
	if sampleRate <= 0 {
		return 0
	}

	tailSize := int64(65536)
	if fileSize < tailSize {
		tailSize = fileSize
	}
	if _, err := r.Seek(fileSize-tailSize, io.SeekStart); err != nil {
		return 0
	}
	tail := make([]byte, tailSize)
	tn, err := io.ReadFull(r, tail)
	if err != nil && tn == 0 {
		return 0
	}
	tail = tail[:tn]

	for i := len(tail) - 14; i >= 0; i-- {
		if tail[i] == 'O' && tail[i+1] == 'g' && tail[i+2] == 'g' && tail[i+3] == 'S' {
			granule := int64(binary.LittleEndian.Uint64(tail[i+6 : i+14]))
			if granule > int64(preSkip) {
				return float64(granule-int64(preSkip)) / float64(sampleRate)
			}
		}
	}
	return 0
}

// ScoreCoverCandidate ranks local image files inside a work folder so main/cover images win (§4.3).
func ScoreCoverCandidate(relPath string, size int64) int64 {
	lower := strings.ToLower(filepath.ToSlash(relPath))
	depth := strings.Count(lower, "/")
	base := filepath.Base(lower)
	score := int64(1000 - depth*200)
	switch {
	case strings.Contains(base, "img_main") || strings.HasPrefix(base, "cover.") || strings.HasPrefix(base, "main.") || strings.HasPrefix(base, "folder."):
		score += 5000
	case strings.Contains(base, "cover") || strings.Contains(base, "jacket") || strings.Contains(base, "rj"):
		score += 2500
	case strings.Contains(base, "sam") || strings.Contains(base, "thumb"):
		score += 500
	}
	score += size / 10240 // slight tie-breaker for higher-res image
	return score
}
