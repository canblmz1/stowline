package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// The local page (Stowline Backups) is served by this SYSTEM service to
// whoever is signed in to the PC. On a shared PC that is several people, so
// every request is tied to the Windows account that sent it (see
// resolveLocalCaller) and that person only ever sees, searches and restores
// their own profile and folders outside anyone's profile -- never another
// account's C:\Users\<name>.

var errCallerUnknown = errors.New("could not tell which Windows account sent this request")

// localCaller is the Windows account behind one local request. Paths are in
// restic's snapshot form ("/C/Users/ayse"), lower-cased.
type localCaller struct {
	SID string
	// Profile is this account's own profile folder ("" if it has none).
	Profile string
	// ProfilesDir is the folder profiles live in, normally /c/users.
	ProfilesDir string
	// OtherProfiles are every other account's profile folders.
	OtherProfiles []string
}

type callerResolver func(r *http.Request) (localCaller, error)

type callerKey struct{}

// callerSID is the requesting account's SID ("" when unknown, which
// matches no one's restore).
func callerSID(r *http.Request) string {
	c, _ := callerFrom(r.Context())
	return c.SID
}

func callerFrom(ctx context.Context) (localCaller, bool) {
	c, ok := ctx.Value(callerKey{}).(localCaller)
	return c, ok
}

// snapshotPathKey turns a Windows path ("C:\Users\ayse") or a snapshot path
// ("/C/Users/ayse/") into the comparable form "/c/users/ayse".
func snapshotPathKey(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), `\`, "/")
	if len(p) >= 2 && p[1] == ':' {
		p = "/" + p[:1] + p[2:]
	}
	p = strings.TrimRight(p, "/")
	if p != "" && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return strings.ToLower(p)
}

// within reports whether p is root or inside it (both already keys).
func within(p, root string) bool {
	if root == "" {
		return false
	}
	return p == root || strings.HasPrefix(p, root+"/")
}

func newLocalCaller(sid, profile, profilesDir string, allProfiles []string) localCaller {
	c := localCaller{SID: sid, Profile: snapshotPathKey(profile), ProfilesDir: snapshotPathKey(profilesDir)}
	for _, p := range allProfiles {
		k := snapshotPathKey(p)
		if k != "" && k != c.Profile {
			c.OtherProfiles = append(c.OtherProfiles, k)
		}
	}
	return c
}

// hidden: p belongs to someone else's profile. Folders directly under the
// profiles folder count as someone's even when Windows no longer lists the
// account (a deleted user's leftover folder); only Public is shared.
func (c localCaller) hidden(path string) bool {
	p := snapshotPathKey(path)
	if within(p, c.Profile) {
		return false
	}
	for _, o := range c.OtherProfiles {
		if within(p, o) {
			return true
		}
	}
	if c.ProfilesDir != "" && strings.HasPrefix(p, c.ProfilesDir+"/") {
		child := strings.SplitN(strings.TrimPrefix(p, c.ProfilesDir+"/"), "/", 2)[0]
		return child != "public"
	}
	return false
}

// mayRestore: p is visible and does not contain anyone else's profile
// (restoring C:\Users or C:\ would bring everyone's files along).
func (c localCaller) mayRestore(path string) bool {
	p := snapshotPathKey(path)
	if p == "" || c.hidden(p) {
		return false
	}
	if c.ProfilesDir != "" && within(c.ProfilesDir, p) {
		return false
	}
	for _, o := range c.OtherProfiles {
		if within(o, p) {
			return false
		}
	}
	return true
}

// withCaller resolves who sent the request before the handler runs; a
// request that cannot be tied to an account gets nothing.
func (s *localUIServer) withCaller(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Caller == nil {
			writeError(w, http.StatusForbidden, "caller_unknown")
			return
		}
		c, err := s.Caller(r)
		if err != nil || c.SID == "" {
			writeError(w, http.StatusForbidden, "caller_unknown")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), callerKey{}, c)))
	}
}
