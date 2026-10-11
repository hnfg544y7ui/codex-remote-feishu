package codexprofile

import "github.com/kxn/codex-remote-feishu/internal/core/state"

// resolveSharedNative inherits the external backend's observed connection.
// Shared native is a separate contract from the private CLI profile preflight;
// it cannot supply credentials, child arguments or managed launch files.
func (r RuntimeResolver) resolveSharedNative(ref state.CodexAdmissionRef) (RuntimeProjection, error) {
	if r.NativeConfigProbeFailed {
		return RuntimeProjection{}, runtimeErrorWithStage(ErrorCodexProbeUnavailable, "shared_native_config")
	}
	preference, ok := r.resolvePreference(ref.ContextPreferenceRef)
	if !ok {
		return RuntimeProjection{}, runtimeError(ErrorProfileRevisionUnavailable)
	}
	projection, ok := r.resolveNative(ref, preference, SharedNativeCapabilitySetV1)
	if !ok {
		return RuntimeProjection{}, runtimeError(ErrorProfileRevisionUnavailable)
	}
	return projection, nil
}
