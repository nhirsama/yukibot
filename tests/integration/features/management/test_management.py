from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

import pytest

from yukibot.adapters.database import MigrationRunner, SqliteDatabase
from yukibot.features.management.commands import ADMIN_HELP, ManagementCommands
from yukibot.features.management.migrations import MANAGEMENT_MIGRATIONS
from yukibot.features.management.repository import SqliteManagementRepository
from yukibot.features.management.service import ManagementService
from yukibot.kernel import ControlCommand, ModuleController


@dataclass
class Owner:
    user_id: int


@dataclass
class FakeModule:
    name: str
    starts: int = 0
    stops: int = 0

    async def start(self) -> None:
        self.starts += 1

    async def stop(self) -> None:
        self.stops += 1


def command(
    raw_arguments: str,
    *,
    actor_id: int | None = 999,
    outgoing: bool = True,
) -> ControlCommand:
    return ControlCommand("/admin", raw_arguments, -1001, 10, actor_id, outgoing)


async def open_management(path: Path) -> tuple[SqliteDatabase, SqliteManagementRepository]:
    database = SqliteDatabase(f"sqlite:///{path}")
    await database.open()
    await MigrationRunner(database, MANAGEMENT_MIGRATIONS).upgrade()
    return database, SqliteManagementRepository(database, Owner(999))


def test_help_response_cannot_be_recognized_as_an_outgoing_command() -> None:
    assert not ADMIN_HELP.startswith("/")


async def test_current_account_can_delegate_admin_by_stable_user_id(tmp_path: Path) -> None:
    database, repository = await open_management(tmp_path / "management.db")
    module = FakeModule("forwarder")
    modules = ModuleController((module,), repository)
    service = ManagementService(repository, modules, Owner(999))
    try:
        await modules.start()
        owner_command = command("admin add 123")
        delegated_command = command("module list", actor_id=123, outgoing=False)

        assert await service.is_authorized(owner_command)
        assert not await service.is_authorized(delegated_command)
        await service.add_admin(owner_command, 123)
        await service.add_admin(owner_command, 123)

        assert await service.is_authorized(delegated_command)
        await service.add_admin(delegated_command, 456)
        assert await service.list_admins() == (999, (123, 456))
        await service.remove_admin(delegated_command, 456)
        assert await service.list_admins() == (999, (123,))
        with pytest.raises(ValueError, match="cannot be removed"):
            await service.remove_admin(delegated_command, 999)
        with pytest.raises(ValueError, match="positive"):
            await service.remove_admin(owner_command, -1)
    finally:
        await modules.stop()
        await database.close()


async def test_admin_commands_manage_persistent_module_state_idempotently(tmp_path: Path) -> None:
    database, repository = await open_management(tmp_path / "modules.db")
    module = FakeModule("forwarder")
    modules = ModuleController((module,), repository)
    commands = ManagementCommands(ManagementService(repository, modules, Owner(999)))
    try:
        await modules.start()

        disabled = await commands.handle(command("module disable forwarder"))
        disabled_again = await commands.handle(command("module disable forwarder"))
        assert disabled.text == "Module forwarder is disabled."
        assert disabled_again.text == disabled.text
        assert module.stops == 1
        assert await repository.get_enabled("forwarder") is False

        enabled = await commands.handle(command("module enable forwarder"))
        enabled_again = await commands.handle(command("module enable forwarder"))
        assert enabled.text == "Module forwarder is enabled and running."
        assert enabled_again.text == enabled.text
        assert module.starts == 2
        assert await repository.get_enabled("forwarder") is True
    finally:
        await modules.stop()
        await database.close()


async def test_admin_list_returns_owner_and_delegated_admins(tmp_path: Path) -> None:
    database, repository = await open_management(tmp_path / "admin-list.db")
    modules = ModuleController((), repository)
    service = ManagementService(repository, modules, Owner(999))
    commands = ManagementCommands(service)
    try:
        await modules.start()
        await service.add_admin(command("admin add 123"), 123)

        explicit = await commands.handle(command("admin list"))

        assert explicit.text == "owner: 999\nadmin: 123"
    finally:
        await modules.stop()
        await database.close()


async def test_command_receipts_are_unique_per_account_chat_and_message(tmp_path: Path) -> None:
    database, repository = await open_management(tmp_path / "receipts.db")
    other_account = SqliteManagementRepository(database, Owner(1000))
    try:
        assert not await repository.is_processed(-1001, 10)
        await repository.mark_processed(-1001, 10)
        await repository.mark_processed(-1001, 10)
        assert await repository.is_processed(-1001, 10)
        assert not await repository.is_processed(-1002, 10)
        assert not await other_account.is_processed(-1001, 10)
        await other_account.mark_processed(-1001, 10)
        assert await other_account.is_processed(-1001, 10)
    finally:
        await database.close()


async def test_account_scoping_migration_discards_ambiguous_legacy_receipts(
    tmp_path: Path,
) -> None:
    database = SqliteDatabase(f"sqlite:///{tmp_path / 'legacy-receipts.db'}")
    await database.open()
    try:
        await MigrationRunner(database, MANAGEMENT_MIGRATIONS[:1]).upgrade()
        await database.execute(
            "INSERT INTO management_command_receipts (chat_id, message_id) VALUES (?, ?)",
            (-1001, 10),
        )

        assert await MigrationRunner(database, MANAGEMENT_MIGRATIONS).upgrade() == (
            ("management", 2),
        )
        columns = await database.fetch_all("PRAGMA table_info(management_command_receipts)")
        repository = SqliteManagementRepository(database, Owner(999))

        assert [row["name"] for row in columns] == [
            "account_id",
            "chat_id",
            "message_id",
            "processed_at",
        ]
        assert not await repository.is_processed(-1001, 10)
    finally:
        await database.close()
