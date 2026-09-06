package dev.migi.app

import com.faceclaw.app.BleProtocol as P
import com.faceclaw.app.NativePage
import org.junit.Assert.*
import org.junit.Test

class NativeDocumentPageTest {
    @Test fun createUsesStartupEnvelopeAndAccurateContainerCount() {
        val page = NativePage("note:1", arrayOf(
            NativePage.Text(16,4,544,30,"Heading"),
            NativePage.Text(16,39,544,100,"Body"),
            NativePage.Text(16,258,544,30,"1/2")),
            arrayOf(NativePage.Image(100,140,64,64,byteArrayOf(1))))
        val wire = P.buildNativePage(101, page, P.ImageTileOptions("img00",10,0,0,576,288),false)
        assertEquals(0, P.readVarintFieldValue(wire,1,-1))
        assertEquals(101, P.readVarintFieldValue(wire,2,-1))
        val startup = P.readFieldBytes(wire,3)
        assertNotNull(startup)
        assertEquals(5, P.readVarintFieldValue(startup,1,-1))
        assertEquals(10000, P.readVarintFieldValue(startup,5,-1))
        assertNull(P.readFieldBytes(wire,7))
    }
    @Test fun rejectsImpossibleContainerCounts() {
        assertThrows(IllegalArgumentException::class.java) { NativePage("bad",emptyArray(),emptyArray()) }
        assertThrows(IllegalArgumentException::class.java) {
            NativePage("bad", Array(9) { NativePage.Text(0,0,10,10,"x") },emptyArray())
        }
    }
}
