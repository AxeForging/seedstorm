-- Teardown (always safe to re-run)
SET FOREIGN_KEY_CHECKS = 0;
DROP TABLE IF EXISTS wishlist_items;
DROP TABLE IF EXISTS wishlists;
DROP TABLE IF EXISTS reviews;
DROP TABLE IF EXISTS payments;
DROP TABLE IF EXISTS shipments;
DROP TABLE IF EXISTS order_items;
DROP TABLE IF EXISTS orders;
DROP TABLE IF EXISTS coupons;
DROP TABLE IF EXISTS product_tags;
DROP TABLE IF EXISTS products;
DROP TABLE IF EXISTS tags;
DROP TABLE IF EXISTS addresses;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS categories;
DROP TABLE IF EXISTS brands;

-- Level 0: no FKs
CREATE TABLE brands (
    id      INT AUTO_INCREMENT PRIMARY KEY,
    name    VARCHAR(100) NOT NULL,
    country VARCHAR(100),
    website VARCHAR(255)
);

CREATE TABLE categories (
    id          INT AUTO_INCREMENT PRIMARY KEY,
    parent_id   INT NULL,
    name        VARCHAR(100) NOT NULL,
    description TEXT,
    slug        CHAR(36) NOT NULL DEFAULT (UUID()),
    FOREIGN KEY (parent_id) REFERENCES categories(id)
);

CREATE TABLE tags (
    id   INT AUTO_INCREMENT PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    slug VARCHAR(100) NOT NULL
);

CREATE TABLE users (
    id         INT AUTO_INCREMENT PRIMARY KEY,
    email      VARCHAR(255) NOT NULL,
    first_name VARCHAR(100) NOT NULL,
    last_name  VARCHAR(100) NOT NULL,
    username   VARCHAR(100) NOT NULL,
    phone      VARCHAR(50),
    metadata   JSON,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE coupons (
    id             INT AUTO_INCREMENT PRIMARY KEY,
    code           VARCHAR(50)                        NOT NULL,
    discount_type  ENUM('percentage','fixed')         NOT NULL DEFAULT 'percentage',
    discount_value DECIMAL(10,2)                      NOT NULL,
    min_order      DECIMAL(10,2),
    expires_at     TIMESTAMP NULL,
    is_active      TINYINT(1) NOT NULL DEFAULT 1
);

-- Level 1: FK to level 0
CREATE TABLE addresses (
    id          INT AUTO_INCREMENT PRIMARY KEY,
    user_id     INT          NOT NULL,
    street      VARCHAR(255) NOT NULL,
    city        VARCHAR(100) NOT NULL,
    state       VARCHAR(100),
    country     VARCHAR(100) NOT NULL,
    postal_code VARCHAR(20),
    is_default  TINYINT(1)   NOT NULL DEFAULT 0,
    FOREIGN KEY (user_id) REFERENCES users(id)
);

CREATE TABLE products (
    id          INT AUTO_INCREMENT PRIMARY KEY,
    category_id INT            NOT NULL,
    brand_id    INT            NOT NULL,
    name        VARCHAR(255)   NOT NULL,
    description TEXT,
    price       DECIMAL(10,2)  NOT NULL,
    stock       INT            NOT NULL DEFAULT 0,
    sku         CHAR(36)       NOT NULL DEFAULT (UUID()),
    is_active   TINYINT(1)     NOT NULL DEFAULT 1,
    metadata    JSON,
    created_at  TIMESTAMP      NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (category_id) REFERENCES categories(id),
    FOREIGN KEY (brand_id)    REFERENCES brands(id)
);

-- Level 2: FK to level 1
CREATE TABLE product_tags (
    id         INT AUTO_INCREMENT PRIMARY KEY,
    product_id INT NOT NULL,
    tag_id     INT NOT NULL,
    FOREIGN KEY (product_id) REFERENCES products(id),
    FOREIGN KEY (tag_id)     REFERENCES tags(id)
);

CREATE TABLE orders (
    id           INT AUTO_INCREMENT PRIMARY KEY,
    user_id      INT                                                                         NOT NULL,
    address_id   INT,
    coupon_id    INT,
    status       ENUM('pending','processing','shipped','delivered','cancelled') NOT NULL DEFAULT 'pending',
    total_amount DECIMAL(12,2)                                                              NOT NULL,
    notes        TEXT,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (user_id)    REFERENCES users(id),
    FOREIGN KEY (address_id) REFERENCES addresses(id),
    FOREIGN KEY (coupon_id)  REFERENCES coupons(id)
);

CREATE TABLE wishlists (
    id         INT AUTO_INCREMENT PRIMARY KEY,
    user_id    INT          NOT NULL,
    name       VARCHAR(100) NOT NULL,
    is_public  TINYINT(1)   NOT NULL DEFAULT 0,
    created_at TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (user_id) REFERENCES users(id)
);

-- Level 3: FK to level 2
CREATE TABLE order_items (
    id         INT AUTO_INCREMENT PRIMARY KEY,
    order_id   INT           NOT NULL,
    product_id INT           NOT NULL,
    quantity   INT           NOT NULL DEFAULT 1,
    unit_price DECIMAL(10,2) NOT NULL,
    FOREIGN KEY (order_id)   REFERENCES orders(id),
    FOREIGN KEY (product_id) REFERENCES products(id)
);

CREATE TABLE shipments (
    id              INT AUTO_INCREMENT PRIMARY KEY,
    order_id        INT          NOT NULL,
    tracking_number VARCHAR(100),
    carrier         VARCHAR(100),
    status          ENUM('pending','in_transit','delivered','failed') NOT NULL DEFAULT 'pending',
    shipped_at      TIMESTAMP NULL,
    delivered_at    TIMESTAMP NULL,
    FOREIGN KEY (order_id) REFERENCES orders(id)
);

CREATE TABLE payments (
    id             INT AUTO_INCREMENT PRIMARY KEY,
    order_id       INT                                                    NOT NULL,
    method         ENUM('card','paypal','bank_transfer','crypto')         NOT NULL DEFAULT 'card',
    amount         DECIMAL(12,2)                                          NOT NULL,
    status         ENUM('pending','completed','failed','refunded')        NOT NULL DEFAULT 'pending',
    transaction_id VARCHAR(255),
    paid_at        TIMESTAMP NULL,
    FOREIGN KEY (order_id) REFERENCES orders(id)
);

CREATE TABLE reviews (
    id         INT AUTO_INCREMENT PRIMARY KEY,
    user_id    INT                                        NOT NULL,
    product_id INT                                        NOT NULL,
    rating     INT                                        NOT NULL,
    title      VARCHAR(255),
    body       TEXT,
    status     ENUM('published','hidden','pending')       NOT NULL DEFAULT 'pending',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (user_id)    REFERENCES users(id),
    FOREIGN KEY (product_id) REFERENCES products(id)
);

CREATE TABLE wishlist_items (
    id          INT AUTO_INCREMENT PRIMARY KEY,
    wishlist_id INT       NOT NULL,
    product_id  INT       NOT NULL,
    added_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (wishlist_id) REFERENCES wishlists(id),
    FOREIGN KEY (product_id)  REFERENCES products(id)
);

SET FOREIGN_KEY_CHECKS = 1;
