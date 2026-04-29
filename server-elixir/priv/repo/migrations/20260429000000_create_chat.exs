defmodule EmojiChat.Repo.Migrations.CreateChat do
  use Ecto.Migration

  def change do
    create table(:rooms, primary_key: false) do
      add :id, :string, size: 36, primary_key: true
      add :owner_id, :string, size: 36, null: false
      add :name, :string, default: ""
      add :created_at, :utc_datetime, null: false, default: fragment("CURRENT_TIMESTAMP")
    end

    create table(:messages) do
      add :room_id, :string, size: 36, null: false
      add :user_id, :string, size: 36, null: false
      add :content, :string, size: 512, null: false
      add :created_at, :utc_datetime, null: false, default: fragment("CURRENT_TIMESTAMP")
    end

    create index(:messages, [:room_id, :created_at])
  end
end
