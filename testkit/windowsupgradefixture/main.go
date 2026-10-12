// windowsupgradefixture exercises production upgrade lifecycle entrypoints with
// deterministic local health endpoints, never Feishu or an AI provider.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/app/install"
	"github.com/kxn/codex-remote-feishu/internal/app/launcher"
	"github.com/kxn/codex-remote-feishu/internal/config"
	"github.com/kxn/codex-remote-feishu/internal/execlaunch"
	relayruntime "github.com/kxn/codex-remote-feishu/internal/runtime"
)

var version = "fixture-old"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "fixture-worker" {
		// This real child holds its executable open until the production process
		// tree stop terminates it; killing only the parent cannot replace the EXE.
		for {
			time.Sleep(time.Hour)
		}
	}
	os.Exit(launcher.Main(launcher.Options{
		Args: os.Args[1:], Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
		Version: version, Branch: "windows-upgrade-smoke",
		Runners: launcher.RunnerSet{RunDaemon: runDaemon},
	}))
}

func runDaemon(ctx context.Context, args []string, version, branch string) error {
	flags := flag.NewFlagSet("fixture daemon", flag.ContinueOnError)
	configPath := flags.String("config", "", "isolated config")
	configHome := flags.String("xdg-config-home", "", "isolated config home")
	dataHome := flags.String("xdg-data-home", "", "isolated data home")
	stateHome := flags.String("xdg-state-home", "", "isolated state home")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *configPath == "" || *configHome == "" || *dataHome == "" || *stateHome == "" {
		return errors.New("fixture requires all explicit isolated paths")
	}
	for key, value := range map[string]string{"XDG_CONFIG_HOME": *configHome, "XDG_DATA_HOME": *dataHome, "XDG_STATE_HOME": *stateHome} {
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	if raw, err := os.ReadFile(*configPath + ".fail-version"); err == nil && strings.TrimSpace(string(raw)) == version {
		return fmt.Errorf("injected target daemon startup failure for %s", version)
	}
	loaded, err := config.LoadAppConfigAtPath(*configPath)
	if err != nil {
		return err
	}
	paths, err := relayruntime.DefaultPaths()
	if err != nil {
		return err
	}
	lock, err := relayruntime.AcquireLock(ctx, paths.DaemonLockFile, false)
	if err != nil {
		return err
	}
	defer lock.Release()
	identity, err := relayruntime.NewServerIdentityWithBranch(version, branch, *configPath, time.Now().UTC())
	if err != nil {
		return err
	}
	if err := relayruntime.WritePID(paths.PIDFile, identity.PID); err != nil {
		return err
	}
	if err := relayruntime.WriteServerIdentity(paths.IdentityFile, identity); err != nil {
		return err
	}
	defer os.Remove(paths.PIDFile)
	defer os.Remove(paths.IdentityFile)
	worker := execlaunch.Command(identity.BinaryPath, "fixture-worker")
	if err := worker.Start(); err != nil {
		return err
	}
	if err := relayruntime.WritePID(filepath.Join(paths.StateDir, "fixture-worker.pid"), worker.Process.Pid); err != nil {
		return err
	}
	defer func() { _ = worker.Process.Kill(); _ = worker.Wait() }()
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("/api/admin/bootstrap-state", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"setupRequired": false, "gateways": []any{}})
	})
	for _, endpoint := range []string{"/api/admin/runtime-status", "/v1/status"} {
		mux.HandleFunc(endpoint, func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(identity) })
	}
	mux.HandleFunc("POST /fixture/upgrade", func(w http.ResponseWriter, _ *http.Request) {
		statePath := filepath.Join(paths.DataDir, "install-state.json")
		state, err := install.LoadState(statePath)
		if err == nil {
			artifact := install.LocalUpgradeArtifactPath(state)
			var target []byte
			target, err = execlaunch.Command(artifact, "version").Output()
			if err == nil {
				_, err = install.RunLocalBinaryUpgradeWithStatePath(install.LocalBinaryUpgradeOptions{
					StatePath: statePath, SourceBinary: artifact, Slot: strings.TrimSpace(string(target)),
				})
			}
		}
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		_, _ = w.Write([]byte("prepared"))
	})
	listener, err := net.Listen("tcp", net.JoinHostPort(loaded.Config.Admin.ListenHost, fmt.Sprint(loaded.Config.Admin.ListenPort)))
	if err != nil {
		return err
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { <-ctx.Done(); _ = server.Close() }()
	return server.Serve(listener)
}
