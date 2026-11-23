//go:build !trusttest

package version

// buildSuffix is empty in every real build (see build_trusttest.go).
const buildSuffix = ""
