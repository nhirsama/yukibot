package management

import "github.com/nhirsama/yukibot/internal/contracts"

// Migrations is the converged management schema, including account-scoped command receipts.
var Migrations = []contracts.Migration{
	{
		Scope:       "management",
		Version:     1,
		Description: "create administrators, module states and account-scoped command receipts",
		Statements: []string{
			`CREATE TABLE management_admins (
    user_id bigint PRIMARY KEY,
    granted_by bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
)`,
			`CREATE TABLE management_modules (
    name text PRIMARY KEY,
    enabled boolean NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
)`,
			`CREATE TABLE management_command_receipts (
    account_id bigint NOT NULL,
    chat_id bigint NOT NULL,
    message_id bigint NOT NULL,
    processed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, chat_id, message_id)
)`,
		},
	},
}
