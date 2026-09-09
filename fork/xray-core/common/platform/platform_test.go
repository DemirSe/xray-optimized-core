package platform_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/xtls/xray-core/common"
	. "github.com/xtls/xray-core/common/platform"
)

func TestGetAssetLocation(t *testing.T) {
	exec, err := os.Executable()
	if err := common.Must(err); err != nil {
		t.Fatal(err)
	}

	loc := GetAssetLocation("t")
	if filepath.Dir(loc) != filepath.Dir(exec) {
		t.Error("asset dir: ", loc, " not in ", exec)
	}

	os.Setenv("xray.location.asset", "/xray")
	if runtime.GOOS == "windows" {
		if v := GetAssetLocation("t"); v != "\\xray\\t" {
			t.Error("asset loc: ", v)
		}
	} else {
		if v := GetAssetLocation("t"); v != "/xray/t" {
			t.Error("asset loc: ", v)
		}
	}
}
