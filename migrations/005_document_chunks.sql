-- 005：文档片段表。embedding 列不固定维度：P0 演示规模使用精确近邻查询，
-- 维度一致性由应用层校验（同一知识库内所有片段与查询向量同维）；
-- 后续引入近似索引时再按实际维度迁移为 vector(D)。

CREATE TABLE IF NOT EXISTS document_chunks (
    id              BIGSERIAL PRIMARY KEY,
    document_id     BIGINT      NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    chunk_index     INTEGER     NOT NULL,
    content         TEXT        NOT NULL,
    start_offset    INTEGER     NOT NULL,
    end_offset      INTEGER     NOT NULL,
    heading         TEXT        NOT NULL DEFAULT '',
    embedding       vector      NOT NULL,
    embedding_model TEXT        NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (document_id, chunk_index)
);

CREATE INDEX IF NOT EXISTS idx_document_chunks_document ON document_chunks (document_id);

COMMENT ON TABLE document_chunks IS '文档片段：offset 为规范化文本的 rune 位置，供引用回溯原文';
