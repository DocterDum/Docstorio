package factorio

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"docstorio/internal/fsutil"
	"docstorio/internal/mods"
	"docstorio/internal/settings"
)

// RCONAddr is where Factorio's RCON interface listens (DESIGN "Launch").
const RCONAddr = "127.0.0.1:27015"

// DetectVersion reads the Factorio version from
// <factorioDir>/data/base/info.json (fallback when FACTORIO_VERSION is not
// set).
func DetectVersion(factorioDir string) (mods.Version, error) {
	path := filepath.Join(factorioDir, "data", "base", "info.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return mods.Version{}, err
	}
	var info struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return mods.Version{}, fmt.Errorf("%s: %w", path, err)
	}
	v, err := mods.ParseVersion(info.Version)
	if err != nil {
		return mods.Version{}, fmt.Errorf("%s: version %q: %w", path, info.Version, err)
	}
	return v, nil
}

// WriteEffectiveSettings reads the user's server-settings.json (src), sets
// "username" and "token" from the stored factorio.com credentials (only when
// both are non-empty; otherwise the user's values are kept) and writes the
// result to dst (fsutil.AtomicWrite, 0600). Other keys are kept as they are.
func WriteEffectiveSettings(src, dst, username, token string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	obj, err := parseObject(data)
	if err != nil {
		return fmt.Errorf("%s: %w", src, err)
	}
	if username != "" && token != "" {
		if obj, err = obj.setValue("username", username); err != nil {
			return err
		}
		if obj, err = obj.setValue("token", token); err != nil {
			return err
		}
	}
	out, err := obj.indented()
	if err != nil {
		return err
	}
	return fsutil.AtomicWrite(dst, out, 0o600)
}

// Args returns the Factorio command line (without the binary) for running
// savePath (absolute) with the given settings, exactly as in DESIGN
// "Launch": config.ini, mod directory, --start-server, the effective
// settings file, --port GamePort, --rcon-bind RCONAddr --rcon-password,
// admin and ban lists, and the whitelist flags when WhitelistEnabled.
func Args(p settings.Paths, s settings.Settings, savePath string) []string {
	args := []string{
		"-c", p.ConfigFile(settings.ConfigINIFile),
		"--mod-directory", p.Mods,
		"--start-server", savePath,
		"--server-settings", p.EffectiveServerSettings(),
		"--port", strconv.Itoa(s.GamePort),
		"--rcon-bind", RCONAddr,
		"--rcon-password", s.RCONPassword,
		"--server-adminlist", p.ConfigFile(settings.AdminListFile),
		"--server-banlist", p.ConfigFile(settings.BanListFile),
	}
	if s.WhitelistEnabled {
		args = append(args, "--use-server-whitelist", "--server-whitelist", p.ConfigFile(settings.WhitelistFile))
	}
	return args
}

// ExtractErrors returns the "Error" lines of output plus the bullet ('•')
// lines following each of them, joined with "\n" (Status.LastError). The
// uptime prefix Factorio puts on log lines ("   0.123 ") is removed.
func ExtractErrors(output []string) string {
	var c errorCollector
	for _, line := range output {
		c.add(line)
	}
	return c.text()
}

// maxErrorLines bounds what an errorCollector keeps (the newest lines).
const maxErrorLines = 100

// uptimePrefix matches the uptime Factorio writes before each log line.
var uptimePrefix = regexp.MustCompile(`^\s*\d+\.\d+\s+`)

// errorCollector gathers the lines ExtractErrors returns, one output line at
// a time, so a long run's output need not be kept.
type errorCollector struct {
	lines   []string
	inError bool // the previous kept line belongs to an Error block
}

func (c *errorCollector) add(line string) {
	line = strings.TrimRight(line, "\r\n \t")
	text := uptimePrefix.ReplaceAllString(line, "")
	switch {
	case text == "Error" || strings.HasPrefix(text, "Error "):
		c.inError = true
		c.keep(text)
	case c.inError && strings.HasPrefix(strings.TrimSpace(text), "•"):
		c.keep(text)
	default:
		c.inError = false
	}
}

func (c *errorCollector) keep(line string) {
	c.lines = append(c.lines, line)
	if len(c.lines) > maxErrorLines {
		c.lines = c.lines[len(c.lines)-maxErrorLines:]
	}
}

func (c *errorCollector) text() string {
	return strings.Join(c.lines, "\n")
}
