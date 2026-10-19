-- Sample shop database for local development and demos.
-- Loaded by docker-compose on first start (docker-entrypoint-initdb.d).

CREATE TABLE customers (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       text        NOT NULL,
    country    text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
COMMENT ON TABLE customers IS 'People who place orders';

CREATE TABLE products (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    sku         text          NOT NULL UNIQUE,
    name        text          NOT NULL,
    price       numeric(10,2) NOT NULL CHECK (price >= 0),
    tags        text[]        NOT NULL DEFAULT '{}',
    attributes  jsonb         NOT NULL DEFAULT '{}'
);

CREATE TABLE orders (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    customer_id bigint      NOT NULL REFERENCES customers(id),
    status      text        NOT NULL DEFAULT 'pending'
                CHECK (status IN ('pending', 'paid', 'shipped', 'cancelled')),
    placed_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX orders_customer_id_idx ON orders (customer_id);
CREATE INDEX orders_placed_at_idx ON orders (placed_at);

CREATE TABLE order_items (
    order_id   bigint        NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    product_id bigint        NOT NULL REFERENCES products(id),
    quantity   int           NOT NULL CHECK (quantity > 0),
    unit_price numeric(10,2) NOT NULL,
    PRIMARY KEY (order_id, product_id)
);

-- Sensitive tables, denied by DENY_TABLES in .env.example.
CREATE TABLE users (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email         text NOT NULL UNIQUE,
    password_hash text NOT NULL
);

CREATE TABLE api_keys (
    id      bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES users(id),
    key_ref text   NOT NULL
);

CREATE VIEW customer_order_totals AS
SELECT c.id AS customer_id, c.name, count(DISTINCT o.id) AS orders,
       coalesce(sum(oi.quantity * oi.unit_price), 0) AS total_spent
FROM customers c
LEFT JOIN orders o ON o.customer_id = c.id AND o.status <> 'cancelled'
LEFT JOIN order_items oi ON oi.order_id = o.id
GROUP BY c.id, c.name;

-- Deterministic sample data.
INSERT INTO customers (name, country, created_at)
SELECT 'Customer ' || g,
       (ARRAY['PK', 'DE', 'US', 'GB', 'AE'])[1 + g % 5],
       timestamptz '2025-01-01' + g * interval '7 hours'
FROM generate_series(1, 200) AS g;

INSERT INTO products (sku, name, price, tags, attributes)
SELECT 'SKU-' || lpad(g::text, 4, '0'),
       'Product ' || g,
       round((5 + (g * 37) % 200)::numeric + 0.99, 2),
       ARRAY[(ARRAY['books', 'games', 'tools', 'garden'])[1 + g % 4]],
       jsonb_build_object('weight_g', 100 + g * 13 % 900, 'color', (ARRAY['red', 'blue', 'black'])[1 + g % 3])
FROM generate_series(1, 50) AS g;

INSERT INTO orders (customer_id, status, placed_at)
SELECT 1 + (g * 7) % 200,
       (ARRAY['pending', 'paid', 'paid', 'shipped', 'cancelled'])[1 + g % 5],
       timestamptz '2025-02-01' + g * interval '3 hours'
FROM generate_series(1, 2000) AS g;

INSERT INTO order_items (order_id, product_id, quantity, unit_price)
SELECT o.id, p.id, 1 + (o.id + p.id) % 4, p.price
FROM orders o
JOIN products p ON p.id IN (1 + o.id % 50, 1 + (o.id * 3) % 50)
ON CONFLICT DO NOTHING;

INSERT INTO users (email, password_hash)
SELECT 'user' || g || '@example.com', 'not-a-real-hash'
FROM generate_series(1, 10) AS g;

ANALYZE;
