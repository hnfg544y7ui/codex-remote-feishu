//go:build linux || darwin

package codexmanaged

import "os"

func readManagedJSONFile(path string) ([]byte, error)    { return os.ReadFile(path) }
func renameManagedJSON(source, destination string) error { return os.Rename(source, destination) }
