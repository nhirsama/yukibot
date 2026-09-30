package management

// PermissionError means the actor may not change administrators.
type PermissionError struct{}

func (e *PermissionError) Error() string {
	return "administrator permission is required"
}

// ValueError is a rejected management argument.
type ValueError struct {
	Message string
}

func (e *ValueError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}
