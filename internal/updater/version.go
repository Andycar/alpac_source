package updater

import (
	"strconv"
	"strings"
)

// NormalizeVersion strips a leading "v" and trims whitespace.
func NormalizeVersion(v string) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "v"))
}

// CompareVersions returns:
//
//	-1 if a <  b
//	 0 if a == b
//	+1 if a >  b
//
// Versions are compared numerically per dot-separated component. When two
// components share the same numeric prefix, a trailing letter suffix is
// treated as a post-release patch (newer): "0.4" < "0.4a" < "0.4c". This
// matches the way releases are published in this project — "0.4a" is a
// hotfix of "0.4", not a pre-release.
//
// Components that fail to parse as integers fall back to lexical comparison.
func CompareVersions(a, b string) int {
	aa := strings.Split(NormalizeVersion(a), ".")
	bb := strings.Split(NormalizeVersion(b), ".")
	n := len(aa)
	if len(bb) > n {
		n = len(bb)
	}
	for i := 0; i < n; i++ {
		ap := ""
		bp := ""
		if i < len(aa) {
			ap = aa[i]
		}
		if i < len(bb) {
			bp = bb[i]
		}
		ai, aerr := parseLeadingInt(ap)
		bi, berr := parseLeadingInt(bp)
		if aerr == nil && berr == nil {
			if ai < bi {
				return -1
			}
			if ai > bi {
				return 1
			}
			// Same numeric value — letter suffix marks a post-release
			// hotfix (newer than the plain version), but an additional
			// dotted patch component still wins over a hotfix suffix:
			// "0.3" < "0.3a" < "0.3.1".
			aSfx := strings.TrimPrefix(ap, strconv.Itoa(ai))
			bSfx := strings.TrimPrefix(bp, strconv.Itoa(bi))
			if aSfx == "" && bSfx == "" {
				continue
			}
			if aSfx == "" && bSfx != "" {
				// Side a has no suffix here. If a still has more
				// components ("0.3.1"), they beat b's hotfix; otherwise
				// b's hotfix beats plain a.
				if i+1 < len(aa) {
					return 1
				}
				return -1
			}
			if aSfx != "" && bSfx == "" {
				if i+1 < len(bb) {
					return -1
				}
				return 1
			}
			if aSfx < bSfx {
				return -1
			}
			if aSfx > bSfx {
				return 1
			}
			continue
		}
		if ap < bp {
			return -1
		}
		if ap > bp {
			return 1
		}
	}
	return 0
}

// parseLeadingInt parses the numeric prefix of s ("12b" → 12).
func parseLeadingInt(s string) (int, error) {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, strconv.ErrSyntax
	}
	return strconv.Atoi(s[:end])
}
