package com.example.emojichat

import com.fasterxml.jackson.databind.ObjectMapper
import org.slf4j.LoggerFactory
import org.springframework.beans.factory.annotation.Value
import org.springframework.stereotype.Component
import org.springframework.web.socket.TextMessage
import org.springframework.web.socket.WebSocketSession
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.Executors
import java.util.concurrent.ScheduledFuture
import java.util.concurrent.TimeUnit

@Component
class RoomHubManager(
    private val mapper: ObjectMapper,
    @Value("\${emojichat.room-idle-timeout-seconds:3600}") private val idleSeconds: Long,
) {
    private val log = LoggerFactory.getLogger(RoomHubManager::class.java)
    private val hubs = ConcurrentHashMap<String, RoomHub>()
    private val scheduler = Executors.newScheduledThreadPool(1)

    fun getOrCreate(roomId: String): RoomHub =
        hubs.computeIfAbsent(roomId) {
            log.info("RoomHub started: {}", it)
            RoomHub(it, this)
        }

    fun remove(roomId: String) {
        hubs.remove(roomId)
        log.info("RoomHub removed: {}", roomId)
    }

    fun scheduleIdleCheck(hub: RoomHub, action: () -> Unit): ScheduledFuture<*> =
        scheduler.schedule(action, idleSeconds, TimeUnit.SECONDS)

    fun toJson(msg: WsMessage): String = mapper.writeValueAsString(msg)
}

class RoomHub(
    val roomId: String,
    private val manager: RoomHubManager,
) {
    private val log = LoggerFactory.getLogger(RoomHub::class.java)
    private val clients = ConcurrentHashMap<WebSocketSession, String>() // session -> userId
    private var idleTask: ScheduledFuture<*>? = null

    @Synchronized
    fun register(session: WebSocketSession, userId: String) {
        clients[session] = userId
        idleTask?.cancel(false)
        idleTask = null
        broadcastSystem(WsMessage(type = "join", userId = userId, onlineCount = clients.size))
    }

    @Synchronized
    fun unregister(session: WebSocketSession) {
        val userId = clients.remove(session) ?: return
        val count = clients.size
        broadcastSystem(WsMessage(type = "leave", userId = userId, onlineCount = count))
        if (count == 0) {
            idleTask = manager.scheduleIdleCheck(this) {
                synchronized(this) {
                    if (clients.isEmpty()) manager.remove(roomId)
                }
            }
        }
    }

    fun broadcast(msg: WsMessage) {
        val payload = TextMessage(manager.toJson(msg))
        for ((session, _) in clients) {
            sendOrDrop(session, payload)
        }
    }

    private fun broadcastSystem(msg: WsMessage) {
        broadcast(msg)
    }

    fun sendTo(session: WebSocketSession, msg: WsMessage) {
        sendOrDrop(session, TextMessage(manager.toJson(msg)))
    }

    private fun sendOrDrop(session: WebSocketSession, payload: TextMessage) {
        try {
            if (session.isOpen) {
                synchronized(session) { session.sendMessage(payload) }
            }
        } catch (e: Exception) {
            log.warn("Send failed, dropping client: {}", e.message)
            clients.remove(session)
            try { session.close() } catch (_: Exception) {}
        }
    }
}
