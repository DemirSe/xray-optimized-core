//go:build !devlog

package common

// Must returns err so callers propagate it instead of panicking.
func Must(err error) error {
	return err
}

// Must2 returns the first parameter, discarding err.
// Only use when err is provably impossible (e.g. writes to an in-memory buffer).
// Internal usage only, if user input can cause err, it must be handled
func Must2[T any](v T, _ error) T {
	return v
}
