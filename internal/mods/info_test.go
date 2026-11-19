package mods

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func readInfoBytes(b []byte) (Info, error) {
	return ReadInfo(bytes.NewReader(b), int64(len(b)))
}

// manyEntries builds a zip with n entries under one folder.
func manyEntries(t *testing.T, n int) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.CreateHeader(&zip.FileHeader{Name: "m/info.json", Method: zip.Store})
	w.Write([]byte(infoJSON(t, "m", "1.0.0", "2.1", []string{})))
	for i := 1; i < n; i++ {
		if _, err := zw.CreateHeader(&zip.FileHeader{Name: fmt.Sprintf("m/%d", i), Method: zip.Store}); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestReadInfoValid(t *testing.T) {
	info, err := readInfoBytes(modZip(t, "Power Armor MK3", "1.2.3", "2.1", "base >= 2.1", "? other"))
	if err != nil {
		t.Fatal(err)
	}
	want := Info{Name: "Power Armor MK3", Version: "1.2.3", Title: "Power Armor MK3 title", FactorioVersion: "2.1", Dependencies: []string{"base >= 2.1", "? other"}}
	if !reflect.DeepEqual(info, want) {
		t.Errorf("got %+v, want %+v", info, want)
	}
	if info.FileName() != "Power Armor MK3_1.2.3.zip" {
		t.Errorf("FileName = %q", info.FileName())
	}
}

func TestReadInfoDependencyDefaults(t *testing.T) {
	// Missing key -> ["base"].
	info, err := readInfoBytes(makeZip(t, zipEntry{"x/info.json", infoJSON(t, "x", "1.0.0", "2.1", nil)}))
	if err != nil || !reflect.DeepEqual(info.Dependencies, []string{"base"}) {
		t.Errorf("missing key: %v %v", info.Dependencies, err)
	}
	// Explicit empty list stays empty (and non-nil).
	info, err = readInfoBytes(makeZip(t, zipEntry{"x/info.json", infoJSON(t, "x", "1.0.0", "2.1", []string{})}))
	if err != nil || info.Dependencies == nil || len(info.Dependencies) != 0 {
		t.Errorf("empty list: %#v %v", info.Dependencies, err)
	}
	// base itself does not default to depending on base.
	info, err = readInfoBytes(makeZip(t, zipEntry{"base/info.json", infoJSON(t, "base", "2.1.24", "2.1", nil)}))
	if err != nil || len(info.Dependencies) != 0 {
		t.Errorf("base: %v %v", info.Dependencies, err)
	}
}

func TestReadInfoBOMAndLimitBoundary(t *testing.T) {
	body := "\xef\xbb\xbf" + infoJSON(t, "x", "1.0.0", "2.1", []string{})
	if _, err := readInfoBytes(makeZip(t, zipEntry{"x/info.json", body})); err != nil {
		t.Errorf("BOM: %v", err)
	}
	exact := infoJSON(t, "x", "1.0.0", "2.1", []string{})
	exact += strings.Repeat(" ", MaxInfoJSON-len(exact))
	if _, err := readInfoBytes(makeZip(t, zipEntry{"x/info.json", exact})); err != nil {
		t.Errorf("info.json of exactly %d bytes: %v", MaxInfoJSON, err)
	}
	if _, err := readInfoBytes(manyEntries(t, MaxZipEntries)); err != nil {
		t.Errorf("%d entries: %v", MaxZipEntries, err)
	}
}

func TestReadInfoRejects(t *testing.T) {
	good := infoJSON(t, "x", "1.0.0", "2.1", []string{})
	tests := []struct {
		name string
		data []byte
	}{
		{"not a zip", []byte("this is not a zip file at all, just some text")},
		{"empty file", []byte{}},
		{"empty zip", makeZip(t)},
		{"file at root", makeZip(t, zipEntry{"info.json", good})},
		{"extra root file", makeZip(t, zipEntry{"x/info.json", good}, zipEntry{"readme.txt", "hi"})},
		{"two folders", makeZip(t, zipEntry{"x/info.json", good}, zipEntry{"y/data.lua", ""})},
		{"no info.json", makeZip(t, zipEntry{"x/data.lua", ""})},
		{"nested info.json only", makeZip(t, zipEntry{"x/sub/info.json", good})},
		{"dotdot folder", makeZip(t, zipEntry{"../info.json", good})},
		{"dot folder", makeZip(t, zipEntry{"./info.json", good})},
		{"absolute path", makeZip(t, zipEntry{"/x/info.json", good})},
		{"backslash path", makeZip(t, zipEntry{"x\\info.json", good})},
		{"info.json too large", makeZip(t, zipEntry{"x/info.json", good + strings.Repeat(" ", MaxInfoJSON)})},
		{"bad json", makeZip(t, zipEntry{"x/info.json", "{not json"})},
		{"wrong types", makeZip(t, zipEntry{"x/info.json", `{"name":"x","version":"1.0.0","dependencies":"base"}`})},
		{"name with slash", makeZip(t, zipEntry{"x/info.json", infoJSON(t, "a/b", "1.0.0", "2.1", nil)})},
		{"name with dots", makeZip(t, zipEntry{"x/info.json", infoJSON(t, "..", "1.0.0", "2.1", nil)})},
		{"name empty", makeZip(t, zipEntry{"x/info.json", infoJSON(t, "", "1.0.0", "2.1", nil)})},
		{"name unicode", makeZip(t, zipEntry{"x/info.json", infoJSON(t, "modé", "1.0.0", "2.1", nil)})},
		{"name too long", makeZip(t, zipEntry{"x/info.json", infoJSON(t, strings.Repeat("a", 101), "1.0.0", "2.1", nil)})},
		{"version two parts", makeZip(t, zipEntry{"x/info.json", infoJSON(t, "x", "1.0", "2.1", nil)})},
		{"version suffix", makeZip(t, zipEntry{"x/info.json", infoJSON(t, "x", "1.0.0-beta", "2.1", nil)})},
		{"version letters", makeZip(t, zipEntry{"x/info.json", infoJSON(t, "x", "a.b.c", "2.1", nil)})},
		{"version out of range", makeZip(t, zipEntry{"x/info.json", infoJSON(t, "x", "1.0.70000", "2.1", nil)})},
		{"too many entries", manyEntries(t, MaxZipEntries+1)},
	}
	for _, tt := range tests {
		if info, err := readInfoBytes(tt.data); !errors.Is(err, ErrInvalidMod) {
			t.Errorf("%s: got %+v, %v; want ErrInvalidMod", tt.name, info, err)
		}
	}
}

// TestReadInfoEntryCountNotFromEndRecord checks that the entry limit is
// applied before archive/zip parses the directory even when the end record's
// 16-bit count is wrong (archive/zip reads headers until a bad one) or the zip
// has data prepended to it.
func TestReadInfoEntryCountNotFromEndRecord(t *testing.T) {
	tooMany := manyEntries(t, MaxZipEntries+1)

	wrapped := append([]byte{}, tooMany...)
	eocd := bytes.LastIndex(wrapped, []byte("PK\x05\x06"))
	binary.LittleEndian.PutUint16(wrapped[eocd+8:], 1)
	binary.LittleEndian.PutUint16(wrapped[eocd+10:], 1)

	prepended := append(bytes.Repeat([]byte{0}, 4096), tooMany...)

	for name, data := range map[string][]byte{"wrapped count": wrapped, "prepended data": prepended} {
		_, err := readInfoBytes(data)
		if !errors.Is(err, ErrInvalidMod) || !strings.Contains(err.Error(), "entries") {
			t.Errorf("%s: %v, want the entry limit error", name, err)
		}
	}

	// A readable zip with prepended data still passes.
	ok := append(bytes.Repeat([]byte{0}, 4096), modZip(t, "x", "1.0.0", "2.1")...)
	if _, err := readInfoBytes(ok); err != nil {
		t.Errorf("prepended valid zip: %v", err)
	}
}

func TestValidateInfo(t *testing.T) {
	ok := []Info{
		{Name: "a", Version: "0.0.0"},
		{Name: "My_Mod-2 Extra", Version: "10.20.30"},
		{Name: strings.Repeat("z", 100), Version: "1.0.0"},
	}
	for _, i := range ok {
		if err := ValidateInfo(i); err != nil {
			t.Errorf("%+v: %v", i, err)
		}
	}
	bad := []Info{
		{Name: "a.b", Version: "1.0.0"},
		{Name: "a", Version: " 1.0.0"},
		{Name: "a\x00", Version: "1.0.0"},
		{Name: "a", Version: "1.0.0\n"},
	}
	for _, i := range bad {
		if err := ValidateInfo(i); !errors.Is(err, ErrInvalidMod) {
			t.Errorf("%+v: %v, want ErrInvalidMod", i, err)
		}
	}
}

func TestBuiltins(t *testing.T) {
	dir := t.TempDir()
	writeBuiltins(t, dir)
	writeFile(t, dir, "menu-simulations/menu.lua", []byte("-- no info.json"))
	writeFile(t, dir, "changelog.txt", []byte("not a dir"))

	mods, err := Builtins(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, m := range mods {
		names = append(names, m.Name)
		if !m.Builtin || m.File != "" || m.Version != "2.1.24" {
			t.Errorf("%s: %+v", m.Name, m)
		}
	}
	want := []string{"base", "elevated-rails", "quality", "recycler", "space-age"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
	if len(mods[0].Dependencies) != 0 {
		t.Errorf("base deps = %v", mods[0].Dependencies)
	}

	if _, err := Builtins(dir + "/missing"); err == nil {
		t.Error("missing data dir: no error")
	}
	writeFile(t, dir, "broken/info.json", []byte("{"))
	if _, err := Builtins(dir); err == nil {
		t.Error("broken info.json: no error")
	}
}
