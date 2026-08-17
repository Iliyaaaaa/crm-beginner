package domain

import "errors"

var (
	ErrNotFound       = errors.New("customer not found")
	ErrDuplicateEmail = errors.New("email already exists")
)
