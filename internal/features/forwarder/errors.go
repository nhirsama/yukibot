package forwarder

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"time"
)

// ValueError is a validation failure surfaced as command text.
type ValueError struct{ msg string }

func (e ValueError) Error() string { return e.msg }

func valueErr(msg string) error { return ValueError{msg: msg} }

// NewValueError returns a validation error with the given message.
func NewValueError(message string) ValueError { return ValueError{msg: message} }

// ForwarderError marks errors the route command reports as text.
type forwarderMarker interface{ ForwarderError() }

// NativeForwardUnsupported means Telegram cannot native-forward the source.
type NativeForwardUnsupported struct{}

func (NativeForwardUnsupported) Error() string   { return "" }
func (NativeForwardUnsupported) ForwarderError() {}

// MessageNotModified means the destination already has the requested content.
type MessageNotModified struct{}

func (MessageNotModified) Error() string   { return "" }
func (MessageNotModified) ForwarderError() {}

// MessageNotFound means the referenced message no longer exists.
type MessageNotFound struct{}

func (MessageNotFound) Error() string   { return "" }
func (MessageNotFound) ForwarderError() {}

// PermanentDeliveryError cannot succeed without a configuration or permission change.
type PermanentDeliveryError struct{ msg string }

func (e PermanentDeliveryError) Error() string {
	if e.msg == "" {
		return ""
	}
	return e.msg
}
func (PermanentDeliveryError) ForwarderError() {}

// NewPermanentDeliveryError returns a permanent delivery failure.
func NewPermanentDeliveryError(msg string) PermanentDeliveryError {
	return PermanentDeliveryError{msg: msg}
}

// DeliveryResultMismatch means Telegram returned a different number of messages.
type DeliveryResultMismatch struct{ msg string }

func (e DeliveryResultMismatch) Error() string { return e.msg }
func (DeliveryResultMismatch) ForwarderError() {}

// NewDeliveryResultMismatch returns a sent-count mismatch.
func NewDeliveryResultMismatch(msg string) DeliveryResultMismatch {
	return DeliveryResultMismatch{msg: msg}
}

// PartialDeliveryState means only part of an album has a destination mapping.
type PartialDeliveryState struct{ msg string }

func (e PartialDeliveryState) Error() string { return e.msg }
func (PartialDeliveryState) ForwarderError() {}

// RouteCycleError means enabled routes contain a forwarding cycle.
type RouteCycleError struct{ msg string }

func (e RouteCycleError) Error() string { return e.msg }
func (RouteCycleError) ForwarderError() {}

// RouteNotFoundError means the requested route does not exist.
type RouteNotFoundError struct{ msg string }

func (e RouteNotFoundError) Error() string { return e.msg }
func (RouteNotFoundError) ForwarderError() {}

// KeyError is a missing repository key. Management maps it to RouteNotFoundError.
type KeyError struct{ ID int }

func (e KeyError) Error() string { return strconv.Itoa(e.ID) }

// RetryAfter means the operation may be retried after Delay.
type RetryAfter struct{ Delay time.Duration }

func (e RetryAfter) Error() string {
	return "retry after " + formatSeconds(e.Delay) + " seconds"
}
func (RetryAfter) ForwarderError() {}

// NewRetryAfter rejects a negative delay.
func NewRetryAfter(delay time.Duration) (RetryAfter, error) {
	if delay < 0 {
		return RetryAfter{}, valueErr("seconds must not be negative")
	}
	return RetryAfter{Delay: delay}, nil
}

func formatSeconds(delay time.Duration) string {
	return strconv.FormatFloat(delay.Seconds(), 'g', -1, 64)
}

func isReportedCommandError(err error) bool {
	var value ValueError
	var marker forwarderMarker
	return errors.As(err, &value) || errors.As(err, &marker)
}

func commandErrorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func permanentDeliveryf(format string, args ...any) PermanentDeliveryError {
	return PermanentDeliveryError{msg: fmt.Sprintf(format, args...)}
}

func typeName(err error) string {
	if err == nil {
		return "nil"
	}
	kind := reflect.TypeOf(err)
	for kind.Kind() == reflect.Pointer {
		kind = kind.Elem()
	}
	if name := kind.Name(); name != "" {
		return name
	}
	return kind.String()
}
