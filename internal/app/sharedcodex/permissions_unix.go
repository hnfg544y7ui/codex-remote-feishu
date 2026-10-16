//go:build !windows

package sharedcodex

import "os"

func privateEnrollment(_ string, info os.FileInfo) bool {
	return info.Mode().Perm()&0077 == 0
}
