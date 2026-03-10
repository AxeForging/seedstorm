-- Teardown (always safe to re-run)
SET FOREIGN_KEY_CHECKS = 0;
DROP TABLE IF EXISTS return_requests;
DROP TABLE IF EXISTS audit_logs;
DROP TABLE IF EXISTS support_tickets;
DROP TABLE IF EXISTS project_assignments;
DROP TABLE IF EXISTS purchase_order_items;
DROP TABLE IF EXISTS purchase_orders;
DROP TABLE IF EXISTS inventory;
DROP TABLE IF EXISTS projects;
DROP TABLE IF EXISTS employees;
DROP TABLE IF EXISTS departments;
DROP TABLE IF EXISTS warehouses;
DROP TABLE IF EXISTS suppliers;
DROP TABLE IF EXISTS companies;
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
    email      VARCHAR(255) NOT NULL UNIQUE,
    first_name VARCHAR(100) NOT NULL,
    last_name  VARCHAR(100) NOT NULL,
    username   VARCHAR(100) NOT NULL UNIQUE,
    role       VARCHAR(20)  NOT NULL DEFAULT 'user' CHECK (role IN ('admin', 'user', 'guest')),
    phone      VARCHAR(50),
    metadata   JSON,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE coupons (
    id             INT AUTO_INCREMENT PRIMARY KEY,
    code           VARCHAR(50)                        NOT NULL UNIQUE,
    discount_type  ENUM('percentage','fixed')         NOT NULL DEFAULT 'percentage',
    discount_value DECIMAL(10,2)                      NOT NULL,
    min_order      DECIMAL(10,2),
    expires_at     TIMESTAMP NULL,
    is_active      TINYINT(1) NOT NULL DEFAULT 1
);

CREATE TABLE companies (
    id           INT AUTO_INCREMENT PRIMARY KEY,
    name         VARCHAR(255) NOT NULL,
    industry     VARCHAR(100),
    website      VARCHAR(255),
    founded_year INT,
    is_active    TINYINT(1) NOT NULL DEFAULT 1
);

CREATE TABLE suppliers (
    id      INT AUTO_INCREMENT PRIMARY KEY,
    name    VARCHAR(255) NOT NULL,
    email   VARCHAR(255),
    phone   VARCHAR(50),
    country VARCHAR(100),
    rating  DECIMAL(3,2)
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
    price       DECIMAL(10,2)  NOT NULL CHECK (price > 0),
    stock       INT            NOT NULL DEFAULT 0 CHECK (stock >= 0),
    rating      SMALLINT       NOT NULL DEFAULT 3 CHECK (rating >= 1 AND rating <= 5),
    sku         CHAR(36)       NOT NULL DEFAULT (UUID()),
    is_active   TINYINT(1)     NOT NULL DEFAULT 1,
    metadata    JSON,
    created_at  TIMESTAMP      NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (category_id) REFERENCES categories(id),
    FOREIGN KEY (brand_id)    REFERENCES brands(id)
);

-- departments: head_employee_id FK added after employees is created (near-cycle)
CREATE TABLE departments (
    id               INT AUTO_INCREMENT PRIMARY KEY,
    company_id       INT          NOT NULL,
    parent_dept_id   INT          NULL,
    head_employee_id INT          NULL,    -- FK constraint added after employees table created
    name             VARCHAR(100) NOT NULL,
    budget           DECIMAL(15,2),
    FOREIGN KEY (company_id)     REFERENCES companies(id),
    FOREIGN KEY (parent_dept_id) REFERENCES departments(id)
);

CREATE TABLE warehouses (
    id         INT AUTO_INCREMENT PRIMARY KEY,
    company_id INT          NOT NULL,
    name       VARCHAR(100) NOT NULL,
    city       VARCHAR(100),
    country    VARCHAR(100),
    is_active  TINYINT(1) NOT NULL DEFAULT 1,
    FOREIGN KEY (company_id) REFERENCES companies(id)
);

-- Level 2: FK to level 1
CREATE TABLE employees (
    id            INT AUTO_INCREMENT PRIMARY KEY,
    department_id INT          NOT NULL,
    manager_id    INT          NULL,
    first_name    VARCHAR(100) NOT NULL,
    last_name     VARCHAR(100) NOT NULL,
    email         VARCHAR(255) NOT NULL,
    title         VARCHAR(100),
    salary        DECIMAL(12,2),
    hired_at      TIMESTAMP NULL,
    status        ENUM('active','inactive','on_leave','terminated') NOT NULL DEFAULT 'active',
    FOREIGN KEY (department_id) REFERENCES departments(id),
    FOREIGN KEY (manager_id)    REFERENCES employees(id)
);

-- Add the deferred FK for departments.head_employee_id now that employees exists
ALTER TABLE departments ADD CONSTRAINT fk_dept_head
    FOREIGN KEY (head_employee_id) REFERENCES employees(id);

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
CREATE TABLE projects (
    id            INT AUTO_INCREMENT PRIMARY KEY,
    department_id INT          NOT NULL,
    lead_id       INT          NOT NULL,
    name          VARCHAR(255) NOT NULL,
    status        ENUM('planning','active','on_hold','completed','cancelled') NOT NULL DEFAULT 'planning',
    start_date    DATE,
    end_date      DATE,
    budget        DECIMAL(15,2),
    FOREIGN KEY (department_id) REFERENCES departments(id),
    FOREIGN KEY (lead_id)       REFERENCES employees(id)
);

CREATE TABLE inventory (
    id           INT AUTO_INCREMENT PRIMARY KEY,
    product_id   INT       NOT NULL,
    warehouse_id INT       NOT NULL,
    quantity     INT       NOT NULL DEFAULT 0,
    reserved_qty INT       NOT NULL DEFAULT 0,
    last_updated TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (product_id)   REFERENCES products(id),
    FOREIGN KEY (warehouse_id) REFERENCES warehouses(id)
);

CREATE TABLE purchase_orders (
    id           INT AUTO_INCREMENT PRIMARY KEY,
    supplier_id  INT          NOT NULL,
    approved_by  INT          NULL,
    status       ENUM('draft','submitted','approved','received','cancelled') NOT NULL DEFAULT 'draft',
    total_amount DECIMAL(15,2),
    ordered_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (supplier_id) REFERENCES suppliers(id),
    FOREIGN KEY (approved_by) REFERENCES employees(id)
);

CREATE TABLE support_tickets (
    id          INT AUTO_INCREMENT PRIMARY KEY,
    user_id     INT          NOT NULL,
    assigned_to INT          NULL,
    order_id    INT          NULL,
    subject     VARCHAR(255) NOT NULL,
    body        TEXT,
    status      ENUM('open','in_progress','resolved','closed')    NOT NULL DEFAULT 'open',
    priority    ENUM('low','medium','high','critical')            NOT NULL DEFAULT 'medium',
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (user_id)     REFERENCES users(id),
    FOREIGN KEY (assigned_to) REFERENCES employees(id),
    FOREIGN KEY (order_id)    REFERENCES orders(id)
);

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

CREATE TABLE audit_logs (
    id            INT AUTO_INCREMENT PRIMARY KEY,
    user_id       INT          NULL,
    employee_id   INT          NULL,
    action        VARCHAR(100),
    resource_type VARCHAR(100),
    resource_id   INT,
    old_value     JSON,
    new_value     JSON,
    ip_address    VARCHAR(45),
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (user_id)     REFERENCES users(id),
    FOREIGN KEY (employee_id) REFERENCES employees(id)
);

-- Level 4: deepest FK chains
CREATE TABLE project_assignments (
    id           INT AUTO_INCREMENT PRIMARY KEY,
    project_id   INT       NOT NULL,
    employee_id  INT       NOT NULL,
    role         ENUM('lead','developer','designer','qa','manager') NOT NULL DEFAULT 'developer',
    assigned_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at TIMESTAMP NULL,
    FOREIGN KEY (project_id)  REFERENCES projects(id),
    FOREIGN KEY (employee_id) REFERENCES employees(id)
);

CREATE TABLE purchase_order_items (
    id                INT AUTO_INCREMENT PRIMARY KEY,
    purchase_order_id INT           NOT NULL,
    product_id        INT           NOT NULL,
    quantity          INT           NOT NULL DEFAULT 1,
    unit_cost         DECIMAL(10,2) NOT NULL,
    FOREIGN KEY (purchase_order_id) REFERENCES purchase_orders(id),
    FOREIGN KEY (product_id)        REFERENCES products(id)
);

CREATE TABLE return_requests (
    id            INT AUTO_INCREMENT PRIMARY KEY,
    order_item_id INT           NOT NULL,
    processed_by  INT           NULL,
    reason        TEXT,
    status        ENUM('pending','approved','rejected','refunded') NOT NULL DEFAULT 'pending',
    refund_amount DECIMAL(10,2),
    requested_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (order_item_id) REFERENCES order_items(id),
    FOREIGN KEY (processed_by)  REFERENCES employees(id)
);

SET FOREIGN_KEY_CHECKS = 1;
