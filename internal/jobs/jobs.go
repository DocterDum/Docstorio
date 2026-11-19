package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Kind names a type of job. The values appear in the API ("kind").
type Kind string

// Job kinds. Install, update and create-world use Run; the rest TryBegin.
const (
	KindStart       Kind = "start"
	KindStop        Kind = "stop"
	KindRestart     Kind = "restart"
	KindAutostart   Kind = "autostart"
	KindCreateWorld Kind = "create-world"
	KindModInstall  Kind = "mod-install"
	KindModUpdate   Kind = "mod-update"
	KindModUpload   Kind = "mod-upload"
	KindModDelete   Kind = "mod-delete"
	KindModEnable   Kind = "mod-enable"
	KindModDisable  Kind = "mod-disable"
)

// State is a job's lifecycle state.
type State string

// Job states as shown by the API.
const (
	Running State = "running"
	Done    State = "done"
	Failed  State = "failed"
)

// Retention limits.
const (
	// PruneAfter is how long a finished job stays retrievable.
	PruneAfter = time.Hour
	// MaxLines caps a job's log; older lines are dropped (n keeps counting).
	MaxLines = 5000
)

// Logger is the logging surface other packages accept for long operations.
// *Job satisfies it; tests can pass a fake.
type Logger interface {
	Logf(format string, args ...any)
}

// Line is one numbered job log line. N starts at 1 and increases by 1.
type Line struct {
	N    int64  `json:"n"`
	Text string `json:"text"`
}

// Snapshot is the body of GET /api/jobs/{id}.
type Snapshot struct {
	ID    string `json:"id"`
	Kind  Kind   `json:"kind"`
	State State  `json:"state"`
	Lines []Line `json:"lines"` // never nil
	Last  int64  `json:"last"`  // n of the newest line, 0 if none
	Error string `json:"error,omitempty"`
}

// BusyError is returned by TryBegin and Run while another job holds the busy
// lock. API: 409 {"error", "code": "busy", "jobId"}.
type BusyError struct {
	JobID string
	Kind  Kind
}

func (e *BusyError) Error() string {
	return fmt.Sprintf("busy: %s job %s is running", e.Kind, e.JobID)
}

// Job is one operation. Its methods are safe for concurrent use (mu protects
// lines, state and err; finished belongs to the Manager and is protected by
// Manager.mu).
type Job struct {
	mu       sync.Mutex
	m        *Manager
	id       string
	kind     Kind
	state    State
	lines    []Line
	last     int64
	err      string
	finished time.Time // zero while running; guarded by m.mu
}

// ID returns the job id (16 lowercase hex characters).
func (j *Job) ID() string { return j.id }

// Kind returns the job kind.
func (j *Job) Kind() Kind { return j.kind }

// Logf appends a redacted line (multi-line text becomes several lines).
func (j *Job) Logf(format string, args ...any) {
	text := j.m.redact(fmt.Sprintf(format, args...))
	j.mu.Lock()
	defer j.mu.Unlock()
	j.appendLocked(text)
}

// appendLocked adds text as one or more lines; j.mu must be held.
func (j *Job) appendLocked(text string) {
	text = strings.TrimRight(text, "\r\n")
	for _, line := range strings.Split(text, "\n") {
		j.last++
		j.lines = append(j.lines, Line{N: j.last, Text: strings.TrimRight(line, "\r")})
	}
	if over := len(j.lines) - MaxLines; over > 0 {
		j.lines = append([]Line(nil), j.lines[over:]...)
	}
}

// Finish ends the job: state done when err is nil, otherwise failed with
// err's (redacted) message, which is also logged as the last line. It
// releases the busy lock. Calling Finish more than once does nothing.
//
// The busy lock is released before the state changes, so a client that sees
// done or failed can start the next job straight away.
func (j *Job) Finish(err error) {
	m := j.m
	m.mu.Lock()
	if !j.finished.IsZero() {
		m.mu.Unlock()
		return
	}
	j.finished = m.now()
	if m.busy == j {
		m.busy = nil
	}
	m.mu.Unlock()

	j.mu.Lock()
	defer j.mu.Unlock()
	if err != nil {
		j.state = Failed
		j.err = m.redact(err.Error())
		j.appendLocked("error: " + j.err)
	} else {
		j.state = Done
	}
}

// Snapshot returns the job with the lines whose n > after.
func (j *Job) Snapshot(after int64) Snapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	lines := []Line{}
	for _, l := range j.lines {
		if l.N > after {
			lines = append(lines, l)
		}
	}
	return Snapshot{ID: j.id, Kind: j.kind, State: j.state, Lines: lines, Last: j.last, Error: j.err}
}

// Manager owns all jobs and the busy lock. mu protects jobs, busy and each
// job's finished time; it is never held while a job function runs or while
// Job.mu is held.
type Manager struct {
	mu     sync.Mutex
	jobs   map[string]*Job
	busy   *Job
	redact func(string) string
	now    func() time.Time
	ctx    context.Context
	cancel context.CancelFunc
}

// New returns a Manager. redact is applied to every job line and error (nil
// means Redact).
func New(redact func(string) string) *Manager {
	if redact == nil {
		redact = Redact
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{jobs: make(map[string]*Job), redact: redact, now: time.Now, ctx: ctx, cancel: cancel}
}

// TryBegin takes the busy lock for a short operation and returns the new
// running job, or *BusyError. The caller must call Finish:
//
//	j, err := m.TryBegin(jobs.KindModEnable)
//	if err != nil { ... }
//	defer func() { j.Finish(err) }()
func (m *Manager) TryBegin(kind Kind) (*Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	if m.busy != nil {
		return nil, &BusyError{JobID: m.busy.id, Kind: m.busy.kind}
	}
	id := newID()
	for m.jobs[id] != nil {
		id = newID()
	}
	j := &Job{m: m, id: id, kind: kind, state: Running}
	m.jobs[id] = j
	m.busy = j
	return j, nil
}

// Run takes the busy lock, starts fn in a new goroutine and returns the job
// immediately. fn's error finishes the job. ctx is cancelled by Close (it is
// not the HTTP request context, which ends when the response is sent).
func (m *Manager) Run(kind Kind, fn func(ctx context.Context, j *Job) error) (*Job, error) {
	j, err := m.TryBegin(kind)
	if err != nil {
		return nil, err
	}
	go func() {
		j.Finish(fn(m.ctx, j))
	}()
	return j, nil
}

// Get returns a job by id (running, or finished less than PruneAfter ago).
func (m *Manager) Get(id string) (*Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	j, ok := m.jobs[id]
	return j, ok
}

// Busy returns the id of the job holding the busy lock, or "".
func (m *Manager) Busy() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.busy == nil {
		return ""
	}
	return m.busy.id
}

// Close cancels the context of running jobs started with Run. It does not
// wait for them.
func (m *Manager) Close() {
	m.cancel()
}

// pruneLocked drops jobs finished PruneAfter or longer ago; m.mu must be
// held.
func (m *Manager) pruneLocked() {
	now := m.now()
	for id, j := range m.jobs {
		if !j.finished.IsZero() && now.Sub(j.finished) >= PruneAfter {
			delete(m.jobs, id)
		}
	}
}

// newID returns 16 random lowercase hex characters.
func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand does not fail on supported platforms; fall back to the
		// clock so an id is still produced.
		return fmt.Sprintf("%016x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// Redact removes the factorio.com token from s: the value of every "token="
// parameter (up to the next '&', whitespace, quote or end) becomes
// "token=REDACTED". It is used for job lines, the Factorio log buffer and
// manager log output (DESIGN "File handling").
func Redact(s string) string {
	const key = "token="
	if !strings.Contains(s, key) {
		return s
	}
	var b strings.Builder
	for {
		i := strings.Index(s, key)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i+len(key)])
		b.WriteString("REDACTED")
		s = s[i+len(key):]
		end := strings.IndexFunc(s, func(r rune) bool {
			return r == '&' || r == '"' || r == '\'' || unicode.IsSpace(r)
		})
		if end < 0 {
			return b.String()
		}
		s = s[end:]
	}
}
