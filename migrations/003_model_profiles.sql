-- 003：模型接入配置档案。端点与凭据引用来自部署配置，密钥正文不落库。

CREATE TABLE model_profiles (
    id                  BIGSERIAL PRIMARY KEY,
    name                TEXT        NOT NULL UNIQUE,
    kind                TEXT        NOT NULL CHECK (kind IN ('chat', 'embedding')),
    provider_type       TEXT        NOT NULL DEFAULT 'openai_compatible',
    model_name          TEXT        NOT NULL,
    base_url            TEXT        NOT NULL,
    credential_ref      TEXT        NOT NULL,
    embedding_dimension INTEGER,
    enabled             BOOLEAN     NOT NULL DEFAULT TRUE,
    config_version      INTEGER     NOT NULL DEFAULT 1,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE model_profiles IS '模型接入档案：展示与连通性验证用；本期不开放任意 URL 与密钥录入';
