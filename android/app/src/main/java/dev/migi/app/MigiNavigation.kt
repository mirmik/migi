package dev.migi.app

import android.content.Context
import android.content.res.ColorStateList
import com.google.android.material.bottomnavigation.BottomNavigationView

internal object MigiNavigation {
    const val VIDEO = 10_006
    fun mainTab(id: Int): Int? = when (id) {
        10_001 -> 0
        10_002 -> 1
        10_003 -> 2
        10_004 -> 3
        else -> null
    }

    fun create(context: Context, selected: Int, onSelect: (Int) -> Boolean) = BottomNavigationView(context).apply {
        menu.add(0, 10_001, 0, R.string.tab_status).setIcon(R.drawable.ic_nav_home)
        menu.add(0, 10_002, 1, R.string.tab_playback).setIcon(R.drawable.ic_nav_music)
        menu.add(0, VIDEO, 2, "Видео").setIcon(R.drawable.ic_video)
        menu.add(0, 10_003, 3, R.string.tab_files).setIcon(R.drawable.ic_nav_files)
        menu.add(0, 10_004, 4, R.string.tab_updates).setIcon(R.drawable.ic_nav_updates)
        val colors = ColorStateList(arrayOf(intArrayOf(android.R.attr.state_checked), intArrayOf()),
            intArrayOf(MigiPalette.primary, MigiPalette.muted))
        itemIconTintList = colors; itemTextColor = colors
        itemActiveIndicatorColor = ColorStateList.valueOf(0x332F4F91)
        isItemActiveIndicatorEnabled = true
        labelVisibilityMode = BottomNavigationView.LABEL_VISIBILITY_LABELED
        setBackgroundColor(MigiPalette.surface); elevation = 0f
        setPadding(context.dp(4), context.dp(5), context.dp(4), context.dp(4))
        menu.findItem(selected)?.isChecked = true
        setOnItemSelectedListener { onSelect(it.itemId) }
    }
}
