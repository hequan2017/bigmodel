-- 007：考试题库。仅单选与判断题；标准答案只保存在教师侧。

CREATE TABLE exam_questions (
    id             BIGSERIAL PRIMARY KEY,
    owner_id       BIGINT      NOT NULL REFERENCES users(id),
    type           TEXT        NOT NULL CHECK (type IN ('single', 'judge')),
    stem           TEXT        NOT NULL,
    options        JSONB,      -- 单选题选项 [{"key":"A","text":"..."}]；判断题为 NULL
    correct_answer TEXT        NOT NULL,
    default_score  INTEGER     NOT NULL CHECK (default_score >= 0),
    level_code     TEXT        NOT NULL DEFAULT '',
    tags           TEXT[]      NOT NULL DEFAULT '{}',
    status         TEXT        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_exam_questions_owner ON exam_questions (owner_id, status);

COMMENT ON TABLE exam_questions IS '客观题库：题型与答案在服务端校验；判分使用试卷快照而非本表';
