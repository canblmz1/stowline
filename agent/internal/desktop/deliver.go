package desktop

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var trMonths = [...]string{"Ocak", "Şubat", "Mart", "Nisan", "Mayıs", "Haziran", "Temmuz", "Ağustos", "Eylül", "Ekim", "Kasım", "Aralık"}

// RestoreFolderName names one delivery, e.g. "23 September 2026 10.44"
// ("23 Eylül 2026 10.44" in Turkish) -- a name a person recognises,
// sortable enough, and valid on Windows (no ':').
func RestoreFolderName(t time.Time) string {
	month := t.Month().String()
	if Turkish {
		month = trMonths[t.Month()-1]
	}
	return fmt.Sprintf("%d %s %d %02d.%02d", t.Day(), month, t.Year(), t.Hour(), t.Minute())
}

// RestoredFolderName is the folder delivered restores go into.
func RestoredFolderName() string { return T("Geri Yüklenenler", "Restored Files") }

// uniquePath returns p, or "p (2)", "p (3)"... whichever does not exist
// yet; a file keeps its extension last ("notlar (2).txt"), a folder name
// is never split ("23 Eylül 2026 10.44 (2)").
func uniquePath(p string, isDir bool) string {
	if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
		return p
	}
	ext := filepath.Ext(p)
	if isDir {
		ext = ""
	}
	base := strings.TrimSuffix(p, ext)
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if _, err := os.Lstat(cand); errors.Is(err, os.ErrNotExist) {
			return cand
		}
	}
}

// DeliverProgress is reported while copying.
type DeliverProgress struct {
	BytesDone  int64
	TotalBytes int64
}

// Deliver copies each restored selection out of the agent's staging folder
// into destRoot\<RestoreFolderName>\<item name>, as the signed-in user, so
// the files get that user's normal permissions. Snapshot selections look
// like "/C/Users/ali/Belgeler/rapor.docx"; restic restored them under
// staging with the same relative path. stagingRoot bounds where staging may
// be (the agent's restore area), so a crafted job can never make this copy
// anything else. Never overwrites: the dated folder is new, and name
// clashes inside it get " (2)".
func Deliver(staging, stagingRoot string, selections []string, destRoot string, now time.Time, progress func(DeliverProgress)) (string, error) {
	if !within(stagingRoot, staging) {
		return "", fmt.Errorf("restore folder is outside the Stowline restore area")
	}
	if len(selections) == 0 {
		return "", errors.New("nothing to deliver")
	}
	var sources []string
	var total int64
	for _, sel := range selections {
		rel := filepath.FromSlash(strings.TrimPrefix(sel, "/"))
		if rel == "" || strings.Contains(rel, "..") {
			return "", fmt.Errorf("invalid selection %q", sel)
		}
		src := filepath.Join(staging, rel)
		if !within(staging, src) {
			return "", fmt.Errorf("invalid selection %q", sel)
		}
		if _, err := os.Lstat(src); err != nil {
			return "", fmt.Errorf("restored item missing: %s", filepath.Base(src))
		}
		sources = append(sources, src)
		_ = filepath.Walk(src, func(_ string, info os.FileInfo, err error) error {
			if err == nil && info.Mode().IsRegular() {
				total += info.Size()
			}
			return nil
		})
	}
	dest := uniquePath(filepath.Join(destRoot, RestoreFolderName(now)), true)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", err
	}
	var done int64
	report := func(n int64) {
		done += n
		if progress != nil {
			progress(DeliverProgress{BytesDone: done, TotalBytes: total})
		}
	}
	for _, src := range sources {
		info, _ := os.Lstat(src)
		target := uniquePath(filepath.Join(dest, filepath.Base(src)), info != nil && info.IsDir())
		if err := copyTree(src, target, report); err != nil {
			return dest, err
		}
	}
	return dest, nil
}

func within(root, p string) bool {
	if root == "" || p == "" {
		return false
	}
	r, err1 := filepath.Abs(root)
	a, err2 := filepath.Abs(p)
	if err1 != nil || err2 != nil {
		return false
	}
	rel, err := filepath.Rel(r, a)
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}

func copyTree(src, dst string, report func(int64)) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case info.IsDir():
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := copyTree(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name()), report); err != nil {
				return err
			}
		}
		_ = os.Chtimes(dst, info.ModTime(), info.ModTime())
		return nil
	case info.Mode().IsRegular():
		return copyFile(src, dst, info, report)
	default:
		return nil // links and devices are not user documents; skip
	}
}

func copyFile(src, dst string, info os.FileInfo, report func(int64)) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	buf := make([]byte, 1<<20)
	for {
		n, rerr := in.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				return werr
			}
			report(int64(n))
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			return rerr
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chtimes(dst, info.ModTime(), info.ModTime())
}

// RestoreRoot picks where delivered restores go: Documents\Geri Yüklenenler,
// unless Documents lives inside a OneDrive folder -- then the user's own
// profile folder instead, so restored files never start syncing to (and
// filling) the person's OneDrive.
func RestoreRoot(documents, profile string, oneDriveRoots []string) string {
	for _, od := range oneDriveRoots {
		if od != "" && (strings.EqualFold(filepath.Clean(documents), filepath.Clean(od)) || within(od, documents)) {
			return filepath.Join(profile, RestoredFolderName())
		}
	}
	return filepath.Join(documents, RestoredFolderName())
}
