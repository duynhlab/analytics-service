SELECT toString(i.product_id) AS product_id, i.product_name,
    toString(sum(toInt128(i.quantity))), toString(uniqExact(i.order_id)),
    toString(sum(toInt128(i.subtotal))) AS subtotal
FROM commerce.order_order_items_live AS i
INNER JOIN captured AS c ON toString(i.order_id) = c.order_id
GROUP BY product_id, i.product_name
ORDER BY sum(toInt128(i.subtotal)) DESC, product_id ASC, i.product_name ASC
LIMIT 10
