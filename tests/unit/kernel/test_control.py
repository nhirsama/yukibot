from __future__ import annotations

import asyncio
import logging

import pytest

from yukibot.kernel import (
    CommandDispatcher,
    CommandRegistry,
    CommandResult,
    ControlCommand,
    split_command,
)


class FakeAuthorizer:
    def __init__(self, authorized: bool = True) -> None:
        self.authorized = authorized
        self.commands: list[ControlCommand] = []

    async def is_authorized(self, command: ControlCommand) -> bool:
        self.commands.append(command)
        return self.authorized


class MemoryReceipts:
    def __init__(self) -> None:
        self.processed: set[tuple[int, int]] = set()

    async def is_processed(self, chat_id: int, message_id: int) -> bool:
        return (chat_id, message_id) in self.processed

    async def mark_processed(self, chat_id: int, message_id: int) -> None:
        self.processed.add((chat_id, message_id))


def test_split_command_preserves_arguments_after_the_first_delimiter() -> None:
    assert split_command("/route  add  7\nnext") == ("/route", " add  7\nnext")
    assert split_command("/Exact") == ("/Exact", "")
    assert split_command("prefix /route") is None
    assert split_command(None) is None


def test_registry_rejects_duplicate_and_reserved_roots() -> None:
    registry = CommandRegistry()

    async def handle(command: ControlCommand) -> CommandResult:
        return CommandResult(command.raw_arguments)

    subscription = registry.register(
        "/route",
        summary="routes",
        help_text="route help",
        handler=handle,
    )
    with pytest.raises(ValueError, match="already registered"):
        registry.register("/route", summary="duplicate", help_text="help", handler=handle)
    with pytest.raises(ValueError, match="reserved"):
        registry.register("/help", summary="help", help_text="help", handler=handle)

    subscription.unregister()
    assert not registry.recognizes("/route list")


async def test_dispatches_exact_registered_root_and_deduplicates_receipts() -> None:
    registry = CommandRegistry()
    calls: list[ControlCommand] = []

    async def handle(command: ControlCommand) -> CommandResult:
        calls.append(command)
        return CommandResult(f"arguments={command.raw_arguments}")

    registry.register("/route", summary="routes", help_text="route help", handler=handle)
    receipts = MemoryReceipts()
    dispatcher = CommandDispatcher(registry, FakeAuthorizer(), receipts)

    first = await dispatcher.dispatch(
        "/route  add 1",
        chat_id=-1001,
        message_id=10,
        actor_id=999,
        outgoing=True,
    )
    replay = await dispatcher.dispatch(
        "/route ignored",
        chat_id=-1001,
        message_id=10,
        actor_id=999,
        outgoing=True,
    )

    assert first.consumed
    assert first.response == "arguments= add 1"
    assert replay.consumed
    assert replay.response is None
    assert replay.duplicate
    assert [command.raw_arguments for command in calls] == [" add 1"]
    assert receipts.processed == {(-1001, 10)}


async def test_unknown_slash_message_is_not_consumed() -> None:
    dispatcher = CommandDispatcher(CommandRegistry(), FakeAuthorizer(), MemoryReceipts())

    outcome = await dispatcher.dispatch(
        "/unknown value",
        chat_id=1,
        message_id=2,
        actor_id=3,
        outgoing=True,
    )

    assert not outcome.consumed
    assert outcome.response is None


async def test_help_is_the_only_framework_command() -> None:
    registry = CommandRegistry()

    async def handle(command: ControlCommand) -> CommandResult:
        return CommandResult(command.raw_arguments)

    registry.register(
        "/route",
        summary="管理消息转发路由",
        help_text="detailed route help",
        handler=handle,
    )
    dispatcher = CommandDispatcher(registry, FakeAuthorizer(), MemoryReceipts())

    listing = await dispatcher.dispatch(
        "/help", chat_id=1, message_id=1, actor_id=999, outgoing=True
    )
    details = await dispatcher.dispatch(
        "/help /route", chat_id=1, message_id=2, actor_id=999, outgoing=True
    )

    assert listing.response is not None
    assert "/help - 列出命令或查看详细帮助" in listing.response
    assert "/route - 管理消息转发路由" in listing.response
    assert details.response == "detailed route help"


async def test_authorization_denial_is_consumed_without_calling_handler() -> None:
    registry = CommandRegistry()
    called = False

    async def handle(command: ControlCommand) -> CommandResult:
        nonlocal called
        called = True
        return CommandResult()

    registry.register("/admin", summary="admin", help_text="help", handler=handle)
    dispatcher = CommandDispatcher(registry, FakeAuthorizer(False), MemoryReceipts())

    outcome = await dispatcher.dispatch(
        "/admin module list",
        chat_id=1,
        message_id=2,
        actor_id=123,
        outgoing=False,
    )

    assert outcome.consumed
    assert outcome.response == "Permission denied."
    assert not called


async def test_handler_exception_becomes_control_response(caplog) -> None:  # type: ignore[no-untyped-def]
    registry = CommandRegistry()

    async def fail(command: ControlCommand) -> CommandResult:
        raise RuntimeError("sensitive detail")

    registry.register("/fail", summary="fail", help_text="help", handler=fail)
    dispatcher = CommandDispatcher(registry, FakeAuthorizer(), MemoryReceipts())

    with caplog.at_level(logging.ERROR):
        outcome = await dispatcher.dispatch(
            "/fail", chat_id=1, message_id=2, actor_id=999, outgoing=True
        )

    assert outcome.response == "Command failed. Check the application logs."
    assert "control command failed" in caplog.text


async def test_registration_matching_is_serialized_with_module_changes() -> None:
    registry = CommandRegistry()
    entered = asyncio.Event()
    release = asyncio.Event()
    route_called = False

    async def route(command: ControlCommand) -> CommandResult:
        nonlocal route_called
        route_called = True
        return CommandResult()

    route_subscription = registry.register(
        "/route", summary="routes", help_text="route help", handler=route
    )

    async def disable(command: ControlCommand) -> CommandResult:
        entered.set()
        await release.wait()
        route_subscription.unregister()
        return CommandResult()

    registry.register("/admin", summary="admin", help_text="admin help", handler=disable)
    dispatcher = CommandDispatcher(registry, FakeAuthorizer(), MemoryReceipts())
    disabling = asyncio.create_task(
        dispatcher.dispatch(
            "/admin disable",
            chat_id=1,
            message_id=1,
            actor_id=999,
            outgoing=True,
        )
    )
    await entered.wait()
    concurrent_route = asyncio.create_task(
        dispatcher.dispatch(
            "/route list",
            chat_id=1,
            message_id=2,
            actor_id=999,
            outgoing=True,
        )
    )
    await asyncio.sleep(0)

    release.set()
    _, route_outcome = await asyncio.gather(disabling, concurrent_route)
    assert not route_outcome.consumed
    assert not route_called
