defmodule EmojiChat.Chat do
  @moduledoc "Context for rooms & messages."
  import Ecto.Query

  alias EmojiChat.{Repo, Room, Message}

  # ─── Rooms ────────────────────────────────────────────────────────

  def create_room(owner_id, name) do
    now = DateTime.utc_now() |> DateTime.truncate(:second)

    %Room{
      id: Ecto.UUID.generate(),
      owner_id: owner_id,
      name: name || "",
      created_at: now
    }
    |> Repo.insert!()
    |> room_to_map()
  end

  def get_room(id) do
    case Repo.get(Room, id) do
      nil -> nil
      room -> room_to_map(room)
    end
  end

  def get_owner(id) do
    Repo.one(from r in Room, where: r.id == ^id, select: r.owner_id)
  end

  def delete_room(id) do
    Repo.delete_all(from m in Message, where: m.room_id == ^id)
    Repo.delete_all(from r in Room, where: r.id == ^id)
    :ok
  end

  def room_exists?(id) do
    Repo.exists?(from r in Room, where: r.id == ^id)
  end

  # ─── Messages ─────────────────────────────────────────────────────

  def list_messages(room_id, limit, before) do
    base = from m in Message, where: m.room_id == ^room_id

    q =
      if is_integer(before) do
        from m in base, where: m.id < ^before
      else
        base
      end

    q
    |> order_by([m], desc: m.id)
    |> limit(^limit)
    |> Repo.all()
    |> Enum.reverse()
    |> Enum.map(&message_to_map/1)
  end

  def insert_message(room_id, user_id, content) do
    now = DateTime.utc_now() |> DateTime.truncate(:second)

    %Message{
      room_id: room_id,
      user_id: user_id,
      content: content,
      created_at: now
    }
    |> Repo.insert!()
    |> message_to_map()
  end

  # ─── Serialization helpers ────────────────────────────────────────

  def room_to_map(%Room{} = r) do
    %{
      id: r.id,
      owner_id: r.owner_id,
      name: r.name,
      created_at: iso(r.created_at)
    }
  end

  def message_to_map(%Message{} = m) do
    %{
      id: m.id,
      room_id: m.room_id,
      user_id: m.user_id,
      content: m.content,
      created_at: iso(m.created_at)
    }
  end

  defp iso(%DateTime{} = dt), do: DateTime.to_iso8601(dt)
  defp iso(%NaiveDateTime{} = dt), do: NaiveDateTime.to_iso8601(dt) <> "Z"
  defp iso(other) when is_binary(other), do: other
  defp iso(_), do: nil
end
