import Config

db_type = System.get_env("DB_TYPE", "sqlite")

case db_type do
  "mysql" ->
    config :emoji_chat, EmojiChat.Repo,
      hostname: System.get_env("MYSQL_HOST", "127.0.0.1"),
      port: String.to_integer(System.get_env("MYSQL_PORT", "3306")),
      database: System.get_env("MYSQL_DB", "emoji_chat"),
      username: System.get_env("MYSQL_USER", "root"),
      password: System.get_env("MYSQL_PASSWORD", ""),
      pool_size: 10,
      charset: "utf8mb4"

  _ ->
    config :emoji_chat, EmojiChat.Repo,
      database: System.get_env("SQLITE_PATH", "emoji_chat.db"),
      pool_size: 5,
      journal_mode: :wal,
      foreign_keys: :on
end

if config_env() == :prod do
  secret_key_base =
    System.get_env("SECRET_KEY_BASE") ||
      raise "SECRET_KEY_BASE not set"

  config :emoji_chat, EmojiChatWeb.Endpoint,
    http: [ip: {0, 0, 0, 0}, port: String.to_integer(System.get_env("PORT", "8080"))],
    secret_key_base: secret_key_base,
    server: true
end
