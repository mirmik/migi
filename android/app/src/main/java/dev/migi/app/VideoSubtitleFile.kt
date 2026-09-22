package dev.migi.app

import java.io.File
import java.io.InputStream

internal object VideoSubtitleFile {
    private const val MAX_BYTES = 4 * 1024 * 1024
    fun mimeForName(name: String): String = when (name.substringAfterLast('.', "").lowercase()) {
        "ass", "ssa" -> "text/x-ssa"
        "srt" -> "application/x-subrip"
        "vtt" -> "text/vtt"
        else -> error("Выберите субтитры ASS, SSA, SRT или VTT")
    }
    fun copy(source: InputStream, destination: File) {
        val temporary = File(destination.path + ".new")
        try {
            var total = 0
            temporary.outputStream().use { output ->
                val buffer = ByteArray(8192)
                while (true) {
                    val count = source.read(buffer)
                    if (count < 0) break
                    total += count
                    require(total <= MAX_BYTES) { "Субтитры превышают 4 МиБ" }
                    output.write(buffer, 0, count)
                }
            }
            require(total > 0) { "Файл субтитров пуст" }
            check(temporary.renameTo(destination)) { "Не удалось сохранить субтитры" }
        } finally {
            temporary.delete()
        }
    }
}
