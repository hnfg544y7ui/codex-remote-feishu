//go:build !linux

package relayruntime

// Retain existing ownership checks on platforms without Linux proc identities.
func lockProcessIdentity(int) (string, string) { return "", "" }

func lockOwnerReplaced(lockRecord) bool { return false }
