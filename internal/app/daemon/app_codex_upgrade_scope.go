package daemon

import (
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/config"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func standaloneCodexUpgradeAffectsInstance(inst *state.InstanceRecord) bool {
	if inst == nil {
		return false
	}
	return !state.IsVSCodeOrDefaultSource(inst.Source)
}

func (a *App) standaloneCodexUpgradeAffectsInstanceIDLocked(instanceID string) bool {
	if strings.TrimSpace(instanceID) == "" {
		return false
	}
	return a.codexUpgradeAffectsInstanceLocked(a.service.Instance(instanceID))
}

func (a *App) codexUpgradeAffectsInstanceLocked(inst *state.InstanceRecord) bool {
	if !standaloneCodexUpgradeAffectsInstance(inst) {
		return false
	}
	if a.headlessRuntime.CodexRuntime.Source != config.CodexRuntimeSourceManaged {
		return true
	}
	return inst.Managed && (inst.Backend == "" || inst.Backend == agentproto.BackendCodex)
}

func (a *App) standaloneCodexUpgradeAffectsSurfaceLocked(surface *state.SurfaceConsoleRecord) bool {
	if surface == nil {
		return false
	}
	if instanceID := strings.TrimSpace(surface.AttachedInstanceID); instanceID != "" {
		if inst := a.service.Instance(instanceID); inst != nil {
			return a.codexUpgradeAffectsInstanceLocked(inst)
		}
	}
	if a.headlessRuntime.CodexRuntime.Source == config.CodexRuntimeSourceManaged && surface.Backend != "" && surface.Backend != agentproto.BackendCodex {
		return false
	}
	return state.NormalizeProductMode(surface.ProductMode) != state.ProductModeVSCode
}

func (a *App) standaloneCodexUpgradeAffectsSurfaceIDLocked(surfaceID string) bool {
	if strings.TrimSpace(surfaceID) == "" {
		return false
	}
	return a.standaloneCodexUpgradeAffectsSurfaceLocked(a.service.Surface(surfaceID))
}
