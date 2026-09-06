package dev.migi.app

import com.faceclaw.app.BmpUtil
import org.junit.Assert.assertArrayEquals
import org.junit.Test

class StockRleTest {
    @Test fun encodesBytePairsIncludingZeroAndUnsignedValues() {
        assertArrayEquals(byteArrayOf(3, 0, 2, -1, 1, 66), BmpUtil.stockRle(byteArrayOf(0, 0, 0, -1, -1, 66)))
        assertArrayEquals(byteArrayOf(), BmpUtil.stockRle(byteArrayOf()))
    }

    @Test fun splitsRunsAt255AndRoundTripsAnEntireBmp() {
        assertArrayEquals(byteArrayOf(-1, 7, 1, 7), BmpUtil.stockRle(ByteArray(256) { 7 }))
        val pixels = ByteArray(576 * 288 / 2) { if (it % 97 < 5) 0x7f else 0 }
        val bmp = BmpUtil.build4bppBmpFromPacked(pixels, 576, 288)
        val encoded = BmpUtil.stockRle(bmp)
        val restored = java.io.ByteArrayOutputStream()
        for (i in encoded.indices step 2) repeat(encoded[i].toInt() and 255) { restored.write(encoded[i + 1].toInt() and 255) }
        assertArrayEquals(bmp, restored.toByteArray())
    }
}
