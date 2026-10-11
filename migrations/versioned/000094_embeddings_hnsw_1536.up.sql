-- Migration 000094: HNSW index for 1536-dim embeddings
--
-- 000002 / 000059 only created HNSW partial indexes for dim=3584, 798 and
-- 1024. Deployments whose embedder outputs 1536-dim vectors (e.g. OpenAI
-- text-embedding-3-small / ada-002) fall through to a sequential scan of the
-- whole embeddings table per query; on large tables this exceeds the
-- multi-store retrieve timeout and surfaces as error 2201.
--
-- Fix: same partial-index pattern as 000059, dim=1536. Matches the exact
-- expression used in repository.go SearchEmbedding query
-- (embedding::halfvec(1536) <=> $1::halfvec(1536)).
--
-- Note: CONCURRENTLY is not used here because plpgsql DO blocks open an
-- implicit transaction. For production backfill the operator should build
-- the index manually using CREATE INDEX CONCURRENTLY before applying this
-- migration, then this block becomes idempotent via IF NOT EXISTS.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'embeddings') THEN
        IF NOT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = 'embeddings_embedding_idx_1536' OR indexname LIKE 'embeddings_embedding%1536%') THEN
            CREATE INDEX embeddings_embedding_idx_1536 ON embeddings
            USING hnsw ((embedding::halfvec(1536)) halfvec_cosine_ops)
            WITH (m = 16, ef_construction = 64)
            WHERE (dimension = 1536);
            RAISE NOTICE '[Migration 000094] Created HNSW index for dimension 1536';
        ELSE
            RAISE NOTICE '[Migration 000094] HNSW index for dimension 1536 already exists';
        END IF;
    ELSE
        RAISE NOTICE '[Migration 000094] embeddings table does not exist · skipping';
    END IF;
END $$;
