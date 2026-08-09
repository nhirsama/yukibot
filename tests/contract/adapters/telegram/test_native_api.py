import sqlite3
from datetime import UTC, datetime, timedelta
from pathlib import Path
from types import SimpleNamespace

import telethon
from telethon import TelegramClient, events
from telethon.tl import types
from telethon.tl.custom.message import Message
from telethon.tl.functions.channels import GetFullChannelRequest, JoinChannelRequest
from telethon.tl.functions.messages import (
    CheckChatInviteRequest,
    CreateForumTopicRequest,
    EditForumTopicRequest,
    ForwardMessagesRequest,
    GetForumTopicsByIDRequest,
    ImportChatInviteRequest,
)

from yukibot.adapters.telegram import (
    TelethonClientAdapter,
    create_telethon_client,
    normalize_message,
)
from yukibot.adapters.telegram.client import telethon_event_types


def test_stable_telethon_api_matches_adapter(tmp_path: Path) -> None:
    client = create_telethon_client(tmp_path / "contract.session", 1, "hash")
    raw_client = client.native_client
    try:
        assert telethon.__version__ == "1.44.0"
        assert isinstance(client, TelethonClientAdapter)
        assert isinstance(raw_client, TelegramClient)
        assert raw_client._init_request.device_model == "Yukibot"  # type: ignore[attr-defined]
        assert telethon_event_types() == (
            events.NewMessage,
            events.MessageEdited,
            events.MessageDeleted,
        )
    finally:
        raw_client.session.close()  # type: ignore[attr-defined]


async def test_stable_adapter_uses_raw_topic_requests() -> None:
    source = types.InputPeerChannel(1001, 11)
    target = types.InputPeerChannel(2001, 22)
    forwarded = SimpleNamespace(
        id=101,
        chat_id=-1000000002001,
        sender_id=42,
        grouped_id=None,
        raw_text="hello",
        entities=(),
        date=datetime.now(UTC),
        chat=None,
        input_chat=target,
        sender=None,
        input_sender=None,
        photo=None,
        audio=None,
        video=None,
        file=None,
        reply_to_msg_id=None,
        out=True,
        noforwards=False,
    )

    class RawClient:
        def __init__(self) -> None:
            self.requests = []

        async def get_input_entity(self, peer):  # type: ignore[no-untyped-def]
            return peer

        async def __call__(self, request):  # type: ignore[no-untyped-def]
            self.requests.append(request)
            if isinstance(request, GetForumTopicsByIDRequest):
                return SimpleNamespace(topics=(SimpleNamespace(id=99, title="Announcements"),))
            return object()

        def _get_response_message(self, request, result, peer):  # type: ignore[no-untyped-def]
            if isinstance(request, ForwardMessagesRequest):
                return [forwarded]
            if isinstance(request, CreateForumTopicRequest):
                return SimpleNamespace(id=303)
            raise AssertionError("unexpected response conversion")

    raw = RawClient()
    client = TelethonClientAdapter(raw)

    messages = await client.forward_messages(target, [7], source, topic_id=99)
    source_topic_title = await client.get_forum_topic_title(source, 99)
    topic_id = await client.create_forum_topic(target, "Source", random_id=123)
    await client.edit_forum_topic(target, topic_id, title="Renamed")

    assert [message.id for message in messages] == [101]
    assert source_topic_title == "Announcements"
    assert topic_id == 303
    assert isinstance(raw.requests[0], ForwardMessagesRequest)
    assert raw.requests[0].top_msg_id == 99
    assert isinstance(raw.requests[1], GetForumTopicsByIDRequest)
    assert raw.requests[1].topics == [99]
    assert isinstance(raw.requests[2], CreateForumTopicRequest)
    assert raw.requests[2].random_id == 123
    assert isinstance(raw.requests[3], EditForumTopicRequest)
    assert raw.requests[3].topic_id == 303


async def test_stable_telethon_message_reaches_the_application_contract(tmp_path: Path) -> None:
    client = create_telethon_client(tmp_path / "message.session", 1, "hash")
    assert isinstance(client, TelethonClientAdapter)
    raw_client = client.native_client
    message = Message(
        id=7,
        peer_id=types.PeerChannel(4348538590),
        date=datetime(2026, 8, 5, tzinfo=UTC),
        message="/admin admin list",
        out=True,
        from_id=types.PeerUser(8919848716),
    )
    message._finish_init(
        raw_client,
        {},
        types.InputPeerChannel(4348538590, 123),
    )
    received = []

    async def handle(event):  # type: ignore[no-untyped-def]
        received.append(event)

    client.add_event_handler(handle, events.NewMessage)
    await client._handlers[handle][0](message)
    client.remove_event_handler(handle)
    raw_client.session.close()  # type: ignore[attr-defined]

    normalized = normalize_message(received[0], datetime.now(UTC))
    assert normalized.ref.chat_id == -1004348538590
    assert normalized.sender_id == 8919848716
    assert normalized.outgoing
    assert normalized.text == "/admin admin list"
    assert handle not in client._handlers
    assert all(
        hasattr(TelegramClient, method)
        for method in (
            "add_event_handler",
            "remove_event_handler",
            "run_until_disconnected",
            "get_me",
            "get_messages",
            "forward_messages",
            "send_file",
            "download_media",
        )
    )


async def test_stable_adapter_resolves_joins_and_reads_public_channel_history() -> None:
    channel_type = type("Channel", (), {})
    source = channel_type()
    source.id = 1001
    source.left = True
    source.username = "public_source"
    joined = channel_type()
    joined.id = 1001
    joined.left = False
    joined.username = "public_source"

    class RawSourceClient:
        def __init__(self) -> None:
            self.requests = []
            self.iteration = None

        async def get_entity(self, reference):  # type: ignore[no-untyped-def]
            return joined if reference is source else source

        async def get_input_entity(self, peer):  # type: ignore[no-untyped-def]
            return "input-source"

        async def __call__(self, request):  # type: ignore[no-untyped-def]
            self.requests.append(request)
            return object()

        async def get_messages(self, chat, *, limit):  # type: ignore[no-untyped-def]
            return [SimpleNamespace(id=88)]

        async def iter_messages(self, chat, **kwargs):  # type: ignore[no-untyped-def]
            self.iteration = (chat, kwargs)
            if False:
                yield None

    raw = RawSourceClient()
    client = TelethonClientAdapter(raw)

    resolved = await client.resolve_peer("@public_source")
    membership = await client.join_channel(resolved)
    latest = await client.get_latest_message_id(membership)
    history = await client.get_messages_after(membership, 80, limit=25)

    assert resolved is source
    assert membership is joined
    assert latest == 88
    assert history == ()
    assert isinstance(raw.requests[0], JoinChannelRequest)
    assert raw.requests[0].channel == "input-source"
    assert raw.iteration == (joined, {"min_id": 80, "reverse": True, "limit": 25})


async def test_stable_adapter_reads_recent_topic_history_with_topic_root() -> None:
    now = datetime.now(UTC)
    chat = types.InputPeerChannel(1001, 11)

    def raw_message(message_id: int, date: datetime, text: str) -> SimpleNamespace:
        return SimpleNamespace(
            id=message_id,
            chat_id=-1000000001001,
            sender_id=42,
            grouped_id=None,
            raw_text=text,
            entities=(),
            date=date,
            chat=None,
            input_chat=chat,
            sender=None,
            input_sender=None,
            photo=None,
            document=None,
            audio=None,
            video=None,
            file=None,
            reply_to_msg_id=42,
            out=False,
            noforwards=False,
        )

    root = raw_message(42, now - timedelta(minutes=30), "Topic root")
    reply = raw_message(43, now - timedelta(minutes=5), "Recent reply")
    old = raw_message(41, now - timedelta(hours=2), "Old reply")

    class RawHistoryClient:
        def __init__(self) -> None:
            self.iteration = None
            self.requested_ids = None

        async def iter_messages(self, peer, **kwargs):  # type: ignore[no-untyped-def]
            self.iteration = (peer, kwargs)
            for item in (reply, old):
                yield item

        async def get_messages(self, peer, *, ids):  # type: ignore[no-untyped-def]
            self.requested_ids = (peer, ids)
            return [root]

    raw = RawHistoryClient()
    client = TelethonClientAdapter(raw)

    messages = await client.get_messages_recent(
        chat,
        since=now - timedelta(hours=1),
        limit=None,
        topic_id=42,
    )

    assert [message.id for message in messages] == [42, 43]
    assert raw.iteration == (chat, {"limit": None, "reply_to": 42})
    assert raw.requested_ids == (chat, [42])


async def test_stable_adapter_reads_existing_invite_and_imports_it() -> None:
    channel_type = type("Channel", (), {})
    channel = channel_type()
    channel.id = 1001
    joined = channel_type()
    joined.id = 2001

    class RawInviteClient:
        def __init__(self) -> None:
            self.requests = []

        async def get_input_entity(self, peer):  # type: ignore[no-untyped-def]
            return "input-channel"

        async def __call__(self, request):  # type: ignore[no-untyped-def]
            self.requests.append(request)
            if isinstance(request, GetFullChannelRequest):
                return SimpleNamespace(
                    full_chat=SimpleNamespace(
                        exported_invite=SimpleNamespace(
                            link="https://t.me/+existing_hash",
                            revoked=False,
                        )
                    )
                )
            if isinstance(request, ImportChatInviteRequest):
                return SimpleNamespace(chats=(joined,))
            if isinstance(request, CheckChatInviteRequest):
                return (
                    types.ChatInvitePeek(joined, expires=None)
                    if request.hash == "peek_hash"
                    else types.ChatInviteAlready(joined)
                )
            raise AssertionError("unexpected request")

    raw = RawInviteClient()
    client = TelethonClientAdapter(raw)

    assert await client.get_invite_link(channel) == "https://t.me/+existing_hash"
    assert await client.check_chat_invite("existing_hash") is joined
    assert await client.check_chat_invite("peek_hash") is None
    assert await client.join_chat_invite("existing_hash") == (joined,)
    assert isinstance(raw.requests[0], GetFullChannelRequest)
    assert isinstance(raw.requests[1], CheckChatInviteRequest)
    assert isinstance(raw.requests[2], CheckChatInviteRequest)
    assert isinstance(raw.requests[3], ImportChatInviteRequest)


def test_v2_alpha_session_is_migrated_without_losing_authorization(tmp_path: Path) -> None:
    session_path = tmp_path / "legacy.session"
    authorization_key = bytes(range(256))
    with sqlite3.connect(session_path) as connection:
        connection.executescript(
            """
            CREATE TABLE version (version INTEGER PRIMARY KEY);
            INSERT INTO version VALUES (10);
            CREATE TABLE datacenter (
                id INTEGER PRIMARY KEY,
                ipv4_addr TEXT,
                ipv6_addr TEXT,
                auth BLOB
            );
            CREATE TABLE user (id INTEGER PRIMARY KEY, dc INTEGER, bot INTEGER, username TEXT);
            CREATE TABLE state (pts INTEGER, qts INTEGER, date INTEGER, seq INTEGER);
            """
        )
        connection.execute(
            "INSERT INTO datacenter VALUES (?, ?, ?, ?)",
            (5, "91.108.56.197:443", None, authorization_key),
        )
        connection.execute("INSERT INTO user VALUES (8919848716, 5, 0, 'owner')")
        connection.execute("INSERT INTO state VALUES (11, 12, 1700000000, 13)")

    client = create_telethon_client(session_path, 1, "hash")
    assert isinstance(client, TelethonClientAdapter)
    client.native_client.session.close()  # type: ignore[attr-defined]

    with sqlite3.connect(session_path) as connection:
        tables = {
            str(row[0])
            for row in connection.execute("SELECT name FROM sqlite_master WHERE type = 'table'")
        }
        session = connection.execute(
            "SELECT dc_id, server_address, port, auth_key FROM sessions"
        ).fetchone()
        state = connection.execute(
            "SELECT pts, qts, date, seq FROM update_state WHERE id = 0"
        ).fetchone()

    assert "sessions" in tables
    assert session == (5, "91.108.56.197", 443, authorization_key)
    assert state == (11, 12, 1700000000, 13)
    assert (tmp_path / "legacy.session.v2.bak").exists()
