package factorio

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"docstorio/internal/jobs"
	"docstorio/internal/settings"
)

var (
	// ErrRunning reports an operation that needs a stopped server.
	// API: 409 server_running.
	ErrRunning = errors.New("the server is running")
	// ErrNotRunning reports an operation that needs a running server
	// (console). API: 409 server_not_running.
	ErrNotRunning = errors.New("the server is not running")
	// ErrNoSaves reports a start without any save in saves/.
	// API: 409 conflict ("create a world first").
	ErrNoSaves = errors.New("there are no saves; create a world first")

	// errShuttingDown refuses launches once Shutdown has been called.
	errShuttingDown = errors.New("the manager is shutting down")
)

// State is the supervisor state (DESIGN "States").
type State string

// Supervisor states. There are no automatic restarts.
const (
	Stopped  State = "stopped"
	Starting State = "starting" // process spawned, ready line not seen yet
	Running  State = "running"  // ReadyMarker seen
	Stopping State = "stopping" // SIGINT sent, waiting for exit
	Failed   State = "failed"   // exited non-zero without a stop request
)

// Timing constants.
const (
	StopTimeout     = 120 * time.Second // SIGINT -> SIGKILL (DESIGN "Stop")
	PlayersInterval = 5 * time.Second   // RCON "/players online" refresh while running
)

const (
	playersTimeout = 5 * time.Second  // one players poll
	consoleTimeout = 30 * time.Second // one console command
	// outputGrace is how long the exit handler waits for the rest of the
	// output after the process exited.
	outputGrace = 2 * time.Second
)

// ReadyMarker is the log text that marks the server as running.
const ReadyMarker = "changing state from(CreatingGame) to(InGame)"

// Status is the supervisor part of GET /api/status (the api adds gamePort,
// versions, spaceAge, autostart and busyJobId).
type Status struct {
	State     State      `json:"state"`
	Save      string     `json:"save"`      // save file name of the current/last run, "" if none
	StartedAt *time.Time `json:"startedAt"` // spawn time of the current/last run
	// LastError holds the "Error" lines (plus the bullet lines after them)
	// of the last failed run's output.
	LastError string   `json:"lastError"`
	Players   []string `json:"players"` // online players, refreshed every PlayersInterval; never nil
	// RestartRequired is set by MarkRestartRequired while the server is
	// starting or running and cleared by every launch.
	RestartRequired bool `json:"restartRequired"`
}

// Config configures a Supervisor.
type Config struct {
	Paths settings.Paths
	// Settings returns the current manager settings (game port, whitelist,
	// factorio.com login, RCON password) at launch time. main wires
	// settings.Store.Get. Nil = settings.Defaults().
	Settings func() settings.Settings
	// WriteModList writes mods/mod-list.json from the desired mod state. It
	// runs before every launch and world creation; an error aborts them.
	// main wires mods.Manager.WriteModList. Nil = skip.
	WriteModList func() error
	// Log receives Factorio's output and the manager events. Nil = a
	// private buffer.
	Log *LogBuffer
	// Command overrides the Factorio binary path (tests use a fake script);
	// "" means Paths.FactorioBin.
	Command string
}

// process is one running Factorio child.
type process struct {
	cmd      *exec.Cmd
	stdin    *os.File      // write end of Factorio's stdin, held open until exit
	done     chan struct{} // closed once the exit has been recorded
	stopping bool          // SIGINT sent by Stop; guarded by Supervisor.mu
}

// Supervisor owns the Factorio child process. All methods are safe for
// concurrent use. mu protects the state fields; it is never held while
// waiting for the process, for RCON replies or for log lines, or while
// calling Config.WriteModList.
type Supervisor struct {
	cfg Config
	log *LogBuffer

	// Fixed after NewSupervisor; tests shorten them.
	stopTimeout     time.Duration
	playersInterval time.Duration
	rconAddr        string

	mu        sync.Mutex
	st        Status
	proc      *process // current child, nil when none
	rcon      *RCON    // client of the current child
	launching bool     // Start or CreateWorld between its checks and the spawn
	closed    bool     // Shutdown was called: no new launches
}

// NewSupervisor returns a stopped Supervisor. It does no I/O.
func NewSupervisor(cfg Config) *Supervisor {
	log := cfg.Log
	if log == nil {
		log = NewLogBuffer(nil)
	}
	return &Supervisor{
		cfg:             cfg,
		log:             log,
		stopTimeout:     StopTimeout,
		playersInterval: PlayersInterval,
		rconAddr:        RCONAddr,
		st:              Status{State: Stopped, Players: []string{}},
	}
}

// Status returns a copy of the current status. It never blocks on RCON or
// the process.
func (s *Supervisor) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.st
	st.Players = append([]string{}, s.st.Players...)
	if s.st.StartedAt != nil {
		t := *s.st.StartedAt
		st.StartedAt = &t
	}
	return st
}

// Start launches Factorio with save, the one-shot "load this save" request
// (a file name in saves/; "" = the newest save by mtime, see ResolveSave).
// It returns once the process has been spawned (state Starting) or the
// launch was refused:
//
//   - ErrRunning when starting, running or stopping,
//   - ErrNoSaves, or an error wrapping fs.ErrNotExist for a missing save,
//   - an error from WriteModList or WriteEffectiveSettings,
//   - an error when the process could not be spawned (state Failed).
//
// Launch order: WriteModList, ResolveSave, WriteEffectiveSettings, spawn
// with Args, clear RestartRequired. Output goes line by line into the
// LogBuffer; the ReadyMarker line switches to Running; a non-zero exit
// without a stop request switches to Failed with LastError set.
func (s *Supervisor) Start(save string) error {
	if err := s.beginLaunch(); err != nil {
		return err
	}
	defer s.endLaunch()

	if err := s.writeModList(); err != nil {
		return err
	}
	p := s.cfg.Paths
	name, err := ResolveSave(p.Saves, save)
	if err != nil {
		return err
	}
	set := s.settings()
	if err := WriteEffectiveSettings(p.ConfigFile(settings.ServerSettingsFile), p.EffectiveServerSettings(),
		set.FactorioUsername, set.FactorioToken); err != nil {
		return fmt.Errorf("writing the effective server settings: %w", err)
	}
	return s.spawn(name, Args(p, set, filepath.Join(p.Saves, name)), set.RCONPassword)
}

// beginLaunch refuses a launch while a process exists or another launch is
// in progress, and marks one as in progress.
func (s *Supervisor) beginLaunch() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errShuttingDown
	}
	if s.proc != nil || s.launching {
		return ErrRunning
	}
	s.launching = true
	return nil
}

func (s *Supervisor) endLaunch() {
	s.mu.Lock()
	s.launching = false
	s.mu.Unlock()
}

// settings returns the current manager settings.
func (s *Supervisor) settings() settings.Settings {
	if s.cfg.Settings == nil {
		return settings.Defaults()
	}
	return s.cfg.Settings()
}

// writeModList runs Config.WriteModList.
func (s *Supervisor) writeModList() error {
	if s.cfg.WriteModList == nil {
		return nil
	}
	if err := s.cfg.WriteModList(); err != nil {
		return fmt.Errorf("writing mod-list.json: %w", err)
	}
	return nil
}

// command returns the Factorio binary.
func (s *Supervisor) command() string {
	if s.cfg.Command != "" {
		return s.cfg.Command
	}
	return s.cfg.Paths.FactorioBin
}

// spawn starts the Factorio process for save and the goroutine that
// follows it.
func (s *Supervisor) spawn(save string, args []string, rconPassword string) error {
	out, w, err := os.Pipe()
	if err != nil {
		return err
	}
	// Factorio reads console commands from stdin and logs an error when it
	// hits EOF, so give it a pipe that stays open until it exits.
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		out.Close()
		w.Close()
		return err
	}
	cmd := exec.Command(s.command(), args...)
	cmd.Stdin = stdinR
	cmd.Stdout = w
	cmd.Stderr = w
	s.log.Eventf("starting Factorio with save %s", save)

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		out.Close()
		w.Close()
		stdinR.Close()
		stdinW.Close()
		return errShuttingDown
	}
	now := time.Now()
	err = cmd.Start()
	w.Close()      // the child has its copy
	stdinR.Close() // likewise
	if err != nil {
		s.st = Status{State: Failed, Save: save, StartedAt: &now, Players: []string{},
			LastError: "starting Factorio: " + err.Error()}
		s.mu.Unlock()
		out.Close()
		stdinW.Close()
		s.log.Eventf("starting Factorio failed: %v", err)
		return fmt.Errorf("starting Factorio: %w", err)
	}
	proc := &process{cmd: cmd, stdin: stdinW, done: make(chan struct{})}
	s.proc = proc
	s.rcon = NewRCON(s.rconAddr, rconPassword)
	s.st = Status{State: Starting, Save: save, StartedAt: &now, Players: []string{}}
	s.mu.Unlock()

	go s.follow(proc, out)
	return nil
}

// follow copies the output of proc into the log buffer, detects the ready
// line, waits for the exit and records it.
func (s *Supervisor) follow(proc *process, out *os.File) {
	var errs errorCollector
	outputDone := make(chan struct{})
	go func() {
		defer close(outputDone)
		ready := false
		readLines(out, func(line string) {
			s.log.Append(line)
			errs.add(line)
			if !ready && strings.Contains(line, ReadyMarker) {
				ready = true
				s.ready(proc)
			}
		})
	}()

	waitErr := proc.cmd.Wait()
	proc.stdin.Close()
	select {
	case <-outputDone:
	case <-time.After(outputGrace):
		// Something else still holds the pipe open; stop reading.
		out.Close()
		<-outputDone
	}
	out.Close()
	s.exited(proc, waitErr, errs.text())
}

// ready switches a starting proc to Running and starts the players poll.
func (s *Supervisor) ready(proc *process) {
	s.mu.Lock()
	ok := s.proc == proc && s.st.State == Starting
	if ok {
		s.st.State = Running
	}
	s.mu.Unlock()
	if ok {
		s.log.Eventf("Factorio is running")
		go s.pollPlayers(proc)
	}
}

// pollPlayers refreshes Status.Players over RCON every playersInterval
// until proc exits. Failed polls keep the previous list.
func (s *Supervisor) pollPlayers(proc *process) {
	ticker := time.NewTicker(s.playersInterval)
	defer ticker.Stop()
	for {
		select {
		case <-proc.done:
			return
		case <-ticker.C:
		}
		s.mu.Lock()
		rc := s.rcon
		ok := s.proc == proc && s.st.State == Running
		s.mu.Unlock()
		if !ok {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), playersTimeout)
		reply, err := rc.Exec(ctx, "/players online")
		cancel()
		if err != nil {
			continue
		}
		names := ParsePlayersOnline(reply)
		s.mu.Lock()
		if s.proc == proc && s.st.State == Running {
			s.st.Players = names
		}
		s.mu.Unlock()
	}
}

// exited records the end of proc: Stopped after a stop request or a clean
// exit, otherwise Failed with LastError.
func (s *Supervisor) exited(proc *process, waitErr error, errText string) {
	s.mu.Lock()
	rc := s.rcon
	s.proc = nil
	s.rcon = nil
	s.st.Players = []string{}
	s.st.RestartRequired = false
	var event string
	switch {
	case proc.stopping:
		s.st.State = Stopped
		event = "Factorio stopped"
	case waitErr == nil:
		s.st.State = Stopped
		event = "Factorio exited"
	default:
		s.st.State = Failed
		if errText == "" {
			errText = "Factorio exited with " + waitErr.Error()
		}
		s.st.LastError = errText
		event = "Factorio failed: " + waitErr.Error()
	}
	s.mu.Unlock()

	if rc != nil {
		rc.Close()
	}
	s.log.Eventf("%s", event)
	close(proc.done)
}

// Stop stops the server: SIGINT (Factorio saves the loaded save), wait up to
// StopTimeout, then SIGKILL. It returns when the process has exited; state
// becomes Stopped. When nothing is running it clears Failed to Stopped and
// returns nil.
func (s *Supervisor) Stop() error {
	return s.stop(context.Background())
}

// stop is Stop with ctx as an additional bound on the wait for the exit.
func (s *Supervisor) stop(ctx context.Context) error {
	s.mu.Lock()
	proc := s.proc
	if proc == nil {
		if s.st.State == Failed {
			s.st.State = Stopped
		}
		s.mu.Unlock()
		return nil
	}
	first := !proc.stopping
	if first {
		proc.stopping = true
		s.st.State = Stopping
	}
	s.mu.Unlock()

	if first {
		s.log.Eventf("stopping Factorio")
		// An error means the process is already gone; the wait below ends.
		_ = proc.cmd.Process.Signal(os.Interrupt)
	}
	timer := time.NewTimer(s.stopTimeout)
	defer timer.Stop()
	select {
	case <-proc.done:
		return nil
	case <-timer.C:
		s.log.Eventf("Factorio did not stop within %s; killing it", s.stopTimeout)
	case <-ctx.Done():
		s.log.Eventf("shutdown time is up; killing Factorio")
	}
	_ = proc.cmd.Process.Kill()
	<-proc.done
	return nil
}

// Restart is Stop followed by Start(save).
func (s *Supervisor) Restart(save string) error {
	if err := s.Stop(); err != nil {
		return err
	}
	return s.Start(save)
}

// Autostart starts the newest save when settings.Autostart is on and at
// least one save exists (no save: do nothing, the UI shows "Create world").
func (s *Supervisor) Autostart() error {
	if !s.settings().Autostart {
		s.log.Eventf("autostart is off")
		return nil
	}
	if _, err := ResolveSave(s.cfg.Paths.Saves, ""); errors.Is(err, ErrNoSaves) {
		s.log.Eventf("no saves yet; create a world to start the server")
		return nil
	} else if err != nil {
		return err
	}
	return s.Start("")
}

// Shutdown stops the server (as Stop) for manager exit and closes RCON. ctx
// bounds the wait; on expiry the process is killed. No launch is possible
// afterwards.
func (s *Supervisor) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return s.stop(ctx) // the exit handler closes RCON
}

// MarkRestartRequired records that a change applies on the next start (no
// effect unless starting or running).
func (s *Supervisor) MarkRestartRequired() {
	s.mu.Lock()
	if s.st.State == Starting || s.st.State == Running {
		s.st.RestartRequired = true
	}
	s.mu.Unlock()
}

// Console runs one RCON command and returns its reply. ErrNotRunning unless
// running.
func (s *Supervisor) Console(ctx context.Context, command string) (string, error) {
	s.mu.Lock()
	rc := s.rcon
	running := s.st.State == Running && rc != nil
	s.mu.Unlock()
	if !running {
		return "", ErrNotRunning
	}
	ctx, cancel := context.WithTimeout(ctx, consoleTimeout)
	defer cancel()
	return rc.Exec(ctx, command)
}

// CreateOptions are the parameters of POST /api/saves.
type CreateOptions struct {
	Name              string  // file name, already checked with fsutil.ValidSaveName
	Seed              *uint32 // --map-gen-seed; nil = random
	UseMapGenSettings bool    // pass config/map-gen-settings.json (when it exists)
	UseMapSettings    bool    // pass config/map-settings.json (when it exists)
}

// CreateWorld runs WriteModList and then "factorio -c <config.ini>
// --mod-directory <mods> --create <saves>/<name>" with the chosen options,
// streaming its output to log and to the LogBuffer. ErrRunning unless
// stopped or failed; ErrSaveExists if the name is taken (Factorio would
// overwrite it silently). Cancelling ctx kills Factorio.
func (s *Supervisor) CreateWorld(ctx context.Context, log jobs.Logger, opts CreateOptions) error {
	logf := func(format string, args ...any) {
		if log != nil {
			log.Logf(format, args...)
		}
	}
	if err := s.beginLaunch(); err != nil {
		return err
	}
	defer s.endLaunch()

	p := s.cfg.Paths
	path, err := savePath(p.Saves, opts.Name)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		return ErrSaveExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := s.writeModList(); err != nil {
		return err
	}

	args := []string{
		"-c", p.ConfigFile(settings.ConfigINIFile),
		"--mod-directory", p.Mods,
		"--create", path,
	}
	optional := []struct {
		use  bool
		flag string
		file string
	}{
		{opts.UseMapGenSettings, "--map-gen-settings", settings.MapGenSettingsFile},
		{opts.UseMapSettings, "--map-settings", settings.MapSettingsFile},
	}
	for _, o := range optional {
		if !o.use {
			continue
		}
		file := p.ConfigFile(o.file)
		if _, err := os.Stat(file); err != nil {
			logf("%s not found; using Factorio's defaults", o.file)
			continue
		}
		args = append(args, o.flag, file)
	}
	if opts.Seed != nil {
		args = append(args, "--map-gen-seed", strconv.FormatUint(uint64(*opts.Seed), 10))
	}

	out, w, err := os.Pipe()
	if err != nil {
		return err
	}
	defer out.Close()
	cmd := exec.CommandContext(ctx, s.command(), args...)
	cmd.Stdout = w
	cmd.Stderr = w
	logf("creating world %s", opts.Name)
	s.log.Eventf("creating world %s", opts.Name)
	err = cmd.Start()
	w.Close()
	if err != nil {
		return fmt.Errorf("starting Factorio: %w", err)
	}
	var errs errorCollector
	readLines(out, func(line string) {
		s.log.Append(line)
		logf("%s", line)
		errs.add(line)
	})
	if err := cmd.Wait(); err != nil {
		s.log.Eventf("creating world %s failed: %v", opts.Name, err)
		if text := errs.text(); text != "" {
			return fmt.Errorf("creating the world failed (%v):\n%s", err, text)
		}
		return fmt.Errorf("creating the world failed: %w", err)
	}
	s.log.Eventf("world %s created", opts.Name)
	logf("world %s created", opts.Name)
	return nil
}
