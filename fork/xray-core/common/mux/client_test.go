package mux_test

import (
	"context"
	"testing"
	"time"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/mux"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/pipe"
)

// ponytail: local stub replaces deleted testing/mocks MuxClientWorkerFactory.
type stubWorkerFactory struct {
	create func() (*mux.ClientWorker, error)
}

func (f stubWorkerFactory) Create() (*mux.ClientWorker, error) {
	return f.create()
}

func TestIncrementalPickerFailure(t *testing.T) {
	picker := mux.IncrementalWorkerPicker{
		Factory: stubWorkerFactory{create: func() (*mux.ClientWorker, error) {
			return nil, errors.New("test")
		}},
	}

	_, err := picker.PickAvailable()
	if err == nil {
		t.Error("expected error, but nil")
	}
}

func TestClientWorkerEOF(t *testing.T) {
	reader, writer := pipe.New(pipe.WithoutSizeLimit())
	if err := common.Must(writer.Close()); err != nil {
		t.Fatal(err)
	}

	worker, err := mux.NewClientWorker(transport.Link{Reader: reader, Writer: writer}, mux.ClientStrategy{})
	if err := common.Must(err); err != nil {
		t.Fatal(err)
	}

	time.Sleep(time.Millisecond * 500)

	f := worker.Dispatch(context.Background(), nil)
	if f {
		t.Error("expected failed dispatching, but actually not")
	}
}

func TestClientWorkerClose(t *testing.T) {
	r1, w1 := pipe.New(pipe.WithoutSizeLimit())
	worker1, err := mux.NewClientWorker(transport.Link{
		Reader: r1,
		Writer: w1,
	}, mux.ClientStrategy{
		MaxConcurrency: 4,
		MaxConnection:  4,
	})
	if err := common.Must(err); err != nil {
		t.Fatal(err)
	}

	r2, w2 := pipe.New(pipe.WithoutSizeLimit())
	worker2, err := mux.NewClientWorker(transport.Link{
		Reader: r2,
		Writer: w2,
	}, mux.ClientStrategy{
		MaxConcurrency: 4,
		MaxConnection:  4,
	})
	if err := common.Must(err); err != nil {
		t.Fatal(err)
	}

	workers := []*mux.ClientWorker{worker1, worker2}
	next := 0
	factory := stubWorkerFactory{create: func() (*mux.ClientWorker, error) {
		w := workers[next]
		next++
		return w, nil
	}}

	picker := &mux.IncrementalWorkerPicker{
		Factory: factory,
	}
	manager := &mux.ClientManager{
		Picker: picker,
	}

	tr1, tw1 := pipe.New(pipe.WithoutSizeLimit())
	ctx1 := session.ContextWithOutbounds(context.Background(), []*session.Outbound{{
		Target: net.TCPDestination(net.DomainAddress("www.example.com"), 80),
	}})
	if err := common.Must(manager.Dispatch(ctx1, &transport.Link{
		Reader: tr1,
		Writer: tw1,
	})); err != nil {
		t.Fatal(err)
	}
	defer tw1.Close()

	if err := common.Must(w1.Close()); err != nil {
		t.Fatal(err)
	}

	time.Sleep(time.Millisecond * 500)
	if !worker1.Closed() {
		t.Error("worker1 is not finished")
	}

	tr2, tw2 := pipe.New(pipe.WithoutSizeLimit())
	ctx2 := session.ContextWithOutbounds(context.Background(), []*session.Outbound{{
		Target: net.TCPDestination(net.DomainAddress("www.example.com"), 80),
	}})
	if err := common.Must(manager.Dispatch(ctx2, &transport.Link{
		Reader: tr2,
		Writer: tw2,
	})); err != nil {
		t.Fatal(err)
	}
	defer tw2.Close()

	if err := common.Must(w2.Close()); err != nil {
		t.Fatal(err)
	}
}
