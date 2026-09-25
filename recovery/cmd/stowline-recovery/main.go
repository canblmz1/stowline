package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const version = "stowline-recovery 0.1.0"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "version":
		fmt.Println(version)
	case "copy":
		err = cmdCopy(os.Args[2:])
	case "verify":
		err = cmdVerify(os.Args[2:])
	case "forget-plan":
		err = cmdForget(os.Args[2:], true)
	case "forget-apply":
		err = cmdForget(os.Args[2:], false)
	case "list-repositories":
		err = cmdListRepositories(os.Args[2:])
	case "list-snapshots":
		err = cmdListSnapshots(os.Args[2:])
	case "restore-to-staging":
		err = cmdRestoreToStaging(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Println(`stowline-recovery — trusted recovery workstation only
  copy --src DIR --dst DIR
  verify --repo DIR --restic PATH --sha256 HEX
  forget-plan --repo DIR --restic PATH --sha256 HEX [--keep-daily N]
  forget-apply --i-understand-destructive-maintenance --confirm-token TOKEN --recovery-copy-verified --repo DIR --restic PATH --sha256 HEX
  list-repositories --root DIR
  list-snapshots --repo DIR --restic PATH --sha256 HEX
  restore-to-staging --repo DIR --restic PATH --sha256 HEX --snapshot ID --staging DIR [--include PATH]...

Endpoint agents must never invoke this binary.
Default is dry-run. forget-apply refuses C:\\Stowline\\Repos\\local.
restore-to-staging only ever writes into --staging: never a live business
location, and never a non-empty directory it did not just create itself.`)
}

// repoMarkerFiles are restic's own top-level layout: present in every
// restic repository regardless of backend, absent from an arbitrary
// directory that merely happens to be under --root. Checking for these
// (rather than trying to open/decrypt anything) means list-repositories
// needs no password and cannot itself leak repository contents.
var repoMarkerDirs = []string{"data", "index", "keys", "snapshots"}

func cmdListRepositories(args []string) error {
	root := flag(args, "--root")
	if root == "" {
		return fmt.Errorf("list-repositories --root DIR")
	}
	if strings.Contains(root, "..") {
		return fmt.Errorf("path rejected")
	}
	const maxDepth = 6
	var found []string
	var walk func(dir string, depth int) error
	walk = func(dir string, depth int) error {
		if depth > maxDepth {
			return nil
		}
		if looksLikeResticRepo(dir) {
			found = append(found, dir)
			return nil // do not descend into a repo's own data/index/keys/snapshots dirs
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil // unreadable subtree: skip, do not abort the whole scan
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if err := walk(filepath.Join(dir, e.Name()), depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root, 0); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(map[string]any{"root": root, "repositories": found}, "", "  ")
	fmt.Println(string(b))
	return nil
}

func looksLikeResticRepo(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, "config")); err != nil {
		return false
	}
	hits := 0
	for _, m := range repoMarkerDirs {
		if fi, err := os.Stat(filepath.Join(dir, m)); err == nil && fi.IsDir() {
			hits++
		}
	}
	return hits >= 2
}

// snapshotSummary is deliberately a small, sanitized subset of restic's own
// snapshot JSON: what an operator needs to pick a snapshot to restore, not
// restic's internal tree/parent blob references.
type snapshotSummary struct {
	ShortID  string   `json:"short_id"`
	ID       string   `json:"id"`
	Time     string   `json:"time"`
	Hostname string   `json:"hostname"`
	Paths    []string `json:"paths"`
}

func cmdListSnapshots(args []string) error {
	repo, restic, sum := flag(args, "--repo"), flag(args, "--restic"), strings.ToLower(flag(args, "--sha256"))
	if repo == "" || restic == "" || sum == "" {
		return fmt.Errorf("list-snapshots --repo DIR --restic PATH --sha256 HEX")
	}
	if err := pinOK(restic, sum); err != nil {
		return err
	}
	if err := rejectPilotLive(repo); err != nil {
		return err
	}
	cmd := exec.Command(restic, "--repo", repo, "snapshots", "--json")
	cmd.Env = resticEnv()
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("restic snapshots failed: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return err
	}
	var snaps []snapshotSummary
	if err := json.Unmarshal(out, &snaps); err != nil {
		return fmt.Errorf("could not parse restic snapshots output: %w", err)
	}
	b, _ := json.MarshalIndent(map[string]any{"repo": repo, "snapshots": snaps}, "", "  ")
	fmt.Println(string(b))
	return nil
}

// liveDataNameFragments blocks the most common ways a staging path could
// accidentally alias a real business location. It is defense in depth, not
// the only guard: the empty-or-self-created check below is what actually
// stops a restore from landing on top of existing files.
// "AppData" is deliberately not in this list: it is where legitimate
// temp/test/tool directories live too (including this binary's own test
// suite's t.TempDir()), so blocking it would refuse harmless targets far
// more often than it would catch a real mistake.
var liveDataNameFragments = []string{
	"documents", "desktop", "pictures", "downloads", "music", "videos",
	"program files", "programdata", "windows", "system32",
}

func rejectLiveStagingTarget(staging string) error {
	clean := filepath.Clean(staging)
	vol := filepath.VolumeName(clean)
	if strings.EqualFold(clean, vol) || strings.EqualFold(clean, vol+`\`) {
		return fmt.Errorf("refusing a drive root as a restore staging target")
	}
	lower := strings.ToLower(clean)
	for _, frag := range liveDataNameFragments {
		if strings.Contains(lower, frag) {
			return fmt.Errorf("refusing staging target that looks like a live data location: contains %q", frag)
		}
	}
	entries, err := os.ReadDir(clean)
	if err == nil && len(entries) > 0 {
		return fmt.Errorf("refusing non-empty staging target %s; restore-to-staging never writes over existing files", clean)
	}
	return nil
}

func cmdRestoreToStaging(args []string) error {
	repo, restic, sum := flag(args, "--repo"), flag(args, "--restic"), strings.ToLower(flag(args, "--sha256"))
	snapshot, staging := flag(args, "--snapshot"), flag(args, "--staging")
	if repo == "" || restic == "" || sum == "" || snapshot == "" || staging == "" {
		return fmt.Errorf("restore-to-staging --repo DIR --restic PATH --sha256 HEX --snapshot ID --staging DIR")
	}
	// Checked first and unconditionally: the destination must be safe
	// regardless of whether the restic pin or repo path also turn out to
	// be valid. A restore must never get far enough to touch a live
	// business location merely because some other flag was also wrong.
	if err := rejectLiveStagingTarget(staging); err != nil {
		return err
	}
	if err := pinOK(restic, sum); err != nil {
		return err
	}
	if err := rejectPilotLive(repo); err != nil {
		return err
	}
	if err := os.MkdirAll(staging, 0700); err != nil {
		return err
	}
	argv := []string{"--repo", repo, "restore", snapshot, "--target", staging}
	for i, a := range args {
		if a == "--include" && i+1 < len(args) {
			if strings.Contains(args[i+1], "..") {
				return fmt.Errorf("path rejected")
			}
			argv = append(argv, "--include", args[i+1])
		}
	}
	cmd := exec.Command(restic, argv...)
	cmd.Env = resticEnv()
	out, err := cmd.CombinedOutput()
	fmt.Print(string(out))
	if err != nil {
		return err
	}
	sum2, err := hashTree(staging)
	if err != nil {
		return err
	}
	ev := map[string]any{
		"restored_at": time.Now().UTC().Format(time.RFC3339),
		"repo":        repo,
		"snapshot":    snapshot,
		"staging":     staging,
		"sha256_tree": sum2,
		"independent": true,
	}
	b, _ := json.MarshalIndent(ev, "", "  ")
	fmt.Println(string(b))
	return os.WriteFile(filepath.Join(staging, "stowline-recovery-restore-manifest.json"), b, 0600)
}

func cmdCopy(args []string) error {
	src, dst := flag(args, "--src"), flag(args, "--dst")
	if src == "" || dst == "" {
		return fmt.Errorf("copy --src DIR --dst DIR")
	}
	if err := rejectPilotLive(src); err != nil {
		return err
	}
	if strings.Contains(src, "..") || strings.Contains(dst, "..") {
		return fmt.Errorf("path rejected")
	}
	if err := copyDir(src, dst); err != nil {
		return err
	}
	sum, err := hashTree(dst)
	if err != nil {
		return err
	}
	ev := map[string]any{"copied_at": time.Now().UTC().Format(time.RFC3339), "src": src, "dst": dst, "sha256_tree": sum, "independent": true}
	b, _ := json.MarshalIndent(ev, "", "  ")
	fmt.Println(string(b))
	return os.WriteFile(filepath.Join(dst, "stowline-recovery-manifest.json"), b, 0600)
}

func cmdVerify(args []string) error {
	repo, restic, sum := flag(args, "--repo"), flag(args, "--restic"), strings.ToLower(flag(args, "--sha256"))
	if repo == "" || restic == "" || sum == "" {
		return fmt.Errorf("verify --repo DIR --restic PATH --sha256 HEX")
	}
	if err := pinOK(restic, sum); err != nil {
		return err
	}
	if err := rejectPilotLive(repo); err != nil {
		return err
	}
	cmd := exec.Command(restic, "--repo", repo, "check")
	cmd.Env = resticEnv()
	out, err := cmd.CombinedOutput()
	fmt.Print(string(out))
	return err
}

func cmdForget(args []string, dry bool) error {
	repo, restic, sum := flag(args, "--repo"), flag(args, "--restic"), strings.ToLower(flag(args, "--sha256"))
	if repo == "" || restic == "" || sum == "" {
		return fmt.Errorf("forget requires --repo --restic --sha256")
	}
	if err := pinOK(restic, sum); err != nil {
		return err
	}
	if err := rejectPilotLive(repo); err != nil {
		return err
	}
	keep := flag(args, "--keep-daily")
	if keep == "" {
		keep = "7"
	}
	argv := []string{"--repo", repo, "forget", "--keep-daily", keep, "--keep-weekly", "5", "--keep-monthly", "12"}
	if dry {
		argv = append(argv, "--dry-run")
	} else {
		if !has(args, "--i-understand-destructive-maintenance") || flag(args, "--confirm-token") == "" || !has(args, "--recovery-copy-verified") {
			return fmt.Errorf("forget-apply requires --i-understand-destructive-maintenance --confirm-token TOKEN --recovery-copy-verified")
		}
	}
	cmd := exec.Command(restic, argv...)
	cmd.Env = resticEnv()
	out, err := cmd.CombinedOutput()
	fmt.Print(string(out))
	return err
}

func resticEnv() []string {
	tmp := os.TempDir()
	overrides := map[string]string{"RESTIC_PASSWORD": os.Getenv("RESTIC_PASSWORD"), "TMP": tmp, "TEMP": tmp}
	env := os.Environ()
	out := make([]string, 0, len(env)+len(overrides))
	set := make(map[string]bool, len(overrides))
	for _, kv := range env {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		if v, ok := overrides[strings.ToUpper(key)]; ok {
			out = append(out, key+"="+v)
			set[strings.ToUpper(key)] = true
			continue
		}
		out = append(out, kv)
	}
	for k, v := range overrides {
		if !set[k] {
			out = append(out, k+"="+v)
		}
	}
	return out
}

func rejectPilotLive(p string) error {
	n := strings.ToLower(filepath.Clean(p))
	if strings.Contains(n, `\stowline\repos\local`) || strings.HasSuffix(n, `\stowline\repos\local`) {
		return fmt.Errorf("refusing live pilot repository; use a disposable synthetic copy")
	}
	return nil
}

func pinOK(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("restic digest mismatch")
	}
	return nil
}

// resticBlobID matches a restic object id: 64 lowercase hex characters.
var resticBlobID = regexp.MustCompile(`^[0-9a-f]{64}$`)

// copyDir preserves the source's own layout except for one deliberate
// correction: a restic REST-backend repository stores data/ blobs flat
// (data/<id>), but restic's local backend -- what every stowline-recovery
// command below actually talks to once copied -- expects them sharded
// into data/<id[:2]>/<id>, and this restic version has no -o
// local.layout override to tell it otherwise (confirmed: "option
// local.layout is not known"). Copying a REST repo's flat data/ straight
// into a local directory therefore produces a tree restic's own local
// backend cannot read back -- confirmed live: restore-to-staging failed
// to find a real, present blob purely because of this layout mismatch.
// Re-sharding here, transparently, means whatever pulled the source tree
// down (rclone or otherwise) never needs to know about this quirk.
func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink %s", path)
		}
		if base := info.Name(); filepath.Base(filepath.Dir(rel)) == "data" && resticBlobID.MatchString(base) {
			target = filepath.Join(dst, filepath.Dir(rel), base[:2], base)
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func hashTree(root string) (string, error) {
	h := sha256.New()
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		fmt.Fprintf(h, "%s\n", filepath.ToSlash(rel))
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(h, f)
		return err
	})
	return hex.EncodeToString(h.Sum(nil)), err
}

func flag(args []string, name string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}

func has(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}
