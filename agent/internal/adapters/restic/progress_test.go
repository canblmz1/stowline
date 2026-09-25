package restic

import (
	"math"
	"testing"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

func TestBackupProgressParserTextAcrossChunks(t *testing.T) {
	var got []domain.BackupProgress
	p := newBackupProgressParser(func(v domain.BackupProgress) { got = append(got, v) })
	p.Feed([]byte("creating VSS snapshot for [c:\\]\r[0:34] 2."))
	p.Feed([]byte("96%  867 files 5.046 GiB, total 307867 files 170.438 GiB\r"))
	if len(got) != 1 || math.Abs(got[0].PercentDone-2.96) > 0.001 {
		t.Fatalf("progress = %#v", got)
	}
}

func TestBackupProgressParserJSONReportsOnlyNumbers(t *testing.T) {
	var got domain.BackupProgress
	p := newBackupProgressParser(func(v domain.BackupProgress) { got = v })
	p.Feed([]byte(`{"message_type":"status","percent_done":0.625,"total_files":8,"files_done":5,"total_bytes":1000,"bytes_done":625,"current_files":["C:\\secret-name.txt"]}` + "\n"))
	if got.PercentDone != 62.5 || got.FilesDone != 5 || got.TotalFiles != 8 || got.BytesDone != 625 || got.TotalBytes != 1000 {
		t.Fatalf("progress = %#v", got)
	}
}

func TestBackupProgressParserIgnoresDuplicateAndUnrelatedOutput(t *testing.T) {
	count := 0
	p := newBackupProgressParser(func(domain.BackupProgress) { count++ })
	p.Feed([]byte("successfully created snapshot for [c:\\]\n"))
	p.Feed([]byte("[0:01] 12.50%  1 files 1 MiB, total 8 files 8 MiB\r"))
	p.Feed([]byte("[0:02] 12.50%  1 files 1 MiB, total 8 files 8 MiB\r"))
	if count != 1 {
		t.Fatalf("reports = %d", count)
	}
}

func TestRestoreProgressParserReadsRestoredCountsAcrossChunks(t *testing.T) {
	var got []domain.RestoreProgress
	p := newRestoreProgressParser(func(u domain.RestoreProgress) { got = append(got, u) })
	p.Feed([]byte(`{"message_type":"status","seconds_elapsed":3,"percent_done":0.25,"total_files":8,"files_restored":2,"total_bytes":4000,"bytes_rest`))
	p.Feed([]byte("ored\":1000}\n{\"message_type\":\"verbose_status\",\"action\":\"restored\",\"item\":\"/C/x/secret-name.txt\"}\n"))
	p.Feed([]byte(`{"message_type":"summary","percent_done":1,"total_files":8,"files_restored":8,"total_bytes":4000,"bytes_restored":4000}` + "\n"))
	if len(got) != 2 {
		t.Fatalf("updates = %+v", got)
	}
	if got[0].PercentDone != 25 || got[0].FilesDone != 2 || got[0].TotalFiles != 8 || got[0].BytesDone != 1000 || got[0].TotalBytes != 4000 {
		t.Fatalf("first = %+v", got[0])
	}
	if got[1].PercentDone != 100 || got[1].BytesDone != 4000 {
		t.Fatalf("summary = %+v", got[1])
	}
}

func TestRestoreProgressParserToleratesNilReport(t *testing.T) {
	var p *restoreProgressParser
	p.Feed([]byte(`{"message_type":"status","percent_done":0.5}` + "\n"))
	newRestoreProgressParser(nil).Feed([]byte(`{"message_type":"status","percent_done":0.5}` + "\n"))
}

func TestBackupProgressParserReadsRealTextStatusLinesWithSizes(t *testing.T) {
	// Captured from restic 0.19.1 with RESTIC_PROGRESS_FPS set and stdout
	// piped (the VSS-required path runs without --json).
	var got []domain.BackupProgress
	p := newBackupProgressParser(func(u domain.BackupProgress) { got = append(got, u) })
	p.Feed([]byte("no parent snapshot found, will read all files\n[0:00] 1 files 88.889 MiB, total 7 files 208.002 MiB, 0 errors\n/C/Stowline-TestData/Muhasebe/arsiv-2026.bin\n"))
	p.Feed([]byte("[0:01] 42.73%  1 files 88.889 MiB, total 7 files 208.002 MiB, 0 errors ETA 0:01\n"))
	p.Feed([]byte("[1:02:03] 100.00%  7 files 1.500 GiB, total 7 files 1.500 GiB, 0 errors\n"))
	if len(got) != 3 {
		t.Fatalf("updates = %+v", got)
	}
	mib := int64(1 << 20)
	if got[0].TotalFiles != 7 || got[0].FilesDone != 1 || got[0].BytesDone != int64(88.889*float64(mib)) || got[0].TotalBytes != int64(208.002*float64(mib)) || math.Abs(got[0].PercentDone-42.73) > 0.01 {
		t.Fatalf("first = %+v", got[0])
	}
	if got[1].PercentDone != 42.73 || got[1].TotalBytes != int64(208.002*float64(mib)) {
		t.Fatalf("second = %+v", got[1])
	}
	if got[2].PercentDone != 100 || got[2].BytesDone != got[2].TotalBytes || got[2].TotalBytes != int64(1.5*float64(1<<30)) {
		t.Fatalf("third = %+v", got[2])
	}
}

func TestBackupProgressParserDerivesPercentWhenTheLineHasNone(t *testing.T) {
	// restic's last status lines (everything read, upload finishing) carry no
	// percentage; reporting 0 made the rings drop from 97% to 0%.
	var got []domain.BackupProgress
	p := newBackupProgressParser(func(u domain.BackupProgress) { got = append(got, u) })
	p.Feed([]byte("[0:40] 97.64%  6 files 203.100 MiB, total 7 files 208.002 MiB, 0 errors ETA 0:01\n"))
	p.Feed([]byte("[0:41] 7 files 208.002 MiB, total 7 files 208.002 MiB, 0 errors\n"))
	last := got[len(got)-1]
	if last.PercentDone != 100 || last.BytesDone != last.TotalBytes {
		t.Fatalf("last = %+v", last)
	}
}
