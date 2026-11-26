package repository

import (
	"context"
	"database/sql"
	"gostorage/internal/domain"
	"time"
)

type Repository struct{ DB *sql.DB }

func (r Repository) RegisterNode(ctx context.Context, n domain.StorageNode) error {
	_, err := r.DB.ExecContext(ctx, `INSERT INTO storage_nodes(id,name,base_url,status,capacity_bytes,used_bytes,last_heartbeat_at) VALUES($1,$2,$3,'active',$4,0,NOW()) ON CONFLICT(id) DO UPDATE SET name=EXCLUDED.name,base_url=EXCLUDED.base_url,capacity_bytes=GREATEST(storage_nodes.used_bytes,EXCLUDED.capacity_bytes),last_heartbeat_at=NOW(),updated_at=NOW()`, n.ID, n.Name, n.BaseURL, n.CapacityBytes)
	return err
}
func (r Repository) ListHealthyNodes(ctx context.Context, ttl time.Duration, limit int) ([]domain.StorageNode, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT id,name,base_url,status,capacity_bytes,used_bytes,last_heartbeat_at FROM storage_nodes WHERE status='active' AND last_heartbeat_at>NOW()-$1::interval AND capacity_bytes>used_bytes ORDER BY capacity_bytes-used_bytes DESC,id LIMIT $2`, ttl.String(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var nodes []domain.StorageNode
	for rows.Next() {
		var n domain.StorageNode
		if err = rows.Scan(&n.ID, &n.Name, &n.BaseURL, &n.Status, &n.CapacityBytes, &n.UsedBytes, &n.LastHeartbeatAt); err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}
func (r Repository) GetUpload(ctx context.Context, id string) (domain.Upload, error) {
	var u domain.Upload
	err := r.DB.QueryRowContext(ctx, `SELECT id,file_id,status,expected_size,expected_chunks,expires_at FROM uploads WHERE id=$1`, id).Scan(&u.ID, &u.FileID, &u.Status, &u.ExpectedSize, &u.ExpectedChunks, &u.ExpiresAt)
	return u, err
}
func (r Repository) CreateUpload(ctx context.Context, u domain.Upload, cs []domain.Chunk) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO uploads(id,file_id,status,expected_size,expected_chunks,expires_at) VALUES($1,$2,$3,$4,$5,$6)`, u.ID, u.FileID, u.Status, u.ExpectedSize, u.ExpectedChunks, u.ExpiresAt)
	for _, c := range cs {
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO chunks(id,upload_id,storage_node_id,chunk_index,offset_bytes,size_bytes,status,storage_key) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, c.ID, c.UploadID, c.StorageNodeID, c.Index, c.Offset, c.Size, c.Status, c.StorageKey)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (r Repository) ListChunks(ctx context.Context, uploadID string) ([]domain.Chunk, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT id,upload_id,storage_node_id,chunk_index,offset_bytes,size_bytes,COALESCE(sha256,''),storage_key,status,upload_attempts FROM chunks WHERE upload_id=$1 ORDER BY chunk_index`, uploadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Chunk
	for rows.Next() {
		var c domain.Chunk
		if err = rows.Scan(&c.ID, &c.UploadID, &c.StorageNodeID, &c.Index, &c.Offset, &c.Size, &c.SHA256, &c.StorageKey, &c.Status, &c.UploadAttempts); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
