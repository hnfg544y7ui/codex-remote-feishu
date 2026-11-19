package codexmanaged

import "time"

// Each outer budget leaves time for validation and recovery after a slow
// official package download. Earlier caller deadlines still take precedence.
const (
	InstallTimeout = 8 * time.Minute
	PrepareTimeout = InstallTimeout + time.Minute
	UpgradeTimeout = 15 * time.Minute
)
