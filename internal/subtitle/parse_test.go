package subtitle

import (
	"bytes"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

func encodeBytes(t *testing.T, utf8Text string, enc transform.Transformer) []byte {
	t.Helper()
	out, err := io.ReadAll(transform.NewReader(strings.NewReader(utf8Text), enc))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return out
}

func TestSubtitleEncodingSniffingAndFormatParsing(t *testing.T) {
	dir := t.TempDir()

	// 1. Shift-JIS encoded Japanese .lrc matched with `01 本編.wav`
	audioPath := filepath.Join(dir, "01 本編.wav")
	_ = os.WriteFile(audioPath, []byte("wav"), 0o644)

	lrcUTF8 := "[ti:耳かき]\n[00:05.50]お帰りなさい、今日も一日お疲れ様でした\n[00:12.00]ゆっくり休んでくださいね\n"
	sjisBytes := encodeBytes(t, lrcUTF8, japanese.ShiftJIS.NewEncoder())
	lrcPath := filepath.Join(dir, "01 本編.lrc")
	if err := os.WriteFile(lrcPath, sjisBytes, 0o644); err != nil {
		t.Fatalf("write sjis lrc: %v", err)
	}

	matched := FindMatchingSubtitle(audioPath)
	if matched != lrcPath {
		t.Fatalf("FindMatchingSubtitle = %q, want %q", matched, lrcPath)
	}
	res, err := LoadAndParse(matched)
	if err != nil {
		t.Fatalf("LoadAndParse sjis lrc: %v", err)
	}
	if res.Encoding != "shift_jis" || len(res.Cues) != 2 {
		t.Fatalf("unexpected sjis lrc result: %+v", res)
	}
	if math.Abs(res.Cues[0].Start-5.5) > 0.01 || !strings.Contains(res.Cues[0].Text, "お帰りなさい") {
		t.Fatalf("unexpected cue[0]: %+v", res.Cues[0])
	}

	// 2. GBK encoded Chinese .srt
	srtUTF8 := "1\n00:00:02,250 --> 00:00:06,500\n欢迎回来，今天辛苦啦\n\n2\n00:00:07,000 --> 00:00:10,000\n给你掏耳朵放松一下吧\n"
	gbkBytes := encodeBytes(t, srtUTF8, simplifiedchinese.GBK.NewEncoder())
	srtRes := ParseBytes(gbkBytes, "02.srt", "srt")
	if srtRes.Encoding != "gbk" || len(srtRes.Cues) != 2 || srtRes.Cues[0].Text != "欢迎回来，今天辛苦啦" {
		t.Fatalf("unexpected gbk srt result: %+v", srtRes)
	}

	// 3. UTF-8 BOM .vtt
	vttRaw := append([]byte{0xEF, 0xBB, 0xBF}, []byte("WEBVTT\n\n00:01.000 --> 00:03.500\nささやきテスト\n")...)
	vttRes := ParseBytes(vttRaw, "03.vtt", "vtt")
	if vttRes.Encoding != "utf-8-bom" || len(vttRes.Cues) != 1 || vttRes.Cues[0].Text != "ささやきテスト" {
		t.Fatalf("unexpected vtt result: %+v", vttRes)
	}

	// 4. .ass with override tags stripped
	assRaw := []byte("[Script Info]\nTitle: Test\n\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\nDialogue: 0,0:00:03.20,0:00:08.40,Default,,0,0,0,,{\\fad(200,200)\\pos(320,50)}耳元で囁くよ\\Nおやすみ\n")
	assRes := ParseBytes(bytes.Clone(assRaw), "04.ass", "ass")
	if len(assRes.Cues) != 1 || assRes.Cues[0].Text != "耳元で囁くよ\nおやすみ" {
		t.Fatalf("unexpected ass result: %+v", assRes)
	}
}
