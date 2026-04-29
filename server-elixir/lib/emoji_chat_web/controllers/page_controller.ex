defmodule EmojiChatWeb.PageController do
  use EmojiChatWeb, :controller

  def index(conn, _params) do
    conn
    |> put_resp_header("location", "/static/index.html")
    |> send_resp(302, "")
  end
end
