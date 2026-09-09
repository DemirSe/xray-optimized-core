// Package common contains common utilities that are shared among other packages.
// See each sub-package for detail.
package common

import (
	"github.com/xtls/xray-core/common/errors"
)

// ErrNoClue is for the situation that existing information is not enough to make a decision. For example, Router may return this error when there is no suitable route.
var ErrNoClue = errors.New("not enough information for making a decision")

// Must/Must2 live in must.go (!devlog) and must_devlog.go (devlog) so
// production keeps the exact current bodies with zero added branches.
// Error2 returns the err from the 2nd parameter.
func Error2(v interface{}, err error) error {
	return err
}
