package main

import (
	"io/fs"
	"path/filepath"
	"sync"
	"time"
)

// folderSize is what "Klasörlerim" shows next to each folder. Complete is
// false while the walk is still running or when it hit its time budget, so
// the page can say "hesaplanıyor" / "en az" instead of a wrong exact number.
type folderSize struct {
	Bytes    int64 `json:"bytes"`
	Files    int64 `json:"files"`
	Complete bool  `json:"complete"`
	Pending  bool  `json:"pending"`
}

// folderSizes walks each backed-up folder in the background and caches the
// totals, so opening the page never waits on a large disk scan.
type folderSizes struct {
	mu      sync.Mutex
	entries map[string]*folderSizeEntry
	Budget  time.Duration // max time for one walk
	MaxAge  time.Duration // recompute after this long
	now     func() time.Time
}

type folderSizeEntry struct {
	size      folderSize
	at        time.Time
	computing bool
}

func (f *folderSizes) clock() time.Time {
	if f.now != nil {
		return f.now()
	}
	return time.Now()
}

// Get returns the cached size (Pending until the first walk finishes) and
// starts a walk when there is none or the cached one is stale.
func (f *folderSizes) Get(path string) folderSize {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.entries == nil {
		f.entries = map[string]*folderSizeEntry{}
	}
	e := f.entries[path]
	if e == nil {
		e = &folderSizeEntry{size: folderSize{Pending: true}}
		f.entries[path] = e
	}
	stale := e.at.IsZero() || (f.MaxAge > 0 && f.clock().Sub(e.at) > f.MaxAge)
	if stale && !e.computing {
		e.computing = true
		go f.walk(path)
	}
	return e.size
}

func (f *folderSizes) walk(path string) {
	budget := f.Budget
	if budget <= 0 {
		budget = time.Minute
	}
	deadline := time.Now().Add(budget)
	var size folderSize
	complete := true
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable subfolder: skip, the backup reports it properly
		}
		if time.Now().After(deadline) {
			complete = false
			return filepath.SkipAll
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				size.Bytes += info.Size()
				size.Files++
			}
		}
		return nil
	})
	size.Complete = complete
	f.mu.Lock()
	e := f.entries[path]
	e.size, e.at, e.computing = size, f.clock(), false
	f.mu.Unlock()
}
