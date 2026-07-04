package errs

import (
	"errors"
	"strings"
)

var ErrAlreadyExists = errors.New("already exists")

func IsAlreadyExists(err error) bool {
	return errors.Is(err, ErrAlreadyExists) ||
		(err != nil && strings.Contains(strings.ToLower(err.Error()), "already exists"))
}
