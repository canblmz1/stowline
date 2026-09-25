//go:build !windows

package application

func volumeFreeBytes(path string) (uint64, error) {
	_ = path
	return 1 << 40, nil
}
