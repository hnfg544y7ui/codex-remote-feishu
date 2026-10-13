package orchestrator

import (
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func topicWorkspacePrivateSurface(svc *Service, actor string) *state.SurfaceConsoleRecord {
	return svc.ensureSurface(control.Action{SurfaceSessionID: "feishu:bot:user:" + actor, GatewayID: "bot", ChatID: "oc_private_" + actor, ActorUserID: actor})
}

func TestTopicWorkspaceClaimsAllowPrivateAndTopicsInEitherOrder(t *testing.T) {
	for _, topicFirst := range []bool{false, true} {
		name := "private-first"
		if topicFirst {
			name = "topic-first"
		}
		t.Run(name, func(t *testing.T) {
			svc := topicTestService()
			private := topicWorkspacePrivateSurface(svc, "private-owner")
			topic := topicTestCreate(t, svc, "om_a")
			otherTopic := topicTestCreate(t, svc, "om_b")
			workspace := normalizeWorkspaceClaimKey(t.TempDir())
			first, second := private, topic
			if topicFirst {
				first, second = topic, private
			}
			for _, surface := range []*state.SurfaceConsoleRecord{first, second, otherTopic} {
				if !svc.claimWorkspace(surface, workspace) {
					t.Fatalf("workspace claim denied for %s", surface.SurfaceSessionID)
				}
				if !svc.surfaceUsesWorkspaceClaims(surface) || svc.surfaceCurrentWorkspaceKey(surface) != workspace {
					t.Fatalf("project metadata lost for %s", surface.SurfaceSessionID)
				}
			}
			if owner := svc.workspaceClaimSurface(workspace); owner != private {
				t.Fatalf("exclusive owner = %#v, want private", owner)
			}
			if svc.workspaceBusyOwnerForSurface(topic, workspace) != nil || svc.workspaceBusyOwnerForSurface(otherTopic, workspace) != nil {
				t.Fatal("topic project rejected by private ownership")
			}
			blockedPrivate := topicWorkspacePrivateSurface(svc, "another-private")
			if svc.claimWorkspace(blockedPrivate, workspace) || svc.workspaceBusyOwnerForSurface(blockedPrivate, workspace) != private {
				t.Fatal("topic sharing disabled legacy private exclusivity")
			}
			claim := svc.workspaceClaims[workspace]
			svc.releaseSurfaceWorkspaceClaim(topic)
			svc.releaseSurfaceWorkspaceClaim(otherTopic)
			if topic.ClaimedWorkspaceKey != "" || otherTopic.ClaimedWorkspaceKey != "" || svc.workspaceClaims[workspace] != claim || svc.workspaceClaimSurface(workspace) != private {
				t.Fatal("topic release changed private workspace claim")
			}
		})
	}
}

func TestTopicWorkspaceClaimsDoNotBecomeFallbackExclusiveOwners(t *testing.T) {
	svc := topicTestService()
	topic := topicTestCreate(t, svc, "om_fallback")
	workspace := normalizeWorkspaceClaimKey(t.TempDir())
	topic.ClaimedWorkspaceKey = workspace
	svc.bindWorkspaceClaim(topic, workspace)
	if len(svc.workspaceClaims) != 0 || svc.workspaceClaimSurface(workspace) != nil || svc.workspaceClaimOwner(workspace).valid() || svc.explicitWorkspaceClaimOwnerWithoutCurrentFallback(workspace).valid() {
		t.Fatal("topic metadata became exclusive ownership through fallback")
	}
	if svc.surfaceMatchesWorkspaceClaimOwner(topic, workspace, svc.workspaceClaimOwnerForSurface(topic)) || svc.findExplicitWorkspaceClaimSurface(workspace, svc.workspaceClaimOwnerForSurface(topic)) != nil {
		t.Fatal("topic accepted as exclusive claim candidate")
	}
	if svc.claimWorkspace(topic, "") {
		t.Fatal("empty project accepted")
	}
	if svc.surfaceCurrentWorkspaceKey(topic) != workspace {
		t.Fatal("invalid claim destroyed existing project metadata")
	}
}

func TestTopicWorkspaceClaimsDiscardOldExclusiveRecords(t *testing.T) {
	for _, scope := range []workspaceClaimOwnerScope{"", workspaceClaimOwnerSurface, workspaceClaimOwnerRoom} {
		t.Run(string(scope), func(t *testing.T) {
			svc := topicTestService()
			topic := topicTestCreate(t, svc, "om_old")
			workspace := normalizeWorkspaceClaimKey(t.TempDir())
			topic.ClaimedWorkspaceKey = workspace
			ownerID := topic.SurfaceSessionID
			if scope == workspaceClaimOwnerRoom {
				ownerID = surfaceFeishuRoomID(topic)
			}
			svc.workspaceClaims[workspace] = &workspaceClaimRecord{WorkspaceKey: workspace, SurfaceSessionID: topic.SurfaceSessionID, OwnerScope: scope, OwnerID: ownerID}
			if svc.workspaceClaimSurface(workspace) != nil || svc.workspaceClaimOwner(workspace).valid() || svc.workspaceClaims[workspace] != nil {
				t.Fatal("old topic record retained exclusive ownership")
			}
			private := topicWorkspacePrivateSurface(svc, "private-owner")
			if !svc.claimWorkspace(private, workspace) {
				t.Fatal("old topic record blocked legacy claim")
			}
		})
	}
}

func TestTopicWorkspaceClaimsPreservePlainRoomExclusivity(t *testing.T) {
	svc := topicTestService()
	room := svc.ensureSurface(control.Action{SurfaceSessionID: "feishu:bot:chat:oc_legacy", GatewayID: "bot", ChatID: "oc_legacy", ActorUserID: "owner"})
	otherRoom := svc.ensureSurface(control.Action{SurfaceSessionID: "feishu:bot:chat:oc_other", GatewayID: "bot", ChatID: "oc_other", ActorUserID: "owner"})
	private := topicWorkspacePrivateSurface(svc, "private-owner")
	topic := topicTestCreate(t, svc, "om_shared")
	workspace := normalizeWorkspaceClaimKey(t.TempDir())
	if !svc.claimWorkspace(room, workspace) || !svc.claimWorkspace(topic, workspace) || svc.claimWorkspace(private, workspace) || svc.claimWorkspace(otherRoom, workspace) {
		t.Fatal("plain-room exclusivity changed")
	}
	if svc.workspaceBusyOwnerForSurface(private, workspace) != room || svc.workspaceBusyOwnerForSurface(otherRoom, workspace) != room {
		t.Fatal("plain-room owner missing from busy projection")
	}
	svc.releaseSurfaceWorkspaceClaim(topic)
	if svc.workspaceClaimSurface(workspace) != room {
		t.Fatal("topic release cleared legacy room owner")
	}
}

func TestTopicWorkspaceSharingRetainsInstanceAndThreadExclusivity(t *testing.T) {
	svc := topicTestService()
	private := topicWorkspacePrivateSurface(svc, "private-owner")
	topic := topicTestCreate(t, svc, "om_routing")
	workspace := normalizeWorkspaceClaimKey(t.TempDir())
	inst := &state.InstanceRecord{InstanceID: "inst_shared", WorkspaceKey: workspace, WorkspaceRoot: workspace, Threads: map[string]*state.ThreadRecord{"thread_shared": {ThreadID: "thread_shared", CWD: workspace}}}
	svc.UpsertInstance(inst)
	if !svc.claimWorkspace(private, workspace) || !svc.claimWorkspace(topic, workspace) {
		t.Fatal("project sharing refused")
	}
	private.AttachedInstanceID = inst.InstanceID
	private.SelectedThreadID = "thread_shared"
	if !svc.claimInstance(private, inst.InstanceID) || !svc.claimThread(private, inst, "thread_shared") {
		t.Fatal("private failed to claim runtime")
	}
	if svc.claimInstance(topic, inst.InstanceID) || svc.claimThread(topic, inst, "thread_shared") || svc.claimKnownThread(topic, inst, "thread_shared") {
		t.Fatal("project sharing allowed runtime or conversation takeover")
	}
	svc.releaseSurfaceWorkspaceClaim(topic)
	if svc.instanceClaimSurface(inst.InstanceID) != private || svc.threadClaimSurface("thread_shared") != private {
		t.Fatal("topic project release changed runtime ownership")
	}
}
