package management

import "errors"

// errIdentityUnavailable matches the Telegram account identity port.
var errIdentityUnavailable = errors.New("Telegram account identity is not available")

// Identity is the authenticated account that owns this management view.
// UserID reports the stable Telegram user ID, or an error when the account
// has not authenticated yet.
type Identity interface {
	UserID() (int64, error)
}

// Owner is an account identity fixed at construction.
type Owner struct {
	ID int64
}

// UserID returns the configured account ID.
// A non-positive ID means the account identity is not available.
func (o Owner) UserID() (int64, error) {
	if o.ID <= 0 {
		return 0, errIdentityUnavailable
	}
	return o.ID, nil
}
