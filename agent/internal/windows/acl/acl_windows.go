//go:build windows

package acl

import (
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/canblmz1/stowline/agent/internal/windows/identity"
)

// Profile is an explicit DACL template. ACL changes are deliberate, not per-CLI.
type Profile string

const (
	PilotWorking   Profile = "pilot-working"
	ServiceSecret  Profile = "service-secret"
	ServiceState   Profile = "service-state"
	RestoreStaging Profile = "restore-staging"
)

// RestrictToAdminSystemAndOwner is the historical pilot convenience ACL
// (SYSTEM + Administrators + current interactive user). Prefer Apply with an
// explicit Profile.
func RestrictToAdminSystemAndOwner(path string) error {
	return Apply(path, PilotWorking)
}

// Apply replaces the DACL for path with the named profile. It is idempotent.
func Apply(path string, profile Profile) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !st.IsDir() && !st.Mode().IsRegular() {
		return fmt.Errorf("acl: unsupported path type")
	}
	admin, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	entries := []windows.EXPLICIT_ACCESS{
		grantAll(system, windows.TRUSTEE_IS_WELL_KNOWN_GROUP),
		grantAll(admin, windows.TRUSTEE_IS_WELL_KNOWN_GROUP),
	}
	switch profile {
	case PilotWorking, RestoreStaging:
		owner, err := currentUserSID()
		if err != nil {
			return err
		}
		entries = append(entries, grantAll(owner, windows.TRUSTEE_IS_USER))
	case ServiceSecret:
		// SYSTEM + Administrators only. Ordinary interactive users get no ACE.
	case ServiceState:
		// SYSTEM + Administrators only. Diagnostic read by the operator is via
		// stowline-agent status running as that identity, not a Users ACE on secrets.
	default:
		return fmt.Errorf("acl: unknown profile %q", profile)
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return fmt.Errorf("ACLFromEntries: %w", err)
	}
	return windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	)
}

func Inspect(path string) (Inspection, error) {
	var out Inspection
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return out, err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return out, err
	}
	if dacl == nil {
		return out, nil
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return out, err
	}
	admin, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return out, err
	}
	cur, err := currentUserSID()
	if err != nil {
		return out, err
	}
	sysStr := system.String()
	adminStr := admin.String()
	curStr := cur.String()
	count := aceCount(dacl)
	for i := uint32(0); i < count; i++ {
		view, err := parseACE(dacl, i)
		if err != nil {
			view = ACEView{Unparsed: true}
		}
		out.ACEs = append(out.ACEs, view)
		if view.SID == "" {
			continue
		}
		out.SIDs = append(out.SIDs, view.SID)
		if identity.IsLocalSystemSID(view.SID) || strings.EqualFold(view.SID, sysStr) {
			out.HasSystem = true
		}
		if identity.IsBuiltinAdministratorsSID(view.SID) || strings.EqualFold(view.SID, adminStr) {
			out.HasAdministrators = true
		}
		if strings.EqualFold(view.SID, curStr) {
			out.HasCurrentUser = true
		}
	}
	return out, nil
}

func parseACE(acl *windows.ACL, idx uint32) (ACEView, error) {
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(acl, idx, &ace); err != nil || ace == nil {
		return ACEView{Unparsed: true}, err
	}
	const (
		accessAllowedACEType = 0x0
		accessDeniedACEType  = 0x1
		inheritedACE         = 0x10
		inheritOnlyACE       = 0x8
	)
	view := ACEView{
		Mask:        uint32(ace.Mask),
		Inherited:   ace.Header.AceFlags&inheritedACE != 0,
		InheritOnly: ace.Header.AceFlags&inheritOnlyACE != 0,
	}
	switch ace.Header.AceType {
	case accessAllowedACEType:
		view.Allow = true
	case accessDeniedACEType:
		view.Deny = true
	default:
		view.Unparsed = true
		return view, nil
	}
	sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	view.SID = sid.String()
	return view, nil
}

func aceCount(acl *windows.ACL) uint32 {
	if acl == nil {
		return 0
	}
	// ACL header: revision(1) sbz1(1) size(2) aceCount(2) sbz2(2)
	type aclHeader struct {
		revision byte
		sbz1     byte
		size     uint16
		count    uint16
		sbz2     uint16
	}
	h := (*aclHeader)(unsafe.Pointer(acl))
	return uint32(h.count)
}

func currentUserSID() (*windows.SID, error) {
	tok := windows.GetCurrentProcessToken()
	tu, err := tok.GetTokenUser()
	if err != nil {
		return nil, err
	}
	return tu.User.Sid, nil
}

func grantAll(sid *windows.SID, kind windows.TRUSTEE_TYPE) windows.EXPLICIT_ACCESS {
	return windows.EXPLICIT_ACCESS{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  kind,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
}
