package model

import (
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
)

var ErrNotFound = errors.New("model: not found")
var ErrDuplicate = errors.New("model: duplicate")
var ErrStorage = errors.New("model: storage operation failed")

func IsDuplicate(err error) bool {
	var e *pgconn.PgError
	return errors.Is(err, ErrDuplicate) || errors.As(err, &e) && e.Code == "23505"
}
func daoError(err error) error {
	if err == nil {
		return nil
	}
	if IsDuplicate(err) {
		return ErrDuplicate
	}
	return ErrStorage
}

type Optional[T any] struct {
	Set   bool
	Value T
}
