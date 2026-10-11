package codexmanaged

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type managedOperations struct {
	lookup      func(context.Context, string) (string, error)
	install     func(context.Context, string, string) error
	readVersion func(context.Context, string) (string, error)
}

func managedOperationsWithEnv(env []string) managedOperations {
	env = append([]string{}, env...)
	return managedOperations{
		lookup: func(ctx context.Context, channel string) (string, error) {
			return lookupManagedChannel(ctx, channel, env)
		},
		install: func(ctx context.Context, prefix, target string) error {
			return installPrivateWithEnv(ctx, prefix, target, env)
		},
		readVersion: func(ctx context.Context, binary string) (string, error) {
			return readBinaryVersionWithEnv(ctx, binary, env)
		},
	}
}

func normalizeManagedOptions(opts Options) (Options, error) {
	opts.Channel, opts.Version = strings.TrimSpace(opts.Channel), strings.TrimSpace(opts.Version)
	if opts.Channel == "" {
		opts.Channel = "stable"
	}
	if opts.Channel != "stable" && opts.Channel != "alpha" {
		return opts, fmt.Errorf("unsupported managed Codex channel %q; use stable or alpha", opts.Channel)
	}
	if opts.Version != "" {
		if _, err := parseVersion(opts.Version); err != nil {
			return opts, err
		}
	}
	return opts, nil
}

// Latest checks metadata only. A pin does not contact the registry.
func Latest(ctx context.Context, opts Options, env []string) (string, error) {
	return latestManaged(ctx, opts, managedOperationsWithEnv(env))
}

func latestManaged(ctx context.Context, opts Options, ops managedOperations) (string, error) {
	opts, err := normalizeManagedOptions(opts)
	if err != nil {
		return "", err
	}
	if opts.Version != "" {
		return opts.Version, nil
	}
	text, err := ops.lookup(ctx, opts.Channel)
	if err != nil {
		return "", err
	}
	parsed, err := parseVersion(text)
	if err != nil {
		return "", err
	}
	if opts.Channel == "stable" && parsed.preview != "" {
		return "", fmt.Errorf("stable Codex channel returned preview version %q", text)
	}
	if opts.Channel == "alpha" && !strings.HasPrefix(parsed.preview, "alpha.") {
		return "", fmt.Errorf("alpha Codex channel returned non-alpha version %q", text)
	}
	return text, nil
}

func lookupManagedChannel(ctx context.Context, channel string, env []string) (string, error) {
	tag := "latest"
	if channel == "alpha" {
		tag = "alpha"
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://registry.npmjs.org/@openai%2fcodex/"+tag, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = proxyFromEnv(env)
	defer transport.CloseIdleConnections()
	response, err := (&http.Client{Timeout: 10 * time.Second, Transport: transport}).Do(req)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("official Codex registry returned HTTP %d", response.StatusCode)
	}
	var record struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1024*1024)).Decode(&record); err != nil {
		return "", err
	}
	return record.Version, nil
}
