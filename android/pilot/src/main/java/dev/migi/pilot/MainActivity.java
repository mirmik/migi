package dev.migi.pilot;

import android.app.Activity;
import android.os.Bundle;
import android.view.Gravity;
import android.widget.LinearLayout;
import android.widget.TextView;

public final class MainActivity extends Activity {
    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);

        TextView title = new TextView(this);
        title.setText(R.string.pilot_ready);
        title.setTextSize(24);
        title.setGravity(Gravity.CENTER);

        TextView version = new TextView(this);
        version.setText(getString(R.string.version_format, BuildConfig.VERSION_NAME, BuildConfig.VERSION_CODE));
        version.setTextSize(16);
        version.setGravity(Gravity.CENTER);

        LinearLayout layout = new LinearLayout(this);
        layout.setOrientation(LinearLayout.VERTICAL);
        layout.setGravity(Gravity.CENTER);
        layout.setPadding(32, 32, 32, 32);
        layout.addView(title);
        layout.addView(version);
        setContentView(layout);
    }
}
