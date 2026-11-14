-- 004：知识库与文档。文档处理状态机由后台 worker 推进（后续功能接入）。

CREATE TABLE knowledge_bases (
    id                  BIGSERIAL PRIMARY KEY,
    owner_id            BIGINT      NOT NULL REFERENCES users(id),
    name                TEXT        NOT NULL,
    description         TEXT        NOT NULL DEFAULT '',
    embedding_model_id  BIGINT      REFERENCES model_profiles(id),
    embedding_dimension INTEGER,
    chunk_size          INTEGER     NOT NULL DEFAULT 600,
    chunk_overlap       INTEGER     NOT NULL DEFAULT 100,
    status              TEXT        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (owner_id, name)
);

COMMENT ON TABLE knowledge_bases IS '知识库：固定绑定一个 Embedding 模型与维度';

CREATE TABLE documents (
    id                BIGSERIAL PRIMARY KEY,
    knowledge_base_id BIGINT      NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    file_path         TEXT        NOT NULL DEFAULT '',
    display_name      TEXT        NOT NULL,
    content_hash      TEXT        NOT NULL,
    media_type        TEXT        NOT NULL,
    source            TEXT        NOT NULL DEFAULT 'upload',
    char_count        INTEGER     NOT NULL DEFAULT 0,
    status            TEXT        NOT NULL DEFAULT 'queued'
                      CHECK (status IN ('queued', 'parsing', 'embedding', 'ready', 'failed', 'disabled')),
    failure_reason    TEXT,
    retry_count       INTEGER     NOT NULL DEFAULT 0,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (knowledge_base_id, content_hash)
);

COMMENT ON TABLE documents IS '文档：仅存服务端生成的相对路径，展示名与内容分离；停用后不可检索';
