-- 008：考试与试卷条目。发布后使用快照判分，不回读题库原题。

CREATE TABLE exams (
    id               BIGSERIAL PRIMARY KEY,
    owner_id         BIGINT      NOT NULL REFERENCES users(id),
    title            TEXT        NOT NULL,
    level_code       TEXT        NOT NULL DEFAULT '',
    status           TEXT        NOT NULL DEFAULT 'draft'
                     CHECK (status IN ('draft', 'published', 'closed', 'disabled')),
    open_at          TIMESTAMPTZ,
    close_at         TIMESTAMPTZ,
    duration_seconds INTEGER,
    published_at     TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (owner_id, title)
);

COMMENT ON TABLE exams IS '考试：发布时冻结时间窗口与时长；修改已发布考试须另建一场';

CREATE TABLE exam_items (
    id                 BIGSERIAL PRIMARY KEY,
    exam_id            BIGINT      NOT NULL REFERENCES exams(id) ON DELETE CASCADE,
    source_question_id BIGINT      NOT NULL REFERENCES exam_questions(id),
    order_no           INTEGER     NOT NULL,
    type               TEXT        NOT NULL,
    stem               TEXT        NOT NULL,
    options            JSONB,
    correct_answer     TEXT        NOT NULL,
    score              INTEGER     NOT NULL CHECK (score >= 0),
    UNIQUE (exam_id, order_no)
);

CREATE INDEX IF NOT EXISTS idx_exam_items_exam ON exam_items (exam_id, order_no);

COMMENT ON TABLE exam_items IS '试卷条目：发布快照包含题面/选项/答案；答案绝不返回给学生接口';
