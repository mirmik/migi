package com.faceclaw.app;

/** Deterministic 4-bit diagnostic image: sun, two mountains, frame and gray steps. */
public final class WidgetProbeImage {
    private WidgetProbeImage() {}

    public static byte[] bmp() {
        if (dev.migi.g2.BuildConfig.DOCUMENT_PROBE) return formulaBmp();
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
    /** Layout proof only, not a general LaTeX renderer. */
    private static byte[] formulaBmp() {
        android.graphics.Bitmap bitmap = android.graphics.Bitmap.createBitmap(240, 80,
            android.graphics.Bitmap.Config.ARGB_8888);
        try {
            android.graphics.Canvas canvas = new android.graphics.Canvas(bitmap);
            canvas.drawColor(android.graphics.Color.BLACK);
            android.graphics.Paint paint = new android.graphics.Paint(android.graphics.Paint.ANTI_ALIAS_FLAG);
            paint.setColor(android.graphics.Color.WHITE);
            paint.setTypeface(android.graphics.Typeface.create("serif", android.graphics.Typeface.ITALIC));
            paint.setTextSize(38);
            canvas.drawText("E", 27, 52, paint);
            paint.setTextSize(21);
            canvas.drawText("k", 51, 61, paint);
            paint.setTextSize(34);
            canvas.drawText("=", 77, 52, paint);
            canvas.drawText("mv", 126, 34, paint);
            paint.setTextSize(20);
            canvas.drawText("2", 174, 19, paint);
            paint.setStrokeWidth(2);
            canvas.drawLine(119, 42, 192, 42, paint);
            paint.setTextSize(30);
            canvas.drawText("2", 146, 73, paint);
            int[] pixels = new int[240 * 80];
            bitmap.getPixels(pixels, 0, 240, 0, 0, 240, 80);
            byte[] packed = new byte[pixels.length / 2];
            for (int i = 0; i < pixels.length; i++) {
                int gray = android.graphics.Color.red(pixels[i]) >> 4;
                packed[i / 2] |= (byte) (gray << ((i & 1) == 0 ? 4 : 0));
            }
            return BmpUtil.build4bppBmpFromPacked(packed, 240, 80);
        } finally { bitmap.recycle(); }
    }

}
