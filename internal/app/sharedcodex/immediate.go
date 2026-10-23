package sharedcodex

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"
)

// ImmediatePlan is the owner's short-lived, frozen description of the work
// that an explicitly confirmed immediate upgrade may interrupt.
type ImmediatePlan struct {
	Token             string    `json:"token"`
	ExpiresAt         time.Time `json:"expiresAt"`
	Version           string    `json:"version"`
	TargetVersion     string    `json:"targetVersion"`
	Generation        uint64    `json:"generation"`
	ActiveThreads     int       `json:"activeThreads"`
	ActiveGoals       int       `json:"activeGoals"`
	UncertainRequests int       `json:"uncertainRequests"`
	PendingRequests   int       `json:"pendingRequests"`
	ThreadNames       []string  `json:"threadNames"`
}

func validImmediateToken(token string) bool {
	if len(token) != 64 {
		return false
	}
	_, err := hex.DecodeString(token)
	return err == nil
}

func (c *Client) PreviewImmediate(ctx context.Context, upgrade Upgrade) (ImmediatePlan, error) {
	if upgrade.Version == "" || upgrade.ExpectedVersion == "" || upgrade.Rollback || upgrade.Immediate || upgrade.ConfirmationToken != "" {
		return ImmediatePlan{}, fmt.Errorf("立即升级预览必须指定当前版本和目标版本")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Keep preview wire separate from execution, including the legacy rollback
	// field: this request is never an authorization to stop native work.
	request := struct {
		Version         string `json:"version"`
		ExpectedVersion string `json:"expectedVersion"`
	}{upgrade.Version, upgrade.ExpectedVersion}
	status, err := c.request(ctx, http.MethodPost, "/v1/upgrade/immediate/preview", request)
	if err != nil {
		return ImmediatePlan{}, err
	}
	plan := status.ImmediatePlan
	now := time.Now()
	if !status.ImmediateUpgradeSupported || plan == nil || status.Version != upgrade.ExpectedVersion ||
		plan.Version != status.Version || plan.TargetVersion != upgrade.Version ||
		status.Generation == 0 || plan.Generation != status.Generation || !validImmediateToken(plan.Token) ||
		!plan.ExpiresAt.After(now) || plan.ExpiresAt.After(now.Add(5*time.Minute)) ||
		plan.ActiveThreads < 0 || plan.ActiveGoals < 0 || plan.UncertainRequests < 0 || plan.PendingRequests < 0 {
		return ImmediatePlan{}, fmt.Errorf("共享后端立即升级预览无效或已过期，请重新检查")
	}
	return *plan, nil
}
