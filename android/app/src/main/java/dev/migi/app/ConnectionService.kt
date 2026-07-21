package dev.migi.app

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.ConnectivityManager
import android.net.Network
import android.os.IBinder
import android.util.Log
import java.util.concurrent.atomic.AtomicInteger

class ConnectionService : Service() {
    private var client: EventStreamClient? = null
    private lateinit var connectivityManager: ConnectivityManager
    private var currentNetwork: Network? = null
    private val networkCallback = object : ConnectivityManager.NetworkCallback() {
        override fun onAvailable(network: Network) {
            if (currentNetwork != network) {
                currentNetwork = network
                client?.reconnectNow("Network became available")
            }
        }

        override fun onLost(network: Network) {
            if (currentNetwork == network) {
                currentNetwork = null
                updateConnectionStatus("Network unavailable")
            }
        }
    }

    override fun onCreate() {
        super.onCreate()
        isRunning = true
        connectivityManager = getSystemService(ConnectivityManager::class.java)
        connectivityManager.registerDefaultNetworkCallback(networkCallback)
        createChannels()
        startForeground(
            CONNECTION_NOTIFICATION_ID,
            connectionNotification("Starting QUIC connection"),
            ServiceInfo.FOREGROUND_SERVICE_TYPE_REMOTE_MESSAGING,
        )
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_RECONFIGURE) {
            client?.close()
            client = null
        }
        if (client == null) {
            val preferences = getSharedPreferences(MainActivity.PREFERENCES, MODE_PRIVATE)
            val endpoint = preferences.getString(MainActivity.KEY_ENDPOINT, null)
            val certificatePin = preferences.getString(MainActivity.KEY_CERTIFICATE_PIN, null)
            val credential = CredentialStore(this).load()
            if (endpoint.isNullOrBlank() || certificatePin.isNullOrBlank() || credential.isNullOrBlank()) {
                updateConnectionStatus("Pair this device before connecting")
                stopSelf()
                return START_NOT_STICKY
            }

            client = runCatching {
                EventStreamClient(
                    context = this,
                    endpoint = endpoint,
                    certificatePin = certificatePin,
                    credential = credential,
                    onState = ::updateConnectionStatus,
                    onEvent = ::showEvent,
                ).also { it.start() }
            }.getOrElse {
                updateConnectionStatus("Invalid endpoint: ${it.message}")
                stopSelf()
                return START_NOT_STICKY
            }
        }
        return START_STICKY
    }

    override fun onDestroy() {
        client?.close()
        client = null
        connectivityManager.unregisterNetworkCallback(networkCallback)
        isRunning = false
        super.onDestroy()
    }

    override fun onBind(intent: Intent?): IBinder? = null

    private fun createChannels() {
        val manager = getSystemService(NotificationManager::class.java)
        manager.createNotificationChannel(
            NotificationChannel(
                CONNECTION_CHANNEL,
                "Migi connection",
                NotificationManager.IMPORTANCE_LOW,
            ).apply { description = "Persistent connection status" },
        )
        manager.createNotificationChannel(
            NotificationChannel(
                EVENT_CHANNEL,
                "Agent events",
                NotificationManager.IMPORTANCE_HIGH,
            ).apply { description = "Agent completion and attention alerts" },
        )
    }

    private fun connectionNotification(text: String): Notification =
        Notification.Builder(this, CONNECTION_CHANNEL)
            .setSmallIcon(android.R.drawable.stat_notify_sync)
            .setContentTitle("Migi")
            .setContentText(text)
            .setOngoing(true)
            .setContentIntent(mainActivityIntent())
            .build()

    private fun updateConnectionStatus(text: String) {
        Log.i(TAG, text)
        getSystemService(NotificationManager::class.java).notify(
            CONNECTION_NOTIFICATION_ID,
            connectionNotification(text),
        )
    }

    private fun showEvent(event: AgentEvent) {
        val notification = Notification.Builder(this, EVENT_CHANNEL)
            .setSmallIcon(android.R.drawable.stat_notify_more)
            .setContentTitle(event.title)
            .setContentText(event.body.ifBlank { event.agent })
            .setStyle(Notification.BigTextStyle().bigText(event.body))
            .setAutoCancel(true)
            .setContentIntent(mainActivityIntent())
            .build()
        getSystemService(NotificationManager::class.java).notify(
            nextEventNotification.incrementAndGet(),
            notification,
        )
    }

    private fun mainActivityIntent(): PendingIntent = PendingIntent.getActivity(
        this,
        0,
        Intent(this, MainActivity::class.java),
        PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
    )

    companion object {
        private const val CONNECTION_CHANNEL = "connection"
        private const val EVENT_CHANNEL = "agent-events-v1"
        private const val CONNECTION_NOTIFICATION_ID = 1
        private const val TAG = "MigiConnection"
        private val nextEventNotification = AtomicInteger(1000)
        const val ACTION_RECONFIGURE = "dev.migi.app.action.RECONFIGURE"

        @Volatile
        var isRunning: Boolean = false
            private set
    }
}
