package log

import (
	"context"
	"io"
	"log"
	"os"
	"time"

	"github.com/xtls/xray-core/common/platform"
)

// Writer is the interface for writing logs.
type Writer interface {
	Write(string) error
	io.Closer
}

// WriterCreator is a function to create LogWriters.
type WriterCreator func() Writer

type generalLogger struct {
	creator WriterCreator
	buffer  chan Message
	// ponytail: raw chan instead of semaphore.Instance (token held while run active).
	access     chan struct{}
	doneCtx    context.Context
	doneCancel context.CancelFunc
}

type serverityLogger struct {
	inner    *generalLogger
	logLevel Severity
}

func newGeneralLogger(creator WriterCreator) *generalLogger {
	access := make(chan struct{}, 1)
	access <- struct{}{}
	doneCtx, doneCancel := context.WithCancel(context.Background())
	return &generalLogger{
		creator:    creator,
		buffer:     make(chan Message, 128),
		access:     access,
		doneCtx:    doneCtx,
		doneCancel: doneCancel,
	}
}

// NewLogger returns a generic log handler that can handle all type of messages.
func NewLogger(logWriterCreator WriterCreator) Handler {
	return newGeneralLogger(logWriterCreator)
}

func ReplaceWithSeverityLogger(severity Severity) {
	RegisterHandler(&serverityLogger{
		inner:    newGeneralLogger(CreateStdoutLogWriter()),
		logLevel: severity,
	})
}

func (l *serverityLogger) Handle(msg Message) {
	switch msg := msg.(type) {
	case *GeneralMessage:
		if msg.Severity <= l.logLevel {
			l.inner.Handle(msg)
		}
	default:
		l.inner.Handle(msg)
	}
}

func (l *generalLogger) run() {
	defer func() { l.access <- struct{}{} }()

	dataWritten := false
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	logger := l.creator()
	if logger == nil {
		return
	}
	defer logger.Close()

	for {
		select {
		case <-l.doneCtx.Done():
			return
		case msg := <-l.buffer:
			logger.Write(msg.String() + platform.LineSeparator())
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
	return nil
}

type consoleLogWriter struct {
	logger *log.Logger
}

func (w *consoleLogWriter) Write(s string) error {
	w.logger.Print(s)
	return nil
}

func (w *consoleLogWriter) Close() error {
	return nil
}

type fileLogWriter struct {
	file   *os.File
	logger *log.Logger
}

func (w *fileLogWriter) Write(s string) error {
	w.logger.Print(s)
	return nil
}

func (w *fileLogWriter) Close() error {
	return w.file.Close()
}

// CreateStdoutLogWriter returns a LogWriterCreator that creates LogWriter for stdout.
func CreateStdoutLogWriter() WriterCreator {
	return func() Writer {
		return &consoleLogWriter{
			logger: log.New(os.Stdout, "", log.Ldate|log.Ltime|log.Lmicroseconds),
		}
	}
}

// CreateStderrLogWriter returns a LogWriterCreator that creates LogWriter for stderr.
func CreateStderrLogWriter() WriterCreator {
	return func() Writer {
		return &consoleLogWriter{
			logger: log.New(os.Stderr, "", log.Ldate|log.Ltime|log.Lmicroseconds),
		}
	}
}

// CreateFileLogWriter returns a LogWriterCreator that creates LogWriter for the given file.
func CreateFileLogWriter(path string) (WriterCreator, error) {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	file.Close()
	return func() Writer {
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
		if err != nil {
			return nil
		}
		return &fileLogWriter{
			file:   file,
			logger: log.New(file, "", log.Ldate|log.Ltime|log.Lmicroseconds),
		}
	}, nil
}

func init() {
	RegisterHandler(NewLogger(CreateStdoutLogWriter()))
}
