import sqlite3

from yukibot.features.forwarder.migrations import FORWARDER_MIGRATIONS


def test_managed_topic_migration_preserves_existing_mapping_and_scopes_topics() -> None:
    database = sqlite3.connect(":memory:")
    try:
        for statement in FORWARDER_MIGRATIONS[2].statements:
            database.execute(statement)
        database.execute(
            """
            INSERT INTO forwarder_managed_topics (
                source_chat_id, destination_chat_id, topic_id, title
            ) VALUES (?, ?, ?, ?)
            """,
            (-1001, -2001, 50, "Source group"),
        )

        migration = FORWARDER_MIGRATIONS[-1]
        assert migration.version == 7
        for statement in migration.statements:
            database.execute(statement)

        database.executemany(
            """
            INSERT INTO forwarder_managed_topics (
                source_chat_id, source_topic_id, destination_chat_id, topic_id, title
            ) VALUES (?, ?, ?, ?, ?)
            """,
            (
                (-1001, 7, -2001, 51, "Source group/Announcements"),
                (-1001, 8, -2001, 52, "Source group/Support"),
            ),
        )
        rows = database.execute(
            """
            SELECT source_topic_id, topic_id, title
            FROM forwarder_managed_topics
            ORDER BY source_topic_id
            """
        ).fetchall()

        assert rows == [
            (0, 50, "Source group"),
            (7, 51, "Source group/Announcements"),
            (8, 52, "Source group/Support"),
        ]
    finally:
        database.close()
