//go:build !devlog

// Package devlog provides error-path-only diagnostics compiled in
// exclusively with `-tags devlog`. Production builds (tag off) get an
// empty inlineable no-op, so error branches cost nothing and the hot
// path is untouched.
package devlog

// Log discards its arguments. Present so devlog call sites compile
// away to nothing without the devlog build tag.
func Log(_ ...any) {}
