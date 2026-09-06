package dev.migi.g2

import android.bluetooth.BluetoothAdapter
import android.content.Context
import android.graphics.Bitmap
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.Paint
import android.os.Handler
import android.os.Looper
import com.faceclaw.app.FaceclawBleCommunicator
import com.faceclaw.app.FaceclawBleCommunicatorListener
import com.faceclaw.app.BleProtocol
import com.faceclaw.app.FrameTimings
import com.faceclaw.app.SurfaceCompositor
import java.nio.ByteBuffer
import java.util.concurrent.Executors

/** Foreground-only hardware experiment. All blocking transport calls run off the UI thread. */
class G2Experiment(context: Context, private val report: (String) -> Unit) : AutoCloseable {
    private val context = context.applicationContext
    companion object {
        // Serialize teardown and reconnect even across Activity recreation.
        private val executor = Executors.newSingleThreadExecutor()
    }
    private val main = Handler(Looper.getMainLooper())
    private var transport: FaceclawBleCommunicator? = null // executor-owned
    @Volatile private var closed = false
    private var sleeping = false
    private var gestureCount = 0
    private var lastGesture = "Ready"
    private var lastGestureType = -1
    private var lastGestureAt = 0L
    private var timer: Runnable? = null

    private fun emit(message: String) {
        main.post { if (!closed) report(message) }
    }

    private fun execute(action: () -> Unit) {
        if (closed) return
        executor.execute {
            if (closed) return@execute
            try { action() } catch (e: Exception) { emit("Ошибка: ${e.message}") }
        }
    }

    fun connect(left: String, right: String) = execute {
        require(BluetoothAdapter.checkBluetoothAddress(left) && BluetoothAdapter.checkBluetoothAddress(right)) {
            "Укажите два BLE-адреса в формате AA:BB:CC:DD:EE:FF"
        }
        require(left != right) { "Адреса дужек должны различаться" }
        release()
        val connection = FaceclawBleCommunicator(context, right, left, "")
        transport = connection
        connection.setListener(object : FaceclawBleCommunicatorListener {
            override fun onLog(line: String) = emit(line)
            override fun onStateChange(phase: String, status: String) {
                // Upstream also emits connected after just one arm connects.
                emit("$phase: $status; сессия пары: ${connection.isSessionReady}")
            }
            override fun onRingEvent(kind: String, containerName: String, eventType: Int, eventSource: Int, systemExitReasonCode: Int, frameId: Int) {
                emit("Событие: $kind, тип=$eventType, источник=$eventSource")
                // Callbacks from a released connection must never affect its replacement.
                execute {
                    try {
                        if (transport !== connection) return@execute
                        if (kind == "display-wake") {
                            cancelTimer()
                            lastGesture = "Double tap: wake"
                            showFrame()
                        } else if (kind in listOf("sys-event", "list-click", "text-click")) {
                            val name = when (eventType) {
                                BleProtocol.EVENT_CLICK -> "Tap"
                                BleProtocol.EVENT_SCROLL_TOP -> "Swipe up"
                                BleProtocol.EVENT_SCROLL_BOTTOM -> "Swipe down"
                                BleProtocol.EVENT_DOUBLE_CLICK -> "Double tap"
                                BleProtocol.EVENT_RING_LONG_PRESS -> "Long press"
                                BleProtocol.EVENT_SHORT_THEN_LONG_PRESS -> "Tap + hold"
                                else -> null
                            }
                            if (name != null) {
                                val now = android.os.SystemClock.elapsedRealtime()
                                if (eventType == lastGestureType && now - lastGestureAt < 300) return@execute
                                lastGestureType = eventType
                                lastGestureAt = now
                                gestureCount++
                                lastGesture = name
                                if (eventType == BleProtocol.EVENT_DOUBLE_CLICK && !sleeping) sleepDisplay()
                                else showFrame()
                            }
                        }
                    } finally {
                        FrameTimings.getInstance().finishFrame(frameId, "handled by Migi experiment")
                    }
                }
            }
            override fun onBatteryState(headsetBattery: Int, headsetCharging: Int) = emit("Батарея: $headsetBattery%; зарядка=$headsetCharging")
            override fun onSilentMode(silent: Boolean) = emit("Тихий режим: $silent")
            override fun onWearState(wearing: Boolean) = emit("Очки надеты: $wearing")
            override fun onPhoneLockState(locked: Boolean) = Unit
            override fun onEvenAppConflict(message: String) = emit(message)
            override fun onFrameMetrics(paintMs: Int, transmitMs: Int, tileCount: Int) = Unit
            override fun onFrameFinished(frameId: Int, outcome: String) = emit("Кадр $frameId: $outcome (видимость проверить на очках)")
            override fun onFirmwareInfo(leftVersion: String, rightVersion: String, capabilities: String) =
                emit("Прошивка L=$leftVersion R=$rightVersion; возможности: $capabilities")
        })
        connection.configureCompositorScreen(640, 480)
        connection.configureSurface("migi-test", 0, 0, 640, 480, 0, SurfaceCompositor.TRANSPARENCY_OPAQUE)
        connection.start()
    }

    fun showTest() = execute { cancelTimer(); showFrame() }

    fun sleep() = execute { cancelTimer(); sleepDisplay() }

    fun sleepAndWakeLater() = execute {
        cancelTimer()
        sleepDisplay()
        val expected = transport
        val callback = Runnable {
            execute {
                if (transport === expected && sleeping) {
                    lastGesture = "Timer wake"
                    showFrame()
                }
            }
        }
        timer = callback
        main.postDelayed(callback, 10_000)
        emit("Пробуждение через 10 секунд; оставьте экран Migi открытым")
    }

    private fun cancelTimer() {
        timer?.let { main.removeCallbacks(it) }
        timer = null
    }

    private fun sleepDisplay() {
        val connection = checkNotNull(transport) { "Сначала подключите очки" }
        check(connection.isSessionReady) { "Сессия пары ещё не готова" }
        if (sleeping) return
        check(connection.firmwareCapabilities.split(' ').contains("wakelease")) { "CFW не сообщает wakelease" }
        check(connection.setFaceclawWakeLeaseEnabled(true)) { "Не доставлено управление пробуждением" }
        connection.setScreenBlanked(true)
        check(connection.awaitEvenHubSessionReady(5_000)) { "Гашение кадра не подтверждено; можно повторить пробуждение" }
        sleeping = connection.suspendEvenHubSession()
        check(sleeping) { "Не удалось приостановить EvenHub" }
        connection.setG2ScreenOn(false)
        emit("EvenHub приостановлен, BLE сохранён. Двойное касание для пробуждения.")
    }

    private fun showFrame() {
        val connection = checkNotNull(transport) { "Сначала подключите очки" }
        check(connection.isSessionReady) { "Сессия обеих дужек ещё не готова" }
        connection.setG2ScreenOn(true)
        check(connection.resumeEvenHubSession()) { "Не удалось восстановить сессию" }
        connection.setScreenBlanked(false)
        sleeping = false
        val frameId = FrameTimings.getInstance().startFrame("migi-test")
        val bitmap = Bitmap.createBitmap(640, 480, Bitmap.Config.ARGB_8888)
        val gray = ByteBuffer.allocate(640 * 480)
        try {
            val canvas = Canvas(bitmap)
            canvas.drawColor(Color.BLACK)
            val paint = Paint(Paint.ANTI_ALIAS_FLAG).apply { color = Color.WHITE; textSize = 42f }
            canvas.drawText("Hello from Migi", 70f, 190f, paint)
            paint.textSize = 28f
            canvas.drawText("Even G2 / test $frameId", 70f, 250f, paint)
            canvas.drawText("Gestures: $gestureCount / $lastGesture", 70f, 305f, paint)
            paint.textSize = 22f
            canvas.drawText("Double tap: sleep / wake", 70f, 350f, paint)
            paint.style = Paint.Style.STROKE
            paint.strokeWidth = 2f
            canvas.drawRect(40f, 80f, 600f, 380f, paint)
            val pixels = IntArray(640 * 480)
            bitmap.getPixels(pixels, 0, 640, 0, 0, 640, 480)
            for (pixel in pixels) gray.put(Color.red(pixel).toByte())
            gray.flip()
        } finally { bitmap.recycle() }
        connection.submitSurfaceFrame(gray, "migi-test", 0, 0, 640, 480, "migi-test-$frameId", 0, frameId)
        emit(if (connection.awaitEvenHubSessionReady(15_000))
            "Передача завершена. Проверьте надпись на обоих дисплеях."
        else "Ожидание кадра истекло. Проверьте журнал и очки; готовность не подтверждена.")
    }

    fun disconnect() = execute { release(); emit("Отключено") }

    private fun release() {
        cancelTimer()
        sleeping = false
        val previous = transport
        transport = null
        previous?.setListener(null)
        if (previous != null) {
            try { previous.setFaceclawWakeLeaseEnabled(false) }
            finally { previous.close() }
        }
    }

    override fun close() {
        if (closed) return
        closed = true
        executor.execute {
            try { release() } catch (e: Exception) {
                android.util.Log.e("MigiG2", "Transport cleanup failed", e)
            }
        }
    }
}
