package factorio

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"docstorio/internal/settings"
)

// fakeFactorio is the fake Factorio binary. It records its arguments in
// args.txt (one per line) and behaves as the "mode" file next to it says,
// printing log lines as Factorio 2.1.24 does (FACTS).
const fakeFactorio = `#!/bin/sh
set -eu
dir=$(dirname "$0")
printf '%s\n' "$@" > "$dir/args.txt"
mode=$(cat "$dir/mode")
case "$mode" in
run|run-exit3|hang)
	echo "   0.000 2026-11-19 10:00:00; Factorio 2.1.24 (build 84519, linux64, headless, space-age)"
	echo "   0.412 Hosting game at IP ADDR:({0.0.0.0:34197})"
	echo "   0.500 Info ServerMultiplayerManager.cpp:808: updateTick(4294967295) changing state from(CreatingGame) to(InGame)"
	echo "   0.502 Info RemoteCommandProcessor.cpp:133: Starting RCON interface at IP ADDR:({127.0.0.1:27015})"
	if [ "$mode" = hang ]; then
		trap '' INT
	else
		trap 'echo "   9.000 Info ServerMultiplayerManager.cpp:1040: Received SIGINT, shutting down"; echo "   9.010 Info AppManagerStates.cpp:2161: Saving map as /factorio/saves/x.zip"; echo "   9.300 Goodbye"; if [ "$mode" = run-exit3 ]; then exit 3; fi; exit 0' INT
	fi
	while :; do sleep 0.05; done
	;;
fail-mods)
	echo "   0.250 Loading mod core 0.0.0 (data.lua)"
	echo '   0.300 Error Util.cpp:81: Failed to load mod "depmod":'
	echo "• depmod"
	echo "    • Missing required dependency missingdep >= 1.0.0"
	echo "    • Dependency modv21 >= 2.0.0 is not satisfied (active: modv21 1.0.0)"
	echo "    • Incompatible with modv21"
	echo "   0.310 Goodbye"
	exit 1
	;;
missing-save)
	echo '   0.400 Error ServerMultiplayerManager.cpp:91: MultiplayerManager failed: "File /factorio/saves/x.zip does not exist."'
	echo "   0.401 Info ServerMultiplayerManager.cpp:808: updateTick(0) changing state from(CreatingGame) to(InitializationFailed)"
	exit 1
	;;
crash)
	echo "   0.100 Info nothing useful" >&2
	exit 2
	;;
create)
	out=""
	prev=""
	for a in "$@"; do
		if [ "$prev" = --create ]; then out=$a; fi
		prev=$a
	done
	echo "   0.100 Creating new map $out"
	printf 'PK' > "$out"
	echo "   2.000 Done."
	;;
create-fail)
	echo '   0.300 Error Util.cpp:81: Failed to load mod "depmod":'
	echo "    • Missing required dependency missingdep >= 1.0.0"
	exit 1
	;;
esac
`

// testEnv is a data folder and a fake Factorio for one test.
type testEnv struct {
	root     string
	settings atomic.Value // settings.Settings
	modLists atomic.Int32 // WriteModList calls
	modErr   atomic.Value // error returned by WriteModList (errBox)
}

type errBox struct{ err error }

func (e *testEnv) setMode(t *testing.T, mode string) {
	t.Helper()
	writeFile(t, filepath.Join(e.root, "mode"), mode)
}

func (e *testEnv) args(t *testing.T) []string {
	t.Helper()
	return strings.Split(strings.TrimRight(readFile(t, filepath.Join(e.root, "args.txt")), "\n"), "\n")
}

var envs sync.Map // *Supervisor -> *testEnv

func envOf(s *Supervisor) *testEnv {
	e, _ := envs.Load(s)
	return e.(*testEnv)
}

// newTestSupervisor returns a Supervisor running the fake Factorio in
// mode, with short timeouts, and the paths of its data folder.
func newTestSupervisor(t *testing.T, mode string) (*Supervisor, settings.Paths) {
	t.Helper()
	root := t.TempDir()
	p := settings.NewPaths(filepath.Join(root, "data"), filepath.Join(root, "opt"))
	for _, d := range []string{p.Saves, p.Mods, p.Config, p.Manager} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, p.ConfigFile(settings.ServerSettingsFile), `{"name": "Docstorio", "username": "", "token": ""}`)
	bin := filepath.Join(root, "factorio")
	if err := os.WriteFile(bin, []byte(fakeFactorio), 0o755); err != nil {
		t.Fatal(err)
	}

	e := &testEnv{root: root}
	e.setMode(t, mode)
	e.settings.Store(settings.Settings{Autostart: true, GamePort: 34197, RCONPassword: "pw"})
	e.modErr.Store(errBox{})
	s := NewSupervisor(Config{
		Paths:    p,
		Settings: func() settings.Settings { return e.settings.Load().(settings.Settings) },
		WriteModList: func() error {
			e.modLists.Add(1)
			return e.modErr.Load().(errBox).err
		},
		Log:     NewLogBuffer(nil),
		Command: bin,
	})
	s.stopTimeout = 5 * time.Second
	s.playersInterval = 50 * time.Millisecond
	envs.Store(s, e)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.Shutdown(ctx)
		envs.Delete(s)
	})
	return s, p
}

// waitFor polls cond until it is true or 5 s have passed.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitState(t *testing.T, s *Supervisor, want State) {
	t.Helper()
	waitFor(t, "state "+string(want), func() bool { return s.Status().State == want })
}

// logText returns the whole log buffer.
func logText(s *Supervisor) string {
	var b strings.Builder
	var after int64
	for {
		lines, last := s.log.Since(after, LogPageMax)
		for _, l := range lines {
			b.WriteString(l.Text + "\n")
		}
		if len(lines) == 0 || lines[len(lines)-1].N == last {
			return b.String()
		}
		after = lines[len(lines)-1].N
	}
}

func TestStartRunStop(t *testing.T) {
	s, p := newTestSupervisor(t, "run")
	e := envOf(s)
	writeSave(t, p.Saves, "old.zip", time.Hour)
	writeSave(t, p.Saves, "world.zip", time.Minute)

	if err := s.Start(""); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, Running)
	st := s.Status()
	if st.Save != "world.zip" || st.StartedAt == nil || st.LastError != "" || st.Players == nil {
		t.Fatalf("status = %+v", st)
	}
	if e.modLists.Load() != 1 {
		t.Errorf("WriteModList called %d times", e.modLists.Load())
	}
	want := Args(p, e.settings.Load().(settings.Settings), filepath.Join(p.Saves, "world.zip"))
	if got := e.args(t); !slices.Equal(got, want) {
		t.Errorf("args =\n%q\nwant\n%q", got, want)
	}
	if _, err := os.Stat(p.EffectiveServerSettings()); err != nil {
		t.Errorf("effective settings: %v", err)
	}

	if err := s.Start(""); !errors.Is(err, ErrRunning) {
		t.Errorf("second Start: %v", err)
	}
	if err := s.CreateWorld(context.Background(), nil, CreateOptions{Name: "new.zip"}); !errors.Is(err, ErrRunning) {
		t.Errorf("CreateWorld while running: %v", err)
	}
	s.MarkRestartRequired()
	if !s.Status().RestartRequired {
		t.Error("RestartRequired not set while running")
	}

	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	st = s.Status()
	if st.State != Stopped || st.RestartRequired || st.Save != "world.zip" {
		t.Fatalf("status after stop = %+v", st)
	}
	log := logText(s)
	for _, want := range []string{
		"[docstorio] starting Factorio with save world.zip",
		"[docstorio] Factorio is running",
		"Received SIGINT, shutting down",
		"Goodbye",
		"[docstorio] Factorio stopped",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	s.MarkRestartRequired()
	if s.Status().RestartRequired {
		t.Error("RestartRequired set while stopped")
	}
	if err := s.Stop(); err != nil {
		t.Errorf("Stop while stopped: %v", err)
	}
}

func TestRestartWithSave(t *testing.T) {
	s, p := newTestSupervisor(t, "run")
	writeSave(t, p.Saves, "old.zip", time.Hour)
	writeSave(t, p.Saves, "world.zip", time.Minute)
	if err := s.Start(""); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, Running)
	if err := s.Restart("old.zip"); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, Running)
	if got := s.Status().Save; got != "old.zip" {
		t.Fatalf("save after restart = %q", got)
	}
	if args := envOf(s).args(t); !slices.Contains(args, filepath.Join(p.Saves, "old.zip")) {
		t.Fatalf("args %q", args)
	}
}

func TestStartRefusals(t *testing.T) {
	s, p := newTestSupervisor(t, "run")
	e := envOf(s)
	if err := s.Start(""); !errors.Is(err, ErrNoSaves) {
		t.Fatalf("no saves: %v", err)
	}
	writeSave(t, p.Saves, "world.zip", 0)
	if err := s.Start("missing.zip"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing save: %v", err)
	}
	e.modErr.Store(errBox{errors.New("disk full")})
	if err := s.Start(""); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("WriteModList error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.root, "args.txt")); err == nil {
		t.Fatal("Factorio ran although the launch was refused")
	}
	if st := s.Status(); st.State != Stopped {
		t.Fatalf("state %s", st.State)
	}
}

func TestFailedRunLastError(t *testing.T) {
	tests := []struct {
		mode string
		want string
	}{
		{"fail-mods", modErrorText},
		{"missing-save", missingSaveText},
		{"crash", "Factorio exited with exit status 2"},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			s, p := newTestSupervisor(t, tt.mode)
			writeSave(t, p.Saves, "x.zip", 0)
			if err := s.Start(""); err != nil {
				t.Fatal(err)
			}
			waitState(t, s, Failed)
			if got := s.Status().LastError; got != tt.want {
				t.Fatalf("lastError =\n%s\nwant\n%s", got, tt.want)
			}
			if !strings.Contains(logText(s), "[docstorio] Factorio failed: exit status") {
				t.Errorf("no failure event in the log")
			}
			// Stop clears failed; the next start clears lastError.
			if err := s.Stop(); err != nil {
				t.Fatal(err)
			}
			if st := s.Status(); st.State != Stopped {
				t.Fatalf("state after Stop = %s", st.State)
			}
			envOf(s).setMode(t, "run")
			if err := s.Start(""); err != nil {
				t.Fatal(err)
			}
			if got := s.Status().LastError; got != "" {
				t.Fatalf("lastError after a new start = %q", got)
			}
		})
	}
}

func TestSpawnFailure(t *testing.T) {
	s, p := newTestSupervisor(t, "run")
	s.cfg.Command = filepath.Join(t.TempDir(), "no-such-binary")
	writeSave(t, p.Saves, "x.zip", 0)
	if err := s.Start(""); err == nil {
		t.Fatal("Start succeeded without a binary")
	}
	st := s.Status()
	if st.State != Failed || !strings.Contains(st.LastError, "starting Factorio") {
		t.Fatalf("status = %+v", st)
	}
}

func TestStopAfterStopRequestIsStopped(t *testing.T) {
	s, p := newTestSupervisor(t, "run-exit3")
	writeSave(t, p.Saves, "x.zip", 0)
	if err := s.Start(""); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, Running)
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	if st := s.Status(); st.State != Stopped || st.LastError != "" {
		t.Fatalf("status = %+v", st)
	}
}

func TestStopKillsAfterTimeout(t *testing.T) {
	s, p := newTestSupervisor(t, "hang")
	s.stopTimeout = 300 * time.Millisecond
	writeSave(t, p.Saves, "x.zip", 0)
	if err := s.Start(""); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, Running)

	stopped := make(chan error, 1)
	go func() { stopped <- s.Stop() }()
	waitFor(t, "state stopping", func() bool {
		st := s.Status().State
		return st == Stopping || st == Stopped
	})
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not return")
	}
	if st := s.Status(); st.State != Stopped {
		t.Fatalf("state %s", st.State)
	}
	if !strings.Contains(logText(s), "killing it") {
		t.Errorf("no kill event in the log")
	}
}

func TestShutdownHonoursContext(t *testing.T) {
	s, p := newTestSupervisor(t, "hang")
	s.stopTimeout = time.Minute
	writeSave(t, p.Saves, "x.zip", 0)
	if err := s.Start(""); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, Running)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("Shutdown ignored its context")
	}
	if st := s.Status(); st.State != Stopped {
		t.Fatalf("state %s", st.State)
	}
	if err := s.Start(""); err == nil {
		t.Fatal("Start after Shutdown succeeded")
	}
}

func TestPlayersAndConsole(t *testing.T) {
	s, p := newTestSupervisor(t, "run")
	f := startFakeRCON(t, "pw", factorioReplies)
	s.rconAddr = f.addr
	writeSave(t, p.Saves, "x.zip", 0)

	if _, err := s.Console(context.Background(), "/version"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("console while stopped: %v", err)
	}
	if err := s.Start(""); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, Running)
	waitFor(t, "players", func() bool { return slices.Equal(s.Status().Players, []string{"alice", "bob"}) })

	out, err := s.Console(context.Background(), "/version")
	if err != nil {
		t.Fatal(err)
	}
	if out != "2.1.24\n" {
		t.Fatalf("console reply %q", out)
	}
	if _, invalid, _ := f.stats(); invalid != 0 {
		t.Errorf("%d invalid RCON packets", invalid)
	}

	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	if st := s.Status(); len(st.Players) != 0 || st.Players == nil {
		t.Fatalf("players after stop = %v", st.Players)
	}
}

func TestAutostart(t *testing.T) {
	s, p := newTestSupervisor(t, "run")
	e := envOf(s)

	// No save: nothing happens.
	if err := s.Autostart(); err != nil {
		t.Fatal(err)
	}
	if s.Status().State != Stopped {
		t.Fatal("started without a save")
	}

	writeSave(t, p.Saves, "x.zip", 0)
	set := e.settings.Load().(settings.Settings)
	set.Autostart = false
	e.settings.Store(set)
	if err := s.Autostart(); err != nil {
		t.Fatal(err)
	}
	if s.Status().State != Stopped {
		t.Fatal("started with autostart off")
	}

	set.Autostart = true
	e.settings.Store(set)
	if err := s.Autostart(); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, Running)
}

// testLogger collects job log lines.
type testLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *testLogger) Logf(format string, args ...any) {
	l.mu.Lock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
	l.mu.Unlock()
}

func TestCreateWorld(t *testing.T) {
	s, p := newTestSupervisor(t, "create")
	e := envOf(s)
	writeFile(t, p.ConfigFile(settings.MapGenSettingsFile), `{"seed": null}`)
	seed := uint32(4294967295)
	var log testLogger

	err := s.CreateWorld(context.Background(), &log, CreateOptions{
		Name: "new.zip", Seed: &seed, UseMapGenSettings: true, UseMapSettings: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	world := filepath.Join(p.Saves, "new.zip")
	want := []string{
		"-c", p.ConfigFile(settings.ConfigINIFile),
		"--mod-directory", p.Mods,
		"--create", world,
		"--map-gen-settings", p.ConfigFile(settings.MapGenSettingsFile),
		"--map-gen-seed", "4294967295",
	}
	if got := e.args(t); !slices.Equal(got, want) {
		t.Fatalf("args =\n%q\nwant\n%q", got, want)
	}
	if !filepath.IsAbs(world) {
		t.Errorf("--create path %q is not absolute", world)
	}
	if got := readFile(t, world); got != "PK" {
		t.Fatalf("world content %q", got)
	}
	if e.modLists.Load() != 1 {
		t.Errorf("WriteModList called %d times", e.modLists.Load())
	}
	joined := strings.Join(log.lines, "\n")
	for _, w := range []string{"map-settings.json not found", "Creating new map", "Done.", "world new.zip created"} {
		if !strings.Contains(joined, w) {
			t.Errorf("job log lacks %q:\n%s", w, joined)
		}
	}
	if !strings.Contains(logText(s), "Creating new map") {
		t.Error("Factorio output missing from the log buffer")
	}
	if st := s.Status(); st.State != Stopped {
		t.Errorf("state %s after create", st.State)
	}

	if err := s.CreateWorld(context.Background(), nil, CreateOptions{Name: "new.zip"}); !errors.Is(err, ErrSaveExists) {
		t.Fatalf("existing name: %v", err)
	}
	if err := s.CreateWorld(context.Background(), nil, CreateOptions{Name: "../x.zip"}); err == nil {
		t.Fatal("bad name accepted")
	}

	// Without options only the base flags are passed.
	if err := s.CreateWorld(context.Background(), nil, CreateOptions{Name: "plain.zip"}); err != nil {
		t.Fatal(err)
	}
	if got := e.args(t); len(got) != 6 {
		t.Fatalf("plain args = %q", got)
	}
}

func TestCreateWorldFailure(t *testing.T) {
	s, _ := newTestSupervisor(t, "create-fail")
	err := s.CreateWorld(context.Background(), nil, CreateOptions{Name: "new.zip"})
	if err == nil || !strings.Contains(err.Error(), `Failed to load mod "depmod"`) ||
		!strings.Contains(err.Error(), "Missing required dependency") {
		t.Fatalf("err = %v", err)
	}
}
