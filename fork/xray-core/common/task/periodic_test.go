package task_test

import (
	"testing"
	"time"

	"github.com/xtls/xray-core/common"
	. "github.com/xtls/xray-core/common/task"
)

func TestPeriodicTaskStop(t *testing.T) {
	value := 0
	task := &Periodic{
		Interval: time.Second * 2,
		Execute: func() error {
			value++
			return nil
		},
	}
	if err := common.Must(task.Start()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second * 5)
	if err := common.Must(task.Close()); err != nil {
		t.Fatal(err)
	}
	if value != 3 {
		t.Fatal("expected 3, but got ", value)
	}
	time.Sleep(time.Second * 4)
	if value != 3 {
		t.Fatal("expected 3, but got ", value)
	}
	if err := common.Must(task.Start()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second * 3)
	if value != 5 {
		t.Fatal("Expected 5, but ", value)
	}
	if err := common.Must(task.Close()); err != nil {
		t.Fatal(err)
	}
}
