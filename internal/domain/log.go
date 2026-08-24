package domain

import "time"

// LogEntry is one log line, after it has crossed from the wire (LogEntry in
// log.proto) into plain Go. OccurredAt is the event time the client sent;
// CreatedAt is filled in by the repository at insert time.
type LogEntry struct {
	Level      string
	Message    string
	Service    string
	OccurredAt time.Time
	CreatedAt  time.Time
}
