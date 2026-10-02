"""Emit deterministic cases evaluated by the retained Python implementation.

Run through ``uv run go test -tags=parity ./tests/parity``. This is a bounded
contract corpus, NOT a claim that every module or possible input is equivalent.
No Telegram credentials, network calls, or production database are used.
"""

import json
from datetime import UTC, datetime
from itertools import product

from yukibot.contracts import (
    MessageRef,
    TelegramContentType,
    TelegramMessage,
    TelegramMessageEdited,
    TelegramMessageReceived,
    TelegramMessagesDeleted,
    TelegramServiceKind,
    TelegramServiceMessage,
)
from yukibot.features.forwarder.jobs import pending_jobs_for_event
from yukibot.features.forwarder.models import MessageFilter, SourceEndpoint
from yukibot.features.summarizer.references import parse_endpoint_reference

NOW = datetime(2026, 1, 2, 3, 4, 5, 123456, tzinfo=UTC)


def message(value):
    kind = TelegramContentType(value.get("kind", "text"))
    return TelegramMessage(
        MessageRef(-1001, 10),
        kind,
        NOW,
        text=value.get("text", ""),
        caption=value.get("caption", ""),
        grouped_id=value.get("group"),
        edited_at=NOW.replace(microsecond=0) if value.get("edited") else None,
        service=(
            TelegramServiceMessage(TelegramServiceKind.OTHER)
            if kind is TelegramContentType.SERVICE
            else None
        ),
    )


def evaluate(op, value):
    if op == "casefold":
        # A sparse table still checks every valid Unicode scalar: absence means
        # unchanged, not untested. Runtime Unicode-version drift must fail.
        return {
            str(point): chr(point).casefold()
            for point in range(0x110000)
            if not 0xD800 <= point <= 0xDFFF and chr(point).casefold() != chr(point)
        }
    if op == "filter":
        rule = MessageFilter(
            tuple(value.get("keywords", [])),
            frozenset(TelegramContentType(x) for x in value.get("allowed", [])),
            frozenset(TelegramContentType(x) for x in value.get("blocked", [])),
            value.get("include_service", False),
        )
        messages = tuple(message(x) for x in value["messages"])
        return {
            "single": [rule.allows(x) for x in messages],
            "album": rule.allows_album(messages),
        }
    if op == "source":
        source = SourceEndpoint(value["chat"], topic_id=value["topic"])
        return source.matches(value["other_chat"], value["other_topic"])
    if op == "reference":
        result = parse_endpoint_reference(value)
        return {"chat": result.chat, "topic": result.topic_id}
    if op == "jobs":
        msg = message(value)
        match value["event"]:
            case "receive":
                event = TelegramMessageReceived(msg)
            case "edit":
                event = TelegramMessageEdited(msg)
            case "delete":
                event = TelegramMessagesDeleted(tuple(value["ids"]), NOW, chat_id=value["chat"])
            case _:
                raise AssertionError("unknown event")
        return [
            {
                "kind": job.kind.value,
                "key": job.deduplication_key,
                "group": job.group_key,
                "available": job.available_at,
                "ids": list(job.event.message_ids)
                if isinstance(job.event, TelegramMessagesDeleted)
                else [job.event.message.ref.message_id],
            }
            for job in pending_jobs_for_event(event, now=100.25, album_delay=value.get("delay", 0))
        ]
    raise AssertionError(f"unknown operation: {op}")


def corpus():
    yield "casefold", None
    # All Unicode scalars whose full folding differs from lowercasing, plus
    # expanding folds in both directions. The oracle computes expected outcomes.
    for point in range(0x110000):
        if 0xD800 <= point <= 0xDFFF:
            continue
        char = chr(point)
        if char.casefold() != char.lower():
            for text, keyword in [(char, char.casefold()), (char.casefold(), char)]:
                yield (
                    "filter",
                    {
                        "keywords": [keyword],
                        "messages": [{"text": text}, {"caption": text}],
                    },
                )
    for text, keyword in [
        ("Straße", "STRASSE"),
        ("İstanbul", "i\u0307stanbul"),
        ("ΟΣ", "ος"),
        ("你好 世界", "世界"),
        ("ABC", "missing"),
        ("Kelvin \u212a", "kelvin k"),
        ("e\u0301", "é"),  # Casefolding must NOT introduce accent normalization.
        ("", ""),
        ("body", "caption"),  # Text takes precedence over caption.
    ]:
        yield (
            "filter",
            {
                "keywords": [keyword],
                "messages": [{"text": text, "caption": "caption"}],
            },
        )
    for kind, allowed, blocked, service, keywords in product(
        ["text", "photo", "service"],
        [[], ["text"], ["text", "photo", "service"]],
        [[], ["photo"], ["text", "photo", "service"]],
        [False, True],
        [[], ["match"], [" absent ", " MATCH ", " "]],
    ):
        yield (
            "filter",
            {
                "allowed": allowed,
                "blocked": blocked,
                "include_service": service,
                "keywords": keywords,
                "messages": [{"text": "match", "kind": kind}, {"caption": "no", "kind": kind}],
            },
        )
    yield "filter", {"messages": []}
    yield (
        "filter",
        {
            "keywords": ["first\nsecond"],
            "messages": [{"text": "first"}, {"caption": "second"}],
        },
    )
    for chat, other_chat, topic, other_topic in product(
        [-1001, 123], [-1001, 123], [None, 0, 1, 2, 99], [None, 0, 1, 2, 99]
    ):
        yield (
            "source",
            {
                "chat": chat,
                "other_chat": other_chat,
                "topic": topic,
                "other_topic": other_topic,
            },
        )
    for reference in [
        "",
        " ",
        "0",
        "-100123",
        "+123",
        "00123",
        "-100123/42",
        "123/1",
        "@example",
        "@",
        "@a/b",
        "https://t.me/example",
        "https://t.me/s/example/42",
        "https://telegram.me/example/0",
        "https://www.t.me/c/123/42",
        "https://t.me/c/123",
        "https://t.me/c/abc/42",
        "https://t.me/c/123/abc",
        "https://t.me/",
        "https://t.me/+invite",
        "https://t.me/joinchat/invite",
        "http://WWW.TELEGRAM.ME/example",
        "https://t.me/example/no",
        "https://t.me/example/42/43",
        "https://evil.example/example",
        "ftp://t.me/example",
        "https://t.me//s//example//42?ignored=1",
    ]:
        yield "reference", reference
    for event, group, edited, kind, delay in product(
        ["receive", "edit"],
        [None, 0, 42, "album"],
        [False, True],
        ["text", "photo", "service"],
        [0, 0.5, -1],
    ):
        yield (
            "jobs",
            {
                "event": event,
                "group": group,
                "edited": edited,
                "kind": kind,
                "delay": delay,
                "text": "编辑 Straße\x1f",
                "caption": "图说",
            },
        )
    for chat, ids in product([None, -1001, 123], [[], [10], [10, 11], [10, 10]]):
        yield "jobs", {"event": "delete", "chat": chat, "ids": ids}


if __name__ == "__main__":
    cases = []
    for index, (op, value) in enumerate(corpus()):
        try:
            expected = {"result": evaluate(op, value)}
        except ValueError as exc:
            expected = {"error": str(exc)}
        cases.append({"name": f"{op}/{index}", "op": op, "input": value, "expected": expected})
    print(json.dumps(cases, ensure_ascii=True))
