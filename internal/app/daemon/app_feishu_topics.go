package daemon

import (
	"fmt"
	"strings"
	"sync"

	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
	"github.com/kxn/codex-remote-feishu/internal/feishuidentity"
)

// Gateway callbacks never acquire App.mu: Apply may be delivering a frozen UI
// operation while another ingress action is holding that mutex.
type topicRoutingIndex struct {
	mu       sync.RWMutex
	roots    map[string]string
	native   map[string]string
	messages map[string]string
}

func topicLookupKey(gatewayID, chatID, id string) string {
	return gatewayID + "\x00" + chatID + "\x00" + id
}

func (a *App) refreshTopicRoutingIndexLocked() {
	roots, native, messages := map[string]string{}, map[string]string{}, map[string]string{}
	if a.surfaceResumeRuntime.store != nil {
		for _, entry := range a.surfaceResumeRuntime.store.Entries() {
			ref, ok := feishuidentity.ParseSurfaceRef(entry.SurfaceSessionID)
			if !ok || !ref.IsTopic() || entry.FeishuTopicOwnerID == "" {
				continue
			}
			roots[topicLookupKey(ref.GatewayID, ref.TopicChatID(), ref.TopicRootMessageID())] = ref.SurfaceID()
			if entry.FeishuTopicID != "" {
				native[topicLookupKey(ref.GatewayID, ref.TopicChatID(), entry.FeishuTopicID)] = ref.SurfaceID()
			}
			for _, id := range entry.FeishuTopicMessageIDs {
				messages[topicLookupKey(ref.GatewayID, ref.TopicChatID(), id)] = ref.SurfaceID()
			}
		}
	}
	a.topicRoutes.mu.Lock()
	a.topicRoutes.roots, a.topicRoutes.native, a.topicRoutes.messages = roots, native, messages
	a.topicRoutes.mu.Unlock()
}

func (a *App) resolveTopicSurface(gatewayID, chatID, nativeID, rootID string) string {
	a.topicRoutes.mu.RLock()
	defer a.topicRoutes.mu.RUnlock()
	byRoot := a.topicRoutes.roots[topicLookupKey(gatewayID, chatID, rootID)]
	byNative := a.topicRoutes.native[topicLookupKey(gatewayID, chatID, nativeID)]
	if byRoot != "" && byNative != "" && byRoot != byNative {
		return ""
	}
	if byRoot != "" {
		return byRoot
	}
	return byNative
}

func (a *App) lookupTopicMessageSurface(gatewayID, messageID, chatID string) string {
	a.topicRoutes.mu.RLock()
	defer a.topicRoutes.mu.RUnlock()
	return a.topicRoutes.messages[topicLookupKey(gatewayID, chatID, messageID)]
}

func (a *App) topicSurfaceLocked(surfaceID string) *state.SurfaceConsoleRecord {
	ref, ok := feishuidentity.ParseSurfaceRef(surfaceID)
	if !ok || !ref.IsTopic() {
		return nil
	}
	for _, surface := range a.service.Surfaces() {
		if surface.SurfaceSessionID == surfaceID {
			return surface
		}
	}
	return nil
}

func (a *App) persistTopicSurfaceLocked(surface *state.SurfaceConsoleRecord) error {
	if surface == nil {
		return nil
	}
	if a.surfaceResumeRuntime.store == nil || !a.surfaceResumeRuntime.writable() {
		return fmt.Errorf("topic persistence is unavailable")
	}
	entry, ok := a.currentSurfaceResumeEntryLocked(surface, false)
	if !ok {
		return fmt.Errorf("topic binding failed validation")
	}
	if err := a.surfaceResumeRuntime.store.Put(entry); err != nil {
		return fmt.Errorf("persist topic binding: %w", err)
	}
	a.refreshTopicRoutingIndexLocked()
	return nil
}

func (a *App) recordTopicDeliveryLocked(surfaceID string, operations []feishu.Operation) error {
	surface := a.topicSurfaceLocked(surfaceID)
	if surface == nil {
		return nil
	}
	ref, _ := feishuidentity.ParseSurfaceRef(surfaceID)
	capturedNativeID := surface.FeishuTopicID
	for _, op := range operations {
		if op.FeishuTopicID == "" {
			continue
		}
		if op.SurfaceSessionID != surfaceID || op.FeishuTopicRootMessageID != ref.TopicRootMessageID() || capturedNativeID != "" && capturedNativeID != op.FeishuTopicID {
			return fmt.Errorf("late topic receipt does not match captured route")
		}
		capturedNativeID = op.FeishuTopicID
	}
	for _, op := range operations {
		if op.FeishuTopicID == "" {
			continue
		}
		if op.SurfaceSessionID != surfaceID || op.FeishuTopicRootMessageID != ref.TopicRootMessageID() ||
			surface.FeishuTopicID != "" && surface.FeishuTopicID != op.FeishuTopicID {
			return fmt.Errorf("late topic receipt does not match captured route")
		}
		surface.FeishuTopicID = op.FeishuTopicID
		if op.FeishuTopicMessageURL != "" {
			surface.FeishuTopicMessageURL = op.FeishuTopicMessageURL
		}
		id := strings.TrimSpace(op.MessageID)
		found := false
		for _, known := range surface.FeishuTopicMessageIDs {
			if known == id {
				found = true
				break
			}
		}
		if id != "" && !found {
			surface.FeishuTopicMessageIDs = append(surface.FeishuTopicMessageIDs, id)
		}
		if len(surface.FeishuTopicMessageIDs) > 512 {
			surface.FeishuTopicMessageIDs = append([]string{ref.TopicRootMessageID()}, surface.FeishuTopicMessageIDs[len(surface.FeishuTopicMessageIDs)-511:]...)
		}
	}
	if err := a.persistTopicSurfaceLocked(surface); err != nil {
		// Output might already be visible, but it cannot authorize execution until
		// its route and callback ownership have been durably captured.
		surface.FeishuTopicClosed = true
		return err
	}
	return nil
}

func (a *App) recordTopicMediaDeliveryLocked(surfaceID, messageID string) error {
	topic := a.topicSurfaceLocked(surfaceID)
	if topic == nil {
		return nil
	}
	if topic.FeishuTopicID == "" || strings.TrimSpace(messageID) == "" {
		return fmt.Errorf("topic media receipt is incomplete")
	}
	return a.recordTopicDeliveryLocked(surfaceID, []feishu.Operation{{SurfaceSessionID: surfaceID, MessageID: messageID, FeishuTopicID: topic.FeishuTopicID, FeishuTopicRootMessageID: topic.FeishuTopicRootMessageID}})
}
func (a *App) recordTopicMediaDelivery(surfaceID, messageID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.recordTopicMediaDeliveryLocked(surfaceID, messageID)
}
