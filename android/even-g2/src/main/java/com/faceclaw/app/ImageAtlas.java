package com.faceclaw.app;

import java.nio.ByteBuffer;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;

/**
 * Process-wide registry of icon/image rasters for the texture-cache pipeline,
 * the image counterpart of GlyphAtlas. Entries are content-addressed: the TS
 * side keys each image by a hash of its dimensions and pixels, so the same
 * icon registered from any thread or rendered at any time dedupes to one
 * entry, and an icon whose content changes is simply a new entry.
 *
 * Unlike glyphs (1-bit ink recolored at draw time), images keep their exact
 * 4bpp values: the cached bytes quantize the registered 8bpp pixels with the
 * same GRAY_TO_NIBBLE table the composite is packed with, and the planner
 * draws them via mode 13 with an identity LUT (top color 15) and the
 * transparent bit — the draw writes exactly the nonzero-nibble pixels, which
 * is what the eligibility check and hole punching are defined over.
 */
public final class ImageAtlas {
    private ImageAtlas() {}

    public static final class Entry {
        public final int width;
        public final int height;
        /** One 4bpp value per pixel, row-major. */
        final byte[] nibbles;
        /** CFW cached-image bytes: [w][h][RLE(w*h pixels)]. */
        public final byte[] cachedBytes;

        Entry(int width, int height, byte[] nibbles) {
            this.width = width;
            this.height = height;
            this.nibbles = nibbles;
            this.cachedBytes = encodeCachedImage();
        }

        /** The 4bpp value the mode-13 draw would write at (col, row); 0 = skipped. */
        public int nibbleAt(int col, int row) {
            return nibbles[row * width + col] & 0xff;
        }

        private byte[] encodeCachedImage() {
            int total = width * height;
            byte[] out = new byte[2 + total];
            out[0] = (byte) width;
            out[1] = (byte) height;
            int o = 2;
            int i = 0;
            while (i < total) {
                int color = nibbles[i] & 0xff;
                int j = i + 1;
                while (j < total && (nibbles[j] & 0xff) == color) j++;
                int run = j - i;
                while (run > 0) {
                    int c = Math.min(run, 0xffff);
                    if (c <= 15) {
                        out[o++] = (byte) ((c << 4) | color);
                    } else if (c <= 255) {
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
    private static final Map<String, Integer> ids = new HashMap<>();
    private static final List<Entry> entries = new ArrayList<>();

    /**
     * Register an image (8bpp grayscale pixels, width*height bytes, row-major)
     * under a content key, returning its id — or the existing id when the key
     * is already registered (the pixels are then ignored: same key means same
     * content). Ids are positive and stable for the process lifetime.
     */
    public static int ensure(String key, int width, int height, ByteBuffer pixels8bpp) {
        if (key == null || width <= 0 || width > 255 || height <= 0 || height > 255
                || pixels8bpp == null || pixels8bpp.remaining() < width * height) {
            throw new IllegalArgumentException("bad image registration " + width + "x" + height);
        }
        synchronized (lock) {
            Integer existing = ids.get(key);
            if (existing != null) return existing;
            byte[] nibbles = new byte[width * height];
            for (int i = 0; i < nibbles.length; i++) {
                nibbles[i] = (byte) BmpUtil.nibbleForGray(pixels8bpp.get(i) & 0xff);
            }
            entries.add(new Entry(width, height, nibbles));
            int id = entries.size(); // ids start at 1
            ids.put(key, id);
            return id;
        }
    }

    public static Entry get(int id) {
        synchronized (lock) {
            return id >= 1 && id <= entries.size() ? entries.get(id - 1) : null;
        }
    }
}
