CREATE TABLE IF NOT EXISTS system_parser_engines (
    engine TEXT PRIMARY KEY,
    enabled BOOLEAN NOT NULL DEFAULT FALSE,
    config TEXT NOT NULL
);
