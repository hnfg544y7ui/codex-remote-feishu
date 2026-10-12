package orchestrator

import "strings"

// User mutations transfer Goal ownership back to the user even when their RPC
// later fails. Keep outstanding pause/probe/get acknowledgements and the queue
// barrier; dropping the lease here would bypass quiescence and delivery gates.
func (s *Service) revokeGoalAutoResumeForUserMutation(instanceID, threadID string) {
	if s == nil || strings.TrimSpace(instanceID) == "" || strings.TrimSpace(threadID) == "" {
		return
	}
	lease := s.goalInterlockLease(instanceID, threadID)
	if lease == nil {
		return
	}
	lease.ExternalMutationSeen = true
	lease.UpdatedAt = s.now()
}
