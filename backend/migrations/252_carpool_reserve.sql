-- 拼车整车预留：先从账号周额度里留出预留比例，剩余部分按各车位百分比（合计 100%）分配。
ALTER TABLE carpools
    ADD COLUMN IF NOT EXISTS reserve_ratio DECIMAL(10,4) NOT NULL DEFAULT 0;

ALTER TABLE carpools
    ADD CONSTRAINT carpools_reserve_ratio_range CHECK (reserve_ratio >= 0 AND reserve_ratio < 1);

COMMENT ON COLUMN carpools.reserve_ratio IS '整车预留比例（0-1）；车位额度 = weekly_quota_usd * (1 - reserve_ratio) * share_percent / 100';
