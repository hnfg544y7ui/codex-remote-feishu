package codexprofile

import (
	"reflect"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func sharedNativeResolverFixture(mode string) RuntimeResolver {
	return RuntimeResolver{
		SharedBackend: true,
		Native: state.CodexNativeConnectionEvidence{
			Revision: 3, ConnectionGeneration: 7, ModelProviderID: "actual-shared-provider",
			ModelEndpointID: "actual-shared-model-endpoint", ChatGPTEndpointID: "actual-shared-chatgpt-endpoint",
		},
		Preference: fixedPreferenceLookup(state.ProfileContextPreference{
			ProfileID: state.NativeCodexProfileID, Revision: 2, Mode: mode,
		}),
	}
}

func TestSharedRuntimeResolverInheritsNativeWithoutPrivateProfileCapability(t *testing.T) {
	resolver := sharedNativeResolverFixture(state.CodexContextModeDefault)
	// These represent a private CLI that was not probed or is unavailable.
	resolver.CapabilityErrorCode = ErrorCodexBinaryUnavailable
	resolver.CapabilityErrorStage = "private_binary_missing"
	projection, err := resolver.Resolve(admissionRef(state.NativeCodexProfileID, 3, 2))
	if err != nil {
		t.Fatal(err)
	}
	if projection.Connection.CapabilitySet != SharedNativeCapabilitySetV1 ||
		projection.Connection.CapabilitySet == CodexProfileCapabilitySetV1 ||
		projection.Connection.Kind != state.CodexProfileKindNative ||
		projection.Connection.ConnectionGeneration != 7 ||
		projection.Connection.ModelProviderID != resolver.Native.ModelProviderID ||
		projection.Connection.ModelEndpointID != resolver.Native.ModelEndpointID ||
		projection.Connection.ChatGPTEndpointID != resolver.Native.ChatGPTEndpointID {
		t.Fatal("shared projection did not preserve observed backend evidence and separate capability contract")
	}
	if !reflect.DeepEqual(projection.Launch, SecretLaunchMaterial{}) {
		t.Fatal("shared native projection produced private child launch material")
	}
}

func TestSharedRuntimeResolverPreservesNativeContextThreadPolicy(t *testing.T) {
	for _, mode := range []string{state.CodexContextModeDefault, state.CodexContextModePrice272K, state.CodexContextModeExtended} {
		t.Run(string(mode), func(t *testing.T) {
			resolver := sharedNativeResolverFixture(mode)
			ref := admissionRef(state.NativeCodexProfileID, 3, 2)
			shared, err := resolver.Resolve(ref)
			if err != nil {
				t.Fatal(err)
			}
			resolver.SharedBackend = false
			resolver.CapabilitySet = CodexProfileCapabilitySetV1
			private, err := resolver.Resolve(ref)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(shared.Thread, private.Thread) || shared.Thread.ThreadPolicyID == "" {
				t.Fatal("shared backend changed existing native context/thread RPC policy")
			}
			if shared.Connection.ConnectionContractID == private.Connection.ConnectionContractID {
				t.Fatal("shared native capability contract reused private profile capability identity")
			}
			if !reflect.DeepEqual(shared.Launch, SecretLaunchMaterial{}) {
				t.Fatal("context overrides leaked into child launch material")
			}
		})
	}
}

func TestSharedRuntimeResolverRejectsAPIAndOAuthBeforePrivateResolution(t *testing.T) {
	for _, id := range []string{"cp_api", state.OAuthCodexProfileID} {
		resolver := sharedNativeResolverFixture(state.CodexContextModeDefault)
		resolver.CapabilitySet = CodexProfileCapabilitySetV1
		resolver.Preference = func(state.CodexContextPreferenceRef) (state.ProfileContextPreference, bool) {
			t.Fatal("shared mode consulted non-native preference resolver")
			return state.ProfileContextPreference{}, false
		}
		_, err := resolver.Resolve(admissionRef(id, 1, 1))
		if RuntimeErrorCode(err) != ErrorSharedBackendProfileUnsupported {
			t.Fatal("shared backend allowed a per-client credential profile")
		}
	}
}

func TestSharedRuntimeResolverRejectsInexactAdmissionAndEvidence(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*RuntimeResolver, *state.CodexAdmissionRef)
	}{
		{"native_revision", func(_ *RuntimeResolver, ref *state.CodexAdmissionRef) { ref.ProfileRef.Revision = 4 }},
		{"native_revision_zero", func(_ *RuntimeResolver, ref *state.CodexAdmissionRef) { ref.ProfileRef.Revision = 0 }},
		{"generation_zero", func(r *RuntimeResolver, _ *state.CodexAdmissionRef) { r.Native.ConnectionGeneration = 0 }},
		{"preference_revision", func(_ *RuntimeResolver, ref *state.CodexAdmissionRef) { ref.ContextPreferenceRef.Revision = 3 }},
		{"preference_revision_zero", func(_ *RuntimeResolver, ref *state.CodexAdmissionRef) { ref.ContextPreferenceRef.Revision = 0 }},
		{"preference_profile", func(_ *RuntimeResolver, ref *state.CodexAdmissionRef) {
			ref.ContextPreferenceRef.ProfileID = "cp_other"
		}},
		{"preference_missing", func(r *RuntimeResolver, _ *state.CodexAdmissionRef) { r.Preference = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolver := sharedNativeResolverFixture(state.CodexContextModeDefault)
			ref := admissionRef(state.NativeCodexProfileID, 3, 2)
			test.edit(&resolver, &ref)
			_, err := resolver.Resolve(ref)
			if RuntimeErrorCode(err) != ErrorProfileRevisionUnavailable {
				t.Fatal("shared backend accepted stale or incomplete profile/context evidence")
			}
		})
	}
}

func TestSharedRuntimeResolverRejectsFailedNativeObservation(t *testing.T) {
	resolver := sharedNativeResolverFixture(state.CodexContextModeDefault)
	resolver.NativeConfigProbeFailed = true
	_, err := resolver.Resolve(admissionRef(state.NativeCodexProfileID, 3, 2))
	if RuntimeErrorCode(err) != ErrorCodexProbeUnavailable || RuntimeErrorStage(err) != "shared_native_config" {
		t.Fatal("shared projection admitted stale backend evidence after failed observation")
	}
}

func TestSharedRuntimeResolverDefaultStillRequiresPrivateCapability(t *testing.T) {
	resolver := sharedNativeResolverFixture(state.CodexContextModeDefault)
	resolver.SharedBackend = false
	_, err := resolver.Resolve(admissionRef(state.NativeCodexProfileID, 3, 2))
	if RuntimeErrorCode(err) != ErrorCodexCapabilityUnsupported {
		t.Fatal("default resolver bypassed existing private profile capability gate")
	}
}
