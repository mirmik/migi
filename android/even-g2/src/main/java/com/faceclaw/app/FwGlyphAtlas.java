package com.faceclaw.app;

import java.nio.ByteBuffer;
import java.nio.ByteOrder;
import java.util.HashMap;
import java.util.Map;

/**
 * Process-wide registry of the glasses' builtin 20 px font's glyph rasters,
 * fed from the TS side (EvenHubFont's extracted firmware font data). Unlike
 * GlyphAtlas/ImageAtlas entries, these are never uploaded: the firmware
 * already owns the font, and mode 15 draws it directly. The planner needs
 * the rasters purely phone-side — for the "would this draw land correctly"
 * check against the composite and for punching drawn ink out of delta rects.
 *
 * Placement model matches the firmware's mode-15 math: a glyph draws at
 * (penX + ofsX, lineY + inkTop) where inkTop already folds in the root
 * font's line height and baseline. The TS side only registers glyphs whose
 * on-glasses rendering it predicts exactly (native-table glyphs; baseline-
 * shifted fallback symbols stay baked).
 */
public final class FwGlyphAtlas {
    private FwGlyphAtlas() {}

    public static final class Entry {
        /** Draw x = pen x + ofsX. */
        public final int ofsX;
        /** First raster row relative to the run's line-top y. */
        public final int inkTop;
        public final int boxW;
        public final int boxH;
        /** 4bpp rows, stride ceil(boxW/2), high nibble = left pixel. */
        final byte[] nibbles;

        Entry(int ofsX, int inkTop, int boxW, int boxH, byte[] nibbles) {
            this.ofsX = ofsX;
            this.inkTop = inkTop;
            this.boxW = boxW;
            this.boxH = boxH;
            this.nibbles = nibbles;
        }

        /** The 4bpp source value at (col, row); the draw skips 0 (transparent). */
        public int nibbleAt(int col, int row) {
            int b = nibbles[row * ((boxW + 1) >> 1) + (col >> 1)] & 0xff;
            return (col & 1) != 0 ? (b & 0x0f) : (b >> 4);
        }
    }

    private static final Object lock = new Object();
    private static final Map<Integer, Entry> entries = new HashMap<>();

    /**
     * Register glyph rasters. Little-endian buffer of records:
     *   [cp u32][ofsX s16][inkTop s16][boxW u8][boxH u8]
     *   [boxH x ceil(boxW/2) nibble bytes]
     * Re-registration of a codepoint is ignored (the builtin font is fixed).
     */
    public static void register(ByteBuffer buffer) {
        if (buffer == null) return;
        ByteBuffer in = buffer.order(ByteOrder.LITTLE_ENDIAN);
        synchronized (lock) {
            while (in.remaining() >= 10) {
                int cp = in.getInt();
                int ofsX = in.getShort();
                int inkTop = in.getShort();
                int boxW = in.get() & 0xff;
                int boxH = in.get() & 0xff;
                byte[] nibbles = new byte[((boxW + 1) >> 1) * boxH];
                in.get(nibbles);
                if (boxW > 0 && boxH > 0 && !entries.containsKey(cp)) {
                    entries.put(cp, new Entry(ofsX, inkTop, boxW, boxH, nibbles));
                }
            }
        }
    }

    public static Entry get(int cp) {
        synchronized (lock) {
            return entries.get(cp);
        }
    }
}
