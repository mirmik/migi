package dev.migi.g2

import android.bluetooth.BluetoothAdapter
import android.content.Context
import android.os.Handler
import android.os.Looper
import com.faceclaw.app.FaceclawBleCommunicator
import com.faceclaw.app.FaceclawBleCommunicatorListener
import com.faceclaw.app.BleProtocol
import com.faceclaw.app.FrameTimings
import com.faceclaw.app.NativePage
import dev.migi.documents.ReadingDocument
import dev.migi.documents.DocumentRenderer
import java.util.concurrent.Executors

data class PagerContent(val id: Long, val title: String, val body: String, val document: ReadingDocument? = null, val savedPage: Int = 0)

/** Shared G2 driver for the local experiment and service-owned pager, never both at once. */
class G2Experiment(
    context: Context,
    private val report: (String) -> Unit,
    private val pagerSource: (() -> PagerContent?)? = null,
    private val onDocumentPage: ((Long, Int) -> Unit)? = null,
    private val onVoiceRecorded: ((java.io.File) -> Unit)? = null,
) : AutoCloseable {
    private val context = context.applicationContext
    companion object {
        // Serialize teardown and reconnect even across Activity recreation.
        private val executor = Executors.newSingleThreadExecutor()
        private val renderExecutor = Executors.newSingleThreadExecutor()
        private val audioExecutor = Executors.newSingleThreadExecutor()
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
    private var preparingDocument = false
    private var preparedDocumentId: Long? = null
    private var preparingDocumentId: Long? = null
    private var documentPages: List<NativePage> = emptyList()
    private var page = 0
    private var pageCount = 1
    private var lastPairReady = false
    private val reconcileQueued = java.util.concurrent.atomic.AtomicBoolean(false)
    private var voice: G2VoiceRecording? = null
    private var voiceBusy = false
    private var voiceTimer: Runnable? = null
    private var voiceGeneration = 0

    fun refreshDisplaySettings() = execute {
        transport?.takeIf { it.isSessionReady }?.let { G2DisplaySettings.applyBrightness(context, it) }
    }

    /** Coalesce events; read current durable state on execution, not an event-time snapshot. */
    fun refreshPager(force: Boolean = false) {
        if (pagerSource == null || closed || !reconcileQueued.compareAndSet(false, true)) return
        execute {
            reconcileQueued.set(false)
            if (voiceBusy) return@execute
            if (force) deliveredId = null
            val connection = transport ?: return@execute
            if (!connection.isSessionReady) return@execute
            val current = pagerSource.invoke()
            val id = current?.id ?: 0L
            if (deliveredId == id) return@execute
            page = current?.savedPage ?: 0
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
                    if (!ready && voice != null) cancelVoice("Запись отменена: соединение потеряно")
                    if (ready && !lastPairReady) {
                        G2DisplaySettings.applyBrightness(context, connection)
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
                        if (kind in listOf("sys-event", "list-click", "text-click")) {
                            if (voiceBusy) {
                                if (eventType == BleProtocol.EVENT_CLICK && voice != null) finishVoice()
                                else if (eventType == BleProtocol.EVENT_DOUBLE_CLICK) cancelVoice("Запись отменена")
                                return@execute
                            }
                            if (eventType == BleProtocol.EVENT_RING_LONG_PRESS) {
                                startVoice(connection)
                                return@execute
                            }
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
                                        val navigationEvent = if (G2DisplaySettings.invertScroll(context)) when (eventType) {
                                            BleProtocol.EVENT_SCROLL_TOP -> BleProtocol.EVENT_SCROLL_BOTTOM
                                            BleProtocol.EVENT_SCROLL_BOTTOM -> BleProtocol.EVENT_SCROLL_TOP
                                            else -> eventType
                                        } else eventType
                                        page = when (navigationEvent) {
                                            BleProtocol.EVENT_SCROLL_TOP -> (page - 1).coerceAtLeast(0)
                                            BleProtocol.EVENT_CLICK, BleProtocol.EVENT_SCROLL_BOTTOM -> if (pagerSource.invoke()?.document != null) (page + 1).coerceAtMost(pageCount - 1) else (page + 1) % pageCount
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

    private fun voiceStatus(text: String) {
        emit(text)
        val connection = transport ?: return
        if (!connection.isSessionReady) return
        sleeping = false
        connection.setG2ScreenOn(true)
        connection.submitNativeText(text, FrameTimings.getInstance().startFrame("voice"))
    }

    private fun startVoice(connection: FaceclawBleCommunicator) {
        if (!connection.isSessionReady) return
        cancelFrameWait()
        voiceBusy = true
        val recording = G2VoiceRecording()
        voice = recording
        val generation = ++voiceGeneration
        val startedAt = android.os.SystemClock.elapsedRealtime()
        try { voiceStatus("Подготовка микрофона…") }
        catch (e: Exception) { cancelVoice("Микрофон: ${e.message}"); return }
        val poll = object : Runnable {
            override fun run() = execute {
                if (voiceGeneration != generation || voice !== recording) return@execute
                if (transport !== connection || !connection.isSessionReady) {
                    cancelVoice("Запись отменена: соединение потеряно"); return@execute
                }
                if (connection.awaitEvenHubSessionReady(0)) {
                    try {
                        check(connection.startG2AudioCapture { data, arm, _ -> recording.accept(data, arm) }) { "Очки не включили микрофон" }
                        voiceStatus("Слушаю…\n\nКасание — закончить\nДвойное — отменить\nМаксимум 60 секунд")
                        watchVoice(connection, recording, generation)
                    } catch (e: Exception) { cancelVoice("Микрофон: ${e.message}") }
                } else if (android.os.SystemClock.elapsedRealtime() - startedAt > 8_000) {
                    cancelVoice("Микрофон: экран не готов")
                } else main.postDelayed(this, 100)
            }
        }
        voiceTimer = poll
        main.post(poll)
    }

    private fun watchVoice(connection: FaceclawBleCommunicator, recording: G2VoiceRecording, generation: Int) {
        val startedAt = android.os.SystemClock.elapsedRealtime()
        val watch = object : Runnable {
            override fun run() = execute {
                if (generation != voiceGeneration || voice !== recording) return@execute
                val elapsed = android.os.SystemClock.elapsedRealtime() - startedAt
                if (!connection.isAudioCaptureActive) cancelVoice("Запись прервана: микрофон отключился")
                else if (elapsed > 5_000 && recording.packetCount() == 0) cancelVoice("Аудио от очков не поступает")
                else if (elapsed >= 60_000) finishVoice()
                else main.postDelayed(this, 1000)
            }
        }
        voiceTimer = watch
        main.postDelayed(watch, 1000)
    }

    private fun cancelVoice(message: String, showStatus: Boolean = true) {
        voiceGeneration++
        voiceTimer?.let(main::removeCallbacks); voiceTimer = null
        voice?.stop(); voice = null
        try { transport?.stopG2AudioCapture() } finally {
            voiceBusy = false
            deliveredId = null
        }
        if (showStatus) {
            try { voiceStatus(message) } catch (e: Exception) { emit(message) }
        } else emit(message)
    }

    private fun finishVoice() {
        val recording = voice ?: return
        recording.stop(); voice = null
        voiceTimer?.let(main::removeCallbacks); voiceTimer = null
        val generation = ++voiceGeneration
        try { transport?.stopG2AudioCapture(); voiceStatus("Сохраняю запись…") }
        catch (e: Exception) { emit("Остановка микрофона: ${e.message}") }
        audioExecutor.execute {
            val result = runCatching {
                val file = recording.save(java.io.File(context.getExternalFilesDir(null) ?: context.filesDir, "voice"))
                onVoiceRecorded?.invoke(file)
                file
            }
            // Keep the recording even if the display connection changes during decoding.
            execute {
                if (generation != voiceGeneration) return@execute
                voiceBusy = false
                result.onSuccess {
                    voiceStatus(if (onVoiceRecorded != null) "Запись в очереди отправки\nЖду ответ модели…" else "Запись сохранена локально")
                    emit("G2 voice WAV=${it.name}, bytes=${it.length()}, lost=${recording.missingPackets}")
                }.onFailure { voiceStatus("Не удалось отправить запись\n${it.message?.take(100)}") }
            }
        }
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
        if (voiceBusy) return
        val content = pagerSource?.invoke()
        if (pagerSource != null && (content == null || content.body.isBlank())) {
            sleepDisplay()
            return
        }
        val connection = checkNotNull(transport) { "Сначала подключите очки" }
        check(connection.isSessionReady) { "Сессия обеих дужек ещё не готова" }
        connection.setG2ScreenOn(true)
        if (content?.document == null) check(connection.resumeEvenHubSession()) { "Не удалось восстановить сессию" }
        sleeping = false
        if (content?.document != null) {
            if (preparedDocumentId != content.id) {
                if (preparingDocumentId == content.id) return
                preparingDocumentId = content.id
                preparingDocument = true
                awaitFrameAsync(connection)
                renderExecutor.execute {
                    val result = runCatching { DocumentRenderer.render(context, content.document, content.id.toString()) }
                    execute {
                        if (preparingDocumentId != content.id) return@execute
                        preparingDocumentId = null
                        preparingDocument = false
                        if (transport !== connection || pagerSource?.invoke()?.id != content.id) return@execute
                        result.onSuccess {
                            preparedDocumentId = content.id
                            documentPages = it
                            pageCount = it.size
                            if (!sleeping) showFrame()
                        }.onFailure {
                            deliveredId = null
                            emit("Не удалось подготовить документ: ${it.message}")
                        }
                    }
                }
                return
            }
            pageCount = documentPages.size
            page = page.coerceIn(0, pageCount - 1)
            check(connection.replaceNativePage(documentPages[page], FrameTimings.getInstance().startFrame("document"))) { "Не удалось переключить страницу документа" }
            onDocumentPage?.invoke(content.id, page)
            awaitFrameAsync(connection)
            return
        }
        preparingDocumentId = null
        preparingDocument = false
        val frameId = FrameTimings.getInstance().startFrame("migi-test")
        val text = if (BuildConfig.DOCUMENT_PROBE) {
            "Migi / Записка 1 из 1"
        } else if (BuildConfig.WIDGET_PROBE) {
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
                if (!preparingDocument && connection.awaitEvenHubSessionReady(0)) {
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
        if (voiceBusy || voice != null) cancelVoice("Запись отменена: отключение", false)
        cancelTimer()
        cancelFrameWait()
        sleeping = false
        preparingDocumentId = null
        preparingDocument = false
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
