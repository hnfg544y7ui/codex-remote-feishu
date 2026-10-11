package codexmanaged

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func releaseDirectory(root, target string) (string, error) {
	if _, err := parseVersion(target); err != nil {
		return "", err
	}
	return filepath.Join(root, "releases", target), nil
}

func componentPaths(binary, goos string) []string {
	base := filepath.Dir(binary)
	// Official platform npm bundles place Windows sandbox tools in resources,
	// not beside codex.exe. Linux executes through bwrap and has no runner exe.
	vendor := filepath.Dir(base)
	if goos == "windows" {
		return []string{binary, filepath.Join(base, "codex-code-mode-host.exe"), filepath.Join(vendor, "codex-resources", "codex-command-runner.exe"), filepath.Join(vendor, "codex-resources", "codex-windows-sandbox-setup.exe")}
	}
	paths := []string{binary, filepath.Join(base, "codex-code-mode-host")}
	if goos == "linux" {
		paths = append(paths, filepath.Join(vendor, "codex-resources", "bwrap"))
	}
	return paths
}

func componentDigest(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	buffer := make([]byte, 128*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := file.Read(buffer)
		if n > 0 {
			_, _ = hash.Write(buffer[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func buildManagedRelease(ctx context.Context, prefix, target, channel string, ops managedOperations) (Release, error) {
	binary, err := installedBinary(prefix)
	if err != nil {
		return Release{}, err
	}
	actual, err := ops.readVersion(ctx, binary)
	if err != nil {
		return Release{}, err
	}
	if actual != target {
		return Release{}, fmt.Errorf("Codex package returned %s, expected %s", actual, target)
	}
	release := Release{Version: target, Binary: binary, Channel: channel, PreparedAt: time.Now().UTC(), Components: map[string]string{}}
	for _, path := range componentPaths(binary, runtime.GOOS) {
		if err := regularExecutable(path); err != nil {
			return Release{}, fmt.Errorf("incomplete official Codex package: %w", err)
		}
		relative, err := filepath.Rel(prefix, path)
		if err != nil {
			return Release{}, err
		}
		digest, err := componentDigest(ctx, path)
		if err != nil {
			return Release{}, err
		}
		release.Components[relative] = digest
	}
	return release, nil
}

func regularExecutable(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("required Codex executable is missing or invalid: %s", path)
	}
	if runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
		return fmt.Errorf("Codex component is not executable: %s", path)
	}
	return nil
}

func safeComponentPath(directory, relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) {
		return "", fmt.Errorf("invalid component path %q", relative)
	}
	clean := filepath.Clean(relative)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("component escapes managed release: %s", relative)
	}
	return filepath.Join(directory, clean), nil
}

func validateManagedFiles(ctx context.Context, root string, release Release, hashes bool) error {
	directory, err := releaseDirectory(root, release.Version)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(directory, release.Binary)
	if err != nil {
		return err
	}
	binary, err := safeComponentPath(directory, relative)
	if err != nil {
		return err
	}
	for _, path := range componentPaths(binary, runtime.GOOS) {
		if err := regularExecutable(path); err != nil {
			return fmt.Errorf("invalid managed Codex %s: %w", release.Version, err)
		}
		key, _ := filepath.Rel(directory, path)
		digest, ok := release.Components[key]
		if !ok || len(digest) != 64 {
			return fmt.Errorf("managed Codex manifest missing required component %s", key)
		}
	}
	for relative, digest := range release.Components {
		path, err := safeComponentPath(directory, relative)
		if err != nil {
			return err
		}
		if err := regularExecutable(path); err != nil {
			return err
		}
		if hashes {
			actual, err := componentDigest(ctx, path)
			if err != nil {
				return err
			}
			if actual != digest {
				return fmt.Errorf("managed Codex component checksum mismatch: %s", relative)
			}
		}
	}
	return nil
}

func validateManagedRelease(ctx context.Context, root string, release Release, ops managedOperations) (Release, error) {
	if err := validateManagedFiles(ctx, root, release, false); err != nil {
		return Release{}, err
	}
	actual, err := ops.readVersion(ctx, release.Binary)
	if err != nil {
		return Release{}, err
	}
	if actual != release.Version {
		return Release{}, fmt.Errorf("active Codex version %s differs from manifest %s", actual, release.Version)
	}
	return release, nil
}

func readReleaseManifest(root, target string) (Release, error) {
	directory, err := releaseDirectory(root, target)
	if err != nil {
		return Release{}, err
	}
	raw, err := readManagedJSONFile(filepath.Join(directory, "remote-release.json"))
	if err != nil {
		return Release{}, err
	}
	var release Release
	if err := json.Unmarshal(raw, &release); err != nil {
		return Release{}, err
	}
	if release.Version != target {
		return Release{}, fmt.Errorf("release manifest version differs from directory")
	}
	return release, nil
}
