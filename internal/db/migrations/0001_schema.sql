-- 咖啡连锁外卖出杯时效与骑手交接服务 - 数据库结构
-- PostgreSQL 13+

-- 封签码全局序列、门店出杯顺序全局队列序列
CREATE SEQUENCE IF NOT EXISTS seal_code_seq;
CREATE SEQUENCE IF NOT EXISTS cup_queue_seq;

CREATE TABLE IF NOT EXISTS regions (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS stores (
    id                        BIGSERIAL PRIMARY KEY,
    region_id                 BIGINT NOT NULL REFERENCES regions(id),
    code                      TEXT UNIQUE NOT NULL,
    name                      TEXT NOT NULL,
    address                   TEXT,
    hourly_capacity           INT NOT NULL DEFAULT 80,        -- 产能：杯/小时
    overload_queue_threshold  INT NOT NULL DEFAULT 12,        -- 爆单阈值：在制+排队杯数
    stale_after_secs          INT NOT NULL DEFAULT 900,       -- 待取区放置过久阈值（秒）
    handover_sla_secs         INT NOT NULL DEFAULT 900,       -- 出杯时效SLA：下单→入待取区（秒）
    acceptance                TEXT NOT NULL DEFAULT 'open'
                              CHECK (acceptance IN ('open','throttled','paused')),
    throttled_rate            NUMERIC(5,2) NOT NULL DEFAULT 0.5,
    status                    TEXT NOT NULL DEFAULT 'active'
                              CHECK (status IN ('active','closed')),
    updated_at                TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS platforms (
    id    BIGSERIAL PRIMARY KEY,
    code  TEXT UNIQUE NOT NULL,   -- meituan / eleme / douyin
    name  TEXT NOT NULL
);

-- 统一账号：区域经理 / 督导 / 门店店员(店长·咖啡师·打包) / 外卖平台客服 / 财务
CREATE TABLE IF NOT EXISTS users (
    id           BIGSERIAL PRIMARY KEY,
    username     TEXT UNIQUE NOT NULL,
    password     TEXT NOT NULL,              -- bcrypt hash
    display_name TEXT NOT NULL,
    role         TEXT NOT NULL CHECK (role IN
                   ('admin','area_manager','supervisor','store_staff',
                    'platform_support','finance')),
    staff_title  TEXT,                       -- 店员岗位：店长/咖啡师/打包员
    region_id    BIGINT REFERENCES regions(id),
    store_id     BIGINT REFERENCES stores(id),
    platform_id  BIGINT REFERENCES platforms(id),
    active       BOOLEAN NOT NULL DEFAULT true,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS riders (
    id          BIGSERIAL PRIMARY KEY,
    rider_code  TEXT UNIQUE NOT NULL,
    name        TEXT NOT NULL,
    phone       TEXT,
    platform_id BIGINT REFERENCES platforms(id)
);

CREATE TABLE IF NOT EXISTS customers (
    id          BIGSERIAL PRIMARY KEY,
    access_code TEXT UNIQUE NOT NULL,        -- 演示用登录码
    name        TEXT,
    phone       TEXT
);

CREATE TABLE IF NOT EXISTS ingredients (
    id    BIGSERIAL PRIMARY KEY,
    name  TEXT NOT NULL,
    unit  TEXT NOT NULL
);

-- 美式 / 拿铁 / 冷萃 / 果咖 / 轻食 / 组合套餐
CREATE TABLE IF NOT EXISTS products (
    id             BIGSERIAL PRIMARY KEY,
    sku            TEXT UNIQUE NOT NULL,
    name           TEXT NOT NULL,
    category       TEXT NOT NULL CHECK (category IN
                     ('americano','latte','cold_brew','fruit_coffee','light_food','combo')),
    price          NUMERIC(10,2) NOT NULL,   -- 售价
    cost           NUMERIC(10,2) NOT NULL,   -- 单杯/份成本
    make_secs      INT NOT NULL DEFAULT 120,
    is_hot_drink   BOOLEAN NOT NULL DEFAULT false,
    needs_straw    BOOLEAN NOT NULL DEFAULT false,  -- 需要吸管
    needs_utensils BOOLEAN NOT NULL DEFAULT false,  -- 需要餐具
    bag_class      TEXT NOT NULL DEFAULT 'cold'
                     CHECK (bag_class IN ('hot','cold','food'))
);

CREATE TABLE IF NOT EXISTS recipes (
    product_id    BIGINT NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    ingredient_id BIGINT NOT NULL REFERENCES ingredients(id),
    qty           NUMERIC(10,3) NOT NULL,
    PRIMARY KEY (product_id, ingredient_id)
);

CREATE TABLE IF NOT EXISTS inventory (
    store_id      BIGINT NOT NULL REFERENCES stores(id),
    ingredient_id BIGINT NOT NULL REFERENCES ingredients(id),
    qty           NUMERIC(12,3) NOT NULL DEFAULT 0,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (store_id, ingredient_id)
);

CREATE TABLE IF NOT EXISTS stock_movements (
    id            BIGSERIAL PRIMARY KEY,
    store_id      BIGINT NOT NULL REFERENCES stores(id),
    ingredient_id BIGINT NOT NULL REFERENCES ingredients(id),
    delta         NUMERIC(12,3) NOT NULL,    -- 负=扣减，正=回补/补货
    reason        TEXT NOT NULL CHECK (reason IN
                    ('order_consume','remake_consume','loss_writeoff',
                     'replenish','cancel_release','shortage_release')),
    order_id      BIGINT,
    staff_id      BIGINT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS stock_losses (
    id            BIGSERIAL PRIMARY KEY,
    store_id      BIGINT NOT NULL REFERENCES stores(id),
    product_id    BIGINT REFERENCES products(id),
    qty           NUMERIC(10,2) NOT NULL DEFAULT 1,
    cost_amount   NUMERIC(10,2) NOT NULL,    -- 报损金额（进班组复盘）
    reason        TEXT NOT NULL CHECK (reason IN
                    ('remake_stale','wrong_cup','remark_missed','leak',
                     'cancel_in_progress','spoilage')),
    order_id      BIGINT,
    cup_id        BIGINT,
    incident_id   BIGINT,
    staff_id      BIGINT REFERENCES users(id),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS merge_groups (
    id         BIGSERIAL PRIMARY KEY,
    store_id   BIGINT NOT NULL REFERENCES stores(id),
    note       TEXT,
    created_by BIGINT REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS orders (
    id                 BIGSERIAL PRIMARY KEY,
    platform_id        BIGINT NOT NULL REFERENCES platforms(id),
    platform_order_no  TEXT NOT NULL,
    store_id           BIGINT NOT NULL REFERENCES stores(id),
    customer_id        BIGINT REFERENCES customers(id),
    rider_id           BIGINT REFERENCES riders(id),
    merge_group_id     BIGINT REFERENCES merge_groups(id),
    status             TEXT NOT NULL CHECK (status IN
                         ('accepted','oos_exception','brewing','ready',
                          'handed_over','completed','cancelled')),
    total_amount       NUMERIC(10,2) NOT NULL DEFAULT 0,
    total_cost         NUMERIC(10,2) NOT NULL DEFAULT 0,
    lost_margin        NUMERIC(10,2) NOT NULL DEFAULT 0, -- 平台取消损失的预期毛利
    ice_hot            TEXT CHECK (ice_hot IN ('ice','hot','normal')),
    sugar              TEXT,
    cup_size           TEXT,
    remark             TEXT,
    ordered_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    rider_eta          TIMESTAMPTZ,          -- 预计骑手到店
    accepted_at        TIMESTAMPTZ,
    rider_arrived_at   TIMESTAMPTZ,
    ready_at           TIMESTAMPTZ,
    handed_over_at     TIMESTAMPTZ,
    cancelled_at       TIMESTAMPTZ,
    cancel_reason      TEXT,
    expected_bags      INT,
    actual_bags        INT,
    hot_cold_split     BOOLEAN,
    straw_utensils_ok  BOOLEAN,
    sla_breach         BOOLEAN NOT NULL DEFAULT false,
    UNIQUE (platform_id, platform_order_no)
);

CREATE INDEX IF NOT EXISTS idx_orders_store_status ON orders(store_id, status);

CREATE TABLE IF NOT EXISTS order_items (
    id         BIGSERIAL PRIMARY KEY,
    order_id   BIGINT NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    product_id BIGINT NOT NULL REFERENCES products(id),
    qty        INT NOT NULL CHECK (qty > 0),
    unit_price NUMERIC(10,2) NOT NULL,
    ice_hot    TEXT CHECK (ice_hot IN ('ice','hot','normal')),
    sugar      TEXT,
    cup_size   TEXT,
    remark     TEXT,
    shortage   BOOLEAN NOT NULL DEFAULT false  -- 制作时发现轻食缺货
);

-- 每杯/每份一条：封签码、出杯顺序、状态、重做链
CREATE TABLE IF NOT EXISTS cups (
    id                 BIGSERIAL PRIMARY KEY,
    order_id           BIGINT NOT NULL REFERENCES orders(id),
    item_id            BIGINT NOT NULL REFERENCES order_items(id),
    seq_in_order       INT NOT NULL,
    seal_code          TEXT UNIQUE NOT NULL,
    status             TEXT NOT NULL DEFAULT 'queued'
                       CHECK (status IN ('queued','brewing','ready','picked_up','discarded')),
    queue_seq          BIGINT,                -- 门店出杯顺序
    made_by            BIGINT REFERENCES users(id),
    label_printed_at   TIMESTAMPTZ,
    brewing_started_at TIMESTAMPTZ,
    sealed_at          TIMESTAMPTZ,           -- 店员扫描封签
    into_pickup_at     TIMESTAMPTZ,           -- 进入待取区
    picked_up_at       TIMESTAMPTZ,
    discarded_at       TIMESTAMPTZ,
    stale_flagged_at   TIMESTAMPTZ,           -- 放置过久标记
    is_remake          BOOLEAN NOT NULL DEFAULT false,
    remake_of_cup_id   BIGINT REFERENCES cups(id),
    sla_breach         BOOLEAN NOT NULL DEFAULT false
);

CREATE INDEX IF NOT EXISTS idx_cups_store_status ON cups(status, queue_seq);

CREATE TABLE IF NOT EXISTS cup_label_prints (
    id         BIGSERIAL PRIMARY KEY,
    cup_id     BIGINT NOT NULL REFERENCES cups(id),
    staff_id   BIGINT REFERENCES users(id),
    printed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 异常事件：把门店/店员/骑手/顾客/平台客服/财务连到同一订单
CREATE TABLE IF NOT EXISTS incidents (
    id                BIGSERIAL PRIMARY KEY,
    order_id          BIGINT NOT NULL REFERENCES orders(id),
    cup_id            BIGINT REFERENCES cups(id),
    type              TEXT NOT NULL CHECK (type IN
                        ('rider_early_wait','remake_stale','remark_missed','wrong_cup',
                         'food_oos','wrong_bag','platform_cancel','leak_complaint',
                         'missing_utensils')),
    status            TEXT NOT NULL DEFAULT 'open'
                        CHECK (status IN ('open','in_process','resolved','closed')),
    responsible_party TEXT CHECK (responsible_party IN
                        ('store','staff','rider','platform','customer','none')),
    detail            TEXT,
    reported_by_role  TEXT,
    reported_by_id    BIGINT,
    store_id          BIGINT NOT NULL REFERENCES stores(id),
    support_user_id   BIGINT REFERENCES users(id),
    finance_user_id   BIGINT REFERENCES users(id),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at       TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_incidents_store ON incidents(store_id, status);

CREATE TABLE IF NOT EXISTS incident_threads (
    id          BIGSERIAL PRIMARY KEY,
    incident_id BIGINT NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
    author_role TEXT NOT NULL,   -- staff/rider/customer/support/finance/system
    author_name TEXT NOT NULL,
    message     TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS compensations (
    id           BIGSERIAL PRIMARY KEY,
    incident_id  BIGINT NOT NULL REFERENCES incidents(id),
    order_id     BIGINT NOT NULL REFERENCES orders(id),
    kind         TEXT NOT NULL CHECK (kind IN
                   ('customer_refund','customer_coupon','rider_wait',
                    'rework_cost','platform_penalty')),
    amount       NUMERIC(10,2) NOT NULL,
    bearer       TEXT NOT NULL CHECK (bearer IN ('store','platform','insurance')),
    status       TEXT NOT NULL DEFAULT 'proposed'
                 CHECK (status IN ('proposed','approved','paid','rejected')),
    proposed_by  BIGINT REFERENCES users(id),
    approved_by  BIGINT REFERENCES users(id),
    note         TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    approved_at  TIMESTAMPTZ,
    paid_at      TIMESTAMPTZ
);

-- 骑手到店交接核验记录（可多次尝试，取错袋时留痕）
CREATE TABLE IF NOT EXISTS handover_attempts (
    id                  BIGSERIAL PRIMARY KEY,
    order_id            BIGINT NOT NULL REFERENCES orders(id),
    rider_id            BIGINT REFERENCES riders(id),
    staff_id            BIGINT REFERENCES users(id),
    order_no_match      BOOLEAN NOT NULL,
    bags_match          BOOLEAN NOT NULL,
    hot_cold_split_ok   BOOLEAN NOT NULL,
    straw_utensils_ok   BOOLEAN NOT NULL,
    passed              BOOLEAN NOT NULL,
    note                TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS overload_events (
    id         BIGSERIAL PRIMARY KEY,
    store_id   BIGINT NOT NULL REFERENCES stores(id),
    type       TEXT NOT NULL CHECK (type IN
                 ('triggered','released','manual_pause','manual_resume')),
    queue_depth INT,
    note       TEXT,
    created_by BIGINT REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 缺货触发的临时下架
CREATE TABLE IF NOT EXISTS temporary_delistings (
    id          BIGSERIAL PRIMARY KEY,
    store_id    BIGINT NOT NULL REFERENCES stores(id),
    product_id  BIGINT NOT NULL REFERENCES products(id),
    reason      TEXT NOT NULL,
    active      BOOLEAN NOT NULL DEFAULT true,
    incident_id BIGINT REFERENCES incidents(id),
    created_by  BIGINT REFERENCES users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    restored_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS shifts (
    id         BIGSERIAL PRIMARY KEY,
    store_id   BIGINT NOT NULL REFERENCES stores(id),
    work_date  DATE NOT NULL,
    shift_name TEXT NOT NULL CHECK (shift_name IN ('morning','mid','evening')),
    leader_id  BIGINT REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (store_id, work_date, shift_name)
);

-- 班组评分与利润影响（事件发生时增量累计，重做成本进入复盘）
CREATE TABLE IF NOT EXISTS shift_metrics (
    shift_id           BIGINT PRIMARY KEY REFERENCES shifts(id) ON DELETE CASCADE,
    slow_orders        INT NOT NULL DEFAULT 0,   -- 出杯慢
    wrong_cup_count    INT NOT NULL DEFAULT 0,   -- 错杯
    missing_utensils   INT NOT NULL DEFAULT 0,   -- 漏餐具/吸管
    remake_count       INT NOT NULL DEFAULT 0,   -- 重做
    platform_cancels   INT NOT NULL DEFAULT 0,   -- 平台取消
    leak_count         INT NOT NULL DEFAULT 0,   -- 漏液投诉
    rider_wait_count   INT NOT NULL DEFAULT 0,
    remake_cost        NUMERIC(10,2) NOT NULL DEFAULT 0,
    compensation_cost  NUMERIC(10,2) NOT NULL DEFAULT 0,
    rider_wait_comp    NUMERIC(10,2) NOT NULL DEFAULT 0,
    cancel_loss        NUMERIC(10,2) NOT NULL DEFAULT 0,
    revenue            NUMERIC(10,2) NOT NULL DEFAULT 0,
    gross_profit       NUMERIC(10,2) NOT NULL DEFAULT 0,
    score              NUMERIC(5,2) NOT NULL DEFAULT 100,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS shift_reviews (
    id            BIGSERIAL PRIMARY KEY,
    shift_id      BIGINT NOT NULL REFERENCES shifts(id) ON DELETE CASCADE,
    supervisor_id BIGINT NOT NULL REFERENCES users(id),
    content       TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 区域经理：按门店按天的指标与利润影响（视图聚合三个班组）
CREATE OR REPLACE VIEW v_store_daily_metrics AS
SELECT
    sh.store_id,
    sh.work_date AS day,
    COALESCE(SUM(m.slow_orders),0)       AS slow_orders,
    COALESCE(SUM(m.wrong_cup_count),0)   AS wrong_cup_count,
    COALESCE(SUM(m.missing_utensils),0)  AS missing_utensils,
    COALESCE(SUM(m.remake_count),0)      AS remake_count,
    COALESCE(SUM(m.platform_cancels),0)  AS platform_cancels,
    COALESCE(SUM(m.leak_count),0)        AS leak_count,
    COALESCE(SUM(m.rider_wait_count),0)  AS rider_wait_count,
    COALESCE(SUM(m.remake_cost),0)       AS remake_cost,
    COALESCE(SUM(m.compensation_cost),0) AS compensation_cost,
    COALESCE(SUM(m.rider_wait_comp),0)   AS rider_wait_comp,
    COALESCE(SUM(m.cancel_loss),0)       AS cancel_loss,
    COALESCE(SUM(m.revenue),0)           AS revenue,
    COALESCE(SUM(m.gross_profit),0)      AS gross_profit,
    COALESCE(SUM(m.gross_profit - m.remake_cost - m.compensation_cost
                 - m.cancel_loss),0)     AS net_profit
FROM shifts sh
LEFT JOIN shift_metrics m ON m.shift_id = sh.id
GROUP BY sh.store_id, sh.work_date;
