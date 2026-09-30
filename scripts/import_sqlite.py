#!/usr/bin/env python3
"""Import a Python yukibot SQLite database into the PostgreSQL schema.

SQLite migration rows are not copied. The Go process owns
yukibot_schema_migrations and its checksums. Naive SQLite timestamps are UTC.
Job available_at values are Unix seconds.
"""

from __future__ import annotations

import hashlib
import os
import sqlite3
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path


def database_url() -> str:
    if value := os.environ.get("YUKIBOT_DATABASE_URL", "").strip():
        return value
    env_path = Path(".env")
    if env_path.is_file():
        for line in env_path.read_text().splitlines():
            line = line.strip()
            if not line or line.startswith("#") or "=" not in line:
                continue
            key, raw = line.split("=", 1)
            if key.strip() == "DATABASE_URL":
                return raw.strip().strip("'").strip('"')
    raise SystemExit("DATABASE_URL is not set")


def sql_literal(value: object) -> str:
    if value is None:
        return "NULL"
    if isinstance(value, bool):
        return "TRUE" if value else "FALSE"
    if isinstance(value, int):
        return str(value)
    text = str(value).replace("'", "''")
    return "'" + text + "'"


def boolean(value: object) -> str:
    if value not in (0, 1):
        raise SystemExit(f"expected 0/1 boolean, got {value!r}")
    return "TRUE" if value else "FALSE"


def timestamp(value: object) -> str:
    if value is None:
        return "NULL"
    if isinstance(value, (int, float)) and not isinstance(value, bool):
        text = datetime.fromtimestamp(float(value), timezone.utc).isoformat()
        return sql_literal(text) + "::timestamptz"
    text = str(value).strip()
    if "T" not in text and "+" not in text and not text.endswith("Z"):
        text = text.replace(" ", "T") + "+00:00"
    return sql_literal(text) + "::timestamptz"


def rows_sql(table: str, columns: list[str], values: list[str], *, override: bool = False) -> str:
    if not values:
        return ""
    clause = " OVERRIDING SYSTEM VALUE" if override else ""
    return (
        f"INSERT INTO {table} ({', '.join(columns)}){clause} VALUES\n"
        + ",\n".join(values)
        + ";\n"
    )


def batches(sql_rows: list[str], size: int = 200) -> list[list[str]]:
    return [sql_rows[i : i + size] for i in range(0, len(sql_rows), size)]


def digest(parts: list[str]) -> str:
    return hashlib.md5(",".join(parts).encode()).hexdigest()


def main() -> None:
    sqlite_path = Path(sys.argv[1] if len(sys.argv) > 1 else "data/yukibot.db")
    if not sqlite_path.is_file():
        raise SystemExit(f"sqlite database not found: {sqlite_path}")
    connection = sqlite3.connect(f"file:{sqlite_path}?mode=ro", uri=True)
    connection.row_factory = sqlite3.Row
    query = connection.execute

    route_rows = list(query("SELECT * FROM forwarder_routes ORDER BY id"))
    link_rows = list(
        query(
            "SELECT * FROM forwarder_message_links ORDER BY route_id, source_chat_id, source_message_id"
        )
    )
    job_rows = list(query("SELECT * FROM forwarder_jobs ORDER BY id"))
    topic_rows = list(query("SELECT * FROM forwarder_managed_topics ORDER BY destination_chat_id, topic_id"))
    cursor_rows = list(query("SELECT * FROM forwarder_poll_cursors ORDER BY source_chat_id"))
    access_rows = list(query("SELECT * FROM forwarder_chat_access ORDER BY chat_id"))
    admin_rows = list(query("SELECT * FROM management_admins ORDER BY user_id"))
    module_rows = list(query("SELECT * FROM management_modules ORDER BY name"))
    receipt_rows = list(
        query("SELECT * FROM management_command_receipts ORDER BY account_id, chat_id, message_id")
    )
    rule_rows = list(query("SELECT * FROM summarizer_rules ORDER BY id"))
    run_rows = list(query("SELECT * FROM summarizer_runs ORDER BY id"))
    model_rows = list(query("SELECT * FROM summarizer_model_config"))

    route_digest = digest(
        [
            "|".join(
                [
                    str(row["id"]),
                    str(row["source_chat_id"]),
                    str(row["destination_chat_id"]),
                    str(row["mode"]),
                    "true" if row["enabled"] else "false",
                    "true" if row["fallback_to_copy"] else "false",
                    "" if row["source_username"] is None else str(row["source_username"]),
                    "" if row["poll_interval_seconds"] is None else str(row["poll_interval_seconds"]),
                ]
            )
            for row in route_rows
        ]
    )
    link_digest = digest(
        [
            "|".join(
                [
                    str(row["route_id"]),
                    str(row["source_chat_id"]),
                    str(row["source_message_id"]),
                    str(row["destination_chat_id"]),
                    str(row["destination_message_id"]),
                    str(row["delivery_mode"]),
                ]
            )
            for row in link_rows
        ]
    )

    statements: list[str] = ["BEGIN;\n"]
    statements.append(
        """TRUNCATE
    summarizer_runs,
    summarizer_rules,
    summarizer_model_config,
    management_command_receipts,
    management_admins,
    management_modules,
    forwarder_message_links,
    forwarder_jobs,
    forwarder_managed_topics,
    forwarder_poll_cursors,
    forwarder_chat_access,
    forwarder_routes
RESTART IDENTITY CASCADE;
"""
    )

    route_sql = []
    for row in route_rows:
        route_sql.append(
            "("
            + ", ".join(
                [
                    sql_literal(row["id"]),
                    sql_literal(row["source_chat_id"]),
                    sql_literal(row["source_topic_id"]),
                    sql_literal(row["destination_chat_id"]),
                    sql_literal(row["destination_topic_id"]),
                    sql_literal(row["mode"]),
                    sql_literal(row["filter_json"]) + "::jsonb",
                    boolean(row["enabled"]),
                    boolean(row["fallback_to_copy"]),
                    sql_literal(row["source_username"]),
                    sql_literal(row["destination_username"]),
                    sql_literal(row["poll_interval_seconds"]),
                ]
            )
            + ")"
        )
    for batch in batches(route_sql):
        statements.append(
            rows_sql(
                "forwarder_routes",
                [
                    "id",
                    "source_chat_id",
                    "source_topic_id",
                    "destination_chat_id",
                    "destination_topic_id",
                    "mode",
                    "filter_json",
                    "enabled",
                    "fallback_to_copy",
                    "source_username",
                    "destination_username",
                    "poll_interval_seconds",
                ],
                batch,
            )
        )

    link_sql = []
    for row in link_rows:
        link_sql.append(
            "("
            + ", ".join(
                [
                    sql_literal(row["route_id"]),
                    sql_literal(row["source_chat_id"]),
                    sql_literal(row["source_message_id"]),
                    sql_literal(row["destination_chat_id"]),
                    sql_literal(row["destination_message_id"]),
                    sql_literal(row["delivery_mode"]),
                    timestamp(row["created_at"]),
                ]
            )
            + ")"
        )
    for batch in batches(link_sql):
        statements.append(
            rows_sql(
                "forwarder_message_links",
                [
                    "route_id",
                    "source_chat_id",
                    "source_message_id",
                    "destination_chat_id",
                    "destination_message_id",
                    "delivery_mode",
                    "created_at",
                ],
                batch,
            )
        )

    job_sql = []
    for row in job_rows:
        job_sql.append(
            "("
            + ", ".join(
                [
                    sql_literal(row["id"]),
                    sql_literal(row["kind"]),
                    sql_literal(row["deduplication_key"]),
                    sql_literal(row["group_key"]),
                    sql_literal(row["payload_json"]) + "::jsonb",
                    sql_literal(row["state"]),
                    sql_literal(row["attempts"]),
                    timestamp(row["available_at"]),
                    sql_literal(row["last_error"]),
                    timestamp(row["created_at"]),
                    timestamp(row["updated_at"]),
                ]
            )
            + ")"
        )
    for batch in batches(job_sql, 50):
        statements.append(
            rows_sql(
                "forwarder_jobs",
                [
                    "id",
                    "kind",
                    "deduplication_key",
                    "group_key",
                    "payload_json",
                    "state",
                    "attempts",
                    "available_at",
                    "last_error",
                    "created_at",
                    "updated_at",
                ],
                batch,
                override=True,
            )
        )

    topic_sql = []
    for row in topic_rows:
        topic_sql.append(
            "("
            + ", ".join(
                [
                    sql_literal(row["source_chat_id"]),
                    sql_literal(row["source_topic_id"]),
                    sql_literal(row["destination_chat_id"]),
                    sql_literal(row["topic_id"]),
                    sql_literal(row["title"]),
                    timestamp(row["created_at"]),
                    timestamp(row["updated_at"]),
                ]
            )
            + ")"
        )
    statements.append(
        rows_sql(
            "forwarder_managed_topics",
            [
                "source_chat_id",
                "source_topic_id",
                "destination_chat_id",
                "topic_id",
                "title",
                "created_at",
                "updated_at",
            ],
            topic_sql,
        )
    )

    cursor_sql = []
    for row in cursor_rows:
        cursor_sql.append(
            "("
            + ", ".join(
                [
                    sql_literal(row["source_chat_id"]),
                    sql_literal(row["last_message_id"]),
                    timestamp(row["updated_at"]),
                ]
            )
            + ")"
        )
    statements.append(
        rows_sql(
            "forwarder_poll_cursors",
            ["source_chat_id", "last_message_id", "updated_at"],
            cursor_sql,
        )
    )

    access_sql = []
    for row in access_rows:
        access_sql.append(
            "("
            + ", ".join(
                [
                    sql_literal(row["chat_id"]),
                    sql_literal(row["title"]),
                    sql_literal(row["username"]),
                    sql_literal(row["invite_link"]),
                    timestamp(row["updated_at"]),
                ]
            )
            + ")"
        )
    statements.append(
        rows_sql(
            "forwarder_chat_access",
            ["chat_id", "title", "username", "invite_link", "updated_at"],
            access_sql,
        )
    )

    admin_sql = []
    for row in admin_rows:
        admin_sql.append(
            "("
            + ", ".join(
                [
                    sql_literal(row["user_id"]),
                    sql_literal(row["granted_by"]),
                    timestamp(row["created_at"]),
                ]
            )
            + ")"
        )
    statements.append(rows_sql("management_admins", ["user_id", "granted_by", "created_at"], admin_sql))

    module_sql = []
    for row in module_rows:
        module_sql.append(
            "("
            + ", ".join(
                [
                    sql_literal(row["name"]),
                    boolean(row["enabled"]),
                    timestamp(row["updated_at"]),
                ]
            )
            + ")"
        )
    statements.append(rows_sql("management_modules", ["name", "enabled", "updated_at"], module_sql))

    receipt_sql = []
    for row in receipt_rows:
        receipt_sql.append(
            "("
            + ", ".join(
                [
                    sql_literal(row["account_id"]),
                    sql_literal(row["chat_id"]),
                    sql_literal(row["message_id"]),
                    timestamp(row["processed_at"]),
                ]
            )
            + ")"
        )
    statements.append(
        rows_sql(
            "management_command_receipts",
            ["account_id", "chat_id", "message_id", "processed_at"],
            receipt_sql,
        )
    )

    rule_sql = []
    for row in rule_rows:
        rule_sql.append(
            "("
            + ", ".join(
                [
                    sql_literal(row["id"]),
                    sql_literal(row["source_chat_id"]),
                    sql_literal(row["source_topic_id"]),
                    sql_literal(row["source_username"]),
                    sql_literal(row["destination_chat_id"]),
                    sql_literal(row["destination_topic_id"]),
                    sql_literal(row["destination_username"]),
                    sql_literal(row["window_seconds"]),
                    boolean(row["enabled"]),
                    timestamp(row["created_at"]),
                    timestamp(row["updated_at"]),
                ]
            )
            + ")"
        )
    statements.append(
        rows_sql(
            "summarizer_rules",
            [
                "id",
                "source_chat_id",
                "source_topic_id",
                "source_username",
                "destination_chat_id",
                "destination_topic_id",
                "destination_username",
                "window_seconds",
                "enabled",
                "created_at",
                "updated_at",
            ],
            rule_sql,
        )
    )

    run_sql = []
    for row in run_rows:
        run_sql.append(
            "("
            + ", ".join(
                [
                    sql_literal(row["id"]),
                    sql_literal(row["rule_id"]),
                    timestamp(row["started_at"]),
                    timestamp(row["completed_at"]),
                    sql_literal(row["first_message_id"]),
                    sql_literal(row["last_message_id"]),
                    sql_literal(row["message_count"]),
                    sql_literal(row["provider"]),
                    sql_literal(row["model"]),
                    sql_literal(row["prompt_version"]),
                    sql_literal(row["output_json"]),
                    timestamp(row["created_at"]),
                ]
            )
            + ")"
        )
    statements.append(
        rows_sql(
            "summarizer_runs",
            [
                "id",
                "rule_id",
                "started_at",
                "completed_at",
                "first_message_id",
                "last_message_id",
                "message_count",
                "provider",
                "model",
                "prompt_version",
                "output_json",
                "created_at",
            ],
            run_sql,
        )
    )

    model_sql = []
    for row in model_rows:
        model_sql.append(
            "("
            + ", ".join(
                [
                    sql_literal(row["id"]),
                    sql_literal(row["provider"]),
                    sql_literal(row["model"]),
                    sql_literal(row["api_key"]),
                    sql_literal(row["base_url"]),
                    sql_literal(row["input_token_limit"]),
                    sql_literal(row["output_token_limit"]),
                    sql_literal(row["temperature"]),
                    sql_literal(row["timeout"]),
                    sql_literal(row["max_retries"]),
                    sql_literal(row["prompt_preset"]),
                    sql_literal(row["custom_prompt"]),
                    sql_literal(row["max_concurrency"]),
                    timestamp(row["updated_at"]),
                ]
            )
            + ")"
        )
    statements.append(
        rows_sql(
            "summarizer_model_config",
            [
                "id",
                "provider",
                "model",
                "api_key",
                "base_url",
                "input_token_limit",
                "output_token_limit",
                "temperature",
                "timeout",
                "max_retries",
                "prompt_preset",
                "custom_prompt",
                "max_concurrency",
                "updated_at",
            ],
            model_sql,
        )
    )

    for table in ("forwarder_routes", "forwarder_jobs", "summarizer_rules", "summarizer_runs"):
        statements.append(
            f"SELECT setval(pg_get_serial_sequence('{table}', 'id'), (SELECT MAX(id) FROM {table}));\n"
        )

    expected = {
        "forwarder_routes": len(route_rows),
        "forwarder_message_links": len(link_rows),
        "forwarder_jobs": len(job_rows),
        "forwarder_managed_topics": len(topic_rows),
        "forwarder_poll_cursors": len(cursor_rows),
        "forwarder_chat_access": len(access_rows),
        "management_admins": len(admin_rows),
        "management_modules": len(module_rows),
        "management_command_receipts": len(receipt_rows),
        "summarizer_rules": len(rule_rows),
        "summarizer_runs": len(run_rows),
        "summarizer_model_config": len(model_rows),
    }
    checks = []
    for table, count in expected.items():
        checks.append(
            f"SELECT COUNT(*) INTO n FROM {table}; IF n <> {count} THEN RAISE EXCEPTION '{table} count %', n; END IF;"
        )
    checks.append(
        "SELECT md5(string_agg(id::text || '|' || source_chat_id::text || '|' || destination_chat_id::text || '|' || mode || '|' || enabled::text || '|' || fallback_to_copy::text || '|' || COALESCE(source_username, '') || '|' || COALESCE(poll_interval_seconds::text, ''), ',' ORDER BY id)) INTO signature FROM forwarder_routes;"
        f" IF signature <> '{route_digest}' THEN RAISE EXCEPTION 'route signature %', signature; END IF;"
    )
    checks.append(
        "SELECT md5(string_agg(route_id::text || '|' || source_chat_id::text || '|' || source_message_id::text || '|' || destination_chat_id::text || '|' || destination_message_id::text || '|' || delivery_mode, ',' ORDER BY route_id, source_chat_id, source_message_id)) INTO signature FROM forwarder_message_links;"
        f" IF signature <> '{link_digest}' THEN RAISE EXCEPTION 'link signature %', signature; END IF;"
    )
    checks.append(
        "SELECT COUNT(*) INTO n FROM forwarder_jobs WHERE state <> 'failed'; IF n <> 0 THEN RAISE EXCEPTION 'unexpected pending jobs %', n; END IF;"
    )
    checks.append(
        "SELECT COUNT(*) INTO n FROM forwarder_routes WHERE jsonb_typeof(filter_json) <> 'object'; IF n <> 0 THEN RAISE EXCEPTION 'bad filters %', n; END IF;"
    )
    statements.append(
        "DO $$\nDECLARE n bigint; signature text;\nBEGIN\n" + "\n".join(checks) + "\nEND $$;\n"
    )
    statements.append("COMMIT;\n")

    url = database_url()
    if not url.startswith("postgres://") and not url.startswith("postgresql://"):
        raise SystemExit("DATABASE_URL must be a postgres URL")
    env = os.environ.copy()
    env["PGCLIENTENCODING"] = "UTF8"
    completed = subprocess.run(
        ["psql", url, "-X", "-q", "-v", "ON_ERROR_STOP=1", "-f", "-"],
        input="".join(statements).encode(),
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        env=env,
        check=False,
    )
    if completed.returncode != 0:
        error = completed.stderr.decode(errors="replace")
        error = redact(error, url)
        raise SystemExit(error.strip() or "psql failed")
    print("imported")
    for table, count in expected.items():
        print(f"{table} {count}")
    print(f"route_signature {route_digest}")
    print(f"link_signature {link_digest}")


def redact(message: str, url: str) -> str:
    message = message.replace(url, "postgres://[redacted]")
    if "://" in message:
        start = message.find("://")
        at = message.find("@", start)
        if at > start:
            message = message[: start + 3] + "[redacted]" + message[at:]
    return message


if __name__ == "__main__":
    main()
