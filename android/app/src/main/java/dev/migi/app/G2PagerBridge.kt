package dev.migi.app

import android.Manifest
import android.content.Context
import android.content.SharedPreferences
import android.content.pm.PackageManager
import android.os.Handler
import android.os.Looper
import android.util.Log
import dev.migi.g2.G2Experiment

/** Service lifetime, no UI references and no blocking BLE calls on the event thread. */
class G2PagerBridge(private val context: Context, private val setDeviceType: (Boolean) -> Unit) : AutoCloseable {
    private val config = context.getSharedPreferences(CONFIG, Context.MODE_PRIVATE)
    private val pagerPrefs = context.getSharedPreferences(MainActivity.PREFERENCES, Context.MODE_PRIVATE)
    private val repository = PagerRepository(context)
    private val documents = DocumentRepository(context)
    private val handler = Handler(Looper.getMainLooper())
    private var driver: G2Experiment? = null
    private var addresses: Pair<String, String>? = null
    private val configListener = SharedPreferences.OnSharedPreferenceChangeListener { _, key ->
        if (key in setOf(ENABLED, "left", "right")) configure()
        if (key in setOf(dev.migi.g2.G2DisplaySettings.BRIGHTNESS_MODE, dev.migi.g2.G2DisplaySettings.BRIGHTNESS_LEVEL)) driver?.refreshDisplaySettings()
    }
    private val pagerListener = SharedPreferences.OnSharedPreferenceChangeListener { _, key ->
        if (key == PagerRepository.KEY_STATE) driver?.refreshPager()
        if (key == DocumentRepository.KEY_GENERATION) driver?.refreshPager(true)
    }
    private val retry = object : Runnable {
        override fun run() {
            driver?.refreshPager()
            handler.postDelayed(this, 5_000)
        }
    }

    init {
        config.registerOnSharedPreferenceChangeListener(configListener)
        pagerPrefs.registerOnSharedPreferenceChangeListener(pagerListener)
        configure()
        handler.post(retry)
    }

    fun configure() {
        try {
            val enabled = config.getBoolean(ENABLED, false)
            val granted = context.checkSelfPermission(Manifest.permission.BLUETOOTH_CONNECT) == PackageManager.PERMISSION_GRANTED
            val pair = Pair(config.getString("left", "").orEmpty(), config.getString("right", "").orEmpty())
            if (!enabled || !granted) {
                release()
                return
            }
            if (driver != null && pair == addresses) return
            release()
            setDeviceType(true)
            driver = G2Experiment(context, { Log.i("MigiG2Pager", it) },
                { documents.active() ?: repository.current() }, documents::savePage)
            addresses = pair
            driver?.connect(pair.first, pair.second)
        } catch (e: Exception) {
            Log.e("MigiG2Pager", "Cannot start glasses output", e)
            release()
        }
    }

    private fun release() {
        driver?.close()
        driver = null
        addresses = null
        setDeviceType(false)
    }

    override fun close() {
        config.unregisterOnSharedPreferenceChangeListener(configListener)
        pagerPrefs.unregisterOnSharedPreferenceChangeListener(pagerListener)
        handler.removeCallbacks(retry)
        driver?.close()
        driver = null
    }

    companion object {
        const val CONFIG = "g2-experiment"
        const val ENABLED = "pager_enabled"
        fun enabled(context: Context) = context.getSharedPreferences(CONFIG, Context.MODE_PRIVATE).getBoolean(ENABLED, false)
    }
}
