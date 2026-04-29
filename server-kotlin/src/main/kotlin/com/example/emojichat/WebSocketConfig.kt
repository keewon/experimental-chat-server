package com.example.emojichat

import org.springframework.context.annotation.Configuration
import org.springframework.http.server.ServerHttpRequest
import org.springframework.http.server.ServerHttpResponse
import org.springframework.http.server.ServletServerHttpRequest
import org.springframework.web.socket.WebSocketHandler
import org.springframework.web.socket.config.annotation.EnableWebSocket
import org.springframework.web.socket.config.annotation.WebSocketConfigurer
import org.springframework.web.socket.config.annotation.WebSocketHandlerRegistry
import org.springframework.web.socket.server.HandshakeInterceptor
import org.springframework.web.util.UriComponentsBuilder

@Configuration
@EnableWebSocket
class WebSocketConfig(
    private val handler: ChatWebSocketHandler,
) : WebSocketConfigurer {

    override fun registerWebSocketHandlers(registry: WebSocketHandlerRegistry) {
        registry.addHandler(handler, "/ws/{roomId}")
            .addInterceptors(WsInterceptor())
            .setAllowedOriginPatterns("*")
    }
}

class WsInterceptor : HandshakeInterceptor {
    override fun beforeHandshake(
        request: ServerHttpRequest,
        response: ServerHttpResponse,
        wsHandler: WebSocketHandler,
        attributes: MutableMap<String, Any>,
    ): Boolean {
        val servletReq = (request as? ServletServerHttpRequest)?.servletRequest ?: return false
        // Path: /ws/{roomId}
        val path = servletReq.requestURI
        val segs = path.split("/").filter { it.isNotEmpty() }
        val roomId = if (segs.size >= 2 && segs[0] == "ws") segs[1] else null

        val query = servletReq.queryString
        val userId = if (query != null) {
            UriComponentsBuilder.newInstance().query(query).build().queryParams.getFirst("userId")
        } else null

        if (roomId.isNullOrBlank() || userId.isNullOrBlank()) return false
        attributes["roomId"] = roomId
        attributes["userId-q"] = userId
        return true
    }

    override fun afterHandshake(
        request: ServerHttpRequest,
        response: ServerHttpResponse,
        wsHandler: WebSocketHandler,
        exception: Exception?,
    ) {}
}
