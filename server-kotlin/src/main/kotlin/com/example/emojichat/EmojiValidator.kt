package com.example.emojichat

object EmojiValidator {
    fun isEmojiOnly(s: String): Boolean {
        if (s.isEmpty()) return false
        var i = 0
        while (i < s.length) {
            val cp = s.codePointAt(i)
            if (!isAllowed(cp)) return false
            i += Character.charCount(cp)
        }
        return true
    }

    private fun isAllowed(cp: Int): Boolean {
        // Zero-width joiner (compound emoji like 👨‍👩‍👧)
        if (cp == 0x200D) return true
        // Variation selectors
        if (cp == 0xFE0E || cp == 0xFE0F) return true
        if (cp in 0xFE00..0xFE0F) return true
        // Skin tone modifiers
        if (cp in 0x1F3FB..0x1F3FF) return true
        // Combining enclosing keycap
        if (cp == 0x20E3) return true
        // Tag characters (e.g. flag sequences 🏴󠁧󠁢)
        if (cp in 0xE0020..0xE007F) return true

        if (isEmojiCodePoint(cp)) return true

        // Digits 0-9 and # * (only meaningful as keycap with VS+keycap, but allow standalone too)
        if ((cp in '0'.code..'9'.code) || cp == '#'.code || cp == '*'.code) return true

        return false
    }

    private fun isEmojiCodePoint(cp: Int): Boolean {
        if (cp in 0x1F600..0x1F64F) return true // Emoticons
        if (cp in 0x1F300..0x1F5FF) return true // Misc Symbols & Pictographs
        if (cp in 0x1F680..0x1F6FF) return true // Transport & Map
        if (cp in 0x1F700..0x1F77F) return true // Alchemical
        if (cp in 0x1F780..0x1F7FF) return true // Geometric Shapes Extended
        if (cp in 0x1F800..0x1F8FF) return true // Supplemental Arrows-C
        if (cp in 0x1F900..0x1F9FF) return true // Supplemental Symbols & Pictographs
        if (cp in 0x1FA00..0x1FA6F) return true // Chess Symbols
        if (cp in 0x1FA70..0x1FAFF) return true // Symbols & Pictographs Extended-A
        if (cp in 0x2600..0x26FF) return true   // Misc Symbols
        if (cp in 0x2700..0x27BF) return true   // Dingbats
        if (cp in 0x2300..0x23FF) return true   // Misc Technical
        if (cp in 0x2B50..0x2B55) return true   // Stars & circles
        if (cp in 0x1F1E0..0x1F1FF) return true // Regional Indicator Symbols (flags)
        if (cp == 0x2764 || cp == 0x2763) return true
        if (cp == 0x270D || cp == 0x2744) return true
        if (cp == 0x00A9 || cp == 0x00AE) return true
        if (cp == 0x203C || cp == 0x2049) return true
        if (cp in 0x2100..0x214F) return true   // Letterlike Symbols
        // Symbol, Other category — Java's "OTHER_SYMBOL"
        if (Character.getType(cp) == Character.OTHER_SYMBOL.toInt()) return true
        return false
    }
}
