package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/nhirsama/yukibot/internal/features/management"
	"github.com/nhirsama/yukibot/internal/kernel"
	"github.com/nhirsama/yukibot/internal/storage/dbsql"
)

// Repository is the PostgreSQL adapter for administrators, module flags, and command receipts.
type Repository struct {
	q        *dbsql.Queries
	identity management.Identity
}

var (
	_ management.AdminStore    = (*Repository)(nil)
	_ kernel.ModuleStateStore  = (*Repository)(nil)
	_ kernel.CommandReceiptStore = (*Repository)(nil)
)

// NewRepository binds queries to the current account identity.
// identity is read when a receipt is checked, not at construction.
func NewRepository(db dbsql.DBTX, identity management.Identity) *Repository {
	return &Repository{q: dbsql.New(db), identity: identity}
}

// IsAdmin reports whether userID is a delegated administrator.
func (r *Repository) IsAdmin(ctx context.Context, userID int64) (bool, error) {
	return r.q.IsAdmin(ctx, userID)
}

// ListAdmins returns delegated administrator user IDs in ascending order.
func (r *Repository) ListAdmins(ctx context.Context) ([]int64, error) {
	rows, err := r.q.ListAdmins(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.UserID)
	}
	return ids, nil
}

// AddAdmin inserts userID. A repeat add does not change grantedBy.
func (r *Repository) AddAdmin(ctx context.Context, userID int64, grantedBy int64) error {
	return r.q.AddAdmin(ctx, dbsql.AddAdminParams{UserID: userID, GrantedBy: grantedBy})
}

// RemoveAdmin deletes userID. Removing an unknown user succeeds.
func (r *Repository) RemoveAdmin(ctx context.Context, userID int64) error {
	return r.q.RemoveAdmin(ctx, userID)
}

// GetEnabled returns the desired flag. A nil pointer means the module has no row.
func (r *Repository) GetEnabled(ctx context.Context, name string) (*bool, error) {
	enabled, err := r.q.GetModuleEnabled(ctx, name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	value := enabled
	return &value, nil
}

// SetEnabled upserts the desired flag for name.
func (r *Repository) SetEnabled(ctx context.Context, name string, enabled bool) error {
	return r.q.SetModuleEnabled(ctx, dbsql.SetModuleEnabledParams{Name: name, Enabled: enabled})
}

// IsProcessed reports whether this account already handled the chat message.
func (r *Repository) IsProcessed(ctx context.Context, chatID int64, messageID int) (bool, error) {
	accountID, err := r.accountID()
	if err != nil {
		return false, err
	}
	return r.q.IsCommandProcessed(ctx, dbsql.IsCommandProcessedParams{
		AccountID: accountID,
		ChatID:    chatID,
		MessageID: int64(messageID),
	})
}

// MarkProcessed records the chat message for this account. Repeating it is a no-op.
func (r *Repository) MarkProcessed(ctx context.Context, chatID int64, messageID int) error {
	accountID, err := r.accountID()
	if err != nil {
		return err
	}
	return r.q.MarkCommandProcessed(ctx, dbsql.MarkCommandProcessedParams{
		AccountID: accountID,
		ChatID:    chatID,
		MessageID: int64(messageID),
	})
}

func (r *Repository) accountID() (int64, error) {
	if r.identity == nil {
		return 0, errors.New("Telegram account identity is not available")
	}
	return r.identity.UserID()
}
