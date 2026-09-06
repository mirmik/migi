package com.faceclaw.app;

import java.nio.ByteBuffer;
import java.nio.ByteOrder;
import java.nio.charset.StandardCharsets;
import java.util.HashMap;
import java.util.Map;

/**
 * Process-wide registry of glyph rasters, fed from the TS side so glyph
 * identity survives to the BLE encoder (see notes/texture-cache-display-list-
 * design.md). A frame's glyph list references entries here by (fontId,
 * encoding); the texture-cache planner uses the raster three ways:
 *
 *  - as the ink mask for the "would this draw land correctly" check against
 *    the composited frame,
 *  - to punch the ink pixels out of the baked delta rect it replaces,
 *  - pre-encoded as the CFW cached-image bytes ([w][h][4bpp RLE]) uploaded
 *    via mode 12 and drawn via mode 14 with a top-color LUT. 1bpp (BDF)
 *    glyphs store ink at 15 so the LUT maps them to exactly the requested
 *    level; AA (TTF) glyphs store true coverage nibbles the LUT scales.
 *
 * Fonts are identified by a stable string key (the embedded font name), NOT a
 * per-JS-context counter: worker threads register independently and must agree
 * on ids. Glyph rasters are immutable once registered; re-registration of the
 * same (fontId, encoding) is ignored.
 *
 * Cell model: a cached glyph image is bbxWidth x cellHeight (the font's line
 * height), with the ink rows at inkTop — so every glyph of a line is drawn at
 * the same y (the line top), which is what lets one mode-14 string carry a
 * whole run. The horizontal bearing (bbxX) is applied phone-side when
 * computing the draw x. Vertical padding is nearly free: it RLE-encodes to a
 * couple of tokens.
 */
public final class GlyphAtlas {
    private GlyphAtlas() {}

    public static final class Glyph {
        /** Cached image width in pixels (the glyph's tight bbox width). */
        public final int width;
        /** Cached image height in pixels (the font's line height). */
        public final int cellHeight;
        /** First row of ink within the cell. */
        public final int inkTop;
        /** Number of ink rows. */
        public final int inkHeight;
        /** Horizontal bearing: draw x = pen x + bbxX. */
        public final int bbxX;
        /** 1bpp ink rows (inkHeight entries), bit (rowBitWidth-1-col) = ink; null for AA glyphs. */
        final int[] rows;
        final int rowBitWidth;
        /** AA coverage, width*inkHeight nibble values 0..15; null for 1bpp glyphs. */
        final byte[] coverage;
        /** CFW cached-image bytes: [w][cellHeight][RLE(w*cellHeight px)]. */
        public final byte[] cachedBytes;

        Glyph(int width, int cellHeight, int inkTop, int inkHeight, int bbxX,
              int[] rows, byte[] coverage) {
            this.width = width;
            this.cellHeight = cellHeight;
            this.inkTop = inkTop;
            this.inkHeight = inkHeight;
            this.bbxX = bbxX;
            this.rows = rows;
            this.coverage = coverage;
            this.rowBitWidth = ((width + 7) >> 3) << 3;
            this.cachedBytes = encodeCachedImage();
        }

        /**
         * The cell pixel's 4bpp source value at (col, row), row cell-relative.
         * 1bpp glyphs are stored at 15 so the draw-time LUT (source*top/15)
         * maps them to exactly the requested top color; AA glyphs carry their
         * true coverage level.
         */
        public int nibbleAt(int col, int row) {
            if (col < 0 || col >= width) return 0;
            int inkRow = row - inkTop;
            if (inkRow < 0 || inkRow >= inkHeight) return 0;
            if (coverage != null) return coverage[inkRow * width + col];
            return ((rows[inkRow] >>> (rowBitWidth - 1 - col)) & 1) != 0 ? 15 : 0;
        }

        /** Whether the cell pixel at (col, row) has any ink (row is cell-relative). */
        public boolean inkAt(int col, int row) {
            return nibbleAt(col, row) != 0;
        }

        /**
         * The firmware cached-image encoding: [width][height][RLE tokens]
         * covering exactly width*cellHeight pixels (no row padding), each
         * pixel's stored color being its nibbleAt value.
         */
        private byte[] encodeCachedImage() {
            int total = width * cellHeight;
            // Worst case one token per pixel (1 byte each) + 2 header bytes.
            byte[] out = new byte[2 + total];
            out[0] = (byte) width;
            out[1] = (byte) cellHeight;
            int o = 2;
            int i = 0;
            while (i < total) {
                int color = nibbleAt(i % width, i / width);
                int j = i + 1;
                while (j < total && nibbleAt(j % width, j / width) == color) j++;
                int run = j - i;
                while (run > 0) {
                    int c = Math.min(run, 0xffff);
                    if (c <= 15) {
                        out[o++] = (byte) ((c << 4) | color);
                    } else if (c <= 255) {
                        // Worst case can exceed the sizing above only via escape
                        // forms, which occur for runs >= 16 (net win); safe.
                        out[o++] = (byte) color;
                        out[o++] = (byte) c;
                    } else {
                        out[o++] = (byte) color;
                        out[o++] = 0;
                        out[o++] = (byte) (c & 0xff);
                        out[o++] = (byte) (c >> 8);
                    }
                    run -= c;
                }
                i = j;
            }
            byte[] trimmed = new byte[o];
            System.arraycopy(out, 0, trimmed, 0, o);
            return trimmed;
        }
    }

    private static final Object lock = new Object();
    private static final Map<String, Integer> fontIds = new HashMap<>();
    private static final Map<Long, Glyph> glyphs = new HashMap<>();
    private static int nextFontId = 1;

    /** Stable id for a font key; assigns one on first use. */
    public static int fontId(String key) {
        synchronized (lock) {
            Integer id = fontIds.get(key);
            if (id == null) {
                id = nextFontId++;
                fontIds.put(key, id);
            }
            return id;
        }
    }

    public static Glyph get(int fontId, int encoding) {
        synchronized (lock) {
            return glyphs.get(key(fontId, encoding));
        }
    }

    private static long key(int fontId, int encoding) {
        return ((long) fontId << 32) | (encoding & 0xffffffffL);
    }

    /**
     * Register a batch of glyph rasters. Little-endian buffer, a sequence of
     * font groups:
     *   [keyLen u8][key utf8][cellHeight u8][count u16]
     * each followed by count glyph records:
     *   [encoding u32][bbxX s8][inkTop u8][width u8][inkHeight u8]
     *   [inkHeight x rows u32]   (bit (ceil(width/8)*8 - 1 - col) = ink)
     * Records for an already-registered (font, encoding) are skipped (glyph
     * rasters are immutable for a given font key). Malformed buffers throw:
     * they indicate a phone-side marshalling bug, never device state.
     */
    public static void register(ByteBuffer buffer) {
        if (buffer == null) return;
        ByteBuffer in = buffer.order(ByteOrder.LITTLE_ENDIAN);
        synchronized (lock) {
            while (in.remaining() > 0) {
                int keyLen = in.get() & 0xff;
                byte[] keyBytes = new byte[keyLen];
                in.get(keyBytes);
                String fontKey = new String(keyBytes, StandardCharsets.UTF_8);
                int cellHeight = in.get() & 0xff;
                int count = in.getShort() & 0xffff;
                Integer idBoxed = fontIds.get(fontKey);
                int id;
                if (idBoxed == null) {
                    id = nextFontId++;
                    fontIds.put(fontKey, id);
                } else {
                    id = idBoxed;
                }
                for (int g = 0; g < count; g++) {
                    int encoding = in.getInt();
                    int bbxX = in.get();
                    int inkTop = in.get() & 0xff;
                    int width = in.get() & 0xff;
                    int inkHeight = in.get() & 0xff;
                    int[] rows = new int[inkHeight];
                    for (int r = 0; r < inkHeight; r++) {
                        rows[r] = in.getInt();
                    }
                    long k = key(id, encoding);
                    if (!glyphs.containsKey(k) && width > 0 && cellHeight > 0
                            && inkTop + inkHeight <= cellHeight) {
                        glyphs.put(k, new Glyph(width, cellHeight, inkTop, inkHeight, bbxX,
                                rows, null));
                    }
                }
            }
        }
    }

    /**
     * Register a batch of antialiased (4bpp) glyph rasters, e.g. TTF renders.
     * Same font-group framing as register(), but each glyph record carries
     * packed coverage instead of 1bpp rows:
     *   [encoding u32][bbxX s8][inkTop u8][width u8][inkHeight u8]
     *   [inkHeight x ceil(width/2) packed bytes, high nibble = left pixel]
     * Nibble values are the final 4bpp source levels (the draw-time LUT
     * scales them by the top color). Already-registered (font, encoding)
     * records are skipped, as in register().
     */
    public static void registerAa(ByteBuffer buffer) {
        if (buffer == null) return;
        ByteBuffer in = buffer.order(ByteOrder.LITTLE_ENDIAN);
        synchronized (lock) {
            while (in.remaining() > 0) {
                int keyLen = in.get() & 0xff;
                byte[] keyBytes = new byte[keyLen];
                in.get(keyBytes);
                String fontKey = new String(keyBytes, StandardCharsets.UTF_8);
                int cellHeight = in.get() & 0xff;
                int count = in.getShort() & 0xffff;
                Integer idBoxed = fontIds.get(fontKey);
                int id;
                if (idBoxed == null) {
                    id = nextFontId++;
                    fontIds.put(fontKey, id);
                } else {
                    id = idBoxed;
                }
                for (int g = 0; g < count; g++) {
                    int encoding = in.getInt();
                    int bbxX = in.get();
                    int inkTop = in.get() & 0xff;
                    int width = in.get() & 0xff;
                    int inkHeight = in.get() & 0xff;
                    int stride = (width + 1) >> 1;
                    byte[] coverage = new byte[width * inkHeight];
                    for (int r = 0; r < inkHeight; r++) {
                        for (int b = 0; b < stride; b++) {
                            int packed = in.get() & 0xff;
                            int col = b * 2;
                            coverage[r * width + col] = (byte) (packed >> 4);
                            if (col + 1 < width) {
                                coverage[r * width + col + 1] = (byte) (packed & 0x0f);
                            }
                        }
                    }
                    long k = key(id, encoding);
                    if (!glyphs.containsKey(k) && width > 0 && cellHeight > 0
                            && inkTop + inkHeight <= cellHeight) {
                        glyphs.put(k, new Glyph(width, cellHeight, inkTop, inkHeight, bbxX,
                                null, coverage));
                    }
                }
            }
        }
    }
}
