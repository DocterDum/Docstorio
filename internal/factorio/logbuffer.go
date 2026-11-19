package factorio

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"unicode/utf8"
)

// Log buffer limits (DESIGN).
const (
	LogLines   = 5000
	LogLineMax = 4 << 10 // longer lines are truncated
	// LogPageMax is the maximum number of lines one Since call returns.
	LogPageMax = 1000
	// EventPrefix starts every manager event line.
	EventPrefix = "[docstorio] "
)

// Line is one numbered log line. N starts at 1 and increases by 1 per line
// for the lifetime of the process (across Factorio runs).
type Line struct {
	N    int64  `json:"n"`
	Text string `json:"text"`
}

// LogBuffer is the ring buffer behind GET /api/logs. It is safe for
// concurrent use; mu protects the ring.
type LogBuffer struct {
	mu     sync.Mutex
	lines  []Line // oldest first, at most LogLines
	next   int64  // n of the next line
	redact func(string) string
}

// NewLogBuffer returns an empty buffer. redact is applied to every line
// (nil = identity; main passes jobs.Redact).
func NewLogBuffer(redact func(string) string) *LogBuffer {
	if redact == nil {
		redact = func(s string) string { return s }
	}
	return &LogBuffer{next: 1, redact: redact}
}

// Append adds one line of Factorio output (trailing "\r\n" trimmed,
// truncated to LogLineMax, redacted) and returns its n.
func (b *LogBuffer) Append(text string) int64 {
	text = truncate(b.redact(strings.TrimRight(text, "\r\n")), LogLineMax)

	b.mu.Lock()
	defer b.mu.Unlock()
	n := b.next
	b.next++
	b.lines = append(b.lines, Line{N: n, Text: text})
	if len(b.lines) > LogLines {
		b.lines = b.lines[len(b.lines)-LogLines:]
	}
	return n
}

// Eventf appends a manager event line prefixed with EventPrefix and also
// writes it to the process's stderr (the container log). A message with
// several lines becomes several event lines.
func (b *LogBuffer) Eventf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	for _, line := range strings.Split(strings.TrimRight(msg, "\n"), "\n") {
		line = EventPrefix + line
		b.Append(line)
		fmt.Fprintln(os.Stderr, b.redact(line))
	}
}

// Since returns up to max (capped at LogPageMax) lines with n > after,
// oldest first, and the newest n in the buffer (0 when empty). If after is
// older than the ring, it starts at the oldest retained line.
func (b *LogBuffer) Since(after int64, max int) (lines []Line, last int64) {
	if max <= 0 || max > LogPageMax {
		max = LogPageMax
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.lines) == 0 {
		return []Line{}, 0
	}
	last = b.lines[len(b.lines)-1].N
	start := 0
	if after >= b.lines[0].N {
		start = int(after - b.lines[0].N + 1)
	}
	if start >= len(b.lines) {
		return []Line{}, last
	}
	end := min(start+max, len(b.lines))
	lines = make([]Line, end-start)
	copy(lines, b.lines[start:end])
	return lines, last
}

// truncate cuts s to at most max bytes without splitting a UTF-8 sequence.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// readLines calls fn for every line read from r until EOF or a read error.
// Line endings are removed; very long lines are cut at LogLineMax (the rest
// of such a line is skipped, so a missing newline cannot grow memory).
func readLines(r io.Reader, fn func(line string)) {
	br := bufio.NewReader(r)
	var buf []byte
	for {
		chunk, isPrefix, err := br.ReadLine()
		if err != nil {
			if len(buf) > 0 {
				fn(string(buf))
			}
			return
		}
		if len(buf) < LogLineMax {
			buf = append(buf, chunk...)
		}
		if !isPrefix {
			fn(truncate(string(buf), LogLineMax))
			buf = buf[:0]
		}
	}
}
