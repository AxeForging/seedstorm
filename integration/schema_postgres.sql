-- Teardown (always safe to re-run)
DROP TABLE IF EXISTS return_requests CASCADE;
DROP TABLE IF EXISTS audit_logs CASCADE;
DROP TABLE IF EXISTS support_tickets CASCADE;
DROP TABLE IF EXISTS project_assignments CASCADE;
DROP TABLE IF EXISTS purchase_order_items CASCADE;
DROP TABLE IF EXISTS purchase_orders CASCADE;
DROP TABLE IF EXISTS inventory CASCADE;
DROP TABLE IF EXISTS projects CASCADE;
DROP TABLE IF EXISTS employees CASCADE;
DROP TABLE IF EXISTS departments CASCADE;
DROP TABLE IF EXISTS warehouses CASCADE;
DROP TABLE IF EXISTS suppliers CASCADE;
DROP TABLE IF EXISTS companies CASCADE;
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
DROP TYPE IF EXISTS employee_status CASCADE;
DROP TYPE IF EXISTS project_status CASCADE;
DROP TYPE IF EXISTS assignment_role CASCADE;
DROP TYPE IF EXISTS po_status CASCADE;
DROP TYPE IF EXISTS ticket_status CASCADE;
DROP TYPE IF EXISTS ticket_priority CASCADE;
DROP TYPE IF EXISTS return_status CASCADE;
DROP TYPE IF EXISTS order_status CASCADE;
DROP TYPE IF EXISTS review_status CASCADE;
DROP TYPE IF EXISTS shipment_status CASCADE;
DROP TYPE IF EXISTS payment_method CASCADE;
DROP TYPE IF EXISTS payment_status CASCADE;
DROP TYPE IF EXISTS discount_type CASCADE;

-- Enum types (existing)
CREATE TYPE order_status    AS ENUM ('pending','processing','shipped','delivered','cancelled');
CREATE TYPE review_status   AS ENUM ('published','hidden','pending');
CREATE TYPE shipment_status AS ENUM ('pending','in_transit','delivered','failed');
CREATE TYPE payment_method  AS ENUM ('card','paypal','bank_transfer','crypto');
CREATE TYPE payment_status  AS ENUM ('pending','completed','failed','refunded');
CREATE TYPE discount_type   AS ENUM ('percentage','fixed');

-- Enum types (new)
CREATE TYPE employee_status AS ENUM ('active','inactive','on_leave','terminated');
CREATE TYPE project_status  AS ENUM ('planning','active','on_hold','completed','cancelled');
CREATE TYPE assignment_role AS ENUM ('lead','developer','designer','qa','manager');
CREATE TYPE po_status       AS ENUM ('draft','submitted','approved','received','cancelled');
CREATE TYPE ticket_status   AS ENUM ('open','in_progress','resolved','closed');
CREATE TYPE ticket_priority AS ENUM ('low','medium','high','critical');
CREATE TYPE return_status   AS ENUM ('pending','approved','rejected','refunded');

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

CREATE TABLE companies (
    id           SERIAL PRIMARY KEY,
    name         VARCHAR(255) NOT NULL,
    industry     VARCHAR(100),
    website      VARCHAR(255),
    founded_year INTEGER,
    is_active    BOOLEAN NOT NULL DEFAULT TRUE
);

CREATE TABLE suppliers (
    id      SERIAL PRIMARY KEY,
    name    VARCHAR(255) NOT NULL,
    email   VARCHAR(255),
    phone   VARCHAR(50),
    country VARCHAR(100),
    rating  NUMERIC(3,2)
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

-- departments: head_employee_id FK added after employees is created (near-cycle)
CREATE TABLE departments (
    id               SERIAL PRIMARY KEY,
    company_id       INTEGER        NOT NULL REFERENCES companies(id),
    parent_dept_id   INTEGER        REFERENCES departments(id),
    head_employee_id INTEGER,       -- FK constraint added after employees table created
    name             VARCHAR(100)   NOT NULL,
    budget           NUMERIC(15,2)
);

CREATE TABLE warehouses (
    id         SERIAL PRIMARY KEY,
    company_id INTEGER      NOT NULL REFERENCES companies(id),
    name       VARCHAR(100) NOT NULL,
    city       VARCHAR(100),
    country    VARCHAR(100),
    is_active  BOOLEAN NOT NULL DEFAULT TRUE
);

-- Level 2: FK to level 1
CREATE TABLE employees (
    id            SERIAL PRIMARY KEY,
    department_id INTEGER         NOT NULL REFERENCES departments(id),
    manager_id    INTEGER         REFERENCES employees(id),
    first_name    VARCHAR(100)    NOT NULL,
    last_name     VARCHAR(100)    NOT NULL,
    email         VARCHAR(255)    NOT NULL,
    title         VARCHAR(100),
    salary        NUMERIC(12,2),
    hired_at      TIMESTAMP,
    status        employee_status NOT NULL DEFAULT 'active'
);

-- Add the deferred FK for departments.head_employee_id now that employees exists
ALTER TABLE departments ADD CONSTRAINT fk_dept_head
    FOREIGN KEY (head_employee_id) REFERENCES employees(id);

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
CREATE TABLE projects (
    id            SERIAL PRIMARY KEY,
    department_id INTEGER        NOT NULL REFERENCES departments(id),
    lead_id       INTEGER        NOT NULL REFERENCES employees(id),
    name          VARCHAR(255)   NOT NULL,
    status        project_status NOT NULL DEFAULT 'planning',
    start_date    DATE,
    end_date      DATE,
    budget        NUMERIC(15,2)
);

CREATE TABLE inventory (
    id           SERIAL PRIMARY KEY,
    product_id   INTEGER   NOT NULL REFERENCES products(id),
    warehouse_id INTEGER   NOT NULL REFERENCES warehouses(id),
    quantity     INTEGER   NOT NULL DEFAULT 0,
    reserved_qty INTEGER   NOT NULL DEFAULT 0,
    last_updated TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE TABLE purchase_orders (
    id           SERIAL PRIMARY KEY,
    supplier_id  INTEGER   NOT NULL REFERENCES suppliers(id),
    approved_by  INTEGER   REFERENCES employees(id),
    status       po_status NOT NULL DEFAULT 'draft',
    total_amount NUMERIC(15,2),
    ordered_at   TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE TABLE support_tickets (
    id          SERIAL PRIMARY KEY,
    user_id     INTEGER         NOT NULL REFERENCES users(id),
    assigned_to INTEGER         REFERENCES employees(id),
    order_id    INTEGER         REFERENCES orders(id),
    subject     VARCHAR(255)    NOT NULL,
    body        TEXT,
    status      ticket_status   NOT NULL DEFAULT 'open',
    priority    ticket_priority NOT NULL DEFAULT 'medium',
    created_at  TIMESTAMP       NOT NULL DEFAULT NOW()
);

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

CREATE TABLE audit_logs (
    id            SERIAL PRIMARY KEY,
    user_id       INTEGER     REFERENCES users(id),
    employee_id   INTEGER     REFERENCES employees(id),
    action        VARCHAR(100),
    resource_type VARCHAR(100),
    resource_id   INTEGER,
    old_value     JSONB,
    new_value     JSONB,
    ip_address    INET,
    created_at    TIMESTAMP NOT NULL DEFAULT NOW()
);

-- Level 4: deepest FK chains
CREATE TABLE project_assignments (
    id          SERIAL PRIMARY KEY,
    project_id  INTEGER         NOT NULL REFERENCES projects(id),
    employee_id INTEGER         NOT NULL REFERENCES employees(id),
    role        assignment_role NOT NULL DEFAULT 'developer',
    assigned_at TIMESTAMP       NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMP
);

CREATE TABLE purchase_order_items (
    id                SERIAL PRIMARY KEY,
    purchase_order_id INTEGER       NOT NULL REFERENCES purchase_orders(id),
    product_id        INTEGER       NOT NULL REFERENCES products(id),
    quantity          INTEGER       NOT NULL DEFAULT 1,
    unit_cost         NUMERIC(10,2) NOT NULL
);

CREATE TABLE return_requests (
    id           SERIAL PRIMARY KEY,
    order_item_id INTEGER       NOT NULL REFERENCES order_items(id),
    processed_by  INTEGER       REFERENCES employees(id),
    reason        TEXT,
    status        return_status NOT NULL DEFAULT 'pending',
    refund_amount NUMERIC(10,2),
    requested_at  TIMESTAMP     NOT NULL DEFAULT NOW()
);
