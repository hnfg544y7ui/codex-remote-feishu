package daemon

import (
	"fmt"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/app/codexprofile"
	"github.com/kxn/codex-remote-feishu/internal/config"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func (a *App) sharedCodexBackendLocked() bool {
	return strings.TrimSpace(a.headlessRuntime.CodexAppServerURL) != ""
}

func sharedCodexMaintenanceError() error {
	return fmt.Errorf("当前连接共享 Codex 后端，CLI 来源与版本由后端宿主管理。要恢复独立 CLI，请清除 codexAppServerURL 并重启 Remote 服务")
}

func sharedCodexMaintenanceEvents(surfaceID string) []eventcontract.Event {
	return []eventcontract.Event{upgradeNoticeEvent(surfaceID, "codex_shared_backend_external", sharedCodexMaintenanceError().Error())}
}

// Shared clients inherit provider/auth from the server, never create private
// profile launch material or claim a different executable's probe as evidence.
func (a *App) applySharedCodexHeadlessProfileLocked(env, args []string, profileID string, admissionRef *state.CodexAdmissionRef) ([]string, []string, *codexprofile.RuntimeProjection, error) {
	if state.NormalizeCodexProfileID(profileID) != state.NativeCodexProfileID {
		return nil, nil, nil, &codexprofile.RuntimeError{Code: codexprofile.ErrorSharedBackendProfileUnsupported}
	}
	ref := state.NormalizeCodexAdmissionRef(admissionRef)
	if ref != nil && state.NormalizeCodexProfileID(ref.ProfileRef.ID) != state.NativeCodexProfileID {
		return nil, nil, nil, &codexprofile.RuntimeError{Code: codexprofile.ErrorSharedBackendProfileUnsupported}
	}
	preferences, err := a.profileContextPreferenceStore()
	if err != nil {
		return nil, nil, nil, err
	}
	evidence, _, _, failed := a.effectiveCodexNativeConnectionLocked()
	if ref == nil {
		current, ok := preferences.CodexCurrent(state.NativeCodexProfileID)
		if !ok {
			return nil, nil, nil, &codexprofile.RuntimeError{Code: codexprofile.ErrorProfileRevisionUnavailable}
		}
		ref = &state.CodexAdmissionRef{
			ProfileRef:           state.CodexProfileRef{ID: state.NativeCodexProfileID, Revision: evidence.Revision},
			ContextPreferenceRef: state.CodexContextPreferenceRef{ProfileID: state.NativeCodexProfileID, Revision: current.Revision},
		}
	}
	resolver := codexprofile.RuntimeResolver{
		SharedBackend: true, Native: evidence,
		NativeConfigProbeFailed: failed || !a.codexNativeConnection.checked,
		Preference: func(requested state.CodexContextPreferenceRef) (state.ProfileContextPreference, bool) {
			return preferences.CodexRevision(requested.ProfileID, requested.Revision)
		},
	}
	projection, err := resolver.Resolve(*ref)
	if err != nil {
		return nil, nil, nil, err
	}
	env = config.UpsertEnvValue(env, config.CodexAppServerURLEnv, a.headlessRuntime.CodexAppServerURL)
	env = config.UpsertEnvValue(env, config.CodexRuntimeProfileIDEnv, state.NativeCodexProfileID)
	return env, args, &projection, nil
}
