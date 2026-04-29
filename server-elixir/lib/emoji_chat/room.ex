defmodule EmojiChat.Room do
  use Ecto.Schema

  @primary_key {:id, :string, autogenerate: false}
  schema "rooms" do
    field :owner_id, :string
    field :name, :string, default: ""
    field :created_at, :utc_datetime
  end
end
