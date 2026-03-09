-- Teardown (always safe to re-run)
DROP TABLE IF EXISTS wishlist_items CASCADE;
DROP TABLE IF EXISTS wishlists CASCADE;
DROP TABLE IF EXISTS reviews CASCADE;
DROP TABLE IF EXISTS payments CASCADE;
DROP TABLE IF EXISTS shipments CASCADE;
DROP TABLE IF EXISTS order_items CASCADE;
DROP TABLE IF EXISTS orders CASCADE;
DROP TABLE IF EXISTS coupons CASCADE;
DROP TABLE IF EXISTS product_tags CASCADE;
DROP TABLE IF EXISTS products CASCADE;
DROP TABLE IF EXISTS tags CASCADE;
DROP TABLE IF EXISTS addresses CASCADE;
DROP TABLE IF EXISTS users CASCADE;
DROP TABLE IF EXISTS categories CASCADE;
DROP TABLE IF EXISTS brands CASCADE;
DROP TYPE IF EXISTS order_status CASCADE;
DROP TYPE IF EXISTS review_status CASCADE;
DROP TYPE IF EXISTS shipment_status CASCADE;
DROP TYPE IF EXISTS payment_method CASCADE;
DROP TYPE IF EXISTS payment_status CASCADE;
DROP TYPE IF EXISTS discount_type CASCADE;

-- Enum types
CREATE TYPE order_status    AS ENUM ('pending','processing','shipped','delivered','cancelled');
CREATE TYPE review_status   AS ENUM ('published','hidden','pending');
CREATE TYPE shipment_status AS ENUM ('pending','in_transit','delivered','failed');
CREATE TYPE payment_method  AS ENUM ('card','paypal','bank_transfer','crypto');
CREATE TYPE payment_status  AS ENUM ('pending','completed','failed','refunded');
CREATE TYPE discount_type   AS ENUM ('percentage','fixed');

-- Level 0: no FKs
CREATE TABLE brands (
    id      SERIAL PRIMARY KEY,
    name    VARCHAR(100) NOT NULL,
    country VARCHAR(100),
    website VARCHAR(255)
);

CREATE TABLE categories (
    id          SERIAL  PRIMARY KEY,
    parent_id   INTEGER REFERENCES categories(id),
    name        VARCHAR(100) NOT NULL,
    description TEXT,
    slug        UUID NOT NULL DEFAULT gen_random_uuid()
);

CREATE TABLE tags (
    id   SERIAL PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    slug VARCHAR(100) NOT NULL
);

CREATE TABLE users (
    id         SERIAL PRIMARY KEY,
    email      VARCHAR(255) NOT NULL,
    first_name VARCHAR(100) NOT NULL,
    last_name  VARCHAR(100) NOT NULL,
    username   VARCHAR(100) NOT NULL,
    phone      VARCHAR(50),
    metadata   JSONB,
    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE TABLE coupons (
    id             SERIAL PRIMARY KEY,
    code           VARCHAR(50)    NOT NULL,
    discount_type  discount_type  NOT NULL DEFAULT 'percentage',
    discount_value NUMERIC(10,2)  NOT NULL,
    min_order      NUMERIC(10,2),
    expires_at     TIMESTAMP,
    is_active      BOOLEAN NOT NULL DEFAULT TRUE
);

-- Level 1: FK to level 0
CREATE TABLE addresses (
    id          SERIAL PRIMARY KEY,
    user_id     INTEGER      NOT NULL REFERENCES users(id),
    street      VARCHAR(255) NOT NULL,
    city        VARCHAR(100) NOT NULL,
    state       VARCHAR(100),
    country     VARCHAR(100) NOT NULL,
    postal_code VARCHAR(20),
    is_default  BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE TABLE products (
    id          SERIAL PRIMARY KEY,
    category_id INTEGER        NOT NULL REFERENCES categories(id),
    brand_id    INTEGER        NOT NULL REFERENCES brands(id),
    name        VARCHAR(255)   NOT NULL,
    description TEXT,
    price       NUMERIC(10,2)  NOT NULL,
    stock       INTEGER        NOT NULL DEFAULT 0,
    sku         UUID           NOT NULL DEFAULT gen_random_uuid(),
    is_active   BOOLEAN        NOT NULL DEFAULT TRUE,
    metadata    JSONB,
    created_at  TIMESTAMP      NOT NULL DEFAULT NOW()
);

-- Level 2: FK to level 1
CREATE TABLE product_tags (
    id         SERIAL PRIMARY KEY,
    product_id INTEGER NOT NULL REFERENCES products(id),
    tag_id     INTEGER NOT NULL REFERENCES tags(id)
);

CREATE TABLE orders (
    id           SERIAL PRIMARY KEY,
    user_id      INTEGER        NOT NULL REFERENCES users(id),
    address_id   INTEGER        REFERENCES addresses(id),
    coupon_id    INTEGER        REFERENCES coupons(id),
    status       order_status   NOT NULL DEFAULT 'pending',
    total_amount NUMERIC(12,2)  NOT NULL,
    notes        TEXT,
    created_at   TIMESTAMP      NOT NULL DEFAULT NOW()
);

CREATE TABLE wishlists (
    id         SERIAL PRIMARY KEY,
    user_id    INTEGER      NOT NULL REFERENCES users(id),
    name       VARCHAR(100) NOT NULL,
    is_public  BOOLEAN      NOT NULL DEFAULT FALSE,
    created_at TIMESTAMP    NOT NULL DEFAULT NOW()
);

-- Level 3: FK to level 2
CREATE TABLE order_items (
    id         SERIAL PRIMARY KEY,
    order_id   INTEGER        NOT NULL REFERENCES orders(id),
    product_id INTEGER        NOT NULL REFERENCES products(id),
    quantity   INTEGER        NOT NULL DEFAULT 1,
    unit_price NUMERIC(10,2)  NOT NULL
);

CREATE TABLE shipments (
    id              SERIAL PRIMARY KEY,
    order_id        INTEGER         NOT NULL REFERENCES orders(id),
    tracking_number VARCHAR(100),
    carrier         VARCHAR(100),
    status          shipment_status NOT NULL DEFAULT 'pending',
    shipped_at      TIMESTAMP,
    delivered_at    TIMESTAMP
);

CREATE TABLE payments (
    id             SERIAL PRIMARY KEY,
    order_id       INTEGER        NOT NULL REFERENCES orders(id),
    method         payment_method NOT NULL DEFAULT 'card',
    amount         NUMERIC(12,2)  NOT NULL,
    status         payment_status NOT NULL DEFAULT 'pending',
    transaction_id VARCHAR(255),
    paid_at        TIMESTAMP
);

CREATE TABLE reviews (
    id         SERIAL PRIMARY KEY,
    user_id    INTEGER       NOT NULL REFERENCES users(id),
    product_id INTEGER       NOT NULL REFERENCES products(id),
    rating     INTEGER       NOT NULL CHECK (rating BETWEEN 1 AND 5),
    title      VARCHAR(255),
    body       TEXT,
    status     review_status NOT NULL DEFAULT 'pending',
    created_at TIMESTAMP     NOT NULL DEFAULT NOW()
);

CREATE TABLE wishlist_items (
    id          SERIAL PRIMARY KEY,
    wishlist_id INTEGER   NOT NULL REFERENCES wishlists(id),
    product_id  INTEGER   NOT NULL REFERENCES products(id),
    added_at    TIMESTAMP NOT NULL DEFAULT NOW()
);
