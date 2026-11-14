-- 001：基础表结构 —— pgvector 扩展、用户与审计事件。
-- 模型配置、知识库、文档、应用、考试等表由各自功能的后续迁移追加。

CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE users (
    id            BIGSERIAL PRIMARY KEY,
    login_name    TEXT        NOT NULL UNIQUE,
    password_hash TEXT        NOT NULL,
    role          TEXT        NOT NULL CHECK (role IN ('admin', 'teacher', 'student')),
    enabled       BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE users IS '平台用户：本期由初始化脚本创建，不提供注册';

CREATE TABLE audit_events (
    event_id      BIGSERIAL PRIMARY KEY,
    actor_id      BIGINT,
    action        TEXT        NOT NULL,
    resource_type TEXT        NOT NULL DEFAULT '',
    resource_id   TEXT        NOT NULL DEFAULT '',
    result        TEXT        NOT NULL,
    request_id    TEXT        NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_audit_events_actor_created
    ON audit_events (actor_id, created_at DESC);

COMMENT ON TABLE audit_events IS '关键操作留痕：提供基础留痕能力，不宣称防篡改审计';
