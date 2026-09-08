package common_test

import (
	"errors"
	"testing"

	. "github.com/xtls/xray-core/common"
)

func TestMust(t *testing.T) {
	// Must returns err instead of panicking; callers must handle it.
	if err := Must(func() error { return errors.New("test error") }()); err == nil {
		t.Error("Must should return non-nil error")
	}
	if err := Must(func() error { return nil }()); err != nil {
		t.Error("Must should return nil error")
	}
	// Must2 returns the value, discarding err.
	if v := Must2(func() (int, error) { return 42, errors.New("test error") }()); v != 42 {
		t.Error("Must2 should return the value")
	}
}
