package domain

import "strings"

// Email is a value object. The unexported field means NewEmail is the ONLY
// way to obtain one - so a valid Email is impossible to construct with bad
// data, and callers holding one never need to re-check it.
type Email struct {
	value string
}

// NewEmail trims whitespace, lowercases, and does a minimal format check.
//
// Normalising here - not at the database - is what makes "Ali@Example.COM"
// and "ali@example.com" collide correctly against the repository's UNIQUE
// constraint. Before this type existed, those were different strings and both
// were silently accepted, defeating the point of the constraint.
func NewEmail(raw string) (Email, error) {
	v := strings.ToLower(strings.TrimSpace(raw))
	if v == "" {
		return Email{}, ErrInvalidEmail
	}

	// Deliberately minimal, not full RFC 5322: that grammar is famous for
	// rejecting valid addresses while still missing real mistakes. This just
	// catches the obviously wrong shapes.
	at := strings.IndexByte(v, '@')
	if at <= 0 || at == len(v)-1 || strings.ContainsAny(v, " \t") {
		return Email{}, ErrInvalidEmail
	}

	return Email{value: v}, nil
}

func (e Email) String() string { return e.value }
