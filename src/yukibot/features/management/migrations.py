"""Database migrations owned by the management feature."""

from yukibot.contracts import Migration

MANAGEMENT_MIGRATIONS = (
    Migration(
        scope="management",
        version=1,
        description="create administrators, module states and command receipts",
        statements=(
            """
            CREATE TABLE management_admins (
                user_id INTEGER PRIMARY KEY,
                granted_by INTEGER NOT NULL,
                created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
            )
            """,
            """
            CREATE TABLE management_modules (
                name TEXT PRIMARY KEY,
                enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
                updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
            )
            """,
            """
            CREATE TABLE management_command_receipts (
                chat_id INTEGER NOT NULL,
                message_id INTEGER NOT NULL,
                processed_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
                PRIMARY KEY (chat_id, message_id)
            )
            """,
        ),
    ),
    Migration(
        scope="management",
        version=2,
        description="scope command receipts to the authenticated Telegram account",
        statements=(
            """
            DROP TABLE management_command_receipts
            """,
            """
            CREATE TABLE management_command_receipts (
                account_id INTEGER NOT NULL,
                chat_id INTEGER NOT NULL,
                message_id INTEGER NOT NULL,
                processed_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
                PRIMARY KEY (account_id, chat_id, message_id)
            )
            """,
        ),
    ),
)

__all__ = ["MANAGEMENT_MIGRATIONS"]
