package dev.migi.app

import android.app.Activity
import android.graphics.Typeface
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.text.Editable
import android.text.TextWatcher
import android.view.Gravity
import android.view.View
import android.view.WindowInsets
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ProgressBar
import android.widget.ScrollView
import android.widget.TextView
import com.google.android.material.button.MaterialButton
import com.google.android.material.card.MaterialCardView
import com.google.android.material.dialog.MaterialAlertDialogBuilder
import java.util.UUID
import java.util.concurrent.Executors
import org.json.JSONObject

/** One server-owned conversation shared with the glasses, with durable request IDs. */
class AgentChatActivity : Activity() {
    private val handler = Handler(Looper.getMainLooper())
    private val executor = Executors.newSingleThreadExecutor()
    private val prefs by lazy { getSharedPreferences("agent-chat", MODE_PRIVATE) }
    private lateinit var title: TextView
    private lateinit var model: TextView
    private lateinit var status: TextView
    private lateinit var context: TextView
    private lateinit var notice: TextView
    private lateinit var progress: ProgressBar
    private lateinit var history: LinearLayout
    private lateinit var scroll: ScrollView
    private lateinit var input: EditText
    private lateinit var send: MaterialButton
    private lateinit var compact: MaterialButton
    private lateinit var newChat: MaterialButton
    private lateinit var stop: MaterialButton
    private var snapshot: JSONObject? = null
    private var active = false
    private var loading = false
    private var limit = 40
    private var rendered = ""
    private var shownThread = ""
    private val poll = Runnable { refresh() }

    private fun column() = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
    private fun label(value: String, size: Float = 15f) = TextView(this).apply {
        text = value; applyMigiText(size); setPadding(0, dp(4), 0, dp(4))
    }
    private fun button(value: String, action: () -> Unit) = MaterialButton(this).apply {
        text = value; isAllCaps = false; textSize = 13f
        setOnClickListener { action() }
    }
    private fun rowButton(row: LinearLayout, button: MaterialButton) {
        row.addView(button, LinearLayout.LayoutParams(0, -2, 1f).apply { marginEnd = dp(4) })
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val root = column().apply {
            setBackgroundColor(MigiPalette.background)
            setPadding(dp(16), dp(12), dp(16), dp(12))
            setOnApplyWindowInsetsListener { view, insets ->
                val bars = insets.getInsets(WindowInsets.Type.systemBars() or WindowInsets.Type.ime())
                view.setPadding(dp(16) + bars.left, dp(8) + bars.top, dp(16) + bars.right, dp(8) + bars.bottom)
                insets
            }
        }
        setContentView(root)
        val heading = LinearLayout(this).apply { gravity = Gravity.CENTER_VERTICAL }
        heading.addView(button("‹") { finish() }, LinearLayout.LayoutParams(dp(52), -2))
        title = label("Агент", 24f).apply { setTypeface(typeface, Typeface.BOLD) }
        heading.addView(title, LinearLayout.LayoutParams(0, -2, 1f))
        heading.addView(button("Чаты", ::chooseChat))
        root.addView(heading)
        val panel = column().apply { setPadding(dp(14), dp(10), dp(14), dp(10)) }
        model = label("Подключение…", 12f).apply { setTextColor(MigiPalette.muted) }
        status = label("Загружаю состояние", 16f)
        context = label("Контекст: —", 13f).apply { setTextColor(MigiPalette.muted) }
        progress = ProgressBar(this, null, android.R.attr.progressBarStyleHorizontal).apply { max = 100; visibility = View.GONE }
        panel.addView(model); panel.addView(status); panel.addView(context); panel.addView(progress)
        root.addView(MaterialCardView(this).apply { applyMigiCard(radiusDp = 18); addView(panel) })
        val actions = LinearLayout(this)
        compact = button("Сжать") { action("compact") }
        newChat = button("Новый чат") { action("new") }
        stop = button("Стоп") { action("stop") }
        rowButton(actions, compact); rowButton(actions, newChat); rowButton(actions, stop)
        root.addView(actions)
        notice = label("", 13f).apply { setTextColor(MigiPalette.muted); visibility = View.GONE }
        root.addView(notice)
        history = column()
        scroll = ScrollView(this).apply { isFillViewport = true; addView(history) }
        root.addView(scroll, LinearLayout.LayoutParams(-1, 0, 1f))
        val composer = LinearLayout(this).apply { gravity = Gravity.BOTTOM }
        input = EditText(this).apply {
            hint = "Сообщение агенту"; setHintTextColor(MigiPalette.muted); applyMigiText(16f)
            background = roundedDrawable(MigiPalette.surfaceHigh, dp(16).toFloat())
            setPadding(dp(14), dp(10), dp(14), dp(10)); minLines = 1; maxLines = 5
            inputType = android.text.InputType.TYPE_CLASS_TEXT or android.text.InputType.TYPE_TEXT_FLAG_MULTI_LINE or android.text.InputType.TYPE_TEXT_FLAG_CAP_SENTENCES
            setText(prefs.getString("draft", ""))
            addTextChangedListener(object : TextWatcher {
                override fun beforeTextChanged(s: CharSequence?, start: Int, count: Int, after: Int) = Unit
                override fun onTextChanged(s: CharSequence?, start: Int, before: Int, count: Int) {
                    prefs.edit().putString("draft", s.toString()).apply()
                }
                override fun afterTextChanged(s: Editable?) = Unit
            })
        }
        send = button("Отправить") { if (input.text.isNotBlank()) action("send", input.text.toString()) }
        composer.addView(input, LinearLayout.LayoutParams(0, -2, 1f))
        composer.addView(send)
        root.addView(composer)
        controls(false, true)
    }

    override fun onStart() { super.onStart(); active = true; refresh() }
    override fun onStop() { active = false; handler.removeCallbacks(poll); super.onStop() }
    override fun onDestroy() { executor.shutdown(); super.onDestroy() }

    private fun controls(busy: Boolean, pending: Boolean) {
        val ready = snapshot != null && !pending
        send.isEnabled = ready && !busy
        compact.isEnabled = ready && !busy
        newChat.isEnabled = ready && !busy
        stop.isEnabled = ready && busy
    }
    private fun action(kind: String, text: String = "", target: String = "") {
        val current = snapshot ?: return
        if (prefs.contains("pending")) return
        val payload = JSONObject().put("action", kind).put("request_id", UUID.randomUUID().toString())
            .put("thread_id", current.getString("thread_id")).put("text", text).put("target_thread", target)
        if (payload.toString().toByteArray(Charsets.UTF_8).size > 64 * 1024) {
            notice.text = "Сообщение слишком велико для одной отправки. Разделите его на части."; notice.visibility = View.VISIBLE; return
        }
        if (!prefs.edit().putString("pending", payload.toString()).commit()) {
            notice.text = "Не удалось сохранить запрос. Попробуйте снова."; notice.visibility = View.VISIBLE; return
        }
        controls(current.optBoolean("busy"), true)
        notice.text = "Отправляю…"; notice.visibility = View.VISIBLE
        refresh()
    }
    private fun chooseChat() {
        val data = snapshot ?: return
        if (data.optBoolean("busy") || prefs.contains("pending")) return
        val chats = data.getJSONArray("chats")
        MaterialAlertDialogBuilder(this).setTitle("Диалоги")
            .setItems(Array(chats.length()) { chats.getJSONObject(it).getString("title") }) { _, index ->
                action("select", target = chats.getJSONObject(index).getString("thread_id"))
            }.show()
    }
    private fun refresh() {
        if (!active || loading) return
        handler.removeCallbacks(poll)
        loading = true
        executor.execute {
            var sentText: String? = null
            var rejection: String? = null
            val result = runCatching {
                val client = AgentChatClient(applicationContext)
                prefs.getString("pending", null)?.let { pending ->
                    try {
                        client.request(pending, limit)
                        val request = JSONObject(pending)
                        check(prefs.edit().remove("pending").commit()) { "Cannot save acknowledgement" }
                        if (request.optString("action") == "send") sentText = request.optString("text")
                    } catch (e: AgentChatClient.ChatError) {
                        if (!e.rejected) throw e
                        check(prefs.edit().remove("pending").commit()) { "Cannot save rejection" }
                        rejection = "Действие не принято: агент занят или чат изменился. Текст сохранён."
                    }
                }
                client.request(limit = limit)
            }
            runOnUiThread {
                loading = false
                if (isDestroyed) return@runOnUiThread
                if (sentText != null && input.text.toString() == sentText) input.setText("")
                result.onSuccess { snapshot = it; render(it) }
                    .onFailure { notice.text = "Нет связи с агентом. ${if (prefs.contains("pending")) "Запрос сохранён и будет повторён с тем же номером." else "Повторяю подключение…"}"; notice.visibility = View.VISIBLE }
                if (rejection != null) { notice.text = rejection; notice.visibility = View.VISIBLE }
                controls(snapshot?.optBoolean("busy") ?: false, prefs.contains("pending"))
                if (active) handler.postDelayed(poll, 2_000)
            }
        }
    }
    private fun render(data: JSONObject) {
        val thread = data.getString("thread_id")
        val changedChat = shownThread != thread
        shownThread = thread
        title.text = "Чат ${data.getInt("chat_number")}"
        model.text = data.optString("model", "Агент Migi")
        val phase = data.optString("status")
        val tools = data.optJSONArray("active_tools")
        status.text = when {
            phase == "compacting" -> "Сжимаю контекст…"
            phase == "stopping" -> "Останавливаю…"
            data.optBoolean("busy") && tools != null && tools.length() > 0 -> "Инструмент: ${tools.getString(0)}"
            data.optBoolean("busy") -> "Выполняю запрос · ${data.optInt("elapsed_seconds")} с"
            phase == "failed" -> "Запрос завершился с ошибкой"
            phase == "cancelled" -> "Остановлено · можно продолжать"
            else -> "Готов к сообщению"
        }
        status.setTextColor(if (phase == "failed") MigiPalette.danger else MigiPalette.secondary)
        val tokens = data.optInt("context_tokens_estimate")
        val window = data.optInt("context_window", 0)
        context.text = if (window > 0) "Контекст ≈ $tokens / $window токенов · ${tokens.toLong()*100/window}%"
            else "Контекст ≈ $tokens токенов · ${data.optInt("context_messages")} сообщений"
        progress.visibility = if (window > 0) View.VISIBLE else View.GONE
        if (window > 0) progress.progress = (tokens.toLong()*100/window).toInt().coerceIn(0, 100)
        notice.text = if (data.isNull("notice")) "" else data.optString("notice")
        notice.visibility = if (notice.text.isEmpty()) View.GONE else View.VISIBLE
        val messages = data.getJSONArray("messages")
        val live = data.optString("live_text")
        val signature = thread + messages.toString() + live
        if (signature == rendered) return
        rendered = signature
        val oldY = scroll.scrollY
        val nearBottom = history.height - scroll.height - oldY < dp(80)
        history.removeAllViews()
        if (data.optInt("message_total") > messages.length()) {
            history.addView(button("Показать более ранние сообщения") { limit = (limit + 40).coerceAtMost(10000); refresh() })
        }
        if (messages.length() == 0) history.addView(label("Здесь появятся сообщения с телефона и подтверждённые голосовые запросы с очков.", 16f))
        for (i in 0 until messages.length()) {
            val message = messages.getJSONObject(i)
            bubble(if (message.getString("role") == "user") "Вы" else "Агент", message.getString("text"), message.getString("role") == "user")
        }
        if (data.optBoolean("busy") && live.isNotEmpty()) bubble("Агент пишет…", live, false)
        scroll.post { if (changedChat || nearBottom) scroll.fullScroll(View.FOCUS_DOWN) else scroll.scrollTo(0, oldY) }
    }
    private fun bubble(who: String, text: String, user: Boolean) {
        val body = column().apply { setPadding(dp(14), dp(10), dp(14), dp(12)) }
        body.addView(label(who, 12f).apply { setTextColor(MigiPalette.muted) })
        body.addView(label(text, 16f).apply { setTextIsSelectable(true); setLineSpacing(0f, 1.12f) })
        history.addView(MaterialCardView(this).apply {
            applyMigiCard(color = if (user) MigiPalette.surfaceBright else MigiPalette.surface, radiusDp = 16)
            addView(body)
        }, LinearLayout.LayoutParams(-1, -2).apply {
            topMargin = dp(8); bottomMargin = dp(4)
            if (user) marginStart = dp(24) else marginEnd = dp(24)
        })
    }
}
