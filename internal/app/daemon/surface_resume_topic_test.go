package daemon

import (
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/app/daemon/surfaceresume"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/feishuidentity"
)

func TestTopicResumeMaterializesMetadataAndSkipsBackgroundRecovery(t *testing.T) {
	stateDir := t.TempDir()
	entry := surfaceresume.Entry{SurfaceSessionID: feishuidentity.TopicRef("app-1", "oc_room", "om_root").SurfaceID(),
		GatewayID: "app-1", ChatID: "oc_room", ActorUserID: "ou_owner", ProductMode: "normal", Backend: "codex",
		ResumeThreadID: "thread-a", ResumeWorkspaceKey: t.TempDir(), ResumeRouteMode: "pinned", ResumeHeadless: true,
		FeishuTopicID: "omt_native", FeishuTopicTitle: "测试", FeishuTopicOwnerID: "ou_owner",
		FeishuTopicMessageIDs: []string{"om_root", "om_card"}, FeishuTopicMessageURL: "https://example.invalid/message"}
	putSurfaceResumeStateForTest(t, stateDir, entry)
	app := newRestoreHintTestApp(stateDir)
	surface := app.surfaceByIDLocked(entry.SurfaceSessionID)
	if surface == nil || surface.FeishuTopicID != "omt_native" || surface.FeishuTopicBoundThreadID != "thread-a" ||
		surface.FeishuTopicRootMessageID != "om_root" || surface.FeishuTopicOwnerID != "ou_owner" || len(surface.FeishuTopicMessageIDs) != 2 {
		t.Fatalf("topic metadata not materialized: %#v", surface)
	}
	if _, ok := app.surfaceResumeRuntime.recovery[entry.SurfaceSessionID]; ok {
		t.Fatal("topic entered background recovery")
	}
	if surfaceAllowsDaemonLifecycleNotice(surface) {
		t.Fatal("topic lifecycle notices should be suppressed")
	}
	entry.FeishuTopicBoundThreadID = "thread-b"
	entry.ResumeThreadID = "thread-b"
	app.materializeSurfaceResumeEntryLocked(entry)
	if surface.FeishuTopicBoundThreadID != "thread-a" {
		t.Fatal("re-materialization rebound topic")
	}
	entry.FeishuTopicBoundThreadID = "thread-a"
	entry.ResumeThreadID = "thread-a"
	entry.FeishuTopicOwnerID = "ou_other"
	app.materializeSurfaceResumeEntryLocked(entry)
	if surface.FeishuTopicOwnerID != "ou_owner" {
		t.Fatal("re-materialization replaced owner")
	}
	current, ok := app.currentSurfaceResumeEntryLocked(surface, false)
	if !ok || current.FeishuTopicID != "omt_native" || current.FeishuTopicBoundThreadID != "thread-a" {
		t.Fatalf("topic projection lost metadata: %#v", current)
	}
	current.FeishuTopicMessageIDs[0] = "mutated"
	if surface.FeishuTopicMessageIDs[0] != "om_root" {
		t.Fatal("resume projection aliases surface IDs")
	}
}

func TestTopicResumePendingUnboundAndClosedRecoveryPolicy(t *testing.T) {
	stateDir := t.TempDir()
	entry := surfaceresume.Entry{SurfaceSessionID: feishuidentity.TopicRef("app-1", "oc_room", "om_pending").SurfaceID(),
		GatewayID: "app-1", ActorUserID: "ou_owner", FeishuTopicOwnerID: "ou_owner", ProductMode: "normal"}
	putSurfaceResumeStateForTest(t, stateDir, entry)
	app := newRestoreHintTestApp(stateDir)
	surface := app.surfaceByIDLocked(entry.SurfaceSessionID)
	if surface == nil || surface.FeishuTopicID != "" || surface.FeishuTopicRootMessageID != "om_pending" {
		t.Fatal("pending topic was discarded on restart")
	}
	current, ok := app.currentSurfaceResumeEntryLocked(surface, false)
	if !ok || current.ResumeThreadID != "" {
		t.Fatal("unbound topic cannot be persisted")
	}
	action := control.Action{Kind: control.ActionTextMessage, SurfaceSessionID: entry.SurfaceSessionID}
	if surfaceResumeEntryAllowsOnDemandRecovery(current, action) {
		t.Fatal("unbound topic should not launch a runtime")
	}
	current.FeishuTopicClosed = true
	if surfaceResumeEntryAllowsOnDemandRecovery(current, action) || surfaceResumeEntryAllowsBackgroundRecovery(current) {
		t.Fatal("closed topic allows recovery")
	}
	app.materializeSurfaceResumeEntryLocked(current)
	if !surface.FeishuTopicClosed {
		t.Fatal("closed state was not restored")
	}
}
