//go:build !windows

package inventory

func fillPlatform(r *Report) {
	r.VSSService = "not-windows"
}
