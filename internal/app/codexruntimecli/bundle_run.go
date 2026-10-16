package codexruntimecli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/app/codexmanaged"
)

type bundleOperations struct {
	snapshot func(context.Context, string, string, []string) (codexmanaged.BundleSnapshot, error)
	verify   func(context.Context, codexmanaged.BundleSnapshot, []string) (codexmanaged.BundleSnapshot, error)
}

func runBundle(ctx context.Context, args []string, stdout, stderr io.Writer, env []string, ops bundleOperations) error {
	flags := flag.NewFlagSet("codex-runtime "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	var binary, version, manifest string
	if args[0] == "bundle" {
		flags.StringVar(&binary, "binary", "", "native executable in a complete Codex runtime bundle")
		flags.StringVar(&version, "version", "", "expected exact CLI version")
	} else {
		flags.StringVar(&manifest, "manifest", "", "private JSON bundle snapshot to verify")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	var result codexmanaged.BundleSnapshot
	var err error
	if args[0] == "bundle" {
		if binary == "" || version == "" {
			return fmt.Errorf("bundle requires --binary and --version")
		}
		result, err = ops.snapshot(ctx, binary, version, env)
	} else {
		if manifest == "" {
			return fmt.Errorf("verify-bundle requires --manifest")
		}
		file, openErr := os.Open(manifest)
		if openErr != nil {
			return openErr
		}
		defer file.Close()
		raw, readErr := io.ReadAll(io.LimitReader(file, (4<<20)+1))
		if readErr != nil {
			return readErr
		}
		if len(raw) > 4<<20 {
			return fmt.Errorf("Codex bundle manifest exceeds size limit")
		}
		var snapshot codexmanaged.BundleSnapshot
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			return err
		}
		result, err = ops.verify(ctx, snapshot, env)
	}
	if err != nil {
		return err
	}
	return writeJSON(stdout, result)
}
