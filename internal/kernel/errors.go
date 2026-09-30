package kernel

import "fmt"

// KernelError is the base type for kernel failures.
type KernelError struct{ msg string }

func (e *KernelError) Error() string { return e.msg }

// DuplicateFeatureError means two lifecycle components share a name.
type DuplicateFeatureError struct{ Message string }

func (e *DuplicateFeatureError) Error() string { return e.Message }

// SupervisorClosedError means a task was submitted after shutdown began.
type SupervisorClosedError struct{}

func (e *SupervisorClosedError) Error() string { return "task supervisor is closed" }

// LifecycleStartError is raised when a feature fails during startup.
type LifecycleStartError struct {
	FeatureName string
	Cause       error
}

func (e *LifecycleStartError) Error() string {
	return fmt.Sprintf("failed to start feature %q: %v", e.FeatureName, e.Cause)
}

func (e *LifecycleStartError) Unwrap() error { return e.Cause }

// StopFailure is one feature that failed while stopping.
type StopFailure struct {
	Name string
	Err  error
}

// LifecycleStopError aggregates reverse-order shutdown failures.
type LifecycleStopError struct {
	Failures []StopFailure
}

func (e *LifecycleStopError) Error() string {
	names := make([]string, len(e.Failures))
	for i, failure := range e.Failures {
		names[i] = failure.Name
	}
	return "failed to stop features: " + joinComma(names)
}

func (e *LifecycleStopError) Unwrap() error {
	if len(e.Failures) == 0 {
		return nil
	}
	return e.Failures[0].Err
}

func joinComma(parts []string) string {
	out := ""
	for i, part := range parts {
		if i > 0 {
			out += ", "
		}
		out += part
	}
	return out
}
