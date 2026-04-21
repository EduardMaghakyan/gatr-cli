package stripe

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// AuditEntry is one line of the append-only ~/.gatr/audit.log. The
// JSON encoding is stable (tested) so operators can grep / pipe to jq.
//
// A Replace DiffOp produces TWO entries: one for the archive, one for
// the create. Each carries the same YamlID and differentiates via
// Action ("archived" then "created").
type AuditEntry struct {
	Timestamp string   `json:"timestamp"`
	ProjectID string   `json:"project_id"`
	Resource  string   `json:"resource"` // product | price | meter
	Action    string   `json:"action"`   // created | updated | replaced | archived
	YamlID    string   `json:"yaml_id"`
	StripeID  string   `json:"stripe_id,omitempty"`
	Changes   []string `json:"changes,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// AuditWriter is how the apply pipeline records successes and
// failures. The file-backed FileAuditWriter is the production
// implementation; tests use an in-memory recorder.
type AuditWriter interface {
	Write(entry AuditEntry) error
}

// FileAuditWriter appends JSONL rows to ~/.gatr/audit.log (or a
// custom path). fsync after every write keeps the log durable across
// crashes — audit.log's whole purpose is to survive a partial apply.
type FileAuditWriter struct {
	path string

	mu sync.Mutex
	f  *os.File
}

// NewFileAuditWriter opens (creating if needed) the target file for
// append-only writes. Caller MUST call Close when done. path=="" uses
// the default ~/.gatr/audit.log; pass an explicit path in tests.
func NewFileAuditWriter(path string) (*FileAuditWriter, error) {
	if path == "" {
		resolved, err := DefaultAuditLogPath()
		if err != nil {
			return nil, err
		}
		path = resolved
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create audit dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open audit log: %w", err)
	}
	return &FileAuditWriter{path: path, f: f}, nil
}

// DefaultAuditLogPath returns ~/.gatr/audit.log. Useful for CLI
// commands that want to reference the path in error messages
// ("resume with: tail -n1 <path>...").
func DefaultAuditLogPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("could not resolve HOME for ~/.gatr/audit.log")
	}
	return filepath.Join(home, ".gatr", "audit.log"), nil
}

// Write serialises the entry and fsyncs. Returned error means the
// entry is NOT durable — the apply pipeline treats this as fatal
// because continuing without audit would let a subsequent crash hide
// completed ops.
func (w *FileAuditWriter) Write(entry AuditEntry) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	b, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("audit marshal: %w", err)
	}
	b = append(b, '\n')
	if _, err := w.f.Write(b); err != nil {
		return fmt.Errorf("audit write: %w", err)
	}
	if err := w.f.Sync(); err != nil {
		return fmt.Errorf("audit sync: %w", err)
	}
	return nil
}

// Close flushes + closes the underlying file. Safe to call multiple
// times; subsequent calls are no-ops.
func (w *FileAuditWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// Path returns the underlying file path — useful for CLI messages.
func (w *FileAuditWriter) Path() string { return w.path }

// ReadAuditLog loads all entries from path. Empty / missing file →
// ([], nil). Malformed JSON on any line → error with line number.
// Used by the apply pipeline to NOT do anything automatic — the log
// is advisory, not authoritative — but useful for tests that want to
// assert on durable state.
func ReadAuditLog(path string) ([]AuditEntry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read audit log: %w", err)
	}
	var entries []AuditEntry
	lineNo := 0
	for _, line := range splitLines(raw) {
		lineNo++
		if len(line) == 0 {
			continue
		}
		var e AuditEntry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("audit log line %d: %w", lineNo, err)
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// splitLines is json-encoding's internal byte-splitter reimplemented
// inline. Avoids bringing in bufio for a two-line helper.
func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			out = append(out, b[start:i])
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}
