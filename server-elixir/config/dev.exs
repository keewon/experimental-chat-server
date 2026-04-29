import Config

config :emoji_chat, EmojiChatWeb.Endpoint,
  http: [ip: {0, 0, 0, 0}, port: 8080],
  check_origin: false,
  code_reloader: false,
  debug_errors: true,
  secret_key_base: "dev_secret_key_base_dev_secret_key_base_dev_secret_key_base____",
  watchers: []

config :logger, :console, format: "[$level] $message\n"
config :phoenix, :stacktrace_depth, 20
config :phoenix, :plug_init_mode, :runtime
