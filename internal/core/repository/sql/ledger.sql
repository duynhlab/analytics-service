ledger AS (
    SELECT toString(p.order_id) AS order_id, t.created_at AS occurred_at,
        t.kind AS kind, e.direction AS direction, toInt128(e.amount_minor) AS amount,
        ((t.kind = 'capture' AND e.direction = 'credit') OR
         (t.kind IN ('refund', 'reversal') AND e.direction = 'debit'))
         AND e.amount_minor > 0 AS valid,
        multiIf(t.kind = 'capture', toInt128(e.amount_minor),
                t.kind = 'reversal', -toInt128(e.amount_minor), toInt128(0)) AS settled
    FROM analytics.payment_ledger_entries_live AS e
    INNER JOIN analytics.payment_ledger_accounts_live AS a ON e.account_id = a.id
    INNER JOIN analytics.payment_ledger_transactions_live AS t ON e.transaction_id = t.id
    INNER JOIN analytics.payment_payments_live AS p ON t.payment_id = p.id
    WHERE a.name = 'merchant_revenue' AND p.currency = {currency:String}
)
