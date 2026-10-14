SELECT 'order', count(), maxOrNull(emitted_at) FROM commerce.order_analytics_cdc_heartbeat_live
UNION ALL
SELECT 'checkout', count(), maxOrNull(emitted_at) FROM commerce.checkout_analytics_cdc_heartbeat_live
UNION ALL
SELECT 'payment', count(), maxOrNull(emitted_at) FROM commerce.payment_analytics_cdc_heartbeat_live
