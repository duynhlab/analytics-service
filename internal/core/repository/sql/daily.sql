SELECT toString(toDate(occurred_at, 'UTC')) AS day,
    toString(sum(settled)), toString(sumIf(amount, kind = 'refund')),
    countIf(NOT valid)
FROM ledger
WHERE occurred_at >= {from:DateTime64(6, 'UTC')} AND occurred_at < {to:DateTime64(6, 'UTC')}
GROUP BY day ORDER BY day
