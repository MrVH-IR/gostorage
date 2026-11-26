package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"time"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-8][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

type Config struct {
	DatabaseURL, ListenAddr, NodeID, NodeName, NodeAddr, StorageDir, InternalToken, CoordinatorURL string
	MaxFileBytes                                                                                   int64
	MaxNodes                                                                                       int
	MaxConcurrentUploads                                                                           int
	NodeCapacityBytes                                                                              int64
	HeartbeatTTL, RequestTimeout                                                                   time.Duration
}

func Load(role string) (Config, error) {
	c := Config{DatabaseURL: os.Getenv("DATABASE_URL"), ListenAddr: env("LISTEN_ADDR", ":8080"), NodeID: os.Getenv("NODE_ID"), NodeName: os.Getenv("NODE_NAME"), NodeAddr: os.Getenv("NODE_ADDR"), StorageDir: env("STORAGE_DIR", "./data"), InternalToken: os.Getenv("INTERNAL_TOKEN"), CoordinatorURL: os.Getenv("COORDINATOR_URL"), MaxFileBytes: 100 << 20, MaxNodes: 32, MaxConcurrentUploads: 4, NodeCapacityBytes: 1 << 30, HeartbeatTTL: 30 * time.Second, RequestTimeout: 2 * time.Minute}
	if c.DatabaseURL == "" {
		c.DatabaseURL = "postgres://gostorage:gostorage@localhost:5432/gostorage_db?sslmode=disable"
	}
	dbURL, e := url.Parse(c.DatabaseURL)
	if e != nil || (dbURL.Scheme != "postgres" && dbURL.Scheme != "postgresql") || dbURL.Host == "" {
		return c, fmt.Errorf("DATABASE_URL must be a PostgreSQL URL")
	}
	if _, _, e = net.SplitHostPort(c.ListenAddr); e != nil {
		return c, fmt.Errorf("LISTEN_ADDR must include a host and port")
	}
	if v := os.Getenv("MAX_FILE_BYTES"); v != "" {
		n, e := strconv.ParseInt(v, 10, 64)
		if e != nil || n <= 0 || n > 10<<30 {
			return c, fmt.Errorf("MAX_FILE_BYTES must be between 1 and 10 GiB")
		}
		c.MaxFileBytes = n
	}
	if v := os.Getenv("MAX_NODES"); v != "" {
		n, e := strconv.Atoi(v)
		if e != nil || n < 2 || n > 256 {
			return c, fmt.Errorf("MAX_NODES must be between 2 and 256")
		}
		c.MaxNodes = n
	}
	if v := os.Getenv("MAX_CONCURRENT_UPLOADS"); v != "" {
		n, e := strconv.Atoi(v)
		if e != nil || n < 1 || n > 128 {
			return c, fmt.Errorf("MAX_CONCURRENT_UPLOADS must be between 1 and 128")
		}
		c.MaxConcurrentUploads = n
	}
	if v := os.Getenv("HEARTBEAT_TTL"); v != "" {
		d, e := time.ParseDuration(v)
		if e != nil || d <= 0 {
			return c, fmt.Errorf("invalid HEARTBEAT_TTL")
		}
		c.HeartbeatTTL = d
	}
	if v := os.Getenv("REQUEST_TIMEOUT"); v != "" {
		d, e := time.ParseDuration(v)
		if e != nil || d <= 0 {
			return c, fmt.Errorf("invalid REQUEST_TIMEOUT")
		}
		c.RequestTimeout = d
	}

	if role == "storage" && (c.NodeID == "" || c.NodeName == "" || c.NodeAddr == "" || c.InternalToken == "" || c.CoordinatorURL == "") {
		return c, fmt.Errorf("storage node requires NODE_ID, NODE_NAME, NODE_ADDR, INTERNAL_TOKEN and COORDINATOR_URL")
	}
	if role == "storage" {
		if !uuidPattern.MatchString(c.NodeID) {
			return c, fmt.Errorf("NODE_ID must be a UUID")
		}
		nodeURL, e := url.ParseRequestURI(c.NodeAddr)
		if e != nil || (nodeURL.Scheme != "http" && nodeURL.Scheme != "https") || nodeURL.Host == "" || nodeURL.User != nil || nodeURL.RawQuery != "" || nodeURL.Fragment != "" {
			return c, fmt.Errorf("invalid NODE_ADDR")
		}
		coordinatorURL, e := url.ParseRequestURI(c.CoordinatorURL)
		if e != nil || (coordinatorURL.Scheme != "http" && coordinatorURL.Scheme != "https") || coordinatorURL.Host == "" || coordinatorURL.User != nil || coordinatorURL.RawQuery != "" || coordinatorURL.Fragment != "" {
			return c, fmt.Errorf("invalid COORDINATOR_URL")
		}
	}
	if v := os.Getenv("NODE_CAPACITY_BYTES"); v != "" {
		n, e := strconv.ParseInt(v, 10, 64)
		if e != nil || n <= 0 {
			return c, fmt.Errorf("invalid NODE_CAPACITY_BYTES")
		}
		c.NodeCapacityBytes = n
	}
	if role == "coordinator" && c.InternalToken == "" {
		return c, fmt.Errorf("INTERNAL_TOKEN is required")
	}
	if (role == "storage" || role == "coordinator") && len(c.InternalToken) < 16 {
		return c, fmt.Errorf("INTERNAL_TOKEN must contain at least 16 characters")
	}
	return c, nil
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
