package desktop

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func write(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreFolderNameIsTurkishAndWindowsSafe(t *testing.T) {
	got := RestoreFolderName(time.Date(2026, 9, 23, 10, 4, 0, 0, time.Local))
	if got != "23 Eylül 2026 10.04" {
		t.Fatalf("got %q", got)
	}
}

func TestDeliverCopiesPickedItemsFlatIntoADatedFolder(t *testing.T) {
	root := t.TempDir()
	staging := filepath.Join(root, "Restore", "job1")
	write(t, filepath.Join(staging, "C", "Veri", "notlar.txt"), "not")
	write(t, filepath.Join(staging, "C", "Veri", "Finance", "2026", "fatura.csv"), "a,b")
	write(t, filepath.Join(staging, "C", "Diğer", "notlar.txt"), "ikinci")
	write(t, filepath.Join(staging, "result-manifest.json"), "{}")
	docs := filepath.Join(root, "Belgeler", "Geri Yüklenenler")
	var last DeliverProgress
	dest, err := Deliver(staging, filepath.Join(root, "Restore"),
		[]string{"/C/Veri/notlar.txt", "/C/Veri/Finance", "/C/Diğer/notlar.txt"},
		docs, time.Date(2026, 9, 23, 10, 44, 0, 0, time.Local), func(p DeliverProgress) { last = p })
	if err != nil {
		t.Fatal(err)
	}
	if dest != filepath.Join(docs, "23 Eylül 2026 10.44") {
		t.Fatalf("dest = %s", dest)
	}
	for rel, want := range map[string]string{
		"notlar.txt":     "not",
		"notlar (2).txt": "ikinci",
		filepath.Join("Finance", "2026", "fatura.csv"): "a,b",
	} {
		b, err := os.ReadFile(filepath.Join(dest, rel))
		if err != nil || string(b) != want {
			t.Fatalf("%s = %q, %v", rel, b, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "result-manifest.json")); !os.IsNotExist(err) {
		t.Fatal("agent bookkeeping files must not be delivered")
	}
	if last.TotalBytes != 12 || last.BytesDone != 12 {
		t.Fatalf("progress = %+v", last)
	}
}

func TestDeliverNeverOverwritesAnEarlierDelivery(t *testing.T) {
	root := t.TempDir()
	staging := filepath.Join(root, "Restore", "job1")
	write(t, filepath.Join(staging, "C", "a.txt"), "x")
	docs := filepath.Join(root, "docs")
	at := time.Date(2026, 9, 23, 10, 44, 0, 0, time.Local)
	first, err := Deliver(staging, filepath.Join(root, "Restore"), []string{"/C/a.txt"}, docs, at, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Deliver(staging, filepath.Join(root, "Restore"), []string{"/C/a.txt"}, docs, at, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || filepath.Base(second) != "23 Eylül 2026 10.44 (2)" {
		t.Fatalf("first=%s second=%s", first, second)
	}
}

func TestDeliverRefusesStagingOutsideTheRestoreArea(t *testing.T) {
	root := t.TempDir()
	elsewhere := filepath.Join(root, "Windows")
	write(t, filepath.Join(elsewhere, "C", "a.txt"), "x")
	if _, err := Deliver(elsewhere, filepath.Join(root, "Restore"), []string{"/C/a.txt"}, filepath.Join(root, "docs"), time.Now(), nil); err == nil {
		t.Fatal("a staging folder outside the restore area must be refused")
	}
	staging := filepath.Join(root, "Restore", "job")
	write(t, filepath.Join(staging, "C", "a.txt"), "x")
	if _, err := Deliver(staging, filepath.Join(root, "Restore"), []string{"/../../Windows/C/a.txt"}, filepath.Join(root, "docs"), time.Now(), nil); err == nil {
		t.Fatal("a selection escaping staging must be refused")
	}
}

func TestRestoreRootStaysOutOfOneDrive(t *testing.T) {
	profile := `C:\Users\ayse`
	od := []string{`C:\Users\ayse\OneDrive`, "", `C:\Users\ayse\OneDrive - Contoso`}
	if got := RestoreRoot(`C:\Users\ayse\OneDrive\Belgeler`, profile, od); got != `C:\Users\ayse\Geri Yüklenenler` {
		t.Fatalf("OneDrive Documents: got %s", got)
	}
	if got := RestoreRoot(`C:\Users\ayse\OneDrive - Contoso\Documents`, profile, od); got != `C:\Users\ayse\Geri Yüklenenler` {
		t.Fatalf("OneDrive for Business Documents: got %s", got)
	}
	if got := RestoreRoot(`C:\Users\ayse\Documents`, profile, od); got != `C:\Users\ayse\Documents\Geri Yüklenenler` {
		t.Fatalf("local Documents: got %s", got)
	}
}

// The tests above pin the Turkish names; run them in Turkish whatever the
// language of the machine running the suite.
func TestMain(m *testing.M) {
	Turkish = true
	os.Exit(m.Run())
}

func TestRestoreNamesAreEnglishOutsideTurkish(t *testing.T) {
	Turkish = false
	defer func() { Turkish = true }()
	if got := RestoreFolderName(time.Date(2026, 9, 23, 10, 4, 0, 0, time.Local)); got != "23 September 2026 10.04" {
		t.Fatalf("got %q", got)
	}
	if got := RestoreRoot(`C:\Users\a\Documents`, `C:\Users\a`, nil); got != `C:\Users\a\Documents\Restored Files` {
		t.Fatalf("got %q", got)
	}
}
