package kernel

// Logger is the narrow logging port used by kernel mechanisms.
type Logger interface {
	Info(msg string, args ...any)
	Error(msg string, args ...any)
	Debug(msg string, args ...any)
}

type discardLogger struct{}

func (discardLogger) Info(string, ...any)  {}
func (discardLogger) Error(string, ...any) {}
func (discardLogger) Debug(string, ...any) {}

// DiscardLogger drops every record.
func DiscardLogger() Logger { return discardLogger{} }

func orLogger(logger Logger) Logger {
	if logger == nil {
		return DiscardLogger()
	}
	return logger
}
