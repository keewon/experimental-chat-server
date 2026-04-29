defmodule EmojiChatWeb.Endpoint do
  use Phoenix.Endpoint, otp_app: :emoji_chat

  plug Plug.Static,
    at: "/static",
    from: :emoji_chat,
    gzip: false,
    only: ~w(index.html app.js style.css)

  plug Plug.RequestId
  plug Plug.Logger

  plug Plug.Parsers,
    parsers: [:urlencoded, :multipart, :json],
    pass: ["*/*"],
    json_decoder: Phoenix.json_library()

  plug Plug.MethodOverride
  plug Plug.Head

  plug EmojiChatWeb.Router
end
