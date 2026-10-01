package dev.migi.app

import android.content.Context
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.graphics.Rect
import android.os.Handler
import android.os.Looper
import android.util.LruCache
import android.view.View
import android.view.ViewTreeObserver
import android.widget.ImageView
import java.io.File
import java.util.concurrent.Executors

/** Loads only visible cards, with at most two requests and a bounded bitmap cache. */
internal class FileThumbnailLoader(context: Context, private val root: View) : AutoCloseable {
    private val app = context.applicationContext
    private val handler = Handler(Looper.getMainLooper())
    private val executor = Executors.newFixedThreadPool(2)
    private val cache = object : LruCache<String, Bitmap>(8 * 1024 * 1024) {
        override fun sizeOf(key: String, value: Bitmap): Int = value.allocationByteCount
    }
    private data class Target(val view: ImageView, val file: SharedFile, var loaded: Boolean = false)
    private val targets = mutableListOf<Target>()
    private val failed = mutableSetOf<String>()
    private val pending = mutableSetOf<String>()
    private var active = 0
    private var closed = false
    private val listener = ViewTreeObserver.OnPreDrawListener { scan(); true }

    init { root.viewTreeObserver.addOnPreDrawListener(listener) }

    fun clear() {
        targets.clear()
        failed.clear()
    }

    fun bind(view: ImageView, file: SharedFile) {
        view.setImageResource(R.drawable.ic_nav_files)
        targets.add(Target(view, file))
    }

    private fun key(file: SharedFile) = file.id + ":" + file.sha256

    private fun scan() {
        if (closed) return
        for (target in targets) {
            val visible = target.view.isShown && target.view.getGlobalVisibleRect(Rect())
            if (!visible) {
                if (target.loaded) {
                    target.view.setImageResource(R.drawable.ic_nav_files)
                    target.loaded = false
                }
                continue
            }
            val key = key(target.file)
            val bitmap = cache.get(key)
            if (bitmap != null) {
                if (!target.loaded) { target.view.setImageBitmap(bitmap); target.loaded = true }
            } else if (active < 2 && key !in pending && key !in failed) {
                active++
                pending.add(key)
                val file = target.file
                executor.execute {
                    val result = runCatching { load(file) }
                    handler.post {
                        active--
                        pending.remove(key)
                        if (!closed) {
                            result.onSuccess { cache.put(key, it) }.onFailure { failed.add(key) }
                            scan()
                        }
                    }
                }
            }
        }
    }

    private fun load(file: SharedFile): Bitmap {
        require(file.id.matches(Regex("[a-f0-9]{32}")) && file.sha256.matches(Regex("[a-fA-F0-9]{64}")))
        val directory = File(app.cacheDir, "file-thumbnails").apply {
            check(mkdirs() || isDirectory)
        }
        val cached = File(directory, "${file.id}-${file.sha256}.jpg")
        fun decode(path: File): Bitmap {
            val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
            BitmapFactory.decodeFile(path.path, bounds)
            require(bounds.outWidth in 1..320 && bounds.outHeight in 1..320) { "Invalid thumbnail dimensions" }
            return requireNotNull(BitmapFactory.decodeFile(path.path)) { "Invalid thumbnail" }
        }
        if (cached.isFile) {
            val bitmap = runCatching { decode(cached) }.getOrNull()
            if (bitmap != null) { cached.setLastModified(System.currentTimeMillis()); return bitmap }
            cached.delete()
        }
        val temporary = File.createTempFile("thumb-", ".tmp", directory)
        try {
            FileExchangeClient(app).downloadThumbnail(file, temporary)
            val bitmap = decode(temporary)
            check(temporary.renameTo(cached)) { "Cannot cache thumbnail" }
            // Only compact JPEGs live here; interrupted requests use .tmp files.
            directory.listFiles()?.filter { it.extension == "jpg" }
                ?.sortedByDescending { it.lastModified() }
                ?.drop(128)?.forEach(File::delete)
            return bitmap
        } finally { temporary.delete() }
    }

    override fun close() {
        closed = true
        root.viewTreeObserver.takeIf { it.isAlive }?.removeOnPreDrawListener(listener)
        targets.clear()
        cache.evictAll()
        executor.shutdownNow()
    }
}
