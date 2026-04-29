defmodule EmojiChatWeb.MessageController do
  use EmojiChatWeb, :controller

  alias EmojiChat.Chat

  def index(conn, %{"id" => room_id} = params) do
    limit = parse_int(params["limit"], 50) |> clamp(1, 200)
    before = parse_int(params["before"], nil)

    json(conn, %{messages: Chat.list_messages(room_id, limit, before)})
  end

  defp parse_int(nil, default), do: default
  defp parse_int("", default), do: default

  defp parse_int(s, default) when is_binary(s) do
    case Integer.parse(s) do
      {n, _} -> n
      :error -> default
    end
  end

  defp clamp(nil, _, _), do: nil
  defp clamp(n, min_v, max_v), do: n |> max(min_v) |> min(max_v)
end
