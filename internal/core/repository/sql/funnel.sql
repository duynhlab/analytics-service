, cohort AS (
    SELECT id, order_id FROM analytics.checkout_checkout_sessions_live
    WHERE currency = {currency:String}
        AND created_at >= {from:DateTime64(6, 'UTC')} AND created_at < {to:DateTime64(6, 'UTC')}
), balances AS (
    SELECT order_id, sum(settled) AS settled, countIf(NOT valid) AS invalid
    FROM ledger WHERE order_id IN (SELECT order_id FROM cohort WHERE order_id IS NOT NULL)
    GROUP BY order_id
)
SELECT toString(count()), toString(countIf(c.order_id IS NOT NULL)),
    toString(countIf(b.settled > 0)),
    toString(countIf(c.order_id IN (SELECT toString(id) FROM analytics.order_orders_live WHERE status = 'completed'))),
    countIf(b.invalid > 0)
FROM cohort AS c
LEFT JOIN balances AS b ON c.order_id = b.order_id
