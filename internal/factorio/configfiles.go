package factorio

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"

	"docstorio/internal/fsutil"
	"docstorio/internal/settings"
)

var (
	// ErrUnknownConfigFile reports a name outside the editable list.
	// API: 404 not_found.
	ErrUnknownConfigFile = errors.New("unknown config file")
	// ErrBadValue reports a server-settings form value whose JSON type does
	// not match the field type, or an unknown key. API: 400 bad_request.
	ErrBadValue = errors.New("invalid server setting")
)

// JSONError reports invalid JSON content. API: 400 invalid_json with line
// and column.
type JSONError struct {
	Line   int    `json:"line"`   // 1-based
	Column int    `json:"column"` // 1-based
	Msg    string `json:"-"`
}

func (e *JSONError) Error() string {
	return fmt.Sprintf("line %d, column %d: %s", e.Line, e.Column, e.Msg)
}

// ConfigFileInfo is one entry of GET /api/config/files.
type ConfigFileInfo struct {
	Name   string `json:"name"`
	Exists bool   `json:"exists"`
}

// Field is one server-settings form field (GET /api/config/server-settings).
type Field struct {
	Key     string   `json:"key"`
	Type    string   `json:"type"` // string | int | bool | enum | password
	Value   any      `json:"value"`
	Options []string `json:"options,omitempty"` // for enum
	Help    string   `json:"help"`              // from the example's _comment_<key>
}

// configFileNames are the files of the config API, in API order.
var configFileNames = []string{
	settings.ServerSettingsFile,
	settings.MapGenSettingsFile,
	settings.MapSettingsFile,
	settings.AdminListFile,
	settings.BanListFile,
	settings.WhitelistFile,
}

// Server-settings form rules (see ServerSettingsFields).
var (
	// formSkipped are example keys owned by the manager settings.
	formSkipped = map[string]bool{"username": true, "password": true, "token": true}
	// allowCommandsOptions are the documented values of "allow_commands".
	allowCommandsOptions = []string{"true", "false", "admins-only"}
)

const commentPrefix = "_comment_"

// ConfigFiles implements the config API over config/. mu serialises
// read-modify-write cycles. The editable files are, in this order,
// settings.ServerSettingsFile, MapGenSettingsFile, MapSettingsFile,
// AdminListFile, BanListFile and WhitelistFile.
type ConfigFiles struct {
	paths settings.Paths
	mu    sync.Mutex
}

// NewConfigFiles returns the config file service.
func NewConfigFiles(p settings.Paths) *ConfigFiles {
	return &ConfigFiles{paths: p}
}

// List returns the editable files with their existence.
func (c *ConfigFiles) List() []ConfigFileInfo {
	list := make([]ConfigFileInfo, 0, len(configFileNames))
	for _, name := range configFileNames {
		_, err := os.Stat(c.paths.ConfigFile(name))
		list = append(list, ConfigFileInfo{Name: name, Exists: err == nil})
	}
	return list
}

// checkName returns ErrUnknownConfigFile for a name outside the list.
func checkName(name string) error {
	if !slices.Contains(configFileNames, name) {
		return fmt.Errorf("%w: %q", ErrUnknownConfigFile, name)
	}
	return nil
}

// Read returns the file content. A missing file returns Factorio's example
// where one exists, else "[]", with exists=false. Unknown name:
// ErrUnknownConfigFile.
func (c *ConfigFiles) Read(name string) (content string, exists bool, err error) {
	if err := checkName(name); err != nil {
		return "", false, err
	}
	data, err := os.ReadFile(c.paths.ConfigFile(name))
	if err == nil {
		return string(data), true, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", false, err
	}
	data, err = os.ReadFile(c.paths.Example(name))
	if err == nil {
		return string(data), false, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", false, err
	}
	return "[]", false, nil
}

// Write checks that content is valid JSON (*JSONError with line/column) and
// writes the file (fsutil.AtomicWrite, 0644).
func (c *ConfigFiles) Write(name, content string) error {
	if err := checkName(name); err != nil {
		return err
	}
	if err := checkJSON(content); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return fsutil.AtomicWrite(c.paths.ConfigFile(name), []byte(content), 0o644)
}

// checkJSON returns a *JSONError when content is not valid JSON.
func checkJSON(content string) error {
	var v any
	err := json.Unmarshal([]byte(content), &v)
	if err == nil {
		return nil
	}
	offset := int64(len(content))
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		offset = syntax.Offset
	}
	line, col := position(content, offset)
	return &JSONError{Line: line, Column: col, Msg: err.Error()}
}

// position returns the 1-based line and column (in characters) of the byte
// that ends the first offset bytes of s (json.SyntaxError.Offset counts
// the offending byte).
func position(s string, offset int64) (line, col int) {
	end := int(max(0, min(offset-1, int64(len(s)))))
	line, col = 1, 1
	for _, r := range s[:end] {
		if r == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	return line, col
}

// ServerSettingsFields derives the form from server-settings.example.json:
// keys in the example's order, "_comment_<key>" becomes Help, the type
// comes from the example value ("game_password" is type password,
// "allow_commands" an enum of its documented values), keys of the
// "visibility" object are flattened as "visibility.public" etc., and lists
// such as "tags" and "admins" are left to the raw editor. Values come from
// config/server-settings.json (the example's value when a key is missing).
// The factorio.com "username", "password" and "token" keys are left out:
// the manager settings own the factorio.com login.
//
// Without an example file (outside the image) the current file serves as
// the template.
func (c *ConfigFiles) ServerSettingsFields() ([]Field, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	tmpl, cur, err := c.loadServerSettings()
	if err != nil {
		return nil, err
	}
	return buildFields(tmpl, cur), nil
}

// UpdateServerSettings applies a partial update (keys as returned by
// ServerSettingsFields) to config/server-settings.json, keeping all other
// keys. A value of the wrong JSON type or an unknown key: ErrBadValue.
func (c *ConfigFiles) UpdateServerSettings(values map[string]any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	tmpl, cur, err := c.loadServerSettings()
	if err != nil {
		return err
	}
	fields := map[string]Field{}
	for _, f := range buildFields(tmpl, cur) {
		fields[f.Key] = f
	}
	for _, key := range slices.Sorted(maps.Keys(values)) {
		f, ok := fields[key]
		if !ok {
			return fmt.Errorf("%w: unknown key %q", ErrBadValue, key)
		}
		raw, err := checkValue(f, values[key])
		if err != nil {
			return fmt.Errorf("%w: %s: %v", ErrBadValue, key, err)
		}
		if sub, ok := strings.CutPrefix(key, "visibility."); ok {
			cur = cur.set("visibility", cur.subObject("visibility").set(sub, raw).compact())
		} else {
			cur = cur.set(key, raw)
		}
	}
	data, err := cur.indented()
	if err != nil {
		return err
	}
	return fsutil.AtomicWrite(c.paths.ConfigFile(settings.ServerSettingsFile), data, 0o644)
}

// loadServerSettings returns the form template (Factorio's example, or the
// current file when there is none) and the current server-settings.json
// (the template when the file is missing). c.mu must be held.
func (c *ConfigFiles) loadServerSettings() (tmpl, cur object, err error) {
	cur, curErr := readObject(c.paths.ConfigFile(settings.ServerSettingsFile))
	if curErr != nil && !errors.Is(curErr, fs.ErrNotExist) {
		return nil, nil, curErr
	}
	tmpl, err = readObject(c.paths.Example(settings.ServerSettingsFile))
	if errors.Is(err, fs.ErrNotExist) && curErr == nil {
		tmpl, err = cur, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if curErr != nil { // missing file
		cur = slices.Clone(tmpl)
	}
	return tmpl, cur, nil
}

// readObject reads a file holding one JSON object.
func readObject(path string) (object, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	obj, err := parseObject(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return obj, nil
}

// buildFields derives the form fields from tmpl with values from cur.
func buildFields(tmpl, cur object) []Field {
	fields := []Field{}
	for _, m := range tmpl {
		if strings.HasPrefix(m.Key, commentPrefix) || formSkipped[m.Key] {
			continue
		}
		comment, _ := tmpl.get(commentPrefix + m.Key)
		if m.Key == "visibility" {
			sub := tmpl.subObject("visibility")
			curSub := cur.subObject("visibility")
			for _, sm := range sub {
				value, _ := curSub.get(sm.Key)
				if f, ok := makeField("visibility."+sm.Key, sm.Value, value, visibilityHelp(comment, sm.Key)); ok {
					fields = append(fields, f)
				}
			}
			continue
		}
		value, _ := cur.get(m.Key)
		if f, ok := makeField(m.Key, m.Value, value, commentText(comment)); ok {
			fields = append(fields, f)
		}
	}
	return fields
}

// makeField builds the field for key from its example value; ok is false
// for values the form does not edit (lists, objects, null, fractions).
// current is the value from the user's file, nil when missing.
func makeField(key string, example, current json.RawMessage, help string) (Field, bool) {
	ex, err := decodeJSON(example)
	if err != nil {
		return Field{}, false
	}
	f := Field{Key: key, Value: ex, Help: help}
	switch v := ex.(type) {
	case string:
		switch key {
		case "game_password":
			f.Type = "password"
		case "allow_commands":
			f.Type = "enum"
			f.Options = allowCommandsOptions
		default:
			f.Type = "string"
		}
	case bool:
		f.Type = "bool"
	case json.Number:
		if _, err := strconv.ParseInt(v.String(), 10, 64); err != nil {
			return Field{}, false
		}
		f.Type = "int"
	default:
		return Field{}, false
	}
	if current != nil {
		if v, err := decodeJSON(current); err == nil {
			f.Value = v
		}
	}
	return f, true
}

// checkValue checks v against the field type and returns it as JSON.
func checkValue(f Field, v any) (json.RawMessage, error) {
	switch f.Type {
	case "string", "password":
		if _, ok := v.(string); !ok {
			return nil, errors.New("must be a string")
		}
	case "enum":
		s, ok := v.(string)
		if !ok || !slices.Contains(f.Options, s) {
			return nil, fmt.Errorf("must be one of %s", strings.Join(f.Options, ", "))
		}
	case "bool":
		if _, ok := v.(bool); !ok {
			return nil, errors.New("must be true or false")
		}
	case "int":
		n, ok := toInt(v)
		if !ok {
			return nil, errors.New("must be a whole number")
		}
		v = n
	default:
		return nil, errors.New("not editable")
	}
	return encodeJSON(v)
}

// toInt converts a decoded JSON number to int64.
func toInt(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		if n != math.Trunc(n) || math.Abs(n) > 1<<53 {
			return 0, false
		}
		return int64(n), true
	case json.Number:
		i, err := strconv.ParseInt(n.String(), 10, 64)
		return i, err == nil
	case int:
		return int64(n), true
	case int64:
		return n, true
	}
	return 0, false
}

// commentText returns a "_comment_" value as text: a string, or a list of
// strings joined with newlines.
func commentText(raw json.RawMessage) string {
	if raw == nil {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return strings.Join(list, "\n")
	}
	return ""
}

// visibilityHelp picks the "<key>: ..." entry of "_comment_visibility".
func visibilityHelp(raw json.RawMessage, key string) string {
	for _, line := range strings.Split(commentText(raw), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), key+":"); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}
