package tgbot

import (
	"database/sql"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// State tracks which input-channel messages have been processed, so backfill
// and realtime never re-run a file. valid_count sentinels:
//
//	-1  inserted at job start, run did not finish (crash) — re-enqueued on startup
//	-2  run finished with a non-retryable error — not re-enqueued
//	>=0 finished successfully
type State struct {
	db *sql.DB
}

// NewState opens (creating if needed) the state DB at path and ensures the
// schema exists.
func NewState(path string) (*State, error) {
	db, err := sql.Open("sqlite3", "file:"+path+"?_journal_mode=WAL")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS processed (
			message_id   INTEGER PRIMARY KEY,
			channel_id   INTEGER NOT NULL,
			processed_at INTEGER NOT NULL,
			total_count  INTEGER NOT NULL DEFAULT 0,
			valid_count  INTEGER NOT NULL DEFAULT -1
		)`); err != nil {
		db.Close()
		return nil, err
	}
	return &State{db: db}, nil
}

// Has reports whether messageID already has a row (any status).
func (s *State) Has(messageID int64) (bool, error) {
	var one int
	err := s.db.QueryRow("SELECT 1 FROM processed WHERE message_id = ?", messageID).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// Insert records messageID at job start with valid_count = -1 (incomplete).
// Uses INSERT OR IGNORE so a duplicate enqueue is harmless.
func (s *State) Insert(messageID, channelID int64) error {
	_, err := s.db.Exec(
		"INSERT OR IGNORE INTO processed (message_id, channel_id, processed_at, valid_count) VALUES (?, ?, ?, -1)",
		messageID, channelID, time.Now().Unix())
	return err
}

// Complete marks a successful run with its counts.
func (s *State) Complete(messageID int64, total, valid int) error {
	_, err := s.db.Exec(
		"UPDATE processed SET total_count = ?, valid_count = ?, processed_at = ? WHERE message_id = ?",
		total, valid, time.Now().Unix(), messageID)
	return err
}

// MarkError records a non-retryable failure (valid_count = -2).
func (s *State) MarkError(messageID int64) error {
	_, err := s.db.Exec(
		"UPDATE processed SET valid_count = -2, processed_at = ? WHERE message_id = ?",
		time.Now().Unix(), messageID)
	return err
}

// IncompleteIDs returns message IDs whose run never finished (valid_count = -1).
func (s *State) IncompleteIDs() ([]int64, error) {
	rows, err := s.db.Query("SELECT message_id FROM processed WHERE valid_count = -1")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// DeleteIncomplete removes rows for runs that never finished (valid_count = -1)
// so they are reprocessed by the next backfill.
func (s *State) DeleteIncomplete() (int, error) {
	res, err := s.db.Exec("DELETE FROM processed WHERE valid_count = -1")
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// Close closes the underlying DB.
func (s *State) Close() error { return s.db.Close() }
