package factorio

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"docstorio/internal/fsutil"
	"docstorio/internal/settings"
)

// Seed creates missing files at startup and never overwrites (DESIGN
// "Process model"):
//
//   - config/config.ini with read-data=<FactorioData> and write-data=<Data>,
//   - config/server-settings.json from server-settings.example.json with
//     "visibility": {"public": false, ...}, "name": "Docstorio" and
//     "description": "" (other keys as in the example),
//   - config/server-adminlist.json, server-banlist.json and
//     server-whitelist.json as "[]".
//
// Without an example file (outside the image) server-settings.json gets
// only those three keys. It expects p.EnsureDirs to have run.
func Seed(p settings.Paths) error {
	ini := "[path]\nread-data=" + p.FactorioData + "\nwrite-data=" + p.Data + "\n"
	if err := writeIfMissing(p.ConfigFile(settings.ConfigINIFile), func() ([]byte, error) {
		return []byte(ini), nil
	}); err != nil {
		return err
	}
	if err := writeIfMissing(p.ConfigFile(settings.ServerSettingsFile), func() ([]byte, error) {
		return seedServerSettings(p)
	}); err != nil {
		return err
	}
	for _, name := range []string{settings.AdminListFile, settings.BanListFile, settings.WhitelistFile} {
		if err := writeIfMissing(p.ConfigFile(name), func() ([]byte, error) {
			return []byte("[]\n"), nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// writeIfMissing writes the content made by content to path (0644) unless
// path exists.
func writeIfMissing(path string, content func() ([]byte, error)) error {
	if _, err := os.Lstat(path); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	data, err := content()
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return fsutil.AtomicWrite(path, data, 0o644)
}

// seedServerSettings returns Factorio's example server settings with the
// Docstorio defaults applied.
func seedServerSettings(p settings.Paths) ([]byte, error) {
	obj := object{}
	example := p.Example(settings.ServerSettingsFile)
	data, err := os.ReadFile(example)
	switch {
	case err == nil:
		if obj, err = parseObject(data); err != nil {
			return nil, fmt.Errorf("%s: %w", example, err)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}
	if obj, err = obj.setValue("name", "Docstorio"); err != nil {
		return nil, err
	}
	if obj, err = obj.setValue("description", ""); err != nil {
		return nil, err
	}
	visibility, err := obj.subObject("visibility").setValue("public", false)
	if err != nil {
		return nil, err
	}
	obj = obj.set("visibility", visibility.compact())
	return obj.indented()
}
