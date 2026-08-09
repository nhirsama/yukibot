"""SQLite persistence for bot-wide management state."""

from __future__ import annotations

from yukibot.contracts import Database, Row

from .ports import OwnerIdentity


class SqliteManagementRepository:
    def __init__(self, database: Database, owner: OwnerIdentity) -> None:
        self._database = database
        self._owner = owner

    async def is_admin(self, user_id: int) -> bool:
        row = await self._database.fetch_one(
            "SELECT 1 AS found FROM management_admins WHERE user_id = ?",
            (user_id,),
        )
        return row is not None

    async def list_admins(self) -> tuple[int, ...]:
        rows = await self._database.fetch_all(
            "SELECT user_id FROM management_admins ORDER BY user_id"
        )
        return tuple(_int_column(row, "user_id") for row in rows)

    async def add_admin(self, user_id: int, *, granted_by: int) -> None:
        await self._database.execute(
            """
            INSERT INTO management_admins (user_id, granted_by)
            VALUES (?, ?)
            ON CONFLICT (user_id) DO NOTHING
            """,
            (user_id, granted_by),
        )

    async def remove_admin(self, user_id: int) -> None:
        await self._database.execute(
            "DELETE FROM management_admins WHERE user_id = ?",
            (user_id,),
        )

    async def get_enabled(self, name: str) -> bool | None:
        row = await self._database.fetch_one(
            "SELECT enabled FROM management_modules WHERE name = ?",
            (name,),
        )
        return _bool_column(row, "enabled") if row is not None else None

    async def set_enabled(self, name: str, *, enabled: bool) -> None:
        await self._database.execute(
            """
            INSERT INTO management_modules (name, enabled)
            VALUES (?, ?)
            ON CONFLICT (name) DO UPDATE SET
                enabled = excluded.enabled,
                updated_at = CURRENT_TIMESTAMP
            """,
            (name, int(enabled)),
        )

    async def is_processed(self, chat_id: int, message_id: int) -> bool:
        row = await self._database.fetch_one(
            """
            SELECT 1 AS found
            FROM management_command_receipts
            WHERE account_id = ? AND chat_id = ? AND message_id = ?
            """,
            (self._owner.user_id, chat_id, message_id),
        )
        return row is not None

    async def mark_processed(self, chat_id: int, message_id: int) -> None:
        await self._database.execute(
            """
            INSERT INTO management_command_receipts (account_id, chat_id, message_id)
            VALUES (?, ?, ?)
            ON CONFLICT (account_id, chat_id, message_id) DO NOTHING
            """,
            (self._owner.user_id, chat_id, message_id),
        )


def _int_column(row: Row, key: str) -> int:
    value = row.get(key)
    if not isinstance(value, int):
        raise TypeError(f"database column {key!r} is not an integer")
    return value


def _bool_column(row: Row, key: str) -> bool:
    value = _int_column(row, key)
    if value not in (0, 1):
        raise ValueError(f"database column {key!r} is not a boolean")
    return bool(value)
