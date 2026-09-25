package inventory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

type Report struct {
	CollectedAt      string            `json:"collected_at"`
	HostnameRedacted string            `json:"hostname_redacted"`
	OS               string            `json:"os"`
	Arch             string            `json:"arch"`
	GoVersion        string            `json:"go_version"`
	UserRedacted     string            `json:"user_redacted"`
	Volumes          []Volume          `json:"volumes"`
	Restic           string            `json:"restic"`
	Rclone           string            `json:"rclone"`
	KnownFolders     map[string]string `json:"known_folders_redacted"`
	PilotCorpusBytes int64             `json:"pilot_corpus_bytes"`
	Elevated         bool              `json:"elevated"`
	VSSService       string            `json:"vss_service"`
	Notes            []string          `json:"notes"`
}

type Volume struct {
	Letter     string  `json:"letter"`
	FileSystem string  `json:"file_system"`
	SizeGB     float64 `json:"size_gb"`
	FreeGB     float64 `json:"free_gb"`
}

func Collect(resticVersion, rcloneVersion, corpusRoot string) (*Report, error) {
	r := &Report{
		CollectedAt:      time.Now().UTC().Format(time.RFC3339),
		HostnameRedacted: "<hostname>",
		OS:               runtime.GOOS + "/" + runtime.GOARCH,
		Arch:             runtime.GOARCH,
		GoVersion:        runtime.Version(),
		UserRedacted:     "<user>",
		Restic:           resticVersion,
		Rclone:           rcloneVersion,
		KnownFolders:     map[string]string{},
		Notes: []string{
			"User, hostname, and profile paths are redacted.",
			"Desktop/Documents on the development PC are OneDrive-redirected; placeholders must not count as complete coverage.",
		},
	}
	if corpusRoot != "" {
		_ = filepath.Walk(corpusRoot, func(p string, info os.FileInfo, err error) error {
			if err == nil && info != nil && !info.IsDir() {
				r.PilotCorpusBytes += info.Size()
			}
			return nil
		})
	}
	r.KnownFolders["Desktop"] = `C:\Users\<user>\OneDrive\Desktop`
	r.KnownFolders["Documents"] = `C:\Users\<user>\OneDrive\Belgeler`
	r.KnownFolders["Downloads"] = `C:\Users\<user>\Downloads`
	r.KnownFolders["UserProfile"] = `C:\Users\<user>`
	fillPlatform(r)
	return r, nil
}

func WriteMarkdown(path string, r *Report) error {
	b, _ := json.MarshalIndent(r, "", "  ")
	md := "# Pilot environment (redacted)\n\nCollected: " + r.CollectedAt + "\n\n```json\n" + string(b) + "\n```\n"
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(md), 0644)
}

func (r *Report) String() string {
	return fmt.Sprintf("os=%s restic=%q rclone=%q", r.OS, r.Restic, r.Rclone)
}
