package dev.migi.documents

import org.json.JSONObject

/** Versioned data, never executable markup. */
data class ReadingBlock(val type: String, val text: String = "", val items: List<String> = emptyList(), val latex: String = "")
data class ReadingDocument(val documentId: String, val title: String, val blocks: List<ReadingBlock>) {
    companion object {
        fun parse(raw: String): ReadingDocument {
            require(raw.toByteArray(Charsets.UTF_8).size <= 32768) { "Документ больше 32 КБ" }
            val json = JSONObject(raw)
            require(json.getInt("schema") == 1) { "Неизвестный формат документа" }
            val id = json.getString("document_id")
            require(id.matches(Regex("[A-Za-z0-9][A-Za-z0-9._-]{0,95}")))
            fun checked(s: String, limit: Int): String {
                require(s.isNotBlank() && s.codePointCount(0, s.length) <= limit)
                require(s.none { it.code < 32 && it != '\n' && it != '\t' })
                return s
            }
            val title = checked(json.getString("title"), 120)
            val input = json.getJSONArray("blocks")
            require(input.length() in 1..100)
            val blocks = (0 until input.length()).map { i ->
                val b = input.getJSONObject(i)
                when (val type = b.getString("type")) {
                    "heading", "paragraph" -> ReadingBlock(type, text = checked(b.getString("text"), if (type == "heading") 120 else 4000))
                    "math" -> ReadingBlock(type, latex = checked(b.getString("latex"), 1000))
                    "list" -> {
                        val items = b.getJSONArray("items")
                        require(items.length() in 1..30)
                        ReadingBlock(type, items = (0 until items.length()).map { checked(items.getString(it), 1000) })
                    }
                    else -> throw IllegalArgumentException("Неизвестный блок: $type")
                }
            }
            return ReadingDocument(id, title, blocks)
        }
    }
}
