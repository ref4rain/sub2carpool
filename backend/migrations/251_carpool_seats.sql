-- 拼车车位：成员按百分比分配账号周额度，额度写到各自订阅的周限额覆盖上；
-- 车位与拼车分组订阅双向绑定（分配订阅即占车位，加车位即分配订阅）。

-- 订阅级周限额覆盖：nil 表示沿用分组周限额
ALTER TABLE user_subscriptions
    ADD COLUMN IF NOT EXISTS weekly_limit_usd DECIMAL(20,8);
COMMENT ON COLUMN user_subscriptions.weekly_limit_usd IS '订阅级周限额覆盖（USD），NULL 沿用分组 weekly_limit_usd；拼车按车位比例写入';

ALTER TABLE carpool_members
    ADD COLUMN IF NOT EXISTS share_percent DECIMAL(6,2) NOT NULL DEFAULT 0;
COMMENT ON COLUMN carpool_members.share_percent IS '该车位可用账号周额度的百分比（0-100），全车合计不超过 100';

-- 旧数据按「(1 - 预留比例) / 座位数」折算成均分比例
UPDATE carpool_members m
SET share_percent = ROUND((100 * (1 - c.reserve_ratio) / GREATEST(c.seats, 1))::numeric, 2)
FROM carpools c
WHERE c.id = m.carpool_id AND m.share_percent = 0;

-- 预留改由「未分配的百分比」表达
ALTER TABLE carpools DROP COLUMN IF EXISTS reserve_ratio;

ALTER TABLE carpool_members
    ADD CONSTRAINT carpool_members_share_range CHECK (share_percent >= 0 AND share_percent <= 100);
