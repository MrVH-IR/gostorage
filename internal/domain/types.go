package domain

import "time"

type UploadStatus string

const (
	UploadPending   UploadStatus = "pending"
	Uploading       UploadStatus = "uploading"
	UploadCompleted UploadStatus = "completed"
	UploadFailed    UploadStatus = "failed"
	UploadDeleting  UploadStatus = "deleting"
	UploadDeleted   UploadStatus = "deleted"
)

type ChunkStatus string

const (
	ChunkPending  ChunkStatus = "pending"
	ChunkStored   ChunkStatus = "stored"
	ChunkFailed   ChunkStatus = "failed"
	ChunkDeleting ChunkStatus = "deleting"
	ChunkDeleted  ChunkStatus = "deleted"
)

type NodeStatus string

const (
	NodeActive   NodeStatus = "active"
	NodeDraining NodeStatus = "draining"
	NodeDisabled NodeStatus = "disabled"
)

type FileStatus string

const (
	FileActive      FileStatus = "active"
	FileDeleting    FileStatus = "deleting"
	FileDeleted     FileStatus = "deleted"
	FileUnavailable FileStatus = "unavailable"
)

type StorageNode struct {
	ID, Name, BaseURL        string
	Status                   NodeStatus
	CapacityBytes, UsedBytes int64
	LastHeartbeatAt          time.Time
}
type File struct {
	ID, OwnerID, Name, ContentType string
	SizeBytes                      int64
	SHA256                         string
	Status                         FileStatus
	CreatedAt                      time.Time
}
type Upload struct {
	ID, FileID     string
	Status         UploadStatus
	ExpectedSize   int64
	ExpectedChunks int
	ExpiresAt      time.Time
}
type Chunk struct {
	ID, UploadID, StorageNodeID string
	Index                       int
	Offset, Size                int64
	SHA256, StorageKey          string
	Status                      ChunkStatus
	UploadAttempts              int
}
