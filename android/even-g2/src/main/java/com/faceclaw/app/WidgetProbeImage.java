package com.faceclaw.app;

/** Deterministic 4-bit diagnostic image: sun, two mountains, frame and gray steps. */
public final class WidgetProbeImage {
    private WidgetProbeImage() {}

    public static byte[] bmp() {
        byte[] packed = new byte[64 * 64 / 2];
        for (int y = 0; y < 64; y++) {
            for (int x = 0; x < 64; x++) {
                int gray = 0;
                if (y >= 28 + Math.abs(x - 22) && y < 50) gray = 7;
                if (y >= 24 + Math.abs(x - 43) && y < 50) gray = 12;
                if ((x - 17) * (x - 17) + (y - 17) * (y - 17) <= 49) gray = 15;
                if (y >= 54 && y <= 59 && x >= 5 && x < 59) gray = 3 + ((x - 5) / 14) * 4;
                if (x == 1 || x == 62 || y == 1 || y == 62) gray = 15;
                int index = (y * 64 + x) / 2;
                packed[index] |= (byte) (gray << ((x & 1) == 0 ? 4 : 0));
            }
        }
        return BmpUtil.build4bppBmpFromPacked(packed, 64, 64);
    }
}
