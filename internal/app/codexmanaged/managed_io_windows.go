package codexmanaged

import (
	"errors"
	"io"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// FILE_SHARE_DELETE lets a concurrent atomic rename preserve the reader's old
// snapshot, matching Unix behavior instead of blocking state publication.
func readManagedJSONFile(path string) ([]byte, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	var handle windows.Handle
	for attempt := 0; attempt < 20; attempt++ {
		handle, err = windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(handle), path)
	defer file.Close()
	return io.ReadAll(file)
}

func renameManagedJSON(source, destination string) error {
	var err error
	for attempt := 0; attempt < 20; attempt++ {
		err = os.Rename(source, destination)
		if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	return err
}
