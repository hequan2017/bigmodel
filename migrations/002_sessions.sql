-- 002：会话表 —— 支持登录认证与登出。

CREATE TABLE IF NOT EXISTS user_sessions (
    id         BIGSERIAL PRIMARY KEY,
    user_id    BIGINT      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT        NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_user_sessions_user_id ON user_sessions (user_id);

COMMENT ON TABLE user_sessions IS '会话：仅存令牌 SHA-256 哈希，不存明文';
