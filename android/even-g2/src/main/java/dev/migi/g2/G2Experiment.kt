package dev.migi.g2

import android.bluetooth.BluetoothAdapter
import android.content.Context
import android.os.Handler
import android.os.Looper
import com.faceclaw.app.FaceclawBleCommunicator
import com.faceclaw.app.FaceclawBleCommunicatorListener
import com.faceclaw.app.BleProtocol
import com.faceclaw.app.FrameTimings
import java.util.concurrent.Executors

data class PagerContent(val id: Long, val title: String, val body: String)

/** Shared G2 driver for the local experiment and service-owned pager, never both at once. */
class G2Experiment(
    context: Context,
    private val report: (String) -> Unit,
    private val pagerSource: (() -> PagerContent?)? = null,
) : AutoCloseable {
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
    private var frameWait: Runnable? = null
    private var frameWaitGeneration = 0
    private var deliveredId: Long? = null
    private var page = 0
    private var pageCount = 1
    private var lastPairReady = false
    private val reconcileQueued = java.util.concurrent.atomic.AtomicBoolean(false)

    /** Coalesce events; read current durable state on execution, not an event-time snapshot. */
    fun refreshPager() {
        if (pagerSource == null || closed || !reconcileQueued.compareAndSet(false, true)) return
        execute {
            reconcileQueued.set(false)
            val connection = transport ?: return@execute
            if (!connection.isSessionReady) return@execute
            val current = pagerSource.invoke()
            val id = current?.id ?: 0L
            if (deliveredId == id) return@execute
            page = 0
            if (current == null || current.body.isBlank()) sleepDisplay()
            else showFrame()
            // Track the queued version; the asynchronous watcher clears it on failure.
            // Never attribute an older queued frame to a newer durable message.
            if (pagerSource.invoke()?.id == current?.id) deliveredId = id
            else refreshPager()
        }
    }

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
                if (pagerSource != null) execute {
                    if (transport !== connection) return@execute
                    val ready = connection.isSessionReady
                    if (ready && !lastPairReady) {
                        deliveredId = null
                        sleeping = false
                    }
                    lastPairReady = ready
                    if (ready) refreshPager()
                }
            }
            override fun onRingEvent(kind: String, containerName: String, eventType: Int, eventSource: Int, systemExitReasonCode: Int, frameId: Int) {
                val receivedAt = android.os.SystemClock.elapsedRealtime()
                emit("Событие: $kind, тип=$eventType, источник=$eventSource")
                // Callbacks from a released connection must never affect its replacement.
                execute {
                    try {
                        if (transport !== connection) return@execute
                        val queueMs = android.os.SystemClock.elapsedRealtime() - receivedAt
                        if (queueMs > 2_000) {
                            emit("Пропущен устаревший жест: $kind/$eventType, очередь ${queueMs}ms")
                            return@execute
                        }
                        if (kind == "display-wake") {
                            cancelTimer()
                            lastGesture = "Double tap: wake"
                            lastGestureType = BleProtocol.EVENT_DOUBLE_CLICK
                            lastGestureAt = android.os.SystemClock.elapsedRealtime()
                            if (pagerSource == null || pagerSource.invoke()?.body?.isNotBlank() == true) showFrame()
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
                                val now = receivedAt
                                if (eventType == lastGestureType && now - lastGestureAt < 300) return@execute
                                lastGestureType = eventType
                                lastGestureAt = now
                                gestureCount++
                                lastGesture = name
                                if (pagerSource != null && pagerSource.invoke()?.id != deliveredId) {
                                    refreshPager() // A gesture for an older page must not act on its replacement.
                                } else if (eventType == BleProtocol.EVENT_DOUBLE_CLICK && !sleeping) sleepDisplay()
                                else if (pagerSource == null || pagerSource.invoke()?.body?.isNotBlank() == true) {
                                    if (pagerSource != null && !sleeping && !BuildConfig.WIDGET_PROBE) {
                                        page = when (eventType) {
                                            BleProtocol.EVENT_SCROLL_TOP -> (page - 1).coerceAtLeast(0)
                                            BleProtocol.EVENT_CLICK, BleProtocol.EVENT_SCROLL_BOTTOM -> (page + 1) % pageCount
                                            else -> page
                                        }
                                    }
                                    showFrame()
                                }
                            }
                        }
                    } finally {
                        if (kind == "display-wake" || eventType == BleProtocol.EVENT_DOUBLE_CLICK) {
                            emit("Жест $kind/$eventType завершён за ${android.os.SystemClock.elapsedRealtime() - receivedAt}ms от получения")
                        }
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
            override fun onFirmwareInfo(leftVersion: String, rightVersion: String, capabilities: String) {
                emit("Прошивка L=$leftVersion R=$rightVersion; возможности: $capabilities")
                refreshPager()
            }
        })
        connection.configureNativeTextOutput(BuildConfig.WIDGET_PROBE)
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
        cancelFrameWait()
        check(connection.firmwareCapabilities.split(' ').contains("wakelease")) { "CFW не сообщает wakelease" }
        check(connection.setFaceclawWakeLeaseEnabled(true)) { "Не доставлено управление пробуждением" }
        // Let the native page lifecycle hide the populated LVGL image.
        sleeping = connection.suspendEvenHubSession()
        check(sleeping) { "Не удалось приостановить EvenHub" }
        connection.setG2ScreenOn(false)
        emit("EvenHub приостановлен, BLE сохранён. Двойное касание для пробуждения.")
    }

    private fun showFrame() {
        val content = pagerSource?.invoke()
        if (pagerSource != null && (content == null || content.body.isBlank())) {
            sleepDisplay()
            return
        }
        val connection = checkNotNull(transport) { "Сначала подключите очки" }
        check(connection.isSessionReady) { "Сессия обеих дужек ещё не готова" }
        connection.setG2ScreenOn(true)
        check(connection.resumeEvenHubSession()) { "Не удалось восстановить сессию" }
        sleeping = false
        val frameId = FrameTimings.getInstance().startFrame("migi-test")
        val text = if (BuildConfig.WIDGET_PROBE) {
            "Кириллица: Ёжик. Тест списка.\n$gestureCount: $lastGesture"
        } else if (content != null) {
            val pages = NativePager.pages(content.body)
            pageCount = pages.size
            page = page.coerceIn(0, pageCount - 1)
            "Migi / Пейджер\n${pages[page]}\n${page + 1}/$pageCount"
        } else "Hello from Migi\nEven G2 / test $frameId\nПривет! Кириллица: Ёжик\nGestures: $gestureCount / $lastGesture\nDouble tap: sleep / wake"
        connection.submitNativeText(text, frameId)
        awaitFrameAsync(connection)
    }

    private fun cancelFrameWait() {
        frameWaitGeneration++
        frameWait?.let { main.removeCallbacks(it) }
        frameWait = null
    }

    /** Never park gesture processing behind a multi-second image upload. */
    private fun awaitFrameAsync(connection: FaceclawBleCommunicator) {
        cancelFrameWait()
        val generation = frameWaitGeneration
        val startedAt = android.os.SystemClock.elapsedRealtime()
        val poll = object : Runnable {
            override fun run() = execute {
                if (generation != frameWaitGeneration || transport !== connection || sleeping) return@execute
                connection.renewPendingWakeClaim()
                if (connection.awaitEvenHubSessionReady(0)) {
                    frameWait = null
                    emit("Передача завершена за ${android.os.SystemClock.elapsedRealtime() - startedAt}ms; очередь жестов свободна.")
                } else if (android.os.SystemClock.elapsedRealtime() - startedAt >= 15_000) {
                    frameWait = null
                    deliveredId = null // Retry current durable content, never an old captured message.
                    emit("Ожидание кадра истекло; пейджер повторит актуальное сообщение")
                } else main.postDelayed(this, 100)
            }
        }
        frameWait = poll
        main.post(poll)
    }

    fun disconnect() = execute { release(); emit("Отключено") }

    private fun release() {
        cancelTimer()
        cancelFrameWait()
        sleeping = false
        deliveredId = null
        lastPairReady = false
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
