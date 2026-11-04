-- 拼车：把一个 OpenAI 订阅账号按座位均分给多名用户。
-- 每个拼车绑定一个账号和一个自动创建的专属订阅分组；分组的 weekly_limit_usd 即每人周额度。
-- 周额度默认跟随账号 7 天窗口自动预估，拼车只维护分组周限额，其余分组配置（模型定价等）由管理员调整。
CREATE TABLE IF NOT EXISTS carpools (
    id                    BIGSERIAL PRIMARY KEY,
    name                  VARCHAR(100)   NOT NULL,
    account_id            BIGINT         NOT NULL,
    group_id              BIGINT         NOT NULL,
    weekly_quota_usd      DECIMAL(20,8)  NOT NULL DEFAULT 0,
    quota_source          VARCHAR(20)    NOT NULL DEFAULT 'auto',
    seats                 INT            NOT NULL DEFAULT 2,
    reserve_ratio         DECIMAL(10,4)  NOT NULL DEFAULT 0,
    validity_days         INT            NOT NULL DEFAULT 30,
    notes                 TEXT           NOT NULL DEFAULT '',
    status                VARCHAR(20)    NOT NULL DEFAULT 'active',
    estimated_weekly_usd  DECIMAL(20,8)  NOT NULL DEFAULT 0,
    window_reset_at       TIMESTAMPTZ,
    baseline_at           TIMESTAMPTZ,
    baseline_used_percent DECIMAL(10,4)  NOT NULL DEFAULT 0,
    created_by            BIGINT,
    created_at            TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    deleted_at            TIMESTAMPTZ,
    CONSTRAINT carpools_seats_range CHECK (seats BETWEEN 1 AND 10),
    CONSTRAINT carpools_reserve_ratio_range CHECK (reserve_ratio >= 0 AND reserve_ratio < 1),
    CONSTRAINT carpools_quota_source_check CHECK (quota_source IN ('auto', 'manual'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_carpools_account_active
    ON carpools (account_id) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_carpools_group_active
    ON carpools (group_id) WHERE deleted_at IS NULL;

COMMENT ON TABLE carpools IS '拼车：一个 OpenAI 账号按座位均分周额度';
COMMENT ON COLUMN carpools.weekly_quota_usd IS '当前生效的账号周额度（与成员扣费同口径），0 表示尚未预估、成员暂不限额；每人额度 = weekly_quota_usd * (1 - reserve_ratio) / seats';
COMMENT ON COLUMN carpools.quota_source IS '周额度来源：auto 跟随预估，manual 手动指定';
COMMENT ON COLUMN carpools.estimated_weekly_usd IS '最近一次有效的预估值 = 基线以来账号 actual_cost 合计 / 基线以来 7 天窗口上涨百分比';
COMMENT ON COLUMN carpools.window_reset_at IS '正在跟踪的上游 7 天窗口重置时间';
COMMENT ON COLUMN carpools.baseline_at IS '本窗口预估样本的起点';
COMMENT ON COLUMN carpools.baseline_used_percent IS '样本起点时账号 7 天窗口已用百分比，排除建车前的用量';

CREATE TABLE IF NOT EXISTS carpool_members (
    id              BIGSERIAL PRIMARY KEY,
    carpool_id      BIGINT       NOT NULL REFERENCES carpools(id) ON DELETE CASCADE,
    user_id         BIGINT       NOT NULL,
    subscription_id BIGINT,
    role            VARCHAR(20)  NOT NULL DEFAULT 'manager',
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT carpool_members_role_check CHECK (role IN ('manager', 'member'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_carpool_members_carpool_user
    ON carpool_members (carpool_id, user_id);
CREATE INDEX IF NOT EXISTS idx_carpool_members_user
    ON carpool_members (user_id);

COMMENT ON COLUMN carpool_members.role IS 'manager 可调整账号与额度（账号级管理员），member 只读';
