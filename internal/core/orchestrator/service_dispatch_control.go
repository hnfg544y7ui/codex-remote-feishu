package orchestrator

import (
	"strings"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

// HoldSurfaceDispatch is a maintenance barrier independent of route/execution
// gates. Disconnect and rebind reset those gates but must not resume work until
// the maintenance caller has confirmed recovery and explicitly released it.
func (s *Service) HoldSurfaceDispatch(surfaceID string) {
	surfaceID = strings.TrimSpace(surfaceID)
	if surfaceID == "" {
		return
	}
	if s.surfaceDispatchHolds == nil {
		s.surfaceDispatchHolds = make(map[string]bool)
	}
	s.surfaceDispatchHolds[surfaceID] = true
}

// ReleaseSurfaceDispatchHold only removes the barrier. It never dispatches,
// clears a queue, or resumes any interrupted native task.
func (s *Service) ReleaseSurfaceDispatchHold(surfaceID string) {
	delete(s.surfaceDispatchHolds, strings.TrimSpace(surfaceID))
}

func (s *Service) surfaceDispatchHeld(surface *state.SurfaceConsoleRecord) bool {
	return surface != nil && s.surfaceDispatchHolds[surface.SurfaceSessionID]
}

func (s *Service) PauseSurfaceDispatch(surfaceID string) {
	surface := s.root.Surfaces[strings.TrimSpace(surfaceID)]
	if surface == nil {
		return
	}
	s.pauseSurfaceDispatchForLocal(surface, time.Time{})
}

func (s *Service) ResumeSurfaceDispatch(surfaceID string, notice *control.Notice) []eventcontract.Event {
	surface := s.root.Surfaces[strings.TrimSpace(surfaceID)]
	if surface == nil {
		return nil
	}
	s.restoreSurfaceDispatchNormal(surface)
	var events []eventcontract.Event
	if notice != nil && (strings.TrimSpace(notice.Code) != "" || strings.TrimSpace(notice.Title) != "" || strings.TrimSpace(notice.Text) != "") {
		events = append(events, eventcontract.Event{
			Kind:             eventcontract.KindNotice,
			SurfaceSessionID: surface.SurfaceSessionID,
			Notice:           notice,
		})
	}
	events = append(events, s.dispatchNext(surface)...)
	return s.filterEventsForSurfaceVisibility(events)
}
