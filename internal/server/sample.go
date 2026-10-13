package server

import (
	"database/sql"
	"errors"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
)

const sampleID = "dbx-basic-workbench-sample"
const sampleApplicationID = 0x44425853
const sampleSeed = `
CREATE TABLE customers (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    email TEXT UNIQUE,
    city TEXT NOT NULL,
    note TEXT
);
CREATE TABLE products (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    category TEXT NOT NULL,
    price NUMERIC NOT NULL CHECK (price >= 0),
    stock INTEGER NOT NULL CHECK (stock >= 0)
);
CREATE TABLE orders (
    id INTEGER PRIMARY KEY,
    customer_id INTEGER NOT NULL REFERENCES customers(id),
    status TEXT NOT NULL CHECK (status IN ('待付款', '已付款', '已发货', '已取消')),
    ordered_at TEXT NOT NULL,
    note TEXT
);
CREATE TABLE order_items (
    id INTEGER PRIMARY KEY,
    order_id INTEGER NOT NULL REFERENCES orders(id),
    product_id INTEGER NOT NULL REFERENCES products(id),
    quantity INTEGER NOT NULL CHECK (quantity > 0),
    unit_price NUMERIC NOT NULL CHECK (unit_price >= 0),
    UNIQUE (order_id, product_id)
);
CREATE INDEX idx_orders_customer ON orders(customer_id);
CREATE INDEX idx_order_items_product ON order_items(product_id);
INSERT INTO customers (id, name, email, city, note) VALUES
    (1, '林晓', 'lin.xiao@example.com', '上海', '工作日配送'),
    (2, '陈宇', 'chen.yu@example.com', '杭州', NULL),
    (3, '王宁', NULL, '深圳', '首次下单'),
    (4, '赵悦', 'zhao.yue@example.com', '北京', NULL);
INSERT INTO products (id, name, category, price, stock) VALUES
    (1, '无线键盘', '办公设备', 239.90, 36),
    (2, '笔记本支架', '办公设备', 89.50, 58),
    (3, '手冲咖啡豆', '食品', 68.80, 120),
    (4, '陶瓷马克杯', '生活用品', 39.90, 75),
    (5, '便签纸套装', '文具', 12.50, 200);
INSERT INTO orders (id, customer_id, status, ordered_at, note) VALUES
    (1001, 1, '已发货', '2026-09-28 09:30:00', '请开电子发票'),
    (1002, 2, '已付款', '2026-09-29 14:05:00', NULL),
    (1003, 3, '待付款', '2026-09-30 10:15:00', NULL),
    (1004, 1, '已付款', '2026-10-01 16:40:00', '与上次订单同一地址'),
    (1005, 4, '已取消', '2026-10-02 11:20:00', '客户取消');
INSERT INTO order_items (id, order_id, product_id, quantity, unit_price) VALUES
    (1, 1001, 1, 1, 239.90),
    (2, 1001, 2, 2, 89.50),
    (3, 1002, 3, 2, 68.80),
    (4, 1002, 4, 1, 39.90),
    (5, 1003, 2, 1, 89.50),
    (6, 1003, 5, 3, 12.50),
    (7, 1004, 3, 1, 68.80),
    (8, 1005, 4, 2, 39.90);
`
const sampleQuery = `SELECT
    o.id AS order_id,
    c.name AS customer,
    c.city,
    p.name AS product,
    oi.quantity,
    oi.unit_price,
    ROUND(oi.quantity * oi.unit_price, 2) AS subtotal,
    o.status,
    o.ordered_at,
    o.note
FROM orders AS o
JOIN customers AS c ON c.id = o.customer_id
JOIN order_items AS oi ON oi.order_id = o.id
JOIN products AS p ON p.id = oi.product_id
ORDER BY o.id, oi.id;
`

func (s *Server) sampleWorkspace() (any, error) {
	s.sampleMu.Lock()
	defer s.sampleMu.Unlock()
	dir := filepath.Join(s.dataDir, "samples")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, errors.New("Cannot create sample directory")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("Sample directory must be a regular directory")
	}
	databasePath := filepath.Join(dir, "basic-workbench.sqlite")
	if err = regularFile(databasePath); errors.Is(err, os.ErrNotExist) {
		temporary, err := os.CreateTemp(dir, ".sample-*.sqlite")
		if err != nil {
			return nil, errors.New("Cannot create sample database")
		}
		name := temporary.Name()
		_ = temporary.Close()
		defer os.Remove(name)
		db, err := sql.Open("sqlite", name)
		if err != nil {
			return nil, errors.New("Cannot initialize sample database")
		}
		_, err = db.Exec("BEGIN; PRAGMA foreign_keys=ON; PRAGMA application_id=1145198675; PRAGMA user_version=1;" + sampleSeed + "COMMIT;")
		closeErr := db.Close()
		if err != nil || closeErr != nil {
			return nil, errors.New("Cannot seed sample database")
		}
		// Linking publishes without replacing another process' completed sample.
		if err = os.Link(name, databasePath); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, errors.New("Cannot publish sample database")
		}
	} else if err != nil {
		return nil, errors.New("Sample database must be a regular file")
	}
	if err = regularFile(databasePath); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return nil, errors.New("Cannot open sample database")
	}
	defer db.Close()
	var appID, version, count int
	if err = db.QueryRow("PRAGMA application_id").Scan(&appID); err != nil {
		return nil, errors.New("Cannot verify sample database")
	}
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return nil, errors.New("Cannot verify sample database")
	}
	if appID != sampleApplicationID || version != 1 {
		return nil, errors.New("The existing sample file is not a recognized Database Workbench sample; it will not be overwritten")
	}
	if err = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('customers','products','orders','order_items')").Scan(&count); err != nil || count != 4 {
		return nil, errors.New("Sample schema is incomplete; the existing file will not be overwritten")
	}
	config := map[string]any{"id": sampleID, "name": "示例数据库", "note": "本地 SQLite 示例：客户、产品、订单和订单明细。可自由编辑，重新打开会保留修改。", "db_type": "sqlite", "host": databasePath, "port": 0, "username": "", "password": "", "database": "main", "color": "#22c55e", "save_password": true}
	err = s.update(func(state *diskState) error {
		for _, existing := range state.Connections {
			if str(existing, "id") == sampleID && (str(existing, "db_type") != "sqlite" || str(existing, "host") != databasePath) {
				return errors.New("The sample connection points to another database; remove that entry before reopening the sample")
			}
		}
		for _, existing := range state.Connections {
			if str(existing, "db_type") == "sqlite" && str(existing, "host") == databasePath {
				config = existing
				return nil
			}
		}
		state.Connections = upsert(state.Connections, config)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"connection": config, "sql": sampleQuery}, nil
}
