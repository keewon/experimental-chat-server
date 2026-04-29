defmodule EmojiChatWeb.Router do
  use EmojiChatWeb, :router

  pipeline :api do
    plug :accepts, ["json"]
  end

  scope "/api", EmojiChatWeb do
    pipe_through :api

    post "/rooms", RoomController, :create
    get "/rooms/:id", RoomController, :show
    delete "/rooms/:id", RoomController, :delete
    get "/rooms/:id/messages", MessageController, :index
  end

  scope "/", EmojiChatWeb do
    get "/ws/:room_id", WSController, :upgrade
    get "/", PageController, :index
  end
end
