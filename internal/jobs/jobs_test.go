package jobs

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"
)

func TestTryBeginBusy(t *testing.T) {
	m := New(nil)
	if m.Busy() != "" {
		t.Fatal("new manager is busy")
	}
	j, err := m.TryBegin(KindStart)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(j.ID()) {
		t.Fatalf("id %q is not 16 hex characters", j.ID())
	}
	if j.Kind() != KindStart || m.Busy() != j.ID() {
		t.Fatalf("kind=%s busy=%q", j.Kind(), m.Busy())
	}

	_, err = m.TryBegin(KindStop)
	var be *BusyError
	if !errors.As(err, &be) || be.JobID != j.ID() || be.Kind != KindStart {
		t.Fatalf("second TryBegin = %v, want BusyError for %s", err, j.ID())
	}
	if _, err := m.Run(KindModInstall, func(context.Context, *Job) error { return nil }); !errors.As(err, &be) {
		t.Fatalf("Run while busy = %v", err)
	}

	j.Finish(nil)
	j.Finish(errors.New("ignored"))
	if m.Busy() != "" {
		t.Fatal("still busy after Finish")
	}
	if s := j.Snapshot(0); s.State != Done || s.Error != "" {
		t.Fatalf("snapshot = %+v", s)
	}

	j2, err := m.TryBegin(KindStop)
	if err != nil {
		t.Fatal(err)
	}
	if j2.ID() == j.ID() {
		t.Fatal("duplicate id")
	}
	j2.Finish(nil)
}

func TestLogfAndSnapshot(t *testing.T) {
	m := New(nil)
	j, err := m.TryBegin(KindModInstall)
	if err != nil {
		t.Fatal(err)
	}
	j.Logf("downloading %s", "x")
	j.Logf("GET https://mods.test/dl?username=u&token=abc123 failed")
	j.Logf("two\nlines\n")

	s := j.Snapshot(0)
	want := []Line{
		{1, "downloading x"},
		{2, "GET https://mods.test/dl?username=u&token=REDACTED failed"},
		{3, "two"},
		{4, "lines"},
	}
	if len(s.Lines) != len(want) {
		t.Fatalf("lines = %+v", s.Lines)
	}
	for i := range want {
		if s.Lines[i] != want[i] {
			t.Fatalf("line %d = %+v, want %+v", i, s.Lines[i], want[i])
		}
	}
	if s.Last != 4 || s.State != Running || s.ID != j.ID() || s.Kind != KindModInstall {
		t.Fatalf("snapshot = %+v", s)
	}

	s = j.Snapshot(3)
	if len(s.Lines) != 1 || s.Lines[0].N != 4 || s.Last != 4 {
		t.Fatalf("after 3 = %+v", s)
	}
	s = j.Snapshot(4)
	if s.Lines == nil || len(s.Lines) != 0 {
		t.Fatalf("after 4 = %#v, want empty non-nil", s.Lines)
	}

	j.Finish(errors.New("download failed: token=zzz"))
	s = j.Snapshot(4)
	if s.State != Failed || s.Error != "download failed: token=REDACTED" {
		t.Fatalf("failed snapshot = %+v", s)
	}
	if len(s.Lines) != 1 || s.Lines[0].Text != "error: download failed: token=REDACTED" {
		t.Fatalf("error line = %+v", s.Lines)
	}
}

func TestMaxLines(t *testing.T) {
	m := New(nil)
	j, _ := m.TryBegin(KindModUpdate)
	for i := 0; i < MaxLines+10; i++ {
		j.Logf("line %d", i)
	}
	s := j.Snapshot(0)
	if len(s.Lines) != MaxLines || s.Lines[0].N != 11 || s.Last != MaxLines+10 {
		t.Fatalf("len=%d first=%d last=%d", len(s.Lines), s.Lines[0].N, s.Last)
	}
	j.Finish(nil)
}

func TestRun(t *testing.T) {
	m := New(nil)
	release := make(chan struct{})
	j, err := m.Run(KindCreateWorld, func(ctx context.Context, j *Job) error {
		j.Logf("creating")
		<-release
		return errors.New("boom")
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.Busy() != j.ID() {
		t.Fatal("Run did not take the busy lock")
	}
	close(release)
	waitState(t, j, Failed)
	if m.Busy() != "" {
		t.Fatal("busy lock not released")
	}
	got, ok := m.Get(j.ID())
	if !ok || got != j {
		t.Fatal("Get did not return the job")
	}
	if s := j.Snapshot(0); s.Error != "boom" || s.Lines[0].Text != "creating" {
		t.Fatalf("snapshot = %+v", s)
	}
	if _, ok := m.Get("nope"); ok {
		t.Fatal("unknown id found")
	}
}

func TestCloseCancelsRun(t *testing.T) {
	m := New(nil)
	j, err := m.Run(KindModInstall, func(ctx context.Context, j *Job) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	m.Close()
	waitState(t, j, Failed)
}

func TestPrune(t *testing.T) {
	m := New(nil)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }

	old, _ := m.TryBegin(KindStart)
	old.Finish(nil)
	running, _ := m.TryBegin(KindStop) // never finished

	now = now.Add(PruneAfter - time.Second)
	if _, ok := m.Get(old.ID()); !ok {
		t.Fatal("job pruned too early")
	}
	now = now.Add(time.Second)
	if _, ok := m.Get(old.ID()); ok {
		t.Fatal("job not pruned after PruneAfter")
	}
	if _, ok := m.Get(running.ID()); !ok {
		t.Fatal("running job pruned")
	}
	running.Finish(nil)
}

func TestRedact(t *testing.T) {
	cases := map[string]string{
		"":                               "",
		"no secrets here":                "no secrets here",
		"token=abc":                      "token=REDACTED",
		"a?username=u&token=abc&x=1":     "a?username=u&token=REDACTED&x=1",
		`url "x?token=abc" done`:         `url "x?token=REDACTED" done`,
		"'token=abc'":                    "'token=REDACTED'",
		"token=a token=b\ttoken=c\nnext": "token=REDACTED token=REDACTED\ttoken=REDACTED\nnext",
		"token=":                         "token=REDACTED",
	}
	for in, want := range cases {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCustomRedact(t *testing.T) {
	m := New(func(s string) string { return "x" + s })
	j, _ := m.TryBegin(KindStart)
	j.Logf("a")
	if s := j.Snapshot(0); s.Lines[0].Text != "xa" {
		t.Fatalf("custom redact not applied: %+v", s.Lines)
	}
	j.Finish(nil)
}

func waitState(t *testing.T, j *Job, want State) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if j.Snapshot(0).State == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("job did not reach %s", want)
}
