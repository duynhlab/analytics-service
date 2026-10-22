SELECT 'order', count(), maxOrNull(emitted_at) FROM analytics.order_analytics_cdc_heartbeat_live
UNION ALL
SELECT 'checkout', count(), maxOrNull(emitted_at) FROM analytics.checkout_analytics_cdc_heartbeat_live
UNION ALL
SELECT 'payment', count(), maxOrNull(emitted_at) FROM analytics.payment_analytics_cdc_heartbeat_live
