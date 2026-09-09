package log

import (
	"context"
	"io"
	"log"
	"os"
	"time"

	"github.com/xtls/xray-core/common/platform"
)

type generalLogger struct {
	logger *log.Logger
	closer io.Closer
	// ponytail: level check folded in from deleted serverityLogger.
	logLevel Severity
	buffer   chan Message
	// ponytail: raw chan instead of semaphore.Instance (token held while run active).
	access     chan struct{}
	doneCtx    context.Context
	doneCancel context.CancelFunc
}

func newGeneralLogger(out io.Writer, closer io.Closer, level Severity) *generalLogger {
	access := make(chan struct{}, 1)
	access <- struct{}{}
	doneCtx, doneCancel := context.WithCancel(context.Background())
	return &generalLogger{
		logger:     log.New(out, "", log.Ldate|log.Ltime|log.Lmicroseconds),
		closer:     closer,
		logLevel:   level,
		buffer:     make(chan Message, 128),
		access:     access,
		doneCtx:    doneCtx,
		doneCancel: doneCancel,
	}
}

// NewLogger returns a generic log handler that can handle all type of messages.
func NewLogger(out io.Writer) Handler {
	var closer io.Closer
	if c, ok := out.(io.Closer); ok {
		closer = c
	}
	return newGeneralLogger(out, closer, Severity_Debug)
}

func ReplaceWithSeverityLogger(severity Severity) {
	RegisterHandler(newGeneralLogger(os.Stdout, nil, severity))
}

func (l *generalLogger) run() {
	defer func() { l.access <- struct{}{} }()

	dataWritten := false
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-l.doneCtx.Done():
			return
		case msg := <-l.buffer:
			l.logger.Print(msg.String() + platform.LineSeparator())
			dataWritten = true
		case <-ticker.C:
			if !dataWritten {
				return
			}
			dataWritten = false
		}
	}
}

func (l *generalLogger) Handle(msg Message) {
	if gm, ok := msg.(*GeneralMessage); ok && gm.Severity > l.logLevel {
		return
	}

	select {
	case l.buffer <- msg:
	default:
	}

	select {
	case <-l.access:
		go l.run()
	default:
	}
}

func (l *generalLogger) Close() error {
	l.doneCancel()
	if l.closer != nil {
		return l.closer.Close()
	}
	return nil
}

// CreateFileLogWriter opens the log file once and returns it as an io.Writer.
func CreateFileLogWriter(path string) (io.Writer, error) {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	return file, nil
}

func init() {
	RegisterHandler(NewLogger(os.Stdout))
}
