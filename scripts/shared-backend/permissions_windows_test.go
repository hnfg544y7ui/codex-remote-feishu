package sharedbackend

import (
	"context"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestHostExportRejectsUnexpectedWindowsFileACLWithoutRepair(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sources")
	m, err := Export(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;FR;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(m.Entrypoint, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Export(context.Background(), root); err == nil {
		t.Fatal("existing source with public Windows ACL was accepted")
	}
	if err := privatePath(m.Entrypoint, false); err == nil {
		t.Fatal("existing unsafe ACL was silently repaired")
	}
}
