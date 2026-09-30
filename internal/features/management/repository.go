package management

import (
	"context"
	"sort"
	"sync"

	"github.com/nhirsama/yukibot/internal/kernel"
)

// AdminStore is the administrator directory used by the management service.
type AdminStore interface {
	IsAdmin(ctx context.Context, userID int64) (bool, error)
	ListAdmins(ctx context.Context) ([]int64, error)
	AddAdmin(ctx context.Context, userID int64, grantedBy int64) error
	RemoveAdmin(ctx context.Context, userID int64) error
}

type receiptKey struct {
	accountID int64
	chatID    int64
	messageID int
}

// store is shared by account views. Administrators and module flags are global.
// Command receipts include the account ID.
type store struct {
	mu       sync.Mutex
	admins   map[int64]int64
	modules  map[string]bool
	receipts map[receiptKey]struct{}
}

func newStore() *store {
	return &store{
		admins:   map[int64]int64{},
		modules:  map[string]bool{},
		receipts: map[receiptKey]struct{}{},
	}
}

// MemoryRepository is an in-memory administration, module-flag, and receipt store.
// WithIdentity shares administrators and module flags. Receipts are scoped by
// the identity user ID together with the chat ID and message ID.
type MemoryRepository struct {
	store    *store
	identity Identity
}

var (
	_ kernel.ModuleStateStore    = (*MemoryRepository)(nil)
	_ kernel.CommandReceiptStore = (*MemoryRepository)(nil)
	_ AdminStore                 = (*MemoryRepository)(nil)
)

// NewMemoryRepository returns a repository for identity with its own store.
func NewMemoryRepository(identity Identity) *MemoryRepository {
	return &MemoryRepository{store: newStore(), identity: identity}
}

// WithIdentity returns a view of the same administrators, module flags, and
// receipts bound to another account identity.
func (r *MemoryRepository) WithIdentity(identity Identity) *MemoryRepository {
	if r == nil {
		return NewMemoryRepository(identity)
	}
	return &MemoryRepository{store: r.store, identity: identity}
}

// IsAdmin reports whether userID was stored as a delegated administrator.
// The account owner is not an administrator unless explicitly stored.
func (r *MemoryRepository) IsAdmin(ctx context.Context, userID int64) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	_, ok := r.store.admins[userID]
	return ok, nil
}

// ListAdmins returns stored administrator user IDs in ascending order.
func (r *MemoryRepository) ListAdmins(ctx context.Context) ([]int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	ids := make([]int64, 0, len(r.store.admins))
	for id := range r.store.admins {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

// AddAdmin stores userID unless that user is already an administrator.
// A repeat add does not change grantedBy.
func (r *MemoryRepository) AddAdmin(ctx context.Context, userID int64, grantedBy int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	if _, ok := r.store.admins[userID]; ok {
		return nil
	}
	r.store.admins[userID] = grantedBy
	return nil
}

// RemoveAdmin deletes userID. Removing an unknown user succeeds.
func (r *MemoryRepository) RemoveAdmin(ctx context.Context, userID int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	delete(r.store.admins, userID)
	return nil
}

// GrantedBy reports the stored grantor. ok is false when userID is not an administrator.
func (r *MemoryRepository) GrantedBy(ctx context.Context, userID int64) (int64, bool, error) {
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	grantedBy, ok := r.store.admins[userID]
	return grantedBy, ok, nil
}

// GetEnabled returns the desired flag. A nil pointer means the module has no row.
func (r *MemoryRepository) GetEnabled(ctx context.Context, name string) (*bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	enabled, ok := r.store.modules[name]
	if !ok {
		return nil, nil
	}
	value := enabled
	return &value, nil
}

// SetEnabled upserts the desired flag for name.
func (r *MemoryRepository) SetEnabled(ctx context.Context, name string, enabled bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	r.store.modules[name] = enabled
	return nil
}

// IsProcessed reports whether this account already handled the chat message.
func (r *MemoryRepository) IsProcessed(ctx context.Context, chatID int64, messageID int) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	accountID, err := r.accountID()
	if err != nil {
		return false, err
	}
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	_, ok := r.store.receipts[receiptKey{accountID: accountID, chatID: chatID, messageID: messageID}]
	return ok, nil
}

// MarkProcessed records the chat message for this account. Repeating it is a no-op.
func (r *MemoryRepository) MarkProcessed(ctx context.Context, chatID int64, messageID int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	accountID, err := r.accountID()
	if err != nil {
		return err
	}
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	r.store.receipts[receiptKey{accountID: accountID, chatID: chatID, messageID: messageID}] = struct{}{}
	return nil
}

func (r *MemoryRepository) accountID() (int64, error) {
	if r.identity == nil {
		return 0, errIdentityUnavailable
	}
	return r.identity.UserID()
}
