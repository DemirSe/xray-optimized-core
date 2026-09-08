package reverse_test

import (
	"testing"

	"github.com/xtls/xray-core/app/reverse"
	"github.com/xtls/xray-core/common"
)

func TestStaticPickerEmpty(t *testing.T) {
	picker, err := reverse.NewStaticMuxPicker()
	if err := common.Must(err); err != nil {
		t.Fatal(err)
	}
	worker, err := picker.PickAvailable()
	if err == nil {
		t.Error("expected error, but nil")
	}
	if worker != nil {
		t.Error("expected nil worker, but not nil")
	}
}
