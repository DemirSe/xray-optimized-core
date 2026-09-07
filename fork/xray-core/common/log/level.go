package log

import "sync/atomic"

// ponytail: global log seviyesi. errors.doLog her çağrıda runtime.Caller +
// alloc yapıyordu, seviye filtresi ise downstream'deydi. Bu kapı, yazılmayacak
// logun maliyetini çağrı anında keser. Varsayılan: Warning (xray defaultu).
var globalLevel atomic.Int32

func init() {
	globalLevel.Store(int32(Severity_Warning))
}

// SetGlobalLevel sets the process-wide log level (called by app/log on start).
func SetGlobalLevel(s Severity) {
	globalLevel.Store(int32(s))
}

// Passes reports whether a message of the given severity will be recorded.
func Passes(s Severity) bool {
	return int32(s) <= globalLevel.Load()
}
