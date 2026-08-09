from datetime import UTC, datetime, timedelta
from pathlib import Path

from yukibot.adapters.database import MigrationRunner, SqliteDatabase
from yukibot.contracts import (
    MessageRef,
    TelegramContentType,
    TelegramMessage,
    TelegramMessageEdited,
    TelegramMessageReceived,
    TelegramMessagesDeleted,
)
from yukibot.features.forwarder.job_repository import SqliteForwardJobRepository
from yukibot.features.forwarder.jobs import ForwardJobKind, pending_jobs_for_event
from yukibot.features.forwarder.migrations import FORWARDER_MIGRATIONS


def message(message_id: int, *, grouped_id: int | None = None) -> TelegramMessage:
    occurred_at = datetime(2026, 1, 1, tzinfo=UTC)
    return TelegramMessage(
        MessageRef(-1001, message_id),
        TelegramContentType.PHOTO if grouped_id is not None else TelegramContentType.TEXT,
        occurred_at,
        grouped_id=grouped_id,
        text=None if grouped_id is not None else "hello",
        caption="album" if grouped_id is not None else None,
        edited_at=occurred_at + timedelta(minutes=1),
    )


async def open_repository(path: Path) -> tuple[SqliteDatabase, SqliteForwardJobRepository]:
    database = SqliteDatabase(f"sqlite:///{path}")
    await database.open()
    await MigrationRunner(database, FORWARDER_MIGRATIONS).upgrade()
    return database, SqliteForwardJobRepository(database)


async def test_enqueue_is_deduplicated_and_payload_round_trips(tmp_path: Path) -> None:
    database, repository = await open_repository(tmp_path / "jobs.db")
    received = TelegramMessageReceived(message(10))
    jobs = pending_jobs_for_event(received, now=100.0, album_delay=0.8)
    try:
        assert await repository.enqueue(jobs) == 1
        assert await repository.enqueue(jobs) == 0

        claimed = tuple(await repository.claim_due(100.0))
        assert len(claimed) == 1
        assert claimed[0].kind is ForwardJobKind.RECEIVE
        assert claimed[0].event == received
        assert claimed[0].attempts == 1

        await repository.mark_succeeded((claimed[0].id,))
        row = await database.fetch_one(
            "SELECT state, attempts FROM forwarder_jobs WHERE id = ?", (claimed[0].id,)
        )
        assert row is None
    finally:
        await database.close()


async def test_album_window_is_extended_and_claimed_as_one_batch(tmp_path: Path) -> None:
    database, repository = await open_repository(tmp_path / "album-jobs.db")
    first = pending_jobs_for_event(
        TelegramMessageReceived(message(10, grouped_id=50)),
        now=100.0,
        album_delay=0.8,
    )
    second = pending_jobs_for_event(
        TelegramMessageReceived(message(11, grouped_id=50)),
        now=100.5,
        album_delay=0.8,
    )
    try:
        await repository.enqueue(first)
        await repository.enqueue(second)
        assert await repository.claim_due(100.8) == ()

        claimed = tuple(await repository.claim_due(101.3))
        assert [job.id for job in claimed] == [1, 2]
        assert [job.event.message.ref.message_id for job in claimed] == [10, 11]  # type: ignore[union-attr]

        await repository.reschedule(
            tuple(job.id for job in claimed),
            available_at=105.0,
            error="temporary",
        )
        assert await repository.claim_due(104.9) == ()
        retried = tuple(await repository.claim_due(105.0))
        assert [job.attempts for job in retried] == [2, 2]
    finally:
        await database.close()


async def test_processing_jobs_are_recovered_after_restart(tmp_path: Path) -> None:
    database, repository = await open_repository(tmp_path / "recovery.db")
    event = TelegramMessageEdited(message(10))
    try:
        await repository.enqueue(pending_jobs_for_event(event, now=100.0, album_delay=0.8))
        first = tuple(await repository.claim_due(100.0))
        assert first[0].attempts == 1

        assert await repository.recover_incomplete() == 1
        recovered = tuple(await repository.claim_due(100.0))
        assert recovered[0].event == event
        assert recovered[0].attempts == 2
    finally:
        await database.close()


async def test_edits_with_the_same_timestamp_but_different_content_are_distinct(
    tmp_path: Path,
) -> None:
    database, repository = await open_repository(tmp_path / "edit-identity.db")
    original = message(10)
    changed = TelegramMessage(
        original.ref,
        original.content_type,
        original.occurred_at,
        text="changed",
        edited_at=original.edited_at,
    )
    first = pending_jobs_for_event(TelegramMessageEdited(original), now=100.0, album_delay=0.8)
    second = pending_jobs_for_event(TelegramMessageEdited(changed), now=100.0, album_delay=0.8)
    try:
        assert first[0].deduplication_key != second[0].deduplication_key
        assert await repository.enqueue(first) == 1
        assert await repository.enqueue(second) == 1
    finally:
        await database.close()


async def test_later_edit_cannot_overtake_a_buffered_album(tmp_path: Path) -> None:
    database, repository = await open_repository(tmp_path / "ordered-jobs.db")
    album = TelegramMessageReceived(message(10, grouped_id=50))
    edit = TelegramMessageEdited(message(11))
    try:
        await repository.enqueue(pending_jobs_for_event(album, now=100.0, album_delay=0.8))
        await repository.enqueue(pending_jobs_for_event(edit, now=100.1, album_delay=0.8))

        assert await repository.claim_due(100.1) == ()
        claimed_album = tuple(await repository.claim_due(100.8))
        assert claimed_album[0].kind is ForwardJobKind.RECEIVE
        await repository.mark_succeeded((claimed_album[0].id,))

        claimed_edit = tuple(await repository.claim_due(100.8))
        assert claimed_edit[0].kind is ForwardJobKind.EDIT
    finally:
        await database.close()


async def test_delete_event_is_split_into_independently_deduplicated_jobs(
    tmp_path: Path,
) -> None:
    database, repository = await open_repository(tmp_path / "delete-jobs.db")
    event = TelegramMessagesDeleted((10, 11), datetime.now(UTC), chat_id=-1001)
    jobs = pending_jobs_for_event(event, now=100.0, album_delay=0.8)
    try:
        assert await repository.enqueue(jobs) == 2
        first = tuple(await repository.claim_due(100.0))
        assert len(first) == 1
        assert first[0].kind is ForwardJobKind.DELETE
        await repository.mark_succeeded((first[0].id,))

        second = tuple(await repository.claim_due(100.0))
        assert len(second) == 1
        assert second[0].event.message_ids == (11,)  # type: ignore[union-attr]
    finally:
        await database.close()
