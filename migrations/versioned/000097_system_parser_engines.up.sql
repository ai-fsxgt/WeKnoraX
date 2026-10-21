CREATE TABLE IF NOT EXISTS system_parser_engines (
    engine VARCHAR(64) PRIMARY KEY,
    enabled BOOLEAN NOT NULL DEFAULT FALSE,
    config JSONB NOT NULL
);
