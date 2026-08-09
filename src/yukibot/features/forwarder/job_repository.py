"""SQLite persistence for feature-owned forwarding jobs."""

from __future__ import annotations

from collections.abc import Sequence

from pydantic import TypeAdapter

from yukibot.contracts import (
    Database,
    Row,
    TelegramMessage,
    TelegramMessageEdited,
    TelegramMessageReceived,
    TelegramMessagesDeleted,
)

from .jobs import ForwardJob, ForwardJobEvent, ForwardJobKind, PendingForwardJob

_JOB_COLUMNS = "id, kind, group_key, payload_json, attempts, available_at"
_MESSAGE_CODEC = TypeAdapter(TelegramMessage)
_DELETION_CODEC = TypeAdapter(TelegramMessagesDeleted)


class SqliteForwardJobRepository:
    def __init__(self, database: Database) -> None:
        self._database = database

    async def enqueue(self, jobs: Sequence[PendingForwardJob]) -> int:
        if not jobs:
            return 0
        inserted = 0
        async with self._database.transaction() as transaction:
            for job in jobs:
                result = await transaction.execute(
                    """
                    INSERT INTO forwarder_jobs (
                        kind, deduplication_key, group_key, payload_json, available_at
                    ) VALUES (?, ?, ?, ?, ?)
                    ON CONFLICT (deduplication_key) DO NOTHING
                    """,
                    (
                        job.kind.value,
                        job.deduplication_key,
                        job.group_key,
                        _event_json(job.event),
                        job.available_at,
                    ),
                )
                inserted += max(result.row_count, 0)
                if job.group_key is not None:
                    await transaction.execute(
                        """
                        UPDATE forwarder_jobs
                        SET available_at = ?, updated_at = CURRENT_TIMESTAMP
                        WHERE group_key = ? AND state = 'pending'
                        """,
                        (job.available_at, job.group_key),
                    )
        return inserted

    async def recover_incomplete(self) -> int:
        result = await self._database.execute(
            """
            UPDATE forwarder_jobs
            SET state = 'pending', updated_at = CURRENT_TIMESTAMP
            WHERE state = 'processing'
            """
        )
        return max(result.row_count, 0)

    async def claim_due(self, now: float) -> Sequence[ForwardJob]:
        async with self._database.transaction() as transaction:
            first = await transaction.fetch_one(
                f"""
                SELECT {_JOB_COLUMNS}
                FROM forwarder_jobs
                WHERE state = 'pending'
                ORDER BY id
                LIMIT 1
                """,
            )
            if first is None:
                return ()
            if _required_number(first, "available_at") > now:
                return ()

            group_key = _optional_str(first, "group_key")
            rows: Sequence[Row]
            if group_key is None:
                rows = (first,)
            else:
                rows = tuple(
                    await transaction.fetch_all(
                        f"""
                        SELECT {_JOB_COLUMNS}
                        FROM forwarder_jobs
                        WHERE state = 'pending' AND group_key = ? AND available_at <= ?
                        ORDER BY id
                        """,
                        (group_key, now),
                    )
                )
            job_ids = tuple(_required_int(row, "id") for row in rows)
            await transaction.execute(
                f"""
                UPDATE forwarder_jobs
                SET state = 'processing', attempts = attempts + 1,
                    updated_at = CURRENT_TIMESTAMP
                WHERE id IN ({_placeholders(job_ids)}) AND state = 'pending'
                """,
                job_ids,
            )
        return tuple(_job_from_row(row) for row in rows)

    async def mark_succeeded(self, job_ids: Sequence[int]) -> None:
        if not job_ids:
            return
        await self._database.execute(
            f"""
            DELETE FROM forwarder_jobs
            WHERE id IN ({_placeholders(job_ids)}) AND state = 'processing'
            """,
            job_ids,
        )

    async def mark_failed(self, job_ids: Sequence[int], error: str) -> None:
        if not job_ids:
            return
        await self._database.execute(
            f"""
            UPDATE forwarder_jobs
            SET state = 'failed', last_error = ?, updated_at = CURRENT_TIMESTAMP
            WHERE id IN ({_placeholders(job_ids)}) AND state = 'processing'
            """,
            (error, *job_ids),
        )

    async def reschedule(
        self,
        job_ids: Sequence[int],
        *,
        available_at: float,
        error: str,
    ) -> None:
        if not job_ids:
            return
        await self._database.execute(
            f"""
            UPDATE forwarder_jobs
            SET state = 'pending', available_at = ?, last_error = ?,
                updated_at = CURRENT_TIMESTAMP
            WHERE id IN ({_placeholders(job_ids)}) AND state = 'processing'
            """,
            (available_at, error, *job_ids),
        )


def _job_from_row(row: Row) -> ForwardJob:
    kind = ForwardJobKind(_required_str(row, "kind"))
    return ForwardJob(
        id=_required_int(row, "id"),
        kind=kind,
        event=_event_from_json(kind, _required_str(row, "payload_json")),
        attempts=_required_int(row, "attempts") + 1,
        group_key=_optional_str(row, "group_key"),
    )


def _event_json(event: ForwardJobEvent) -> str:
    if isinstance(event, (TelegramMessageReceived, TelegramMessageEdited)):
        return _MESSAGE_CODEC.dump_json(event.message).decode()
    return _DELETION_CODEC.dump_json(event).decode()


def _event_from_json(kind: ForwardJobKind, payload: str) -> ForwardJobEvent:
    if kind is ForwardJobKind.DELETE:
        return _DELETION_CODEC.validate_json(payload)
    message = _MESSAGE_CODEC.validate_json(payload)
    if kind is ForwardJobKind.RECEIVE:
        return TelegramMessageReceived(message)
    return TelegramMessageEdited(message)


def _placeholders(values: Sequence[object]) -> str:
    if not values:
        raise ValueError("at least one SQL value is required")
    return ",".join("?" for _ in values)


def _required_str(row: Row, key: str) -> str:
    value = row.get(key)
    if not isinstance(value, str):
        raise TypeError(f"database column {key!r} is not a string")
    return value


def _optional_str(row: Row, key: str) -> str | None:
    value = row.get(key)
    if value is not None and not isinstance(value, str):
        raise TypeError(f"database column {key!r} is not a string or null")
    return value


def _required_int(row: Row, key: str) -> int:
    value = row.get(key)
    if not isinstance(value, int):
        raise TypeError(f"database column {key!r} is not an integer")
    return value


def _required_number(row: Row, key: str) -> float:
    value = row.get(key)
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise TypeError(f"database column {key!r} is not numeric")
    return float(value)
