package com.faceclaw.app;

/** Immutable page snapshot supplied by the document renderer. */
public final class NativePage {
    public final String fingerprint;
    public final Text[] texts;
    public final Image[] images;
    public NativePage(String fingerprint, Text[] texts, Image[] images) {
        this.fingerprint = fingerprint; this.texts = texts.clone(); this.images = images.clone();
        if (texts.length < 1 || texts.length > 8 || images.length > 3) throw new IllegalArgumentException("page container count");
    }
    public static final class Text {
        public final int x,y,width,height;
        public final String text;
        public Text(int x,int y,int width,int height,String text) {
            this.x=x;this.y=y;this.width=width;this.height=height;this.text=text;
        }
    }
    public static final class Image {
        public final int x,y,width,height;
        public final byte[] bmp;
        public Image(int x,int y,int width,int height,byte[] bmp) {
            this.x=x;this.y=y;this.width=width;this.height=height;this.bmp=bmp.clone();
        }
    }
}
