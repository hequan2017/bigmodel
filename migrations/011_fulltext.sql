-- 011：全文检索通道与运行记录检索统计（§7.4 子集）。
-- fts 由应用侧中文分词（gse，纯 Go 无 cgo）生成空格分隔词元后经
-- to_tsvector('simple', …) 写入；'simple' 配置不做语言归一化。
-- 迁移内置回填使用 to_tsvector('simple', content)：PG 默认解析器对连续
-- 中文不切分，回填词元质量有限；教师端 POST /api/kb/{id}/reindex 可按
-- 分词结果重建全部片段索引。

ALTER TABLE document_chunks ADD COLUMN fts TSVECTOR;

CREATE INDEX IF NOT EXISTS idx_document_chunks_fts
    ON document_chunks USING GIN (fts);

UPDATE document_chunks SET fts = to_tsvector('simple', content);

COMMENT ON COLUMN document_chunks.fts
    IS '全文检索向量：词元由应用侧中文分词生成，reindex 端点可重建';

ALTER TABLE app_runs ADD COLUMN retrieval_stats JSONB;

COMMENT ON COLUMN app_runs.retrieval_stats
    IS '检索通道统计：每路候选数/耗时/降级原因（§7.4 多路召回流水线）';
