defmodule EmojiChat.Message do
  use Ecto.Schema

  schema "messages" do
    field :room_id, :string
    field :user_id, :string
    field :content, :string
    field :created_at, :utc_datetime
  end
end
