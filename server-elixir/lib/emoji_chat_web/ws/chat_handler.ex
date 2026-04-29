defmodule EmojiChatWeb.WS.ChatHandler do
  @moduledoc """
  Raw WebSocket handler (WebSock behaviour). One process per connection.
  Subscribes to the room PubSub topic and forwards messages to the client.
  """

  @behaviour WebSock

  alias EmojiChat.{Chat, EmojiValidator, RoomPresence}
  require Logger

  @impl true
  def init(state) do
    %{room_id: room_id} = state
    Phoenix.PubSub.subscribe(EmojiChat.PubSub, "room:" <> room_id)
    RoomPresence.join(room_id, state.user_id)
    {:ok, state}
  end

  @impl true
  def handle_in({json, [opcode: :text]}, state) do
    case Jason.decode(json) do
      {:ok, %{"type" => "message", "content" => content}} when is_binary(content) ->
        handle_message(String.trim(content), state)

      _ ->
        {:ok, state}
    end
  end

  def handle_in(_, state), do: {:ok, state}

  defp handle_message("", state), do: {:ok, state}

  defp handle_message(content, state) do
    if EmojiValidator.emoji_only?(content) do
      msg = Chat.insert_message(state.room_id, state.user_id, content)

      Phoenix.PubSub.broadcast(
        EmojiChat.PubSub,
        "room:" <> state.room_id,
        {:room_msg, Map.put(msg, :type, "message")}
      )

      {:ok, state}
    else
      payload = Jason.encode!(%{type: "error", message: "emoji only! 🙅"})
      {:push, {:text, payload}, state}
    end
  end

  @impl true
  def handle_info({:room_msg, payload}, state) do
    {:push, {:text, Jason.encode!(payload)}, state}
  end

  def handle_info({:room_presence, count, user_id, kind}, state) do
    payload =
      Jason.encode!(%{type: kind, user_id: user_id, online_count: count})

    {:push, {:text, payload}, state}
  end

  def handle_info(_msg, state), do: {:ok, state}

  @impl true
  def terminate(_reason, state) do
    RoomPresence.leave(state.room_id)
    :ok
  end
end
