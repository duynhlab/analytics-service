, captured AS (
    SELECT order_id FROM ledger
    WHERE order_id IS NOT NULL
        AND occurred_at >= {from:DateTime64(6, 'UTC')} AND occurred_at < {to:DateTime64(6, 'UTC')}
    GROUP BY order_id HAVING sum(settled) > 0
)
