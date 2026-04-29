defmodule EmojiChatWeb.WSController do
  use EmojiChatWeb, :controller

  alias EmojiChat.Chat

  def upgrade(conn, %{"room_id" => room_id} = params) do
    user_id = params["userId"] || ""

    cond do
      room_id == "" or user_id == "" ->
        send_resp(conn, 400, "missing roomId or userId")

      not Chat.room_exists?(room_id) ->
        send_resp(conn, 404, "room not found")

      true ->
        conn
        |> WebSockAdapter.upgrade(
          EmojiChatWeb.WS.ChatHandler,
          %{room_id: room_id, user_id: user_id},
          []
        )
        |> halt()
    end
  end
end
