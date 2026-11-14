-- 009：作答记录与答案。每人每场一次；保存使用完整快照与版本控制。

CREATE TABLE exam_attempts (
    id              BIGSERIAL PRIMARY KEY,
    exam_id         BIGINT      NOT NULL REFERENCES exams(id) ON DELETE CASCADE,
    user_id         BIGINT      NOT NULL REFERENCES users(id),
    status          TEXT        NOT NULL DEFAULT 'in_progress'
                    CHECK (status IN ('in_progress', 'submitted')),
    started_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deadline_at     TIMESTAMPTZ NOT NULL,
    answer_revision INTEGER     NOT NULL DEFAULT 1,
    submitted_at    TIMESTAMPTZ,
    submit_reason   TEXT,
    total_score     INTEGER,
    UNIQUE (exam_id, user_id)
);

COMMENT ON TABLE exam_attempts IS '作答：deadline_at = min(开始时间+时长, close_at)，以数据库时间为准';

CREATE TABLE exam_answers (
    id           BIGSERIAL PRIMARY KEY,
    attempt_id   BIGINT      NOT NULL REFERENCES exam_attempts(id) ON DELETE CASCADE,
    exam_item_id BIGINT      NOT NULL REFERENCES exam_items(id) ON DELETE CASCADE,
    answer       TEXT        NOT NULL,
    saved_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (attempt_id, exam_item_id)
);

COMMENT ON TABLE exam_answers IS '已保存答案：每次保存整体替换快照，未保存的题目视为未作答';
