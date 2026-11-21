package protocol

import (
	"encoding/json"
	"errors"
)

// ignoreFieldTypeError drops the error encoding/json reports when a field
// holds a value of the wrong type. The decoder has already left that field at
// its zero value and decoded the rest, and Claude Code's formats evolve, so
// one mistyped optional field must not cost the whole payload. Errors about
// the input as a whole stay: bad syntax, truncation and a top-level value
// that is not an object, which the decoder reports with no field name.
func ignoreFieldTypeError(err error) error {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) && typeErr.Field != "" {
		return nil
	}
	return err
}
