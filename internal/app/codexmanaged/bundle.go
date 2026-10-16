package codexmanaged

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// BundleSnapshot describes one complete runtime, independent of selection state.
// Component paths are relative to BundleRoot; owner metadata may wrap this value.
type BundleSnapshot struct {
	SchemaVersion int               `json:"schemaVersion"`
	Binary        string            `json:"binary"`
	Version       string            `json:"version"`
	BundleRoot    string            `json:"bundleRoot"`
	Layout        string            `json:"layout"`
	Components    map[string]string `json:"components"`
}

type bundleVersionReader func(context.Context, string) (string, error)

// SnapshotBundle validates all required files and the CLI version without
// changing an installation, persisted selection, credentials, or configuration.
func SnapshotBundle(ctx context.Context, binary, expected string, env []string) (BundleSnapshot, error) {
	return snapshotBundle(ctx, binary, expected, runtime.GOOS, managedOperationsWithEnv(env).readVersion)
}

// VerifyBundle checks the entire recorded runtime before executing --version.
func VerifyBundle(ctx context.Context, snapshot BundleSnapshot, env []string) (BundleSnapshot, error) {
	return verifyBundle(ctx, snapshot, runtime.GOOS, managedOperationsWithEnv(env).readVersion)
}

func snapshotBundle(ctx context.Context, binary, expected, goos string, readVersion bundleVersionReader) (BundleSnapshot, error) {
	if _, err := parseVersion(expected); err != nil {
		return BundleSnapshot{}, err
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		return BundleSnapshot{}, err
	}
	// Native bundles may be passed through an npm shim symlink. Resolve that
	// explicit binary once; recorded components themselves must be regular files.
	binary, err = filepath.EvalSymlinks(binary)
	if err != nil {
		return BundleSnapshot{}, err
	}
	for _, layout := range []string{"npm-vendor", "flat"} {
		root, required, err := bundleLayout(binary, layout, goos)
		if err != nil {
			continue
		}
		snapshot := BundleSnapshot{SchemaVersion: 1, Binary: binary, Version: expected,
			BundleRoot: root, Layout: layout, Components: map[string]string{}}
		complete := true
		for _, relative := range required {
			path, err := checkedBundleComponent(root, relative)
			if err != nil {
				complete = false
				break
			}
			checksum, err := componentDigest(ctx, path)
			if err != nil {
				return BundleSnapshot{}, err
			}
			snapshot.Components[relative] = checksum
		}
		if complete {
			return verifyBundle(ctx, snapshot, goos, readVersion)
		}
	}
	return BundleSnapshot{}, fmt.Errorf("incomplete Codex runtime bundle: native CLI and every required helper must share one supported layout")
}

func verifyBundle(ctx context.Context, snapshot BundleSnapshot, goos string, readVersion bundleVersionReader) (BundleSnapshot, error) {
	if snapshot.SchemaVersion != 1 {
		return BundleSnapshot{}, fmt.Errorf("unsupported Codex bundle schema %d", snapshot.SchemaVersion)
	}
	if _, err := parseVersion(snapshot.Version); err != nil {
		return BundleSnapshot{}, err
	}
	if !filepath.IsAbs(snapshot.Binary) || !filepath.IsAbs(snapshot.BundleRoot) {
		return BundleSnapshot{}, fmt.Errorf("Codex bundle binary and root must be absolute")
	}
	root, required, err := bundleLayout(snapshot.Binary, snapshot.Layout, goos)
	if err != nil {
		return BundleSnapshot{}, err
	}
	if !sameBundlePath(root, snapshot.BundleRoot) {
		return BundleSnapshot{}, fmt.Errorf("Codex binary does not belong to recorded bundle root")
	}
	for _, relative := range required {
		if _, ok := snapshot.Components[relative]; !ok {
			return BundleSnapshot{}, fmt.Errorf("Codex bundle manifest missing required component %s", relative)
		}
	}
	for relative, checksum := range snapshot.Components {
		if len(checksum) != 64 {
			return BundleSnapshot{}, fmt.Errorf("invalid Codex component checksum")
		}
		if _, err := hex.DecodeString(checksum); err != nil {
			return BundleSnapshot{}, fmt.Errorf("invalid Codex component checksum")
		}
		path, err := checkedBundleComponent(root, relative)
		if err != nil {
			return BundleSnapshot{}, err
		}
		actual, err := componentDigest(ctx, path)
		if err != nil {
			return BundleSnapshot{}, err
		}
		if !strings.EqualFold(actual, checksum) {
			return BundleSnapshot{}, fmt.Errorf("Codex bundle component checksum mismatch: %s", relative)
		}
	}
	actual, err := readVersion(ctx, snapshot.Binary)
	if err != nil {
		return BundleSnapshot{}, err
	}
	if actual != snapshot.Version {
		return BundleSnapshot{}, fmt.Errorf("Codex bundle version %s differs from expected %s", actual, snapshot.Version)
	}
	return snapshot, nil
}

func bundleLayout(binary, layout, goos string) (string, []string, error) {
	wantName := "codex"
	if goos == "windows" {
		wantName += ".exe"
	}
	if filepath.Base(binary) != wantName {
		return "", nil, fmt.Errorf("bundle requires the native %s executable", wantName)
	}
	root := filepath.Dir(binary)
	paths := componentPaths(binary, goos)
	switch layout {
	case "npm-vendor":
		if filepath.Base(root) != "bin" {
			return "", nil, fmt.Errorf("npm bundle requires bin directory")
		}
		root = filepath.Dir(root)
	case "flat":
		for index, path := range paths {
			paths[index] = filepath.Join(root, filepath.Base(path))
		}
	default:
		return "", nil, fmt.Errorf("unsupported Codex bundle layout %q", layout)
	}
	required := make([]string, 0, len(paths))
	for _, path := range paths {
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return "", nil, err
		}
		required = append(required, relative)
	}
	return root, required, nil
}

func sameBundlePath(left, right string) bool {
	left, right = filepath.Clean(left), filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func checkedBundleComponent(root, relative string) (string, error) {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	if !sameBundlePath(resolvedRoot, root) {
		return "", fmt.Errorf("Codex bundle root must be canonical")
	}
	path, err := safeComponentPath(root, relative)
	if err != nil {
		return "", err
	}
	if filepath.Clean(relative) != relative {
		return "", fmt.Errorf("noncanonical Codex component path %q", relative)
	}
	// Lexical containment alone permits a directory symlink to escape the root.
	// Reject links for the root and every component of a recorded relative path.
	current := root
	parts := append([]string{""}, strings.Split(relative, string(os.PathSeparator))...)
	for _, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("Codex bundle components cannot traverse symlinks")
		}
	}
	if err := regularExecutable(path); err != nil {
		return "", err
	}
	return path, nil
}
