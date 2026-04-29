package com.example.emojichat

import com.fasterxml.jackson.annotation.JsonInclude
import com.fasterxml.jackson.annotation.JsonProperty

data class Room(
    val id: String,
    @JsonProperty("owner_id") val ownerId: String,
    val name: String,
    @JsonProperty("created_at") val createdAt: String,
)

data class Message(
    val id: Long,
    @JsonProperty("room_id") val roomId: String,
    @JsonProperty("user_id") val userId: String,
    val content: String,
    @JsonProperty("created_at") val createdAt: String,
)

@JsonInclude(JsonInclude.Include.NON_DEFAULT)
data class WsMessage(
    val type: String,
    val content: String? = null,
    val id: Long = 0,
    @JsonProperty("user_id") val userId: String? = null,
    @JsonProperty("created_at") val createdAt: String? = null,
    @JsonProperty("online_count") val onlineCount: Int = 0,
    val message: String? = null,
)

data class CreateRoomRequest(
    val name: String?,
    @JsonProperty("owner_id") val ownerId: String?,
)
