defmodule EmojiChat.Application do
  @moduledoc false
  use Application

  @impl true
  def start(_type, _args) do
    children = [
      EmojiChat.Repo,
      {Phoenix.PubSub, name: EmojiChat.PubSub},
      {Registry, keys: :unique, name: EmojiChat.RoomRegistry},
      {DynamicSupervisor, strategy: :one_for_one, name: EmojiChat.RoomSup},
      EmojiChatWeb.Endpoint
    ]

    opts = [strategy: :one_for_one, name: EmojiChat.Supervisor]
    Supervisor.start_link(children, opts)
  end

  @impl true
  def config_change(changed, _new, removed) do
    EmojiChatWeb.Endpoint.config_change(changed, removed)
    :ok
  end
end
