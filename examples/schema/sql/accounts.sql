-- accounts —— 账号主表（玩具示例）。
--
-- 登录中间件据 guid 反查本表确认身份：缺表 / 缺行会让所有登录态接口直接报错，
-- 故它是登录态入口测试的隐式前置（provision 建表 + seeder 造行）。
--
-- 这是“权威 schema”：约束（UNIQUE / 长度 / 默认值）必须与生产一致——provision 读本文件原样执行，
-- 测试才不会“对着一张约束更松的表”绿、却在生产因 char(12) 截断 / 唯一键冲突而炸。
-- DROP ... IF EXISTS 安全：provision 每 daemon 只执行一次，且容器每次全新。
DROP TABLE IF EXISTS accounts;
CREATE TABLE accounts (
    account_id  BIGINT       NOT NULL,
    guid        CHAR(12)     NOT NULL,            -- 定长 12：seeder 造 guid 必须限长，不能塞 32 位 UUID
    email       VARCHAR(128) NOT NULL DEFAULT '',
    status      TINYINT      NOT NULL DEFAULT 1,  -- 1=启用
    create_time INT          NOT NULL DEFAULT 0,
    PRIMARY KEY (account_id),
    UNIQUE KEY uk_guid (guid)                     -- seeder 用唯一值原语生成 guid，避免并发撞键
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
