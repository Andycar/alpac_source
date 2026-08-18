//go:build !linux

package transcodesvc

// availableMemBytes is Linux-only (/proc/meminfo). Elsewhere the memory
// admission gate is disabled (returns -1 → skipped).
func availableMemBytes() int64 { return -1 }
