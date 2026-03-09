-- Complex e-commerce schema for integration testing (PostgreSQL)

DROP TABLE IF EXISTS reviews CASCADE;
DROP TABLE IF EXISTS order_items CASCADE;
DROP TABLE IF EXISTS orders CASCADE;
DROP TABLE IF EXISTS addresses CASCADE;
DROP TABLE IF EXISTS products CASCADE;
DROP TABLE IF EXISTS categories CASCADE;
DROP TABLE IF EXISTS users CASCADE;
DROP TYPE IF EXISTS order_status CASCADE;
DROP TYPE IF EXISTS review_status CASCADE;

CREATE TYPE order_status AS ENUM ('pending', 'processing', 'shipped', 'delivered', 'cancelled');
CREATE TYPE review_status AS ENUM ('published', 'hidden', 'pending');

CREATE TABLE users (
    id         SERIAL PRIMARY KEY,
    email      VARCHAR(255) NOT NULL,
    first_name VARCHAR(100) NOT NULL,
    last_name  VARCHAR(100) NOT NULL,
    username   VARCHAR(100) NOT NULL,
    phone      VARCHAR(50),
    is_active  BOOLEAN      NOT NULL DEFAULT TRUE,
    created_at TIMESTAMP    NOT NULL DEFAULT NOW()
);

CREATE TABLE addresses (
    id          SERIAL PRIMARY KEY,
    user_id     INTEGER      NOT NULL REFERENCES users(id),
    street      VARCHAR(255) NOT NULL,
    city        VARCHAR(100) NOT NULL,
    state       VARCHAR(100),
    country     VARCHAR(100) NOT NULL,
    postal_code VARCHAR(20),
    is_default  BOOLEAN      NOT NULL DEFAULT FALSE
);

CREATE TABLE categories (
    id          SERIAL PRIMARY KEY,
    name        VARCHAR(100) NOT NULL,
    description TEXT,
    slug        UUID         NOT NULL DEFAULT gen_random_uuid()
);

CREATE TABLE products (
    id          SERIAL PRIMARY KEY,
    category_id INTEGER        NOT NULL REFERENCES categories(id),
    name        VARCHAR(255)   NOT NULL,
    description TEXT,
    price       NUMERIC(10, 2) NOT NULL,
    stock       INTEGER        NOT NULL DEFAULT 0,
    sku         UUID           NOT NULL DEFAULT gen_random_uuid(),
    is_active   BOOLEAN        NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMP      NOT NULL DEFAULT NOW()
);

CREATE TABLE orders (
    id              SERIAL PRIMARY KEY,
    user_id         INTEGER        NOT NULL REFERENCES users(id),
    shipping_addr   INTEGER        REFERENCES addresses(id),
    status          order_status   NOT NULL DEFAULT 'pending',
    total_amount    NUMERIC(12, 2) NOT NULL,
    notes           TEXT,
    created_at      TIMESTAMP      NOT NULL DEFAULT NOW()
);

CREATE TABLE order_items (
    id         SERIAL PRIMARY KEY,
    order_id   INTEGER        NOT NULL REFERENCES orders(id),
    product_id INTEGER        NOT NULL REFERENCES products(id),
    quantity   INTEGER        NOT NULL DEFAULT 1,
    unit_price NUMERIC(10, 2) NOT NULL
);

CREATE TABLE reviews (
    id         SERIAL PRIMARY KEY,
    user_id    INTEGER      NOT NULL REFERENCES users(id),
    product_id INTEGER      NOT NULL REFERENCES products(id),
    rating     INTEGER      NOT NULL CHECK (rating BETWEEN 1 AND 5),
    title      VARCHAR(255),
    body       TEXT,
    status     review_status NOT NULL DEFAULT 'pending',
    created_at TIMESTAMP    NOT NULL DEFAULT NOW()
);
