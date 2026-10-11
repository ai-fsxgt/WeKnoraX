-- Down migration: drop the 1536-dim HNSW partial index added in 000094.
DROP INDEX IF EXISTS embeddings_embedding_idx_1536;
