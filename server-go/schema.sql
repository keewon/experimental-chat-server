CREATE DATABASE IF NOT EXISTS emoji_chat
  CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

USE emoji_chat;

CREATE TABLE IF NOT EXISTS rooms (
    id         CHAR(36)     NOT NULL PRIMARY KEY,
    owner_id   CHAR(36)     NOT NULL,
    name       VARCHAR(64)  NOT NULL DEFAULT '',
    created_at TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS messages (
    id         BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    room_id    CHAR(36)     NOT NULL,
    user_id    CHAR(36)     NOT NULL,
    content    VARCHAR(512) NOT NULL,
    created_at TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_room_created (room_id, created_at),
    FOREIGN KEY (room_id) REFERENCES rooms(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
