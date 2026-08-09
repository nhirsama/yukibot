"""Telegram delivery adapter for the out-of-band command dispatcher."""

from __future__ import annotations

import logging
from typing import Protocol

from yukibot.contracts import TelegramMessage
from yukibot.kernel import CommandDispatcher, split_command

from .client import NativeClient, PeerRegistry
from .rate_limit import TelegramRequestLimiter


class IncomingCommandRouter(Protocol):
    async def route(self, message: TelegramMessage, *, execute: bool = True) -> bool: ...


class TelegramCommandRouter:
    def __init__(
        self,
        dispatcher: CommandDispatcher,
        client: NativeClient,
        peers: PeerRegistry,
        request_limiter: TelegramRequestLimiter,
        *,
        logger: logging.Logger | None = None,
    ) -> None:
        self._dispatcher = dispatcher
        self._client = client
        self._peers = peers
        self._request_limiter = request_limiter
        self._logger = logger or logging.getLogger(__name__)
        self._response_ids: dict[tuple[int, int], None] = {}

    async def route(self, message: TelegramMessage, *, execute: bool = True) -> bool:
        message_key = (message.ref.chat_id, message.ref.message_id)
        if message.outgoing and message_key in self._response_ids:
            self._logger.info(
                "telegram control response consumed",
                extra={
                    "chat_id": message.ref.chat_id,
                    "message_id": message.ref.message_id,
                },
            )
            return True
        # Edited commands remain control-plane messages but cannot execute again.
        if not execute:
            return self._dispatcher.recognizes(message.text)
        outcome = await self._dispatcher.dispatch(
            message.text,
            chat_id=message.ref.chat_id,
            message_id=message.ref.message_id,
            actor_id=message.sender_id,
            outgoing=message.outgoing,
        )
        if not outcome.consumed:
            return False
        parsed = split_command(message.text)
        command_name = parsed[0] if parsed is not None else None
        self._logger.info(
            "telegram control command consumed",
            extra={
                "command": command_name,
                "chat_id": message.ref.chat_id,
                "message_id": message.ref.message_id,
                "actor_id": message.sender_id,
                "outgoing": message.outgoing,
                "duplicate": outcome.duplicate,
                "has_response": bool(outcome.response),
            },
        )
        if outcome.response:
            peer = self._peers.get(message.ref.chat_id)
            if peer is None:
                raise RuntimeError(f"command chat {message.ref.chat_id} is not in the peer cache")
            async with self._request_limiter.slot(message.ref.chat_id):
                response = await self._client.send_message(
                    peer,
                    outcome.response,
                    reply_to=message.ref.message_id,
                )
            self._remember_response(message.ref.chat_id, response.id)
            self._logger.info(
                "telegram control response sent",
                extra={
                    "command": command_name,
                    "chat_id": message.ref.chat_id,
                    "message_id": message.ref.message_id,
                    "response_message_id": response.id,
                },
            )
        return True

    def _remember_response(self, chat_id: int, message_id: int) -> None:
        key = (chat_id, message_id)
        self._response_ids[key] = None
        if len(self._response_ids) > 4096:
            self._response_ids.pop(next(iter(self._response_ids)))
