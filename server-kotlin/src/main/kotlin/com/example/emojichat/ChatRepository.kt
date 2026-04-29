package com.example.emojichat

import jakarta.annotation.PostConstruct
import org.slf4j.LoggerFactory
import org.springframework.beans.factory.annotation.Value
import org.springframework.jdbc.core.JdbcTemplate
import org.springframework.stereotype.Repository
import java.sql.Statement
import java.sql.Timestamp
import java.time.Instant
import java.time.OffsetDateTime
import java.time.ZoneOffset
import java.time.format.DateTimeFormatter

@Repository
class ChatRepository(
    private val jdbc: JdbcTemplate,
    @Value("\${emojichat.db-type:sqlite}") private val dbType: String,
) {
    private val log = LoggerFactory.getLogger(ChatRepository::class.java)

    @PostConstruct
    fun init() {
        if (dbType.equals("sqlite", ignoreCase = true)) {
            jdbc.execute("PRAGMA journal_mode=WAL")
            jdbc.execute("PRAGMA foreign_keys=ON")
            jdbc.execute(
                """
                CREATE TABLE IF NOT EXISTS rooms (
                    id         TEXT NOT NULL PRIMARY KEY,
                    owner_id   TEXT NOT NULL,
                    name       TEXT NOT NULL DEFAULT '',
                    created_at TEXT NOT NULL DEFAULT (datetime('now'))
                )
                """.trimIndent()
            )
            jdbc.execute(
                """
                CREATE TABLE IF NOT EXISTS messages (
                    id         INTEGER PRIMARY KEY AUTOINCREMENT,
                    room_id    TEXT NOT NULL,
                    user_id    TEXT NOT NULL,
                    content    TEXT NOT NULL,
                    created_at TEXT NOT NULL DEFAULT (datetime('now')),
                    FOREIGN KEY (room_id) REFERENCES rooms(id) ON DELETE CASCADE
                )
                """.trimIndent()
            )
            jdbc.execute("CREATE INDEX IF NOT EXISTS idx_messages_room_created ON messages(room_id, created_at)")
            log.info("SQLite tables ready")
        }
    }

    fun createRoom(id: String, ownerId: String, name: String): Room {
        val now = nowIso()
        jdbc.update(
            "INSERT INTO rooms (id, owner_id, name, created_at) VALUES (?, ?, ?, ?)",
            id, ownerId, name, now,
        )
        return Room(id, ownerId, name, now)
    }

    fun findRoom(id: String): Room? {
        val rs = jdbc.query(
            "SELECT id, owner_id, name, created_at FROM rooms WHERE id = ?",
            { row, _ ->
                Room(
                    row.getString("id"),
                    row.getString("owner_id"),
                    row.getString("name"),
                    readTimestamp(row.getObject("created_at")),
                )
            },
            id,
        )
        return rs.firstOrNull()
    }

    fun findOwner(id: String): String? {
        val rs = jdbc.query(
            "SELECT owner_id FROM rooms WHERE id = ?",
            { row, _ -> row.getString("owner_id") },
            id,
        )
        return rs.firstOrNull()
    }

    fun deleteRoom(id: String) {
        jdbc.update("DELETE FROM rooms WHERE id = ?", id)
    }

    fun roomExists(id: String): Boolean {
        val count = jdbc.queryForObject(
            "SELECT COUNT(*) FROM rooms WHERE id = ?", Int::class.java, id,
        ) ?: 0
        return count > 0
    }

    fun listMessages(roomId: String, limit: Int, before: Long?): List<Message> {
        val sql = if (before != null) {
            "SELECT id, room_id, user_id, content, created_at FROM messages " +
                "WHERE room_id = ? AND id < ? ORDER BY id DESC LIMIT ?"
        } else {
            "SELECT id, room_id, user_id, content, created_at FROM messages " +
                "WHERE room_id = ? ORDER BY id DESC LIMIT ?"
        }

        val args: Array<Any> = if (before != null) arrayOf(roomId, before, limit) else arrayOf(roomId, limit)

        val rows = jdbc.query(sql, { row, _ ->
            Message(
                id = row.getLong("id"),
                roomId = row.getString("room_id"),
                userId = row.getString("user_id"),
                content = row.getString("content"),
                createdAt = readTimestamp(row.getObject("created_at")),
            )
        }, *args)

        // Reverse to chronological order
        return rows.reversed()
    }

    fun insertMessage(roomId: String, userId: String, content: String): Pair<Long, String> {
        val now = nowIso()
        val con = jdbc.dataSource!!.connection
        con.use { conn ->
            conn.prepareStatement(
                "INSERT INTO messages (room_id, user_id, content, created_at) VALUES (?, ?, ?, ?)",
                Statement.RETURN_GENERATED_KEYS,
            ).use { ps ->
                ps.setString(1, roomId)
                ps.setString(2, userId)
                ps.setString(3, content)
                ps.setString(4, now)
                ps.executeUpdate()
                val keys = ps.generatedKeys
                if (keys.next()) {
                    return keys.getLong(1) to now
                }
            }
        }
        return 0L to now
    }

    private fun nowIso(): String =
        OffsetDateTime.now(ZoneOffset.UTC).truncatedTo(java.time.temporal.ChronoUnit.SECONDS)
            .format(DateTimeFormatter.ISO_OFFSET_DATE_TIME)

    private fun readTimestamp(value: Any?): String = when (value) {
        null -> ""
        is Timestamp -> OffsetDateTime.ofInstant(value.toInstant(), ZoneOffset.UTC)
            .format(DateTimeFormatter.ISO_OFFSET_DATE_TIME)
        is Instant -> OffsetDateTime.ofInstant(value, ZoneOffset.UTC)
            .format(DateTimeFormatter.ISO_OFFSET_DATE_TIME)
        is String -> value
        else -> value.toString()
    }
}
