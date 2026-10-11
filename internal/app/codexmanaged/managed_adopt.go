package codexmanaged

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Adopt only the explicitly requested, complete official private npm package.
// Legacy Ensure may already be running this binary; do not rename or overwrite
// the release. Adding its verified manifest leaves executable inodes untouched.
func adoptManagedRelease(ctx context.Context, directory, target, channel string, ops managedOperations) (Release, error) {
	metadata := filepath.Join(directory, "node_modules", "@openai", "codex", "package.json")
	raw, err := os.ReadFile(metadata)
	if err != nil {
		return Release{}, fmt.Errorf("cannot adopt legacy managed Codex without official npm metadata: %w", err)
	}
	var pkg struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return Release{}, err
	}
	if pkg.Name != "@openai/codex" || pkg.Version != target {
		return Release{}, fmt.Errorf("legacy private npm package is not official Codex %s", target)
	}
	release, err := buildManagedRelease(ctx, directory, target, channel, ops)
	if err != nil {
		return Release{}, fmt.Errorf("cannot adopt incomplete legacy managed Codex: %w", err)
	}
	if err := writeManagedJSON(filepath.Join(directory, "remote-release.json"), release); err != nil {
		return Release{}, err
	}
	return release, nil
}
