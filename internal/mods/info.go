package mods

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ErrInvalidMod reports a zip or info.json that breaks the DESIGN rules
// (wrapped with the reason). API: 400 bad_request.
var ErrInvalidMod = errors.New("invalid mod")

// Zip limits (DESIGN "File handling").
const (
	MaxInfoJSON   = 1 << 20 // info.json is read through io.LimitReader
	MaxZipEntries = 50000
)

// Info is the subset of info.json the manager uses.
type Info struct {
	Name            string   `json:"name"`
	Version         string   `json:"version"`
	Title           string   `json:"title"`
	FactorioVersion string   `json:"factorio_version"`
	Dependencies    []string `json:"dependencies"`
}

// FileName returns the canonical zip name "<name>_<version>.zip".
func (i Info) FileName() string {
	return i.Name + "_" + i.Version + ".zip"
}

var (
	nameRe    = regexp.MustCompile(`^[A-Za-z0-9_\- ]{1,100}$`)
	versionRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
)

// ValidateInfo checks name against ^[A-Za-z0-9_\- ]{1,100}$ and version
// against ^\d+\.\d+\.\d+$; it returns an error wrapping ErrInvalidMod.
// The version must also parse with ParseVersion (components <= 65535), so
// it can be compared.
func ValidateInfo(i Info) error {
	if !nameRe.MatchString(i.Name) {
		return fmt.Errorf("%w: info.json name %q is not allowed", ErrInvalidMod, i.Name)
	}
	if !versionRe.MatchString(i.Version) {
		return fmt.Errorf("%w: info.json version %q is not A.B.C", ErrInvalidMod, i.Version)
	}
	if _, err := ParseVersion(i.Version); err != nil {
		return fmt.Errorf("%w: info.json version %q is out of range", ErrInvalidMod, i.Version)
	}
	return nil
}

// ReadInfo reads info.json from a zip given as io.ReaderAt (an upload's temp
// file or an opened zip in mods/) without extracting anything: more than
// MaxZipEntries entries, entries not all under exactly one top-level folder,
// or a missing "<folder>/info.json" are rejected; info.json is read through
// io.LimitReader(MaxInfoJSON). A missing "dependencies" key defaults to
// ["base"] (Factorio's default). The result is checked with ValidateInfo.
// Errors wrap ErrInvalidMod.
func ReadInfo(r io.ReaderAt, size int64) (Info, error) {
	// The central directory is counted before archive/zip parses (and
	// allocates) it.
	if n, err := zipEntryCount(r, size, MaxZipEntries+1); err != nil {
		return Info{}, err
	} else if n > MaxZipEntries {
		return Info{}, fmt.Errorf("%w: zip has more than %d entries", ErrInvalidMod, MaxZipEntries)
	}
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return Info{}, fmt.Errorf("%w: not a readable zip: %v", ErrInvalidMod, err)
	}
	if len(zr.File) > MaxZipEntries {
		return Info{}, fmt.Errorf("%w: zip has more than %d entries", ErrInvalidMod, MaxZipEntries)
	}
	if len(zr.File) == 0 {
		return Info{}, fmt.Errorf("%w: zip is empty", ErrInvalidMod)
	}

	folder := ""
	var infoFile *zip.File
	for _, f := range zr.File {
		top, rest, ok := strings.Cut(f.Name, "/")
		if !ok || top == "" || top == "." || top == ".." {
			return Info{}, fmt.Errorf("%w: entry %q is not inside a top-level folder", ErrInvalidMod, f.Name)
		}
		if folder == "" {
			folder = top
		} else if top != folder {
			return Info{}, fmt.Errorf("%w: zip has more than one top-level folder (%q, %q)", ErrInvalidMod, folder, top)
		}
		if rest == "info.json" {
			infoFile = f
		}
	}
	if infoFile == nil {
		return Info{}, fmt.Errorf("%w: %s/info.json is missing", ErrInvalidMod, folder)
	}

	rc, err := infoFile.Open()
	if err != nil {
		return Info{}, fmt.Errorf("%w: cannot open info.json: %v", ErrInvalidMod, err)
	}
	defer rc.Close()
	return parseInfo(rc)
}

// zipEntryCount returns an upper bound on the number of central-directory
// headers archive/zip parses, counting at most limit. archive/zip does not
// trust the 16-bit entry count of the end-of-central-directory record: it
// reads headers until it meets a bad one, starting either at the recorded
// directory offset or at the end record minus the directory size (its
// correction for data prepended to the zip). Both starts are walked and the
// larger count is returned. An archive that archive/zip would read through
// zip64 records is rejected.
func zipEntryCount(r io.ReaderAt, size int64, limit int) (int, error) {
	const eocdLen = 22
	const maxComment = 65535
	if size < eocdLen {
		return 0, fmt.Errorf("%w: not a zip", ErrInvalidMod)
	}
	tail := int64(eocdLen + maxComment)
	if tail > size {
		tail = size
	}
	buf := make([]byte, tail)
	if _, err := r.ReadAt(buf, size-tail); err != nil && !errors.Is(err, io.EOF) {
		return 0, fmt.Errorf("%w: cannot read zip: %v", ErrInvalidMod, err)
	}
	i := bytes.LastIndex(buf[:len(buf)-eocdLen+4], []byte("PK\x05\x06"))
	if i < 0 {
		return 0, fmt.Errorf("%w: not a zip", ErrInvalidMod)
	}
	eocd := size - tail + int64(i)
	records := binary.LittleEndian.Uint16(buf[i+10:])
	dirSize := binary.LittleEndian.Uint32(buf[i+12:])
	dirOffset := binary.LittleEndian.Uint32(buf[i+16:])

	// archive/zip switches to the zip64 end record when one of these
	// fields holds its marker value and a zip64 locator precedes the end
	// record.
	if records == 0xffff || dirSize == 0xffff || dirOffset == 0xffffffff {
		var loc [4]byte
		if eocd >= 20 {
			if _, err := r.ReadAt(loc[:], eocd-20); err != nil {
				return 0, fmt.Errorf("%w: cannot read zip: %v", ErrInvalidMod, err)
			}
		}
		if string(loc[:]) == "PK\x06\x07" {
			return 0, fmt.Errorf("%w: zip64 archives are not supported", ErrInvalidMod)
		}
	}

	n := countDirectoryHeaders(r, size, int64(dirOffset), limit)
	if start := eocd - int64(dirSize); start != int64(dirOffset) {
		n = max(n, countDirectoryHeaders(r, size, start, limit))
	}
	return n, nil
}

// countDirectoryHeaders counts the consecutive central-directory headers
// starting at off, stopping at the first incomplete or bad one, or at limit.
func countDirectoryHeaders(r io.ReaderAt, size, off int64, limit int) int {
	const headerLen = 46
	if off < 0 || off >= size {
		return 0
	}
	br := bufio.NewReader(io.NewSectionReader(r, off, size-off))
	var h [headerLen]byte
	n := 0
	for n < limit {
		if _, err := io.ReadFull(br, h[:]); err != nil || string(h[:4]) != "PK\x01\x02" {
			break
		}
		rest := int(binary.LittleEndian.Uint16(h[28:])) + int(binary.LittleEndian.Uint16(h[30:])) + int(binary.LittleEndian.Uint16(h[32:]))
		if d, err := br.Discard(rest); err != nil || d != rest {
			break
		}
		n++
	}
	return n
}

// parseInfo reads and decodes info.json through the MaxInfoJSON limit and
// validates the result.
func parseInfo(r io.Reader) (Info, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxInfoJSON+1))
	if err != nil {
		return Info{}, fmt.Errorf("%w: cannot read info.json: %v", ErrInvalidMod, err)
	}
	if len(data) > MaxInfoJSON {
		return Info{}, fmt.Errorf("%w: info.json is larger than %d bytes", ErrInvalidMod, MaxInfoJSON)
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))

	var raw struct {
		Name            string    `json:"name"`
		Version         string    `json:"version"`
		Title           string    `json:"title"`
		FactorioVersion string    `json:"factorio_version"`
		Dependencies    *[]string `json:"dependencies"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Info{}, fmt.Errorf("%w: info.json: %v", ErrInvalidMod, err)
	}
	info := Info{
		Name:            raw.Name,
		Version:         raw.Version,
		Title:           raw.Title,
		FactorioVersion: raw.FactorioVersion,
		Dependencies:    []string{},
	}
	switch {
	case raw.Dependencies != nil:
		info.Dependencies = append(info.Dependencies, *raw.Dependencies...)
	case raw.Name != Base:
		// Factorio's default; base itself does not depend on itself.
		info.Dependencies = []string{Base}
	}
	if err := ValidateInfo(info); err != nil {
		return Info{}, err
	}
	return info, nil
}

// Mod is an installed mod: a zip in mods/ or a built-in.
type Mod struct {
	Info
	Builtin bool      // from /opt/factorio/data; never downloaded or deleted
	File    string    // zip file name in mods/ ("" for built-ins)
	Size    int64     // zip size (0 for built-ins)
	ModTime time.Time // zip mtime (zero for built-ins)
}

// Built-in mod names with special rules.
const (
	Base     = "base"      // cannot be disabled
	Core     = "core"      // excluded by Builtins
	SpaceAge = "space-age" // GET /api/status "spaceAge" = desired enabled
)

// Builtins returns the built-in mods: each directory in dataDir (normally
// /opt/factorio/data) that contains an info.json, except "core". Their
// info.json files are read from disk through MaxInfoJSON. Builtin is true,
// File is "". The result has "base" first, then the others by name.
func Builtins(dataDir string) ([]Mod, error) {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return nil, fmt.Errorf("built-in mods: %w", err)
	}
	out := []Mod{}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == Core {
			continue
		}
		f, err := os.Open(filepath.Join(dataDir, e.Name(), "info.json"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("built-in mod %s: %w", e.Name(), err)
		}
		info, err := parseInfo(f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("built-in mod %s: %w", e.Name(), err)
		}
		out = append(out, Mod{Info: info, Builtin: true})
	}
	sort.Slice(out, func(i, j int) bool { return lessName(out[i].Name, out[j].Name) })
	return out, nil
}

// lessName orders mod names: "base" first, then case-insensitively by name.
func lessName(a, b string) bool {
	if a == Base || b == Base {
		return a == Base && b != Base
	}
	la, lb := strings.ToLower(a), strings.ToLower(b)
	if la != lb {
		return la < lb
	}
	return a < b
}
