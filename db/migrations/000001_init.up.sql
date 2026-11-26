-- PostgreSQL schema for v1.0.
-- UUID generation is performed by the application; no extension is required.

CREATE TABLE users (
    id UUID PRIMARY KEY,
    email VARCHAR(320) NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE files (
    id UUID PRIMARY KEY,
    owner_id UUID NOT NULL REFERENCES users(id),
    original_name VARCHAR(255) NOT NULL,
    content_type VARCHAR(255) NOT NULL,
    size_bytes BIGINT NOT NULL CHECK (size_bytes > 0),
    sha256 CHAR(64) NOT NULL,
    status VARCHAR(20) NOT NULL CHECK (status IN ('active', 'deleting', 'deleted', 'unavailable')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE TABLE uploads (
    id UUID PRIMARY KEY,
    file_id UUID NOT NULL REFERENCES files(id),
    status VARCHAR(20) NOT NULL CHECK (status IN ('pending', 'uploading', 'completed', 'failed', 'deleting', 'deleted')),
    expected_size BIGINT NOT NULL CHECK (expected_size > 0),
    expected_chunks INTEGER NOT NULL CHECK (expected_chunks >= 2),
    expires_at TIMESTAMPTZ NOT NULL,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    error_message TEXT
);

CREATE TABLE storage_nodes (
    id UUID PRIMARY KEY,
    name VARCHAR(100) NOT NULL UNIQUE,
    base_url VARCHAR(500) NOT NULL UNIQUE,
    status VARCHAR(20) NOT NULL CHECK (status IN ('active', 'draining', 'disabled')),
    capacity_bytes BIGINT NOT NULL CHECK (capacity_bytes > 0),
    used_bytes BIGINT NOT NULL DEFAULT 0 CHECK (used_bytes >= 0),
    last_heartbeat_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (used_bytes <= capacity_bytes)
);

CREATE TABLE chunks (
    id UUID PRIMARY KEY,
    upload_id UUID NOT NULL REFERENCES uploads(id) ON DELETE CASCADE,
    storage_node_id UUID NOT NULL REFERENCES storage_nodes(id),
    chunk_index INTEGER NOT NULL CHECK (chunk_index >= 0),
    offset_bytes BIGINT NOT NULL CHECK (offset_bytes >= 0),
    size_bytes BIGINT NOT NULL CHECK (size_bytes > 0),
    sha256 CHAR(64),
    status VARCHAR(20) NOT NULL CHECK (status IN ('pending', 'stored', 'failed', 'deleting', 'deleted')),
    storage_key VARCHAR(100) NOT NULL UNIQUE,
    upload_attempts INTEGER NOT NULL DEFAULT 0 CHECK (upload_attempts >= 0),
    stored_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    error_message TEXT,
    UNIQUE (upload_id, chunk_index)
);

CREATE INDEX idx_uploads_status_expires
    ON uploads (status, expires_at);

CREATE INDEX idx_nodes_status_heartbeat
    ON storage_nodes (status, last_heartbeat_at);

CREATE INDEX idx_chunks_upload_order
    ON chunks (upload_id, chunk_index);

CREATE INDEX idx_chunks_node_status
    ON chunks (storage_node_id, status);
