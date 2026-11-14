-- 006：问答应用与运行记录。发布快照与草稿分离；运行记录含会话历史。

CREATE TABLE apps (
    id                 BIGSERIAL PRIMARY KEY,
    owner_id           BIGINT      NOT NULL REFERENCES users(id),
    name               TEXT        NOT NULL,
    status             TEXT        NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published', 'disabled')),
    draft_config       JSONB       NOT NULL,
    published_config   JSONB,
    published_revision INTEGER     NOT NULL DEFAULT 0,
    published_at       TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (owner_id, name)
);

COMMENT ON TABLE apps IS '教育问答应用：草稿与发布快照分离，发布后改草稿不影响已发布版本';

CREATE TABLE app_runs (
    id              BIGSERIAL PRIMARY KEY,
    app_id          BIGINT      NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    revision        INTEGER     NOT NULL DEFAULT 0,
    user_id         BIGINT      NOT NULL REFERENCES users(id),
    conversation_id TEXT        NOT NULL,
    mode            TEXT        NOT NULL DEFAULT 'published' CHECK (mode IN ('debug', 'published')),
    status          TEXT        NOT NULL DEFAULT 'running'
                    CHECK (status IN ('running', 'succeeded', 'failed', 'canceled')),
    error_code      TEXT,
    question        TEXT        NOT NULL DEFAULT '',
    duration_ms     INTEGER,
    started_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at     TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_app_runs_conversation
    ON app_runs (app_id, user_id, conversation_id, started_at DESC);

COMMENT ON TABLE app_runs IS '应用运行记录：revision 记录所用配置快照版本，debug 模式 revision 为 0';

CREATE TABLE app_messages (
    id              BIGSERIAL PRIMARY KEY,
    run_id          BIGINT      NOT NULL REFERENCES app_runs(id) ON DELETE CASCADE,
    app_id          BIGINT      NOT NULL,
    user_id         BIGINT      NOT NULL,
    conversation_id TEXT        NOT NULL,
    role            TEXT        NOT NULL CHECK (role IN ('user', 'assistant')),
    content         TEXT        NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_app_messages_conversation
    ON app_messages (app_id, user_id, conversation_id, id);

COMMENT ON TABLE app_messages IS '短期会话历史：仅成功运行的问答进入历史，供后续轮次作上下文';
