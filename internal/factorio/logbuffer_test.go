package factorio

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLogBufferNumbersAndSince(t *testing.T) {
	b := NewLogBuffer(nil)
	if lines, last := b.Since(0, 10); len(lines) != 0 || last != 0 || lines == nil {
		t.Fatalf("empty buffer: %v %d", lines, last)
	}
	for i := 1; i <= 5; i++ {
		if n := b.Append(fmt.Sprintf("line %d\r\n", i)); n != int64(i) {
			t.Fatalf("n = %d, want %d", n, i)
		}
	}
	lines, last := b.Since(2, 2)
	if last != 5 || len(lines) != 2 || lines[0] != (Line{3, "line 3"}) || lines[1] != (Line{4, "line 4"}) {
		t.Fatalf("Since(2, 2) = %v, %d", lines, last)
	}
	if lines, last := b.Since(5, 10); len(lines) != 0 || last != 5 {
		t.Fatalf("Since(5) = %v, %d", lines, last)
	}
	if lines, _ := b.Since(99, 10); len(lines) != 0 {
		t.Fatalf("Since(99) = %v", lines)
	}
}

func TestLogBufferRingAndPageCap(t *testing.T) {
	b := NewLogBuffer(nil)
	for i := 1; i <= LogLines+3; i++ {
		b.Append(fmt.Sprint(i))
	}
	lines, last := b.Since(0, 100000)
	if last != LogLines+3 {
		t.Fatalf("last = %d", last)
	}
	if len(lines) != LogPageMax {
		t.Fatalf("page has %d lines, want %d", len(lines), LogPageMax)
	}
	if lines[0].N != 4 {
		t.Fatalf("oldest retained n = %d, want 4", lines[0].N)
	}
	lines, _ = b.Since(LogLines+3-10, 0)
	if len(lines) != 10 || lines[9].Text != fmt.Sprint(LogLines+3) {
		t.Fatalf("tail = %v", lines)
	}
}

func TestLogBufferTruncatesAndRedacts(t *testing.T) {
	b := NewLogBuffer(func(s string) string { return strings.ReplaceAll(s, "token=abc", "token=REDACTED") })
	b.Append(strings.Repeat("é", LogLineMax)) // 2 bytes per rune
	b.Append("GET /download?username=u&token=abc")
	lines, _ := b.Since(0, 0)
	if l := lines[0].Text; len(l) > LogLineMax || !utf8.ValidString(l) {
		t.Fatalf("truncated line: %d bytes, valid=%v", len(l), utf8.ValidString(l))
	}
	if lines[1].Text != "GET /download?username=u&token=REDACTED" {
		t.Fatalf("redacted line = %q", lines[1].Text)
	}
}

func TestLogBufferEventf(t *testing.T) {
	b := NewLogBuffer(nil)
	b.Eventf("starting %s", "x.zip")
	b.Eventf("two\nlines")
	lines, _ := b.Since(0, 0)
	want := []string{"[docstorio] starting x.zip", "[docstorio] two", "[docstorio] lines"}
	if len(lines) != len(want) {
		t.Fatalf("lines = %v", lines)
	}
	for i, w := range want {
		if lines[i].Text != w {
			t.Errorf("line %d = %q, want %q", i, lines[i].Text, w)
		}
	}
}

func TestReadLines(t *testing.T) {
	long := strings.Repeat("x", LogLineMax*3)
	input := "a\r\nb\n" + long + "\nlast"
	var got []string
	readLines(strings.NewReader(input), func(l string) { got = append(got, l) })
	if len(got) != 4 || got[0] != "a" || got[1] != "b" || got[3] != "last" {
		t.Fatalf("lines = %q", got)
	}
	if len(got[2]) != LogLineMax {
		t.Fatalf("long line has %d bytes, want %d", len(got[2]), LogLineMax)
	}
}
