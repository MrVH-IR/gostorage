package service

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type State struct{ DB *sql.DB }

func (s State) MarkChunkStored(ctx context.Context, id, sha string) error {
	result, err := s.DB.ExecContext(ctx, `UPDATE chunks SET status='stored',sha256=$2,stored_at=NOW(),updated_at=NOW() WHERE id=$1 AND status='pending'`, id, sha)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("chunk is not pending")
	}
	return nil
}
func (s State) MarkChunkDeleted(ctx context.Context, id string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var nodeID string
	var size int64
	err = tx.QueryRowContext(ctx, `UPDATE chunks SET status='deleted',updated_at=NOW() WHERE id=$1 AND status<>'deleted' RETURNING storage_node_id,size_bytes`, id).Scan(&nodeID, &size)
	if err == sql.ErrNoRows {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE storage_nodes SET used_bytes=GREATEST(0,used_bytes-$2),updated_at=NOW() WHERE id=$1`, nodeID, size); err != nil {
		return err
	}
	return tx.Commit()
}
func (s State) CompleteUpload(ctx context.Context, tx *sql.Tx, uploadID, fileID, digest string) error {
	result, err := tx.ExecContext(ctx, `UPDATE uploads SET status='completed',completed_at=NOW(),updated_at=NOW() WHERE id=$1 AND status='uploading' AND (SELECT COUNT(*) FROM chunks WHERE upload_id=$1 AND status='stored')=expected_chunks`, uploadID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("upload has incomplete chunks or invalid state")
	}
	result, err = tx.ExecContext(ctx, `UPDATE files SET status='active',sha256=$2 WHERE id=$1 AND status='unavailable'`, fileID, digest)
	if err != nil {
		return err
	}
	n, err = result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("file cannot become active")
	}
	return nil
}
func (s State) FailUpload(ctx context.Context, uploadID, fileID, chunkID, message string) error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := s.DB.BeginTx(cleanupCtx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(cleanupCtx, `UPDATE uploads SET status='failed',error_message=$2,updated_at=NOW() WHERE id=$1 AND status='uploading'`, uploadID, message)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(cleanupCtx, `UPDATE files SET status='unavailable' WHERE id=$1 AND status='unavailable'`, fileID)
	if err != nil {
		return err
	}
	if chunkID != "" {
		_, err = tx.ExecContext(cleanupCtx, `UPDATE chunks SET status='failed',error_message=$2,updated_at=NOW() WHERE id=$1 AND status='pending'`, chunkID, message)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s State) BeginDelete(ctx context.Context, tx *sql.Tx, fileID string) error {
	_, err := tx.ExecContext(ctx, `UPDATE files SET status='deleting' WHERE id=$1 AND status<>'deleted'`, fileID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE uploads SET status='deleting',updated_at=NOW() WHERE file_id=$1 AND status NOT IN ('deleted','deleting')`, fileID)
	return err
}

func (s State) ClaimCleanup(ctx context.Context, uploadID string) (string, bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	var fileID string
	err = tx.QueryRowContext(ctx, `UPDATE uploads SET status='deleting',updated_at=NOW() WHERE id=$1 AND ((status IN ('pending','uploading','failed') AND expires_at<NOW()) OR (status='deleting' AND updated_at<NOW()-INTERVAL '2 minutes')) RETURNING file_id`, uploadID).Scan(&fileID)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE files SET status='deleting' WHERE id=$1 AND status IN ('unavailable','deleting')`, fileID); err != nil {
		return "", false, err
	}
	return fileID, true, tx.Commit()
}
func (s State) CompleteDelete(ctx context.Context, fileID string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE uploads SET status='deleted',updated_at=NOW() WHERE file_id=$1 AND status='deleting'`, fileID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE files SET status='deleted',deleted_at=NOW() WHERE id=$1 AND status='deleting'`, fileID); err != nil {
		return err
	}
	return tx.Commit()
}
func (s State) CompleteCleanup(ctx context.Context, uploadID, fileID string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var remaining int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks WHERE upload_id=$1 AND status<>'deleted'`, uploadID).Scan(&remaining); err != nil {
		return err
	}
	if remaining != 0 {
		return fmt.Errorf("upload still has undeleted chunks")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE uploads SET status='deleted',updated_at=NOW() WHERE id=$1 AND status IN ('pending','uploading','failed','deleting')`, uploadID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE files SET status='deleted',deleted_at=NOW() WHERE id=$1 AND status IN ('unavailable','deleting')`, fileID); err != nil {
		return err
	}
	return tx.Commit()
}
