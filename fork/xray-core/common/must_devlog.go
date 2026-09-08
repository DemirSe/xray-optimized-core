//go:build devlog

package common

import (
	"github.com/xtls/xray-core/common/devlog"
)

// Must returns err so callers propagate it instead of panicking.
// devlog build additionally logs the fail-fast site when err is non-nil.
func Must(err error) error {
	if err != nil {
		devlog.Log("common.Must: ", err)
	}
	return err
}

// Must2 returns the first parameter, discarding err.
// Only use when err is provably impossible (e.g. writes to an in-memory buffer).
// Internal usage only, if user input can cause err, it must be handled.
// devlog build additionally logs the fail-fast site when err is non-nil.
func Must2[T any](v T, err error) T {
	if err != nil {
		devlog.Log("common.Must2: ", err)
	}
	return v
}
