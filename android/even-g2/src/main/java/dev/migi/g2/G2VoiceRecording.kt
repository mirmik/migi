package dev.migi.g2

import com.faceclaw.app.FaceclawLc3Decoder
import java.io.ByteArrayOutputStream
import java.io.File
import java.nio.ByteBuffer
import java.nio.ByteOrder
import java.util.UUID

/** BLE callbacks only copy bounded packets; decoding and disk writes run off the gesture thread. */
class G2VoiceRecording {
    private val packets = mutableListOf<ByteArray>()
    private var arm: String? = null
    private var accepting = true
    private var counter = -1
    var missingPackets = 0; private set
    @Synchronized fun accept(data: ByteArray, source: String) {
        if (!accepting || data.size != 205 || packets.size >= 1200) return
        if (arm == null) arm = source
        if (source != arm) return
        val next = data[204].toInt() and 255
        if (counter >= 0) {
            val gap = (next - counter) and 255
            if (gap == 0 || gap > 128) return // duplicate or stale packet
            missingPackets += gap - 1
        }
        counter = next
        packets += data.copyOf()
    }
    @Synchronized fun stop() { accepting = false }
    @Synchronized fun packetCount() = packets.size

    fun save(directory: File): File {
        val snapshot = synchronized(this) { check(!accepting); packets.toList() }
        require(snapshot.size >= 4) { "Слишком мало аудио от очков" }
        val decoder = FaceclawLc3Decoder()
        val pcm = ByteArrayOutputStream()
        val samples = ShortArray(800)
        try {
            for (packet in snapshot) {
                val count = decoder.decodePacket(packet, samples)
                for (i in 0 until count) {
                    pcm.write(samples[i].toInt() and 255)
                    pcm.write((samples[i].toInt() shr 8) and 255)
                }
            }
        } finally { decoder.close() }
        require(pcm.size() >= 6400) { "Не удалось декодировать аудио" }
        check(directory.mkdirs() || directory.isDirectory)
        val destination = File(directory, "g2-${UUID.randomUUID()}.wav")
        val pending = File(directory, destination.name + ".part")
        try {
            pending.outputStream().use { output ->
                output.write(wavHeader(pcm.size()))
                pcm.writeTo(output)
                output.fd.sync()
            }
            check(pending.renameTo(destination)) { "Не удалось сохранить запись" }
        } finally { pending.delete() }
        return destination
    }

    companion object {
        fun wavHeader(bytes: Int): ByteArray {
            require(bytes >= 0 && bytes <= 60 * 16000 * 2 && bytes % 2 == 0)
            return ByteBuffer.allocate(44).order(ByteOrder.LITTLE_ENDIAN).apply {
                put("RIFF".toByteArray()); putInt(36 + bytes); put("WAVEfmt ".toByteArray())
                putInt(16); putShort(1); putShort(1); putInt(16000); putInt(32000)
                putShort(2); putShort(16); put("data".toByteArray()); putInt(bytes)
            }.array()
        }
    }
}
