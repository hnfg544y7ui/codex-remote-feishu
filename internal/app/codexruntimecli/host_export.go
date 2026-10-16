package codexruntimecli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	sharedbackend "github.com/kxn/codex-remote-feishu/scripts/shared-backend"
)

func runHostExport(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("codex-runtime host-export", flag.ContinueOnError)
	flags.SetOutput(stderr)
	directory := flags.String("directory", "", "explicit private owner source export root")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*directory) == "" {
		return fmt.Errorf("host-export requires --directory and no positional arguments")
	}
	result, err := sharedbackend.Export(ctx, *directory)
	if err != nil {
		return err
	}
	return writeJSON(stdout, result)
}
