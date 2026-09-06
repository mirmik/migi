package dev.migi.app

import android.Manifest
import android.app.Activity
import android.app.AlertDialog
import android.bluetooth.BluetoothManager
import android.content.pm.PackageManager
import android.content.Intent
import android.widget.Switch
import android.bluetooth.BluetoothAdapter
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
    private lateinit var pagerSwitch: Switch

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
        label("Отключите Faceclaw и Even App от очков. Пейджер работает при закрытом экране Migi; локальный тест — только на этом экране.")
        left = EditText(this).apply { hint = "BLE-адрес левой дужки"; setSingleLine(); setText(prefs.getString("left", "")) }
        right = EditText(this).apply { hint = "BLE-адрес правой дужки"; setSingleLine(); setText(prefs.getString("right", "")) }
        content.addView(left)
        button("Выбрать левую из сопряжённых") { pickDevice(left) }
        content.addView(right)
        button("Выбрать правую из сопряжённых") { pickDevice(right) }
        pagerSwitch = Switch(this).apply {
            text = "Пейджер на очках — автоматически"
            isChecked = G2PagerBridge.enabled(this@G2ExperimentActivity)
            setOnCheckedChangeListener { _, enabled ->
                if (enabled) {
                    val l = this@G2ExperimentActivity.left.text.toString().trim().uppercase(Locale.ROOT)
                    val r = this@G2ExperimentActivity.right.text.toString().trim().uppercase(Locale.ROOT)
                    if (!bluetoothAllowed() || !BluetoothAdapter.checkBluetoothAddress(l) ||
                        !BluetoothAdapter.checkBluetoothAddress(r) || l == r) {
                        isChecked = false
                        appendLog("Разрешите Bluetooth и выберите две разные дужки")
                    } else {
                        experiment?.close()
                        experiment = null
                        prefs.edit().putString("left", l).putString("right", r)
                            .putBoolean(G2PagerBridge.ENABLED, true).apply()
                        startForegroundService(Intent(this@G2ExperimentActivity, ConnectionService::class.java))
                        appendLog("Пейджер включён. Новые сообщения сами разбудят очки, даже при блокировке телефона.")
                    }
                } else {
                    prefs.edit().putBoolean(G2PagerBridge.ENABLED, false).apply()
                    if (experiment == null) experiment = G2Experiment(this@G2ExperimentActivity, ::appendLog)
                    appendLog("Пейджер на очках выключен")
                }
            }
        }
        content.addView(pagerSwitch)
        button("Подключиться") {
            if (G2PagerBridge.enabled(this)) {
                appendLog("Для локального теста сначала выключите автоматический пейджер")
            } else if (bluetoothAllowed()) {
                val manager = getSystemService(BluetoothManager::class.java)
                if (manager?.adapter?.isEnabled != true) {
                    appendLog("Включите Bluetooth в настройках телефона")
                } else {
                    val l = this@G2ExperimentActivity.left.text.toString().trim().uppercase(Locale.ROOT)
                    val r = this@G2ExperimentActivity.right.text.toString().trim().uppercase(Locale.ROOT)
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
        if (!G2PagerBridge.enabled(this)) experiment = G2Experiment(this, ::appendLog)
    }

    override fun onStop() {
        experiment?.close()
        experiment = null
        if (!G2PagerBridge.enabled(this)) appendLog("Локальный тест завершён")
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
