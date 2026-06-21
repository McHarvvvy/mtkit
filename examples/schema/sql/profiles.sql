-- profiles —— 用户资料表（玩具示例，承载昵称）。
--
-- setNickname 写本表并失效 Redis 缓存；getNickname 经缓存读穿回本表。
-- 用例靠“前门 set→get + 预读→改→再读”验证这条写后缓存失效链路（见 entry/http/profile_test.go）。
DROP TABLE IF EXISTS profiles;
CREATE TABLE profiles (
    account_id  BIGINT      NOT NULL,
    nickname    VARCHAR(64) NOT NULL DEFAULT '',
    update_time INT         NOT NULL DEFAULT 0,
    PRIMARY KEY (account_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
