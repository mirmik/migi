package dev.migi.app

import org.junit.Assert.*
import org.junit.Test

class VideoChunkCacheTest {
    @Test fun seekingKeepsRecentlyReadBlocksAndEvictsOldOnes() {
        val cache = VideoChunkCache(2)
        cache.put(0, byteArrayOf(1)); cache.put(2, byteArrayOf(2))
        assertArrayEquals(byteArrayOf(1), cache.get(0))
        cache.put(4, byteArrayOf(3))
        assertNull(cache.get(2))
        assertArrayEquals(byteArrayOf(1), cache.get(0))
        assertArrayEquals(byteArrayOf(3), cache.get(4))
    }
    @Test(expected = IllegalArgumentException::class) fun oversizedBlocksCannotExceedMemoryBound() {
        VideoChunkCache().put(0, ByteArray(VideoChunkCache.CHUNK_SIZE + 1))
    }
}
