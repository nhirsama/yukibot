"""Narrow native-client protocol and the stable Telethon v1 adapter."""

from __future__ import annotations

import shutil
import sqlite3
from collections.abc import Awaitable, Callable, Sequence
from dataclasses import dataclass
from datetime import UTC, datetime
from importlib import import_module
from io import BytesIO
from pathlib import Path
from typing import Any, Protocol, cast


class NativePeerId(Protocol):
    def __int__(self) -> int: ...


class NativePeer(Protocol):
    @property
    def id(self) -> NativePeerId: ...

    @property
    def name(self) -> str: ...


class NativeMessage(Protocol):
    id: int
    chat_id: int
    sender_id: int | None
    grouped_id: int | None
    text: str | None
    text_html: str | None
    date: object | None
    chat: NativePeer
    sender: NativePeer | None
    photo: object | None
    audio: object | None
    video: object | None
    file: object | None
    replied_message_id: int | None
    outgoing: bool
    can_forward: bool
    _raw: object


class NativeDeletedEvent(Protocol):
    message_ids: Sequence[int]
    channel_id: int | None
    chat_id: int | None


class NativeDialog(Protocol):
    chat: NativePeer


class NativeAlbum(Protocol):
    async def add_photo(self, file: BytesIO, *, caption_html: str | None = None) -> None: ...

    async def add_video(self, file: BytesIO, *, caption_html: str | None = None) -> None: ...

    async def send(
        self, peer: object, *, reply_to: int | None = None
    ) -> Sequence[NativeMessage]: ...


type NativeHandler = Callable[[object], Awaitable[object]]


class NativeClient(Protocol):
    def add_event_handler(self, handler: NativeHandler, event_cls: type[object]) -> None: ...

    def remove_event_handler(self, handler: NativeHandler) -> None: ...

    async def connect(self) -> None: ...

    async def disconnect(self) -> None: ...

    async def run_until_disconnected(self) -> None: ...

    async def is_authorized(self) -> bool: ...

    async def interactive_login(self) -> object: ...

    async def get_me(self) -> NativePeer: ...

    def get_dialogs(self) -> Awaitable[Sequence[NativeDialog]]: ...

    def get_messages_with_ids(
        self, chat: object, message_ids: list[int]
    ) -> Awaitable[Sequence[NativeMessage]]: ...

    async def resolve_peer(self, reference: int | str) -> NativePeer: ...

    async def join_channel(self, peer: NativePeer) -> NativePeer: ...

    async def get_invite_link(self, peer: NativePeer) -> str | None: ...

    async def check_chat_invite(self, invite_hash: str) -> NativePeer | None: ...

    async def join_chat_invite(self, invite_hash: str) -> Sequence[NativePeer]: ...

    async def get_latest_message_id(self, chat: object) -> int: ...

    async def get_messages_after(
        self,
        chat: object,
        after_message_id: int,
        *,
        limit: int,
    ) -> Sequence[NativeMessage]: ...

    async def get_messages_recent(
        self,
        chat: object,
        *,
        since: datetime,
        limit: int | None = None,
        topic_id: int | None = None,
    ) -> Sequence[NativeMessage]: ...

    async def forward_messages(
        self,
        target: object,
        message_ids: list[int],
        source: object,
        *,
        topic_id: int | None = None,
    ) -> Sequence[NativeMessage]: ...

    async def create_forum_topic(
        self,
        chat: object,
        title: str,
        *,
        random_id: int,
    ) -> int: ...

    async def edit_forum_topic(self, chat: object, topic_id: int, *, title: str) -> None: ...

    async def get_forum_topic_title(self, chat: object, topic_id: int) -> str | None: ...

    async def send_message(
        self,
        chat: object,
        text: str | None = None,
        *,
        html: str | None = None,
        reply_to: int | None = None,
    ) -> NativeMessage: ...

    async def send_photo(
        self,
        chat: object,
        file: object,
        *,
        caption_html: str | None = None,
        reply_to: int | None = None,
    ) -> NativeMessage: ...

    async def send_file(
        self,
        chat: object,
        file: object,
        *,
        caption_html: str | None = None,
        reply_to: int | None = None,
    ) -> NativeMessage: ...

    async def edit_message(
        self,
        chat: object,
        message_id: int,
        *,
        text: str | None = None,
        html: str | None = None,
    ) -> NativeMessage: ...

    async def delete_messages(
        self, chat: object, message_ids: list[int], *, revoke: bool = True
    ) -> int: ...

    async def download(self, media: object, file: BytesIO) -> None: ...

    def prepare_album(self) -> NativeAlbum: ...


class PeerRegistry:
    """In-memory authorization-aware peers indexed by Bot API dialog ID."""

    def __init__(self) -> None:
        self._peers: dict[int, NativePeer] = {}

    def remember(self, peer: NativePeer | None) -> None:
        if peer is not None:
            self._peers[peer_dialog_id(peer)] = peer

    def get(self, chat_id: int) -> NativePeer | None:
        return self._peers.get(chat_id)

    def clear(self) -> None:
        self._peers.clear()


class AccountIdentity:
    """Authenticated Telegram account identity shared with the control plane."""

    def __init__(self) -> None:
        self._user_id: int | None = None

    @property
    def user_id(self) -> int:
        if self._user_id is None:
            raise RuntimeError("Telegram account identity is not available")
        return self._user_id

    def set(self, peer: NativePeer) -> None:
        user_id = peer_dialog_id(peer)
        if user_id <= 0:
            raise ValueError("authenticated Telegram account must have a positive user ID")
        self._user_id = user_id

    def clear(self) -> None:
        self._user_id = None


class TelethonClientLifecycle:
    """Connect and authorize before features start, then disconnect after they drain."""

    name = "telegram-client"

    def __init__(
        self,
        client: NativeClient,
        peers: PeerRegistry,
        identity: AccountIdentity | None = None,
    ) -> None:
        self._client = client
        self._peers = peers
        self._identity = identity or AccountIdentity()
        self._started = False

    async def start(self) -> None:
        if self._started:
            return
        try:
            await self._client.connect()
            if not await self._client.is_authorized():
                await self._client.interactive_login()
            me = await self._client.get_me()
            self._identity.set(me)
            self._peers.remember(me)
            dialogs = await self._client.get_dialogs()
            for dialog in dialogs:
                self._peers.remember(dialog.chat)
        except BaseException:
            await self._client.disconnect()
            self._peers.clear()
            self._identity.clear()
            raise
        self._started = True

    async def stop(self) -> None:
        try:
            await self._client.disconnect()
        finally:
            self._started = False
            self._peers.clear()
            self._identity.clear()


def create_telethon_client(
    session_path: Path,
    api_id: int,
    api_hash: str,
) -> NativeClient:
    """Create the stable Telethon client without leaking its API elsewhere."""

    session_path.expanduser().parent.mkdir(parents=True, exist_ok=True)
    _migrate_v2_session(session_path.expanduser())
    telethon = import_module("telethon")
    client_type = cast(Any, telethon).TelegramClient
    client = client_type(
        session_path,
        api_id,
        api_hash,
        catch_up=True,
        device_model="yukibot",
        flood_sleep_threshold=0,
        sequential_updates=True,
    )
    return TelethonClientAdapter(client)


def telethon_event_types() -> tuple[type[object], type[object], type[object]]:
    events = cast(Any, import_module("telethon.events"))
    return events.NewMessage, events.MessageEdited, events.MessageDeleted


def peer_dialog_id(peer: NativePeer) -> int:
    """Normalize Telethon peers to Bot API dialog IDs."""

    native = cast(Any, peer)
    if hasattr(native, "channel_id"):
        return -(1_000_000_000_000 + int(native.channel_id))
    if hasattr(native, "chat_id"):
        return -int(native.chat_id)
    if hasattr(native, "user_id"):
        return int(native.user_id)
    value = int(native.id)
    if value <= 0:
        return value
    peer_name = type(peer).__name__
    reference_name = type(getattr(peer, "ref", None)).__name__
    if peer_name == "Channel" or reference_name == "ChannelRef":
        return -(1_000_000_000_000 + value)
    if peer_name in ("Chat", "Group") or reference_name == "GroupRef":
        return -value
    return value


class TelethonClientAdapter:
    """Map the stable Telethon v1 API onto the project's native client port."""

    def __init__(self, client: object) -> None:
        self._client = cast(Any, client)
        self._handlers: dict[NativeHandler, list[NativeHandler]] = {}

    @property
    def native_client(self) -> object:
        return self._client

    def add_event_handler(self, handler: NativeHandler, event_cls: type[object]) -> None:
        async def adapted(raw_event: object) -> object:
            event = (
                _StableDeletedEvent(raw_event)
                if type(raw_event).__name__ == "Event" and hasattr(raw_event, "deleted_ids")
                else _StableMessage(raw_event)
            )
            return await handler(event)

        self._handlers.setdefault(handler, []).append(adapted)
        self._client.add_event_handler(adapted, event_cls)

    def remove_event_handler(self, handler: NativeHandler) -> None:
        for adapted in self._handlers.pop(handler, ()):
            self._client.remove_event_handler(adapted)

    async def connect(self) -> None:
        await self._client.connect()

    async def disconnect(self) -> None:
        await self._client.disconnect()

    async def run_until_disconnected(self) -> None:
        await self._client.run_until_disconnected()

    async def is_authorized(self) -> bool:
        return cast(bool, await self._client.is_user_authorized())

    async def interactive_login(self) -> object:
        return cast(object, await self._client.start())

    async def get_me(self) -> NativePeer:
        peer = await self._client.get_me()
        if peer is None:
            raise RuntimeError("Telethon did not return the authenticated account")
        return cast(NativePeer, peer)

    async def get_dialogs(self) -> Sequence[NativeDialog]:
        dialogs = await self._client.get_dialogs()
        return tuple(_StableDialog(dialog.entity) for dialog in dialogs)

    async def get_messages_with_ids(
        self, chat: object, message_ids: list[int]
    ) -> Sequence[NativeMessage]:
        messages = await self._client.get_messages(chat, ids=message_ids)
        return tuple(
            _StableMessage(message, chat=chat) for message in messages if message is not None
        )

    async def resolve_peer(self, reference: int | str) -> NativePeer:
        peer = await self._client.get_entity(reference)
        if peer is None:
            raise RuntimeError(f"Telegram did not resolve chat {reference!r}")
        return cast(NativePeer, peer)

    async def join_channel(self, peer: NativePeer) -> NativePeer:
        peer_type = type(peer).__name__
        if peer_type in {"Chat", "Group"}:
            return peer
        if peer_type != "Channel":
            raise ValueError("automatic joining is only supported for channels and supergroups")
        if not bool(getattr(peer, "left", False)):
            return peer
        functions = cast(Any, import_module("telethon.tl.functions.channels"))
        try:
            await self._client(
                functions.JoinChannelRequest(channel=await self._client.get_input_entity(peer))
            )
        except Exception as error:
            if type(error).__name__ != "UserAlreadyParticipantError":
                raise
        joined = await self._client.get_entity(peer)
        if joined is None or bool(getattr(joined, "left", False)):
            raise RuntimeError("Telegram did not confirm channel membership")
        return cast(NativePeer, joined)

    async def get_invite_link(self, peer: NativePeer) -> str | None:
        peer_type = type(peer).__name__
        if peer_type == "Channel":
            functions = cast(Any, import_module("telethon.tl.functions.channels"))
            details = await self._client(
                functions.GetFullChannelRequest(channel=await self._client.get_input_entity(peer))
            )
        elif peer_type == "Chat":
            functions = cast(Any, import_module("telethon.tl.functions.messages"))
            details = await self._client(functions.GetFullChatRequest(chat_id=int(peer.id)))
        else:
            return None
        exported = getattr(details.full_chat, "exported_invite", None)
        if exported is None or bool(getattr(exported, "revoked", False)):
            return None
        link = getattr(exported, "link", None)
        return link if isinstance(link, str) and link.strip() else None

    async def join_chat_invite(self, invite_hash: str) -> Sequence[NativePeer]:
        if not invite_hash:
            raise ValueError("Telegram invite hash must not be empty")
        functions = cast(Any, import_module("telethon.tl.functions.messages"))
        updates = await self._client(functions.ImportChatInviteRequest(hash=invite_hash))
        chats = getattr(updates, "chats", ())
        return tuple(cast(NativePeer, chat) for chat in chats)

    async def check_chat_invite(self, invite_hash: str) -> NativePeer | None:
        if not invite_hash:
            raise ValueError("Telegram invite hash must not be empty")
        functions = cast(Any, import_module("telethon.tl.functions.messages"))
        result = await self._client(functions.CheckChatInviteRequest(hash=invite_hash))
        types = cast(Any, import_module("telethon.tl.types"))
        if not isinstance(result, types.ChatInviteAlready):
            return None
        chat = getattr(result, "chat", None)
        return cast(NativePeer, chat) if chat is not None else None

    async def get_latest_message_id(self, chat: object) -> int:
        messages = await self._client.get_messages(chat, limit=1)
        if not messages or messages[0] is None:
            return 0
        return int(messages[0].id)

    async def get_messages_after(
        self,
        chat: object,
        after_message_id: int,
        *,
        limit: int,
    ) -> Sequence[NativeMessage]:
        if after_message_id < 0:
            raise ValueError("after_message_id must not be negative")
        if limit <= 0:
            raise ValueError("limit must be positive")
        messages = []
        async for message in self._client.iter_messages(
            chat,
            min_id=after_message_id,
            reverse=True,
            limit=limit,
        ):
            messages.append(_StableMessage(message, chat=chat))
        return tuple(messages)

    async def get_messages_recent(
        self,
        chat: object,
        *,
        since: datetime,
        limit: int | None = None,
        topic_id: int | None = None,
    ) -> Sequence[NativeMessage]:
        if since.tzinfo is None:
            raise ValueError("since must be timezone-aware")
        if limit is not None and limit <= 0:
            raise ValueError("limit must be positive")
        if topic_id is not None and topic_id <= 0:
            raise ValueError("topic_id must be positive")
        raw_messages: list[object] = []
        arguments: dict[str, object] = {"limit": limit}
        if topic_id is not None:
            arguments["reply_to"] = topic_id
        async for message in self._client.iter_messages(chat, **arguments):
            message_date = cast(datetime | None, getattr(message, "date", None))
            if message_date is not None:
                normalized_date = (
                    message_date
                    if message_date.tzinfo is not None
                    else message_date.replace(tzinfo=UTC)
                )
                if normalized_date < since:
                    break
            raw_messages.append(message)
        if topic_id is not None and not any(
            int(getattr(message, "id", 0)) == topic_id for message in raw_messages
        ):
            topic_messages = await self._client.get_messages(chat, ids=[topic_id])
            for message in topic_messages:
                if message is None:
                    continue
                message_date = cast(datetime | None, getattr(message, "date", None))
                topic_date = (
                    message_date
                    if message_date is None or message_date.tzinfo is not None
                    else message_date.replace(tzinfo=UTC)
                )
                if topic_date is None or topic_date >= since:
                    raw_messages.append(message)
        raw_messages.sort(key=lambda message: int(getattr(message, "id", 0)))
        selected = raw_messages if limit is None else raw_messages[-limit:]
        return tuple(_StableMessage(message, chat=chat) for message in selected)

    async def forward_messages(
        self,
        target: object,
        message_ids: list[int],
        source: object,
        *,
        topic_id: int | None = None,
    ) -> Sequence[NativeMessage]:
        if topic_id is None:
            messages = await self._client.forward_messages(target, message_ids, from_peer=source)
        else:
            functions = cast(Any, import_module("telethon.tl.functions.messages"))
            target_peer = await self._client.get_input_entity(target)
            request = functions.ForwardMessagesRequest(
                from_peer=await self._client.get_input_entity(source),
                id=message_ids,
                to_peer=target_peer,
                top_msg_id=topic_id,
            )
            result = await self._client(request)
            messages = self._client._get_response_message(request, result, target_peer)
        items = messages if isinstance(messages, Sequence) else (messages,)
        if any(message is None for message in items):
            raise RuntimeError("Telegram did not return every forwarded message")
        return tuple(_StableMessage(message, chat=target) for message in items)

    async def create_forum_topic(
        self,
        chat: object,
        title: str,
        *,
        random_id: int,
    ) -> int:
        functions = cast(Any, import_module("telethon.tl.functions.messages"))
        target_peer = await self._client.get_input_entity(chat)
        request = functions.CreateForumTopicRequest(
            peer=target_peer,
            title=title,
            random_id=random_id,
        )
        result = await self._client(request)
        message = self._client._get_response_message(request, result, target_peer)
        if message is None or int(message.id) <= 0:
            raise RuntimeError("Telegram did not return the created forum topic")
        return int(message.id)

    async def edit_forum_topic(self, chat: object, topic_id: int, *, title: str) -> None:
        functions = cast(Any, import_module("telethon.tl.functions.messages"))
        await self._client(
            functions.EditForumTopicRequest(
                peer=await self._client.get_input_entity(chat),
                topic_id=topic_id,
                title=title,
            )
        )

    async def get_forum_topic_title(self, chat: object, topic_id: int) -> str | None:
        functions = cast(Any, import_module("telethon.tl.functions.messages"))
        result = await self._client(
            functions.GetForumTopicsByIDRequest(
                peer=await self._client.get_input_entity(chat),
                topics=[topic_id],
            )
        )
        for topic in getattr(result, "topics", ()):
            if int(getattr(topic, "id", 0)) != topic_id:
                continue
            title = getattr(topic, "title", None)
            return title.strip() if isinstance(title, str) and title.strip() else None
        return None

    async def send_message(
        self,
        chat: object,
        text: str | None = None,
        *,
        html: str | None = None,
        reply_to: int | None = None,
    ) -> NativeMessage:
        message = await self._client.send_message(
            chat,
            html if html is not None else text,
            parse_mode="html" if html is not None else None,
            reply_to=reply_to,
        )
        return _StableMessage(message, chat=chat)

    async def send_photo(
        self,
        chat: object,
        file: object,
        *,
        caption_html: str | None = None,
        reply_to: int | None = None,
    ) -> NativeMessage:
        message = await self._client.send_file(
            chat,
            _uploadable_file(file),
            caption=caption_html,
            parse_mode="html",
            force_document=False,
            reply_to=reply_to,
        )
        return _StableMessage(message, chat=chat)

    async def send_file(
        self,
        chat: object,
        file: object,
        *,
        caption_html: str | None = None,
        reply_to: int | None = None,
    ) -> NativeMessage:
        message = await self._client.send_file(
            chat,
            _uploadable_file(file),
            caption=caption_html,
            parse_mode="html",
            reply_to=reply_to,
        )
        return _StableMessage(message, chat=chat)

    async def edit_message(
        self,
        chat: object,
        message_id: int,
        *,
        text: str | None = None,
        html: str | None = None,
    ) -> NativeMessage:
        message = await self._client.edit_message(
            chat,
            message_id,
            html if html is not None else text,
            parse_mode="html" if html is not None else None,
        )
        return _StableMessage(message, chat=chat)

    async def delete_messages(
        self, chat: object, message_ids: list[int], *, revoke: bool = True
    ) -> int:
        result = await self._client.delete_messages(chat, message_ids, revoke=revoke)
        return int(getattr(result, "pts_count", 0))

    async def download(self, media: object, file: BytesIO) -> None:
        source = media.message if isinstance(media, _StableFile) else media
        await self._client.download_media(source, file=file)

    def prepare_album(self) -> NativeAlbum:
        return _StableAlbum(self)


@dataclass(slots=True)
class _StableDialog:
    chat: NativePeer


class _StableFile:
    def __init__(self, message: object) -> None:
        self.message = message
        raw = cast(Any, message)
        self.payload = raw.photo or raw.document
        file_name = getattr(raw.file, "name", None)
        extension = getattr(raw.file, "ext", None)
        self.name = file_name or (f"file{extension}" if extension else None)
        self._attributes = tuple(getattr(raw.document, "attributes", ()) or ())


class _StableMessage:
    def __init__(self, message: object, *, chat: object | None = None) -> None:
        raw = cast(Any, message)
        nested = getattr(raw, "message", None)
        if hasattr(nested, "id"):
            raw = nested
        self._raw = raw
        self.id = int(raw.id)
        self.chat_id = int(raw.chat_id)
        self.sender_id = cast(int | None, raw.sender_id)
        self.grouped_id = cast(int | None, raw.grouped_id)
        self.text = cast(str | None, raw.raw_text)
        self.text_html = _message_html(raw)
        self.date = raw.date
        self.chat = cast(NativePeer, chat or raw.chat or raw.input_chat)
        self.sender = cast(NativePeer | None, raw.sender or raw.input_sender)
        self.photo = raw.photo
        self.audio = raw.audio
        self.video = raw.video
        self.file: object | None = _StableFile(raw) if raw.file is not None else None
        self.replied_message_id = cast(int | None, raw.reply_to_msg_id)
        self.outgoing = bool(raw.out)
        self.can_forward = not bool(raw.noforwards)


class _StableDeletedEvent:
    def __init__(self, event: object) -> None:
        raw = cast(Any, event)
        self.message_ids = tuple(int(message_id) for message_id in raw.deleted_ids)
        self.chat_id = cast(int | None, raw.chat_id)
        self.channel_id = None


class _StableAlbum:
    def __init__(self, client: TelethonClientAdapter) -> None:
        self._client = client
        self._files: list[BytesIO] = []
        self._captions: list[str] = []

    async def add_photo(self, file: BytesIO, *, caption_html: str | None = None) -> None:
        file.name = "photo.jpg"
        self._append(file, caption_html)

    async def add_video(self, file: BytesIO, *, caption_html: str | None = None) -> None:
        file.name = "video.mp4"
        self._append(file, caption_html)

    async def send(self, peer: object, *, reply_to: int | None = None) -> Sequence[NativeMessage]:
        messages = await self._client._client.send_file(
            peer,
            self._files,
            caption=self._captions,
            parse_mode="html",
            reply_to=reply_to,
        )
        items = messages if isinstance(messages, Sequence) else (messages,)
        return tuple(_StableMessage(message, chat=peer) for message in items)

    def _append(self, file: BytesIO, caption_html: str | None) -> None:
        self._files.append(file)
        self._captions.append(caption_html or "")


def _message_html(message: Any) -> str | None:
    text = cast(str | None, message.raw_text)
    if text is None:
        return None
    html = import_module("telethon.extensions.html")
    return cast(str, html.unparse(text, message.entities or ()))


def _uploadable_file(file: object) -> object:
    return file.payload if isinstance(file, _StableFile) else file


def _migrate_v2_session(path: Path) -> None:
    """Convert the pinned v2-alpha SQLite session once, retaining a backup."""

    if not path.exists():
        return
    with sqlite3.connect(path) as connection:
        tables = {
            str(row[0])
            for row in connection.execute("SELECT name FROM sqlite_master WHERE type = 'table'")
        }
        if "sessions" in tables or not {"datacenter", "user"}.issubset(tables):
            return
        user = connection.execute("SELECT dc FROM user LIMIT 1").fetchone()
        if user is None:
            return
        dc = connection.execute(
            "SELECT ipv4_addr, auth FROM datacenter WHERE id = ?", (int(user[0]),)
        ).fetchone()
        state = connection.execute("SELECT pts, qts, date, seq FROM state LIMIT 1").fetchone()
    if dc is None or not isinstance(dc[1], bytes) or len(dc[1]) != 256:
        raise RuntimeError("the Telethon v2 session has no reusable authorization key")

    address, separator, raw_port = str(dc[0]).rpartition(":")
    if not separator or not address:
        raise RuntimeError("the Telethon v2 session contains an invalid data-center address")
    temporary = path.with_name(f"{path.stem}.migrating.session")
    temporary.unlink(missing_ok=True)
    sessions = import_module("telethon.sessions")
    crypto = import_module("telethon.crypto")
    types = import_module("telethon.tl.types")
    session = cast(Any, sessions).SQLiteSession(str(temporary))
    try:
        session.set_dc(int(user[0]), address, int(raw_port))
        session.auth_key = cast(Any, crypto).AuthKey(dc[1])
        if state is not None:
            session.set_update_state(
                0,
                cast(Any, types).updates.State(
                    pts=int(state[0]),
                    qts=int(state[1]),
                    date=datetime.fromtimestamp(int(state[2]), tz=UTC),
                    seq=int(state[3]),
                    unread_count=0,
                ),
            )
        session.save()
    finally:
        session.close()

    backup = path.with_name(f"{path.name}.v2.bak")
    if not backup.exists():
        shutil.copy2(path, backup)
    temporary.replace(path)
