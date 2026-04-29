defmodule EmojiChat.EmojiValidator do
  @moduledoc """
  Server-side check that a string is composed only of emoji code points
  (and the modifiers / joiners that compose them). Mirrors the Go
  implementation in server-go/main.go.
  """

  @doc "Returns true iff every code point in `s` belongs to an allowed emoji range."
  def emoji_only?(""), do: false

  def emoji_only?(s) when is_binary(s) do
    s
    |> String.to_charlist()
    |> Enum.all?(&allowed?/1)
  end

  def emoji_only?(_), do: false

  # ZWJ
  defp allowed?(0x200D), do: true
  # Variation selectors
  defp allowed?(cp) when cp in 0xFE00..0xFE0F, do: true
  # Skin tone modifiers
  defp allowed?(cp) when cp in 0x1F3FB..0x1F3FF, do: true
  # Combining enclosing keycap
  defp allowed?(0x20E3), do: true
  # Tag characters (flag sequences like 🏴󠁧󠁢)
  defp allowed?(cp) when cp in 0xE0020..0xE007F, do: true
  # Digits, # and *
  defp allowed?(cp) when cp in ?0..?9, do: true
  defp allowed?(?#), do: true
  defp allowed?(?*), do: true

  # Common emoji blocks
  defp allowed?(cp) when cp in 0x1F600..0x1F64F, do: true
  defp allowed?(cp) when cp in 0x1F300..0x1F5FF, do: true
  defp allowed?(cp) when cp in 0x1F680..0x1F6FF, do: true
  defp allowed?(cp) when cp in 0x1F700..0x1F77F, do: true
  defp allowed?(cp) when cp in 0x1F780..0x1F7FF, do: true
  defp allowed?(cp) when cp in 0x1F800..0x1F8FF, do: true
  defp allowed?(cp) when cp in 0x1F900..0x1F9FF, do: true
  defp allowed?(cp) when cp in 0x1FA00..0x1FA6F, do: true
  defp allowed?(cp) when cp in 0x1FA70..0x1FAFF, do: true
  defp allowed?(cp) when cp in 0x2600..0x26FF, do: true
  defp allowed?(cp) when cp in 0x2700..0x27BF, do: true
  defp allowed?(cp) when cp in 0x2300..0x23FF, do: true
  defp allowed?(cp) when cp in 0x2B50..0x2B55, do: true
  defp allowed?(cp) when cp in 0x1F1E0..0x1F1FF, do: true
  defp allowed?(cp) when cp in 0x2100..0x214F, do: true
  defp allowed?(0x2764), do: true
  defp allowed?(0x2763), do: true
  defp allowed?(0x270D), do: true
  defp allowed?(0x2744), do: true
  defp allowed?(0x00A9), do: true
  defp allowed?(0x00AE), do: true
  defp allowed?(0x203C), do: true
  defp allowed?(0x2049), do: true

  defp allowed?(_), do: false
end
