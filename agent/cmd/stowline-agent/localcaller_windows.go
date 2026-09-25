//go:build windows

package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	modIphlpapi             = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedTcpTable = modIphlpapi.NewProc("GetExtendedTcpTable")
)

const (
	tcpTableOwnerPidAll   = 5
	errInsufficientBuffer = 122
	profileListKey        = `SOFTWARE\Microsoft\Windows NT\CurrentVersion\ProfileList`
)

// mibTCPRowOwnerPID is MIB_TCPROW_OWNER_PID. Ports are in network byte
// order in the low 16 bits; addresses are in network byte order.
type mibTCPRowOwnerPID struct {
	State      uint32
	LocalAddr  uint32
	LocalPort  uint32
	RemoteAddr uint32
	RemotePort uint32
	OwningPID  uint32
}

func netPort(v uint32) uint16 {
	b := [2]byte{byte(v), byte(v >> 8)}
	return binary.BigEndian.Uint16(b[:])
}

// pidOfLoopbackClient finds the process that owns the client end of a
// loopback TCP connection to serverPort from clientPort.
func pidOfLoopbackClient(clientPort, serverPort uint16) (uint32, error) {
	size := uint32(64 * 1024)
	for attempt := 0; attempt < 4; attempt++ {
		buf := make([]byte, size)
		r, _, _ := procGetExtendedTcpTable.Call(
			uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0,
			uintptr(windows.AF_INET), tcpTableOwnerPidAll, 0)
		if r == errInsufficientBuffer {
			size += 16 * 1024
			continue
		}
		if r != 0 {
			return 0, fmt.Errorf("GetExtendedTcpTable: %d", r)
		}
		n := *(*uint32)(unsafe.Pointer(&buf[0]))
		rowSize := unsafe.Sizeof(mibTCPRowOwnerPID{})
		for i := uint32(0); i < n; i++ {
			off := 4 + uintptr(i)*rowSize
			if off+rowSize > uintptr(len(buf)) {
				break
			}
			row := (*mibTCPRowOwnerPID)(unsafe.Pointer(&buf[off]))
			if netPort(row.LocalPort) == clientPort && netPort(row.RemotePort) == serverPort && row.OwningPID != 0 {
				return row.OwningPID, nil
			}
		}
		return 0, errCallerUnknown
	}
	return 0, errCallerUnknown
}

func sidOfProcess(pid uint32) (string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	var tok windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &tok); err != nil {
		return "", err
	}
	defer tok.Close()
	u, err := tok.GetTokenUser()
	if err != nil {
		return "", err
	}
	return u.User.Sid.String(), nil
}

// windowsProfiles reads every account's profile folder and the profiles
// folder itself from the registry.
func windowsProfiles() (bySID map[string]string, profilesDir string, err error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, profileListKey, registry.READ)
	if err != nil {
		return nil, "", err
	}
	defer k.Close()
	if dir, _, e := k.GetStringValue("ProfilesDirectory"); e == nil {
		profilesDir, _ = registry.ExpandString(dir)
	}
	names, err := k.ReadSubKeyNames(-1)
	if err != nil {
		return nil, "", err
	}
	bySID = map[string]string{}
	for _, sid := range names {
		sk, e := registry.OpenKey(k, sid, registry.QUERY_VALUE)
		if e != nil {
			continue
		}
		if p, _, e := sk.GetStringValue("ProfileImagePath"); e == nil && p != "" {
			if x, e := registry.ExpandString(p); e == nil {
				p = x
			}
			bySID[sid] = p
		}
		sk.Close()
	}
	return bySID, profilesDir, nil
}

// resolveLocalCaller ties a request on the loopback listener to the Windows
// account of the process that opened the connection.
func resolveLocalCaller(r *http.Request) (localCaller, error) {
	_, cport, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return localCaller{}, errCallerUnknown
	}
	local, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok {
		return localCaller{}, errCallerUnknown
	}
	_, sport, err := net.SplitHostPort(local.String())
	if err != nil {
		return localCaller{}, errCallerUnknown
	}
	cp, err1 := strconv.ParseUint(cport, 10, 16)
	sp, err2 := strconv.ParseUint(sport, 10, 16)
	if err1 != nil || err2 != nil {
		return localCaller{}, errCallerUnknown
	}
	pid, err := pidOfLoopbackClient(uint16(cp), uint16(sp))
	if err != nil {
		return localCaller{}, errCallerUnknown
	}
	sid, err := sidOfProcess(pid)
	if err != nil {
		return localCaller{}, errCallerUnknown
	}
	profiles, dir, err := windowsProfiles()
	if err != nil {
		return localCaller{}, errCallerUnknown
	}
	all := make([]string, 0, len(profiles))
	for _, p := range profiles {
		all = append(all, p)
	}
	return newLocalCaller(sid, profiles[sid], dir, all), nil
}

// grantReadTo gives one account inheritable read access to dir: the person
// who asked for the restore, not every user of the PC.
func grantReadTo(dir, sid string) error {
	if sid == "" {
		return errCallerUnknown
	}
	who, err := windows.StringToSid(sid)
	if err != nil {
		return err
	}
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_READ | windows.GENERIC_EXECUTE,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(who),
		},
	}}, dacl)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}
