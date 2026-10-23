package codexupgraderuntime

import (
	"context"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/app/codexupgrade"
	"github.com/kxn/codex-remote-feishu/internal/app/sharedcodex"
)

type InspectFunc func(context.Context, codexupgrade.InspectOptions) (codexupgrade.Installation, error)

type Transaction struct {
	ID                 string
	Install            codexupgrade.Installation
	CurrentVersion     string
	TargetVersion      string
	InitiatorSurface   string
	InitiatorUserID    string
	RestartInstanceIDs []string
	PausedSurfaceIDs   map[string]bool
	StartedAt          time.Time
	Rollback           bool
	Automatic          bool
	Immediate          bool
	ConfirmationToken  string
	RecoveryRequired   bool
	SharedTargets      map[string]SharedClientTarget
}

type SharedClientTarget struct {
	InstanceID   string
	ThreadID     string
	WorkspaceKey string
	ContractID   string
}

type OwnerCardFlowStage string

const (
	OwnerFlowStageOpen             OwnerCardFlowStage = "open"
	OwnerFlowStageChecking         OwnerCardFlowStage = "checking"
	OwnerFlowStageReady            OwnerCardFlowStage = "ready"
	OwnerFlowStageRunning          OwnerCardFlowStage = "running"
	OwnerFlowStageSucceeded        OwnerCardFlowStage = "succeeded"
	OwnerFlowStageFailed           OwnerCardFlowStage = "failed"
	OwnerFlowStageImmediateConfirm OwnerCardFlowStage = "immediate_confirm"
)

type OwnerCardFlowRecord struct {
	ImmediateSupported bool
	Immediate          bool
	ImmediatePlan      *sharedcodex.ImmediatePlan
	SourceKind         codexupgrade.SourceKind
	PreviousVersion    string
	CanRollback        bool
	Rollback           bool
	FlowID             string
	SurfaceSessionID   string
	OwnerUserID        string
	MessageID          string
	Stage              OwnerCardFlowStage
	CurrentVersion     string
	LatestVersion      string
	TargetVersion      string
	Checked            bool
	HasUpdate          bool
	CanUpgrade         bool
	CreatedAt          time.Time
	UpdatedAt          time.Time
	ExpiresAt          time.Time
}

type State struct {
	SharedOwner  func(string, string) (sharedcodex.Maintenance, error)
	Inspect      InspectFunc
	LatestLookup func(context.Context) (string, error)
	Install      func(context.Context, codexupgrade.Installation, string) error
	Active       *Transaction
	NextSeq      int64
	NextFlowSeq  int64
	ActiveFlow   *OwnerCardFlowRecord
}

func NewState() State {
	return State{}
}
