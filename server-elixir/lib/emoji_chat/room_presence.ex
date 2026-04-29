defmodule EmojiChat.RoomPresence do
  @moduledoc """
  One GenServer per active room.

  Each WebSocket process calls `join/2` (which monitors the caller) to
  add itself, and `leave/1` (or simply exits) to remove itself. On any
  membership change we broadcast `{:room_presence, count, user_id, kind}`
  on `Phoenix.PubSub` (topic `"room:<room_id>"`).

  When the last member leaves we keep the GenServer alive for one hour
  and then shut it down (matches the Go `roomIdleTimeout`).
  """

  use GenServer

  @idle_timeout_ms 60 * 60 * 1000

  # ─── Public API ────────────────────────────────────────────────

  def join(room_id, user_id) do
    pid = ensure_started(room_id)
    GenServer.call(pid, {:join, self(), user_id})
  end

  def leave(room_id) do
    case Registry.lookup(EmojiChat.RoomRegistry, room_id) do
      [{pid, _}] -> GenServer.cast(pid, {:leave, self()})
      [] -> :ok
    end
  end

  defp ensure_started(room_id) do
    case Registry.lookup(EmojiChat.RoomRegistry, room_id) do
      [{pid, _}] ->
        pid

      [] ->
        case DynamicSupervisor.start_child(
               EmojiChat.RoomSup,
               {__MODULE__, room_id}
             ) do
          {:ok, pid} -> pid
          {:error, {:already_started, pid}} -> pid
        end
    end
  end

  # ─── GenServer ─────────────────────────────────────────────────

  def child_spec(room_id) do
    %{
      id: {__MODULE__, room_id},
      start: {__MODULE__, :start_link, [room_id]},
      restart: :temporary
    }
  end

  def start_link(room_id) do
    GenServer.start_link(__MODULE__, room_id, name: via(room_id))
  end

  defp via(room_id), do: {:via, Registry, {EmojiChat.RoomRegistry, room_id}}

  @impl true
  def init(room_id) do
    state = %{room_id: room_id, members: %{}, idle_ref: schedule_idle()}
    {:ok, state}
  end

  @impl true
  def handle_call({:join, pid, user_id}, _from, state) do
    ref = Process.monitor(pid)
    members = Map.put(state.members, pid, {user_id, ref})
    state = %{state | members: members, idle_ref: cancel_idle(state.idle_ref)}
    broadcast(state, user_id, "join")
    {:reply, :ok, state}
  end

  @impl true
  def handle_cast({:leave, pid}, state), do: {:noreply, drop(state, pid)}

  @impl true
  def handle_info({:DOWN, _ref, :process, pid, _}, state),
    do: {:noreply, drop(state, pid)}

  def handle_info(:idle_check, state) do
    if map_size(state.members) == 0 do
      {:stop, :normal, state}
    else
      {:noreply, %{state | idle_ref: nil}}
    end
  end

  def handle_info(_, state), do: {:noreply, state}

  # ─── Helpers ───────────────────────────────────────────────────

  defp drop(state, pid) do
    case Map.pop(state.members, pid) do
      {nil, _} ->
        state

      {{user_id, ref}, rest} ->
        Process.demonitor(ref, [:flush])
        state = %{state | members: rest}
        broadcast(state, user_id, "leave")
        if rest == %{}, do: %{state | idle_ref: schedule_idle()}, else: state
    end
  end

  defp broadcast(state, user_id, kind) do
    Phoenix.PubSub.broadcast(
      EmojiChat.PubSub,
      "room:" <> state.room_id,
      {:room_presence, map_size(state.members), user_id, kind}
    )
  end

  defp schedule_idle, do: Process.send_after(self(), :idle_check, @idle_timeout_ms)
  defp cancel_idle(nil), do: nil

  defp cancel_idle(ref) do
    Process.cancel_timer(ref)
    nil
  end
end
