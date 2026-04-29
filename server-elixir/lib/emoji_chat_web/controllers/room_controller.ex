defmodule EmojiChatWeb.RoomController do
  use EmojiChatWeb, :controller

  alias EmojiChat.Chat

  def create(conn, params) do
    owner_id = trim(params["owner_id"])

    if owner_id == "" do
      json_error(conn, 400, "owner_id required")
    else
      room = Chat.create_room(owner_id, params["name"])
      conn |> put_status(201) |> json(room)
    end
  end

  def show(conn, %{"id" => id}) do
    case Chat.get_room(id) do
      nil -> json_error(conn, 404, "room not found")
      room -> json(conn, room)
    end
  end

  def delete(conn, %{"id" => id}) do
    user_id = trim(get_req_header(conn, "x-user-id") |> List.first())

    cond do
      user_id == "" ->
        json_error(conn, 400, "X-User-Id header required")

      true ->
        case Chat.get_owner(id) do
          nil ->
            json_error(conn, 404, "room not found")

          owner when owner != user_id ->
            json_error(conn, 403, "only the room owner can delete")

          _ ->
            Chat.delete_room(id)
            conn |> send_resp(204, "")
        end
    end
  end

  defp trim(nil), do: ""
  defp trim(s) when is_binary(s), do: String.trim(s)

  defp json_error(conn, status, msg) do
    conn |> put_status(status) |> json(%{error: msg})
  end
end
