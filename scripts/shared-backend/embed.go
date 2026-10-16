// Package sharedbackend embeds the complete managed Python owner implementation.
package sharedbackend

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

//go:embed host.py owner.py runtime_selection.py runtime_environment.py app_environment.py app_windows.py bootstrap.py
var source embed.FS

// Manifest identifies an immutable, fully verified owner implementation.
type Manifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	Directory     string            `json:"directory"`
	Entrypoint    string            `json:"entrypoint"`
	Bootstrap     string            `json:"bootstrap"`
	SHA256        string            `json:"sha256"`
	Files         map[string]string `json:"files"`
}

func sourceManifest() (Manifest, []string, error) {
	m := Manifest{SchemaVersion: 1, Files: map[string]string{}}
	entries, err := source.ReadDir(".")
	if err != nil {
		return m, nil, err
	}
	var names []string
	for _, entry := range entries {
		raw, err := source.ReadFile(entry.Name())
		if err != nil {
			return m, nil, err
		}
		digest := sha256.Sum256(raw)
		m.Files[entry.Name()] = hex.EncodeToString(digest[:])
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	checksum := sha256.New()
	for _, name := range names {
		fmt.Fprintf(checksum, "%s\x00%s\n", name, m.Files[name])
	}
	m.SHA256 = hex.EncodeToString(checksum.Sum(nil))
	return m, names, nil
}

// Export writes a new content-addressed package or verifies an existing one.
// Existing corrupt packages are never repaired in place or silently overwritten.
func Export(ctx context.Context, directory string) (Manifest, error) {
	m, names, err := sourceManifest()
	if err != nil {
		return m, err
	}
	if directory == "" {
		return m, fmt.Errorf("host-export requires an explicit directory")
	}
	if err := ctx.Err(); err != nil {
		return m, err
	}
	root, err := filepath.Abs(directory)
	if err != nil {
		return m, err
	}
	if err := noSymlinkComponents(root); err != nil {
		return m, err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return m, err
	}
	if err := noSymlinkComponents(root); err != nil {
		return m, err
	}
	if err := securePath(root, true); err != nil {
		return m, err
	}
	m.Directory = filepath.Join(root, m.SHA256)
	m.Entrypoint = filepath.Join(m.Directory, "host.py")
	m.Bootstrap = filepath.Join(m.Directory, "bootstrap.py")
	if _, err := os.Lstat(m.Directory); err == nil {
		return m, verify(ctx, m, names)
	} else if !os.IsNotExist(err) {
		return m, err
	}
	staging, err := os.MkdirTemp(root, ".host-stage-")
	if err != nil {
		return m, err
	}
	defer os.RemoveAll(staging)
	if err := securePath(staging, true); err != nil {
		return m, err
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return m, err
		}
		raw, err := source.ReadFile(name)
		if err != nil {
			return m, err
		}
		path := filepath.Join(staging, name)
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			return m, err
		}
		if err := securePath(path, false); err != nil {
			return m, err
		}
	}
	if err := os.Rename(staging, m.Directory); err != nil {
		// Another exporter may have published the same complete package.
		if verifyErr := verify(ctx, m, names); verifyErr != nil {
			return m, fmt.Errorf("host package publication failed: %w", verifyErr)
		}
	}
	return m, verify(ctx, m, names)
}

func noSymlinkComponents(path string) error {
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("host package path must not contain symlinks")
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if filepath.Dir(current) == current {
			return nil
		}
	}
}

func verify(ctx context.Context, m Manifest, names []string) error {
	if err := noSymlinkComponents(m.Directory); err != nil {
		return err
	}
	if err := privatePath(m.Directory, true); err != nil {
		return err
	}
	entries, err := os.ReadDir(m.Directory)
	if err != nil {
		return err
	}
	if len(entries) != len(names) {
		return fmt.Errorf("existing host package has unexpected contents")
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := filepath.Join(m.Directory, name)
		if err := privatePath(path, false); err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		raw, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
		file.Close()
		if err != nil {
			return err
		}
		if len(raw) > 1<<20 {
			return fmt.Errorf("existing host package component exceeds size limit")
		}
		digest := sha256.Sum256(raw)
		if hex.EncodeToString(digest[:]) != m.Files[name] {
			return fmt.Errorf("existing host package checksum mismatch: %s", name)
		}
	}
	return nil
}
