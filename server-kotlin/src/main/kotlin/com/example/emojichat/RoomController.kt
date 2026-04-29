package com.example.emojichat

import org.springframework.http.HttpStatus
import org.springframework.http.ResponseEntity
import org.springframework.web.bind.annotation.*
import java.util.UUID

@RestController
@RequestMapping("/api/rooms")
class RoomController(private val repo: ChatRepository) {

    @PostMapping
    fun create(@RequestBody req: CreateRoomRequest): ResponseEntity<Any> {
        val ownerId = req.ownerId?.takeIf { it.isNotBlank() }
            ?: return error(HttpStatus.BAD_REQUEST, "owner_id required")
        val room = repo.createRoom(
            id = UUID.randomUUID().toString(),
            ownerId = ownerId,
            name = req.name ?: "",
        )
        return ResponseEntity.status(HttpStatus.CREATED).body(room)
    }

    @GetMapping("/{id}")
    fun get(@PathVariable id: String): ResponseEntity<Any> {
        val room = repo.findRoom(id) ?: return error(HttpStatus.NOT_FOUND, "room not found")
        return ResponseEntity.ok(room)
    }

    @DeleteMapping("/{id}")
    fun delete(
        @PathVariable id: String,
        @RequestHeader(name = "X-User-Id", required = false) userId: String?,
    ): ResponseEntity<Any> {
        if (userId.isNullOrBlank()) return error(HttpStatus.BAD_REQUEST, "X-User-Id header required")
        val owner = repo.findOwner(id) ?: return error(HttpStatus.NOT_FOUND, "room not found")
        if (owner != userId) return error(HttpStatus.FORBIDDEN, "only the room owner can delete")
        repo.deleteRoom(id)
        return ResponseEntity.noContent().build()
    }

    @GetMapping("/{id}/messages")
    fun messages(
        @PathVariable id: String,
        @RequestParam(required = false) limit: Int?,
        @RequestParam(required = false) before: Long?,
    ): Map<String, Any> {
        val safeLimit = (limit ?: 50).coerceIn(1, 200)
        return mapOf("messages" to repo.listMessages(id, safeLimit, before))
    }

    private fun error(status: HttpStatus, msg: String): ResponseEntity<Any> =
        ResponseEntity.status(status).body(mapOf("error" to msg))
}

@RestController
class RootController {
    @GetMapping("/")
    fun root(): ResponseEntity<Void> =
        ResponseEntity.status(HttpStatus.FOUND)
            .header("Location", "/static/index.html")
            .build()
}
