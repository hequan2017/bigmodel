-- 010：逐题得分。判分在交卷/到期结算时写入。

ALTER TABLE exam_answers ADD COLUMN IF NOT EXISTS awarded_score INTEGER NOT NULL DEFAULT 0;
