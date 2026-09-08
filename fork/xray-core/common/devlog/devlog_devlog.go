//go:build devlog

// Package devlog provides error-path-only diagnostics compiled in
// exclusively with `-tags devlog`. Call sites must stay on error
// branches only; the hot path stays untouched.
package devlog

import stdlog "log"

// Log writes diagnostics to the standard logger (stderr). Only linked
// into builds with the devlog tag.
func Log(v ...any) {
	stdlog.Print(v...)
}
