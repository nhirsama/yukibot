package summarizer

import "errors"

// SummarizerError is an expected summarizer failure.
type SummarizerError struct {
	Msg string
}

func (e *SummarizerError) Error() string {
	if e == nil {
		return ""
	}
	return e.Msg
}

// SummaryRuleNotFoundError means the requested summary rule does not exist.
type SummaryRuleNotFoundError struct {
	Msg string
}

func (e *SummaryRuleNotFoundError) Error() string {
	if e == nil {
		return ""
	}
	return e.Msg
}

func (e *SummaryRuleNotFoundError) Unwrap() error {
	if e == nil {
		return nil
	}
	return &SummarizerError{Msg: e.Msg}
}

// SummaryModelUnavailableError means the configured model cannot generate a summary.
type SummaryModelUnavailableError struct {
	Msg string
}

func (e *SummaryModelUnavailableError) Error() string {
	if e == nil {
		return ""
	}
	return e.Msg
}

func (e *SummaryModelUnavailableError) Unwrap() error {
	if e == nil {
		return nil
	}
	return &SummarizerError{Msg: e.Msg}
}

// NoMessagesToSummarizeError means the selected window has no useful messages.
type NoMessagesToSummarizeError struct {
	Msg string
}

func (e *NoMessagesToSummarizeError) Error() string {
	if e == nil {
		return ""
	}
	return e.Msg
}

func (e *NoMessagesToSummarizeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return &SummarizerError{Msg: e.Msg}
}

// ValueError is a validation failure. Command handlers surface its message.
type ValueError struct {
	Msg string
}

func (e *ValueError) Error() string {
	if e == nil {
		return ""
	}
	return e.Msg
}

// TypeName matches the Python exception name wrapped around model failures.
func (e *ValueError) TypeName() string { return "ValueError" }

// ErrRuleMissing is returned by repositories when Replace targets an unknown rule.
var ErrRuleMissing = errors.New("summary rule is missing")
