import asyncio
from datetime import UTC, datetime

import pytest

from tests.contract.adapters.telegram.conftest import (
    FakeDialog,
    FakeMessage,
    FakeNativeClient,
    FakePeer,
)
from yukibot.adapters.database import MigrationRunner, SqliteDatabase
from yukibot.bootstrap import build_runtime
from yukibot.config import Settings
from yukibot.contracts import (
    MessageRef,
    TelegramContentType,
    TelegramMessage,
    TelegramMessageReceived,
)
from yukibot.features.forwarder import DestinationEndpoint, Route, SourceEndpoint
from yukibot.features.forwarder.job_repository import SqliteForwardJobRepository
from yukibot.features.forwarder.jobs import pending_jobs_for_event
from yukibot.features.forwarder.migrations import FORWARDER_MIGRATIONS
from yukibot.features.forwarder.repository import SqliteRouteRepository
from yukibot.features.summarizer.repository import SqliteSummaryRepository
from yukibot.kernel import LifecycleState


class NewEvent:
    pass


class EditEvent:
    pass


class DeleteEvent:
    pass


@pytest.fixture
async def start_runtime():
    """Always stop background runtimes, including on assertion/startup failure."""
    running = []

    async def start(runtime, client):
        task = asyncio.create_task(runtime.application.run(install_signal_handlers=False))
        running.append((runtime, task))
        ready = asyncio.create_task(client.update_pump_started.wait())
        try:
            done, _ = await asyncio.wait(
                (task, ready), timeout=5, return_when=asyncio.FIRST_COMPLETED
            )
            if task in done:
                await task  # Surface startup errors rather than a misleading timeout.
                pytest.fail("runtime stopped before the test requested shutdown")
            assert ready in done, "Telegram update pump did not become ready"
        finally:
            ready.cancel()
            await asyncio.gather(ready, return_exceptions=True)
        return task

    yield start
    for runtime, task in reversed(running):
        runtime.application.request_shutdown("test cleanup")
        await asyncio.wait_for(task, timeout=5)


async def wait_for_delivery(client, task):
    # A wall-clock timeout is only a failure bound, not an assumed startup speed.
    async with asyncio.timeout(5):
        while not client.calls:
            if task.done():
                await task
                pytest.fail("runtime stopped before delivery")
            await asyncio.sleep(0.001)


async def wait_for_empty_queue(runtime, task):
    async with asyncio.timeout(5):
        while True:
            row = await runtime.database.fetch_one("SELECT COUNT(*) AS n FROM forwarder_jobs")
            assert row is not None
            if row["n"] == 0:
                return
            if task.done():
                await task
                pytest.fail("runtime stopped before draining durable jobs")
            await asyncio.sleep(0.001)


async def test_composed_runtime_starts_and_stops_all_resources(
    tmp_path,
    start_runtime,
    monkeypatch,  # type: ignore[no-untyped-def]
) -> None:
    monkeypatch.setattr(
        "yukibot.adapters.telegram.event_source.telethon_event_types",
        lambda: (NewEvent, EditEvent, DeleteEvent),
    )
    settings = Settings(
        telegram_api_id=1,
        telegram_api_hash="hash",
        telegram_session_path=tmp_path / "user.session",
        database_url=f"sqlite:///{tmp_path / 'app.db'}",
        forwarder_album_delay=0,
    )
    client = FakeNativeClient()
    runtime = build_runtime(settings, native_client=client)  # type: ignore[arg-type]

    task = await start_runtime(runtime, client)
    assert client.connected
    assert client.update_pump_calls == 1
    assert runtime.application.lifecycle.started_features == (
        "database",
        "telegram-client",
        "task-supervisor",
        "management",
        "modules",
        "telegram",
    )

    runtime.application.request_shutdown("test")
    await asyncio.wait_for(task, timeout=5)

    assert client.disconnected
    assert client.update_pump_stopped.is_set()
    assert client.handlers == {}
    assert runtime.application.lifecycle.state is LifecycleState.STOPPED
    assert not await runtime.database.ping()


async def test_composed_runtime_persists_and_deduplicates_forwarding(
    tmp_path,
    start_runtime,
    monkeypatch,  # type: ignore[no-untyped-def]
) -> None:
    monkeypatch.setattr(
        "yukibot.adapters.telegram.event_source.telethon_event_types",
        lambda: (NewEvent, EditEvent, DeleteEvent),
    )
    settings = Settings(
        telegram_api_id=1,
        telegram_api_hash="hash",
        database_url=f"sqlite:///{tmp_path / 'forwarding.db'}",
        forwarder_album_delay=0,
    )
    source = FakePeer(-1001)
    destination = FakePeer(-2001)
    client = FakeNativeClient()
    client.dialogs.extend((FakeDialog(source), FakeDialog(destination)))
    client.messages[(-1001, 10)] = FakeMessage(10, source)
    runtime = build_runtime(settings, native_client=client)  # type: ignore[arg-type]
    running = await start_runtime(runtime, client)

    routes = SqliteRouteRepository(runtime.database)
    await routes.add(Route(1, SourceEndpoint(-1001), DestinationEndpoint(-2001)))
    event = TelegramMessageReceived(
        TelegramMessage(
            MessageRef(-1001, 10),
            TelegramContentType.TEXT,
            datetime.now(UTC),
            text="hello",
        )
    )
    await runtime.bus.publish(event)
    await wait_for_delivery(client, running)
    await wait_for_empty_queue(runtime, running)
    assert len(client.calls) == 1

    await runtime.bus.publish(event)
    await wait_for_empty_queue(runtime, running)
    assert len(client.calls) == 1

    runtime.application.request_shutdown("test")
    await asyncio.wait_for(running, timeout=5)


async def test_recovered_jobs_run_only_after_telegram_is_ready(
    tmp_path,
    start_runtime,
    monkeypatch,  # type: ignore[no-untyped-def]
) -> None:
    monkeypatch.setattr(
        "yukibot.adapters.telegram.event_source.telethon_event_types",
        lambda: (NewEvent, EditEvent, DeleteEvent),
    )
    database_url = f"sqlite:///{tmp_path / 'recovered.db'}"
    event = TelegramMessageReceived(
        TelegramMessage(
            MessageRef(-1001, 10),
            TelegramContentType.TEXT,
            datetime.now(UTC),
            text="hello",
        )
    )
    async with SqliteDatabase(database_url) as database:
        await MigrationRunner(database, FORWARDER_MIGRATIONS).upgrade()
        await SqliteRouteRepository(database).add(
            Route(1, SourceEndpoint(-1001), DestinationEndpoint(-2001))
        )
        jobs = SqliteForwardJobRepository(database)
        await jobs.enqueue(pending_jobs_for_event(event, now=100.0, album_delay=0))
        assert len(await jobs.claim_due(100.0)) == 1

    source = FakePeer(-1001)
    destination = FakePeer(-2001)
    client = FakeNativeClient()
    client.dialogs.extend((FakeDialog(source), FakeDialog(destination)))
    client.messages[(-1001, 10)] = FakeMessage(10, source)
    runtime = build_runtime(
        Settings(
            telegram_api_id=1,
            telegram_api_hash="hash",
            database_url=database_url,
            forwarder_album_delay=0,
        ),
        native_client=client,  # type: ignore[arg-type]
    )
    running = await start_runtime(runtime, client)
    await wait_for_delivery(client, running)

    assert len(client.calls) == 1
    runtime.application.request_shutdown("test")
    await asyncio.wait_for(running, timeout=5)


async def test_runtime_control_plane_manages_modules_admins_and_routes(
    tmp_path,
    start_runtime,
    monkeypatch,  # type: ignore[no-untyped-def]
) -> None:
    monkeypatch.setattr(
        "yukibot.adapters.telegram.event_source.telethon_event_types",
        lambda: (NewEvent, EditEvent, DeleteEvent),
    )
    settings = Settings(
        telegram_api_id=1,
        telegram_api_hash="hash",
        database_url=f"sqlite:///{tmp_path / 'control.db'}",
        forwarder_album_delay=0,
    )
    client = FakeNativeClient()
    client.dialogs = [FakeDialog(FakePeer(-1001)), FakeDialog(FakePeer(-2001))]
    runtime = build_runtime(settings, native_client=client)  # type: ignore[arg-type]
    ordinary_messages: list[TelegramMessageReceived] = []

    async def record_ordinary(event: TelegramMessageReceived) -> None:
        ordinary_messages.append(event)

    runtime.bus.subscribe(TelegramMessageReceived, record_ordinary)
    running = await start_runtime(runtime, client)

    chat = FakePeer(-4321, "control chat")
    owner = client.me
    await client.handlers[NewEvent](  # type: ignore[operator]
        FakeMessage(1, chat, text="/help", sender=owner, outgoing=True)
    )
    help_response = client.calls[-1]
    assert help_response[0:2] == ("message", -4321)
    assert "/admin - 管理管理员和功能模块" in str(help_response[2])
    assert "/route - 管理消息转发路由" in str(help_response[2])
    assert "/summary - 生成并发送消息总结" in str(help_response[2])
    assert help_response[-1] == 1

    await client.handlers[NewEvent](  # type: ignore[operator]
        FakeMessage(
            101,
            chat,
            text="/admin module disable summarizer",
            sender=owner,
            outgoing=True,
        )
    )
    assert not runtime.commands.recognizes("/summary list")
    modules = {item.name: item for item in await runtime.modules.list_modules()}
    assert modules["summarizer"].running is False

    await client.handlers[NewEvent](  # type: ignore[operator]
        FakeMessage(
            102,
            chat,
            text="/admin module enable summarizer",
            sender=owner,
            outgoing=True,
        )
    )
    assert runtime.commands.recognizes("/summary list")
    assert runtime.commands.recognizes("/route list")

    await client.handlers[NewEvent](  # type: ignore[operator]
        FakeMessage(
            103,
            chat,
            text="/summary add -1001 -2001 6h",
            sender=owner,
            outgoing=True,
        )
    )
    summary_rules = await SqliteSummaryRepository(runtime.database).list_all()
    assert len(summary_rules) == 1
    assert summary_rules[0].window_seconds == 21600

    client.messages[(-1001, 55)] = FakeMessage(55, FakePeer(-1001), text="summary input")
    await client.handlers[NewEvent](  # type: ignore[operator]
        FakeMessage(
            104,
            chat,
            text="/summary run 1",
            sender=owner,
            outgoing=True,
        )
    )
    assert "/summary model set" in str(client.calls[-1][2])

    await client.handlers[NewEvent](  # type: ignore[operator]
        FakeMessage(
            105,
            chat,
            text=(
                "/summary model set openai gpt-test "
                "-api-key top-secret -base-url https://models.example/v1"
            ),
            sender=owner,
            outgoing=True,
        )
    )
    model_config = await SqliteSummaryRepository(runtime.database).get_model_config()
    assert model_config is not None
    assert model_config.api_key == "top-secret"
    assert "top-secret" not in str(client.calls[-1][2])

    await client.handlers[NewEvent](  # type: ignore[operator]
        FakeMessage(
            2,
            chat,
            text="/admin module disable forwarder",
            sender=owner,
            outgoing=True,
        )
    )
    assert not runtime.commands.recognizes("/route list")
    assert (await runtime.modules.list_modules())[0].running is False

    await client.handlers[NewEvent](  # type: ignore[operator]
        FakeMessage(
            3,
            chat,
            text="/admin module enable forwarder",
            sender=owner,
            outgoing=True,
        )
    )
    assert runtime.commands.recognizes("/route list")
    assert (await runtime.modules.list_modules())[0].running is True

    await client.handlers[NewEvent](  # type: ignore[operator]
        FakeMessage(
            4,
            chat,
            text="/route add -1001 -2001",
            sender=owner,
            outgoing=True,
        )
    )
    routes = await SqliteRouteRepository(runtime.database).list_all()
    assert [route.id for route in routes] == [1]

    await client.handlers[NewEvent](  # type: ignore[operator]
        FakeMessage(5, chat, text="/admin admin add 123", sender=owner, outgoing=True)
    )
    await client.handlers[NewEvent](  # type: ignore[operator]
        FakeMessage(
            6,
            chat,
            text="/admin admin list",
            sender=owner,
            outgoing=True,
        )
    )
    assert client.calls[-1][2] == "owner: 999\nadmin: 123"

    await client.handlers[NewEvent](  # type: ignore[operator]
        FakeMessage(
            7,
            chat,
            text="/route list",
            sender=FakePeer(123, "delegated admin"),
            outgoing=False,
        )
    )
    assert client.calls[-1][2] == "1: peer (-1001) -> peer (-2001) (forward, enabled)"

    await client.handlers[NewEvent](  # type: ignore[operator]
        FakeMessage(
            8,
            chat,
            text="/route check",
            sender=owner,
            outgoing=True,
        )
    )
    assert "频道检查完成" in str(client.calls[-1][2])
    assert "已加入 2" in str(client.calls[-1][2])

    await client.handlers[NewEvent](  # type: ignore[operator]
        FakeMessage(
            9,
            chat,
            text="/route check",
            sender=FakePeer(123, "delegated admin"),
            outgoing=False,
        )
    )
    assert "频道检查完成" in str(client.calls[-1][2])
    assert "已加入 2" in str(client.calls[-1][2])

    await client.handlers[NewEvent](  # type: ignore[operator]
        FakeMessage(
            10,
            chat,
            text="/route rebuild",
            sender=owner,
            outgoing=True,
        )
    )
    assert client.calls[-1][2] == (
        "没有可自动重建的未加入频道。\n使用 /route rebuild status 查看进度。"
    )

    calls_before_unknown = len(client.calls)
    await client.handlers[NewEvent](  # type: ignore[operator]
        FakeMessage(11, chat, text="/unknown value", sender=owner, outgoing=True)
    )
    assert len(client.calls) == calls_before_unknown
    assert ordinary_messages[-1].message.text == "/unknown value"

    runtime.application.request_shutdown("test")
    await asyncio.wait_for(running, timeout=5)
