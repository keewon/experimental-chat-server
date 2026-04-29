defmodule EmojiChat.Repo do
  @adapter Application.compile_env(
             :emoji_chat,
             [EmojiChat.Repo, :adapter],
             Ecto.Adapters.SQLite3
           )

  use Ecto.Repo,
    otp_app: :emoji_chat,
    adapter: @adapter
end
