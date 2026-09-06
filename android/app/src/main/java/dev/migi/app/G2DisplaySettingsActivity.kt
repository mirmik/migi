package dev.migi.app

import android.app.Activity
import android.content.Intent
import android.os.Bundle
import android.widget.*
import dev.migi.g2.G2DisplaySettings

class G2DisplaySettingsActivity : Activity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val prefs = getSharedPreferences(G2DisplaySettings.PREFS, MODE_PRIVATE)
        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(24, 24, 24, 24)
            setOnApplyWindowInsetsListener { view, insets ->
                val bars = insets.getInsets(android.view.WindowInsets.Type.systemBars())
                view.setPadding(24 + bars.left, 24 + bars.top, 24 + bars.right, 24 + bars.bottom)
                insets
            }
        }
        fun label(value: String, size: Float = 18f): TextView = TextView(this).apply {
            text = value; textSize = size; setPadding(8, 20, 8, 12); root.addView(this)
        }
        label("Настройки очков", 26f)
        root.addView(Button(this).apply {
            text = "Голосовые записи"
            setOnClickListener { startActivity(Intent(this@G2DisplaySettingsActivity, G2VoiceRecordingsActivity::class.java)) }
        })
        label("Прокрутка", 22f)
        root.addView(Switch(this).apply {
            text = "Инвертировать прокрутку"
            isChecked = G2DisplaySettings.invertScroll(this@G2DisplaySettingsActivity)
            setOnCheckedChangeListener { _, inverted ->
                if (!prefs.edit().putBoolean(G2DisplaySettings.INVERT_SCROLL, inverted).commit()) {
                    Toast.makeText(this@G2DisplaySettingsActivity, "Не удалось сохранить настройку. Повторите переключение.", Toast.LENGTH_SHORT).show()
                }
            }
        })
        label("Меняет местами свайпы вперёд и назад в документах и пейджере. Применяется сразу.", 15f)
        label("Яркость", 22f)
        val auto = Switch(this).apply {
            text = "Автоматическая яркость"
            isChecked = prefs.getString(G2DisplaySettings.BRIGHTNESS_MODE, "device") == "auto"
            root.addView(this)
        }
        val levelLabel = label("")
        val slider = SeekBar(this).apply {
            min = 1; max = 100
            progress = prefs.getInt(G2DisplaySettings.BRIGHTNESS_LEVEL, 50).coerceIn(1, 100)
            contentDescription = "Яркость дисплеев"
            root.addView(this)
        }
        fun update() {
            slider.isEnabled = !auto.isChecked
            levelLabel.text = if (auto.isChecked) "По датчику освещения" else "Уровень яркости: ${slider.progress} из 100"
        }
        slider.setOnSeekBarChangeListener(object : SeekBar.OnSeekBarChangeListener {
            override fun onProgressChanged(view: SeekBar?, value: Int, fromUser: Boolean) = update()
            override fun onStartTrackingTouch(view: SeekBar?) = Unit
            override fun onStopTrackingTouch(view: SeekBar?) = Unit
        })
        auto.setOnCheckedChangeListener { _, _ -> update() }
        update()
        label("Яркость сохраняется кнопкой ниже и восстанавливается при подключении. До первого применения сохраняется текущая настройка очков.", 15f)
        root.addView(Button(this).apply {
            text = "Применить яркость"
            setOnClickListener {
                val saved = prefs.edit().putString(G2DisplaySettings.BRIGHTNESS_MODE, if (auto.isChecked) "auto" else "manual")
                    .putInt(G2DisplaySettings.BRIGHTNESS_LEVEL, slider.progress).commit()
                Toast.makeText(this@G2DisplaySettingsActivity,
                    if (!saved) "Не удалось сохранить яркость. Повторите применение."
                    else if (G2PagerBridge.enabled(this@G2DisplaySettingsActivity)) "Яркость сохранена" else "Сохранено. Включите подключение к очкам.", Toast.LENGTH_SHORT).show()
            }
        })
        label("Базовый шрифт", 22f)
        label("Сейчас используется встроенный шрифт очков. Этот режим не поддерживает изменение размера текста.", 16f)
        root.addView(Button(this).apply {
            text = "Подключение и проверка очков"
            setOnClickListener { startActivity(Intent(this@G2DisplaySettingsActivity, G2ExperimentActivity::class.java)) }
        })
        setContentView(ScrollView(this).apply { addView(root) })
    }
}
