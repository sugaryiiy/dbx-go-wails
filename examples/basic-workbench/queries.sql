-- 查看产品，可在单表结果中练习编辑。
SELECT id, name, category, price, stock
FROM products
ORDER BY id;

-- 按客户汇总有效订单金额。
SELECT c.id, c.name, c.city,
       ROUND(SUM(oi.quantity * oi.unit_price), 2) AS total_amount
FROM customers AS c
JOIN orders AS o ON o.customer_id = c.id
JOIN order_items AS oi ON oi.order_id = o.id
WHERE o.status != '已取消'
GROUP BY c.id, c.name, c.city
ORDER BY total_amount DESC;

-- 查找没有填写备注的客户，练习 NULL 条件。
SELECT id, name, email, city, note
FROM customers
WHERE note IS NULL;
