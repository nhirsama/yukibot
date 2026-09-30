package telegram

import (
	"errors"
	"sync"
)

// AccountIdentity is the authenticated Telegram user.
// UserID fails until login fills it, so management can depend on the port
// without this package importing the feature.
type AccountIdentity struct {
	mu sync.Mutex
	id int64
}

// Set records the logged-in user ID.
func (a *AccountIdentity) Set(id int64) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.id = id
	a.mu.Unlock()
}

// Clear forgets the account. Startup failure uses this.
func (a *AccountIdentity) Clear() { a.Set(0) }

// UserID returns the stable Telegram user ID.
func (a *AccountIdentity) UserID() (int64, error) {
	if a == nil {
		return 0, errors.New("Telegram account identity is not available")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.id <= 0 {
		return 0, errors.New("Telegram account identity is not available")
	}
	return a.id, nil
}
