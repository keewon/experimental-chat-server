import Config

config :emoji_chat,
  ecto_repos: [EmojiChat.Repo],
  generators: [timestamp_type: :utc_datetime]

# Endpoint defaults; overridden in runtime.exs.
config :emoji_chat, EmojiChatWeb.Endpoint,
  url: [host: "localhost"],
  adapter: Bandit.PhoenixAdapter,
  render_errors: [
    formats: [json: EmojiChatWeb.ErrorJSON],
    layout: false
  ],
  pubsub_server: EmojiChat.PubSub,
  live_view: [signing_salt: "emoji-chat-salt"]

# Choose Ecto adapter at compile time based on DB_TYPE env var.
# DB_TYPE=mysql mix compile  ⇒ MyXQL
# DB_TYPE=sqlite (default)   ⇒ SQLite3
config :emoji_chat, EmojiChat.Repo,
  adapter:
    case System.get_env("DB_TYPE", "sqlite") do
      "mysql" -> Ecto.Adapters.MyXQL
      _ -> Ecto.Adapters.SQLite3
    end

config :phoenix, :json_library, Jason

config :logger, :console,
  format: "$time $metadata[$level] $message\n",
  metadata: [:request_id]

import_config "#{config_env()}.exs"
