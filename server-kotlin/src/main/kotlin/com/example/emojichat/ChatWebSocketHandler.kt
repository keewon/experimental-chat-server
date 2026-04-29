package com.example.emojichat

import com.fasterxml.jackson.databind.ObjectMapper
import com.fasterxml.jackson.module.kotlin.readValue
import org.slf4j.LoggerFactory
import org.springframework.stereotype.Component
import org.springframework.web.socket.CloseStatus
import org.springframework.web.socket.TextMessage
import org.springframework.web.socket.WebSocketSession
import org.springframework.web.socket.handler.TextWebSocketHandler

@Component
class ChatWebSocketHandler(
    private val manager: RoomHubManager,
    private val repo: ChatRepository,
    private val mapper: ObjectMapper,
) : TextWebSocketHandler() {
    private val log = LoggerFactory.getLogger(ChatWebSocketHandler::class.java)

    override fun afterConnectionEstablished(session: WebSocketSession) {
        val (roomId, userId) = parseAttrs(session) ?: run {
            session.close(CloseStatus.BAD_DATA)
            return
        }
        if (!repo.roomExists(roomId)) {
            session.close(CloseStatus.NOT_ACCEPTABLE)
            return
        }
        val hub = manager.getOrCreate(roomId)
        session.attributes["hub"] = hub
        session.attributes["userId"] = userId
        hub.register(session, userId)
    }

    override fun handleTextMessage(session: WebSocketSession, message: TextMessage) {
        val hub = session.attributes["hub"] as? RoomHub ?: return
        val userId = session.attributes["userId"] as? String ?: return

        val incoming = try {
            mapper.readValue<WsMessage>(message.payload)
        } catch (_: Exception) {
            return
        }

        if (incoming.type != "message") return
        val content = incoming.content?.trim().orEmpty()
        if (content.isEmpty()) return

        if (!EmojiValidator.isEmojiOnly(content)) {
            hub.sendTo(session, WsMessage(type = "error", message = "emoji only! 🙅"))
            return
        }

        val (id, createdAt) = try {
            repo.insertMessage(hub.roomId, userId, content)
        } catch (e: Exception) {
            log.warn("DB insert error: {}", e.message)
            return
        }

        hub.broadcast(
            WsMessage(
                type = "message",
                id = id,
                userId = userId,
                content = content,
                createdAt = createdAt,
            ),
        )
    }

    override fun afterConnectionClosed(session: WebSocketSession, status: CloseStatus) {
        (session.attributes["hub"] as? RoomHub)?.unregister(session)
    }

    private fun parseAttrs(session: WebSocketSession): Pair<String, String>? {
        val roomId = session.attributes["roomId"] as? String ?: return null
        val userId = session.attributes["userId-q"] as? String ?: return null
        if (roomId.isBlank() || userId.isBlank()) return null
        return roomId to userId
    }
}
