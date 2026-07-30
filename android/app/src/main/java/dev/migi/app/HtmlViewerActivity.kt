package dev.migi.app

import android.app.Activity
import android.content.Context
import android.content.Intent
import android.os.Bundle
import android.view.Gravity
import android.view.ViewGroup
import android.webkit.CookieManager
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.Button
import android.widget.FrameLayout
import android.widget.LinearLayout
import android.widget.TextView
import java.io.ByteArrayInputStream
import java.io.File

class HtmlViewerActivity : Activity() {
    private var webView: WebView? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        val name = intent.getStringExtra(EXTRA_NAME).orEmpty().take(255)
        val temporary = HtmlViewerPolicy.resolveViewerFile(
            cacheDir,
            intent.getStringExtra(EXTRA_PATH),
        )
        val html = runCatching {
            requireNotNull(temporary) { "Invalid viewer file" }
            require(temporary.length() in 1..HtmlViewerPolicy.MAX_HTML_BYTES) {
                "HTML exceeds viewer limit"
            }
            temporary.readText(Charsets.UTF_8)
        }.also {
            temporary?.delete()
        }.getOrElse { error ->
            showError(error.message ?: error.javaClass.simpleName)
            return
        }

        val browser = WebView(this).apply {
            settings.apply {
                javaScriptEnabled = true
                javaScriptCanOpenWindowsAutomatically = false
                domStorageEnabled = false
                allowFileAccess = false
                allowContentAccess = false
                blockNetworkLoads = true
                setSupportMultipleWindows(false)
                setSupportZoom(true)
                builtInZoomControls = true
                displayZoomControls = false
                mediaPlaybackRequiresUserGesture = true
            }
            val cookies = CookieManager.getInstance()
            cookies.setAcceptCookie(false)
            cookies.setAcceptThirdPartyCookies(this, false)
            webViewClient = IsolatedViewerClient()
            loadDataWithBaseURL(
                HtmlViewerPolicy.ORIGIN,
                html,
                "text/html",
                Charsets.UTF_8.name(),
                null,
            )
        }
        webView = browser

        title = name.ifBlank { getString(R.string.files_title) }
        val closeSize = (48 * resources.displayMetrics.density).toInt()
        val closeMargin = (6 * resources.displayMetrics.density).toInt()
        setContentView(FrameLayout(this).apply {
            addView(
                browser,
                FrameLayout.LayoutParams(
                    ViewGroup.LayoutParams.MATCH_PARENT,
                    ViewGroup.LayoutParams.MATCH_PARENT,
                ),
            )
            addView(Button(this@HtmlViewerActivity).apply {
                text = "×"
                textSize = 24f
                minWidth = 0
                minHeight = 0
                setPadding(0, 0, 0, 0)
                alpha = 0.72f
                contentDescription = getString(R.string.close_html_viewer)
                setOnClickListener { finish() }
            }, FrameLayout.LayoutParams(closeSize, closeSize, Gravity.TOP or Gravity.END).apply {
                setMargins(closeMargin, closeMargin, closeMargin, closeMargin)
            })
        })
    }

    override fun onDestroy() {
        webView?.apply {
            stopLoading()
            removeAllViews()
            destroy()
        }
        webView = null
        super.onDestroy()
    }

    private fun showError(message: String) {
        setContentView(LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            val padding = (20 * resources.displayMetrics.density).toInt()
            setPadding(padding, padding, padding, padding)
            addView(TextView(this@HtmlViewerActivity).apply {
                text = getString(R.string.html_viewer_failed, message)
                textSize = 18f
            }, matchWidth())
            addView(Button(this@HtmlViewerActivity).apply {
                setText(R.string.close_html_viewer)
                setOnClickListener { finish() }
            }, matchWidth())
        })
    }

    private fun matchWidth() = LinearLayout.LayoutParams(
        ViewGroup.LayoutParams.MATCH_PARENT,
        ViewGroup.LayoutParams.WRAP_CONTENT,
    )

    private class IsolatedViewerClient : WebViewClient() {
        override fun shouldOverrideUrlLoading(view: WebView?, request: WebResourceRequest?): Boolean = true

        override fun shouldInterceptRequest(
            view: WebView?,
            request: WebResourceRequest?,
        ): WebResourceResponse? {
            if (!HtmlViewerPolicy.blocksSubresource(request?.url?.scheme)) return null
            return WebResourceResponse(
                "text/plain",
                Charsets.UTF_8.name(),
                ByteArrayInputStream(ByteArray(0)),
            )
        }
    }

    companion object {
        private const val EXTRA_NAME = "name"
        private const val EXTRA_PATH = "path"

        fun intent(context: Context, name: String, file: File): Intent =
            Intent(context, HtmlViewerActivity::class.java)
                .putExtra(EXTRA_NAME, name)
                .putExtra(EXTRA_PATH, file.absolutePath)
    }
}

internal object HtmlViewerPolicy {
    const val CACHE_DIRECTORY = "html-viewer"
    const val FILE_PREFIX = "view-"
    const val MAX_HTML_BYTES = 16L * 1024 * 1024
    const val ORIGIN = "https://viewer.invalid/"

    fun resolveViewerFile(cacheDirectory: File, rawPath: String?): File? {
        if (rawPath.isNullOrBlank()) return null
        return runCatching {
            val root = File(cacheDirectory, CACHE_DIRECTORY).canonicalFile
            val candidate = File(rawPath).canonicalFile
            candidate.takeIf {
                it.isFile &&
                    it.parentFile == root &&
                    it.name.startsWith(FILE_PREFIX) &&
                    it.name.endsWith(".html")
            }
        }.getOrNull()
    }

    fun blocksSubresource(scheme: String?): Boolean =
        scheme?.lowercase() !in setOf("about", "blob", "data")
}
