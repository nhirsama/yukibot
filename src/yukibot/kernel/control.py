"""Out-of-band command registration and dispatch.

Control-plane invariants:

* modules own one exact slash-prefixed first token and parse their own arguments;
* registered roots are always consumed, while unknown slash messages stay data-plane;
* duplicate roots fail at registration and ``/help`` remains framework-owned;
* authorization, serialized execution, exception conversion and receipt deduplication
  are framework responsibilities;
* adapters consume recognized edits without executing them again;
* command handlers must be stateless or idempotent.
"""

from __future__ import annotations

import asyncio
import logging
from collections.abc import Awaitable, Callable
from dataclasses import dataclass
from typing import Protocol


@dataclass(frozen=True, slots=True)
class ControlCommand:
    name: str
    raw_arguments: str
    chat_id: int
    message_id: int
    actor_id: int | None
    outgoing: bool


@dataclass(frozen=True, slots=True)
class CommandResult:
    text: str | None = None


@dataclass(frozen=True, slots=True)
class CommandDispatch:
    consumed: bool
    response: str | None = None
    duplicate: bool = False


type CommandHandler = Callable[[ControlCommand], Awaitable[CommandResult]]


@dataclass(frozen=True, slots=True)
class CommandRegistration:
    name: str
    summary: str
    help_text: str
    handler: CommandHandler


class CommandAuthorizer(Protocol):
    async def is_authorized(self, command: ControlCommand) -> bool: ...


class CommandReceiptStore(Protocol):
    async def is_processed(self, chat_id: int, message_id: int) -> bool: ...

    async def mark_processed(self, chat_id: int, message_id: int) -> None: ...


class CommandSubscription:
    def __init__(self, unregister: Callable[[], None]) -> None:
        self._unregister = unregister
        self._active = True

    @property
    def active(self) -> bool:
        return self._active

    def unregister(self) -> None:
        if self._active:
            self._active = False
            self._unregister()


class CommandRegistry:
    """Store exact command roots registered by active modules."""

    def __init__(self) -> None:
        self._commands: dict[str, CommandRegistration] = {}

    def register(
        self,
        name: str,
        *,
        summary: str,
        help_text: str,
        handler: CommandHandler,
    ) -> CommandSubscription:
        if not name.startswith("/") or any(character.isspace() for character in name):
            raise ValueError("a command name must be one slash-prefixed token")
        if name == "/help":
            raise ValueError("/help is reserved by the command framework")
        if name in self._commands:
            raise ValueError(f"command {name!r} is already registered")
        self._commands[name] = CommandRegistration(name, summary, help_text, handler)

        def unregister() -> None:
            self._commands.pop(name, None)

        return CommandSubscription(unregister)

    def get(self, name: str) -> CommandRegistration | None:
        return self._commands.get(name)

    def list_commands(self) -> tuple[CommandRegistration, ...]:
        return tuple(self._commands[name] for name in sorted(self._commands))

    def recognizes(self, text: str | None) -> bool:
        parsed = split_command(text)
        return parsed is not None and (parsed[0] == "/help" or parsed[0] in self._commands)


class CommandDispatcher:
    """Authorize, deduplicate and execute registered commands serially."""

    def __init__(
        self,
        registry: CommandRegistry,
        authorizer: CommandAuthorizer,
        receipts: CommandReceiptStore,
        *,
        logger: logging.Logger | None = None,
    ) -> None:
        self._registry = registry
        self._authorizer = authorizer
        self._receipts = receipts
        self._lock = asyncio.Lock()
        self._logger = logger or logging.getLogger(__name__)

    def recognizes(self, text: str | None) -> bool:
        return self._registry.recognizes(text)

    async def dispatch(
        self,
        text: str | None,
        *,
        chat_id: int,
        message_id: int,
        actor_id: int | None,
        outgoing: bool,
    ) -> CommandDispatch:
        parsed = split_command(text)
        if parsed is None:
            return CommandDispatch(False)
        name, raw_arguments = parsed
        command = ControlCommand(
            name,
            raw_arguments,
            chat_id,
            message_id,
            actor_id,
            outgoing,
        )
        async with self._lock:
            registration = self._registry.get(name)
            if name != "/help" and registration is None:
                return CommandDispatch(False)
            if await self._receipts.is_processed(chat_id, message_id):
                return CommandDispatch(True, duplicate=True)
            try:
                response: str | None
                if not await self._authorizer.is_authorized(command):
                    response = "Permission denied."
                elif name == "/help":
                    response = self._help(raw_arguments)
                else:
                    assert registration is not None
                    response = (await registration.handler(command)).text
            except asyncio.CancelledError:
                raise
            except Exception as error:
                self._logger.exception(
                    "control command failed",
                    extra={
                        "command": name,
                        "chat_id": chat_id,
                        "message_id": message_id,
                        "actor_id": actor_id,
                        "error_type": type(error).__name__,
                    },
                )
                response = "Command failed. Check the application logs."
            await self._receipts.mark_processed(chat_id, message_id)
            return CommandDispatch(True, response)

    def _help(self, raw_arguments: str) -> str:
        requested = raw_arguments.strip()
        if requested:
            registration = self._registry.get(requested)
            if registration is None:
                return f"未知命令: {requested}"
            return registration.help_text

        lines = ["可用命令:", "/help - 列出命令或查看详细帮助"]
        lines.extend(
            f"{registration.name} - {registration.summary}"
            for registration in self._registry.list_commands()
        )
        lines.append("使用 /help /命令 查看详细帮助。")
        return "\n".join(lines)


def split_command(text: str | None) -> tuple[str, str] | None:
    """Split only the first token and preserve all text after its first delimiter."""

    if not text or not text.startswith("/"):
        return None
    for index, character in enumerate(text):
        if character.isspace():
            return text[:index], text[index + 1 :]
    return text, ""
