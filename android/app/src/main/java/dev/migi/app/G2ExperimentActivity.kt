package dev.migi.app

import android.Manifest
import android.app.Activity
import android.app.AlertDialog
import android.bluetooth.BluetoothManager
import android.content.pm.PackageManager
import android.os.Bundle
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import dev.migi.g2.G2Experiment
import java.util.Locale

/** Deliberately scoped to a visible Activity until hardware transport is verified. */
class G2ExperimentActivity : Activity() {
    private lateinit var left: EditText
    private lateinit var right: EditText
    private lateinit var log: TextView
    private var experiment: G2Experiment? = null
    private val lines = ArrayDeque<String>()

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val prefs = getSharedPreferences("g2-experiment", MODE_PRIVATE)
        val content = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(24, 24, 24, 24)
            setOnApplyWindowInsetsListener { view, insets ->
                val bars = insets.getInsets(android.view.WindowInsets.Type.systemBars())
                view.setPadding(24 + bars.left, 24 + bars.top, 24 + bars.right, 24 + bars.bottom)
                insets
            }
        }
        fun label(value: String) = content.addView(TextView(this).apply { text = value; textSize = 17f })
        fun button(value: String, action: () -> Unit) = content.addView(Button(this).apply {
            text = value
            setOnClickListener { action() }
        })
        label("Even G2 — эксперимент")
        label("Нужна установленная CFW. Отключите Faceclaw и Even App от очков. При уходе с этого экрана соединение закрывается.")
        left = EditText(this).apply { hint = "BLE-адрес левой дужки"; setSingleLine(); setText(prefs.getString("left", "")) }
        right = EditText(this).apply { hint = "BLE-адрес правой дужки"; setSingleLine(); setText(prefs.getString("right", "")) }
        content.addView(left)
        button("Выбрать левую из сопряжённых") { pickDevice(left) }
        content.addView(right)
        button("Выбрать правую из сопряжённых") { pickDevice(right) }
        button("Подключиться") {
            if (bluetoothAllowed()) {
                val manager = getSystemService(BluetoothManager::class.java)
                if (manager?.adapter?.isEnabled != true) {
                    appendLog("Включите Bluetooth в настройках телефона")
                } else {
                    val l = left.text.toString().trim().uppercase(Locale.ROOT)
                    val r = right.text.toString().trim().uppercase(Locale.ROOT)
                    prefs.edit().putString("left", l).putString("right", r).apply()
                    experiment?.connect(l, r)
                }
            }
        }
        button("Показать тест") { experiment?.showTest() }
        button("Усыпить дисплей") { experiment?.sleep() }
        button("Разбудить дисплей") { experiment?.showTest() }
        button("Усыпить на 10 секунд") { experiment?.sleepAndWakeLater() }
        button("Отключиться") { experiment?.disconnect() }
        log = TextView(this).apply { textSize = 12f; setTextIsSelectable(true) }
        content.addView(log)
        setContentView(ScrollView(this).apply { addView(content) })
    }

    override fun onStart() {
        super.onStart()
        experiment = G2Experiment(this, ::appendLog)
    }

    override fun onStop() {
        experiment?.close()
        experiment = null
        appendLog("Экран закрыт: соединение завершается. Для нового теста нажмите «Подключиться».")
        super.onStop()
    }

    private fun bluetoothAllowed(): Boolean {
        if (checkSelfPermission(Manifest.permission.BLUETOOTH_CONNECT) == PackageManager.PERMISSION_GRANTED) return true
        requestPermissions(arrayOf(Manifest.permission.BLUETOOTH_CONNECT), 42)
        appendLog("Разрешите доступ к устройствам поблизости и повторите действие")
        return false
    }

    private fun pickDevice(target: EditText) {
        if (!bluetoothAllowed()) return
        try {
            val devices = getSystemService(BluetoothManager::class.java)?.adapter?.bondedDevices
                ?.sortedBy { it.name ?: it.address }.orEmpty()
            if (devices.isEmpty()) {
                appendLog("Сопряжённых устройств нет. Введите BLE-адреса из Faceclaw.")
                return
            }
            AlertDialog.Builder(this).setTitle("Выберите дужку")
                .setItems(devices.map { "${it.name ?: "Без имени"}\n${it.address}" }.toTypedArray()) { _, index ->
                    target.setText(devices[index].address)
                }.setNegativeButton("Отмена", null).show()
        } catch (e: SecurityException) { appendLog("Нет разрешения Bluetooth: ${e.message}") }
    }

    private fun appendLog(message: String) {
        lines.addLast(message)
        while (lines.size > 100) lines.removeFirst()
        log.text = lines.joinToString("\n")
    }
}
