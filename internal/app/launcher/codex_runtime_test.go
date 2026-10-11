package launcher

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"testing"
)

func TestCodexRuntimeDispatch(t *testing.T) {
	args := []string{"prepare", "-channel", "stable"}
	decision, err := Detect(append([]string{"codex-runtime"}, args...))
	if err != nil || decision.Role != RoleCodexRuntime || !reflect.DeepEqual(decision.Args, args) {
		t.Fatalf("decision = %+v, err = %v", decision, err)
	}
	for _, failure := range []bool{false, true} {
		called := false
		exitCode := Main(Options{
			Args: append([]string{"codex-runtime"}, args...), Version: "vtest",
			Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
			Runners: RunnerSet{RunCodexRuntime: func(ctx context.Context, got []string, _, _ io.Writer, version string) error {
				called = true
				if ctx == nil || !reflect.DeepEqual(got, args) || version != "vtest" {
					t.Fatalf("runner args/version = %v/%s", got, version)
				}
				if failure {
					return errors.New("candidate verification failed")
				}
				return nil
			}},
		})
		if !called || (exitCode == 1) != failure {
			t.Fatalf("called = %v, exit = %d, failure = %v", called, exitCode, failure)
		}
	}
}
