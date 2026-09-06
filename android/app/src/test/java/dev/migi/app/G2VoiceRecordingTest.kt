package dev.migi.app

import dev.migi.g2.G2VoiceRecording
import java.nio.ByteBuffer
import java.nio.ByteOrder
import org.junit.Assert.*
import org.junit.Test

class G2VoiceRecordingTest {
    private fun packet(counter: Int) = ByteArray(205).apply { this[204] = counter.toByte() }
    @Test fun filtersDuplicatesOtherArmAndPacketsAfterStop() {
        val recording = G2VoiceRecording()
        recording.accept(byteArrayOf(1), "R")
        recording.accept(packet(254), "L")
        recording.accept(packet(254), "L")
        recording.accept(packet(255), "R")
        recording.accept(packet(0), "L")
        recording.accept(packet(255), "L")
        recording.stop()
        recording.accept(packet(1), "L")
        assertEquals(2, recording.packetCount())
        assertEquals(1, recording.missingPackets)
    }
    @Test fun memoryIsBoundedToOneMinute() {
        val recording = G2VoiceRecording()
        repeat(5000) { recording.accept(packet(it), "L") }
        assertEquals(1200, recording.packetCount())
    }
    @Test fun wavHeaderDescribes16kMonoPcmAndExactFileSize() {
        val header = G2VoiceRecording.wavHeader(32000)
        val bytes = ByteBuffer.wrap(header).order(ByteOrder.LITTLE_ENDIAN)
        assertEquals("RIFF", String(header, 0, 4))
        assertEquals(32036, bytes.getInt(4))
        assertEquals(1, bytes.getShort(22).toInt())
        assertEquals(16000, bytes.getInt(24))
        assertEquals(16, bytes.getShort(34).toInt())
        assertEquals(32000, bytes.getInt(40))
    }
}
