// Package jsoninput provides exact, case-sensitive validation for bounded JSON
// inputs whose field spelling is part of a command contract.
package jsoninput

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

var (
	// ErrInvalid indicates that a JSON value violates the expected shape.
	ErrInvalid = errors.New("JSON input is invalid")
	// ErrDuplicateField indicates that an object repeats an exact field name.
	ErrDuplicateField = errors.New("JSON object contains a duplicate field")
)

// RejectDuplicateFields rejects duplicate keys at every object depth and
// requires exactly one top-level JSON value.
func RejectDuplicateFields(content []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	if err := walkValue(decoder); err != nil {
		return err
	}
	return RequireEOF(decoder)
}

// ValidateObjectFields decodes one object and enforces exact, case-sensitive
// field names. Unknown fields may be retained when allowUnknown is true, but a
// case variant of a known field is always rejected.
func ValidateObjectFields(
	content []byte,
	knownFields []string,
	allowUnknown bool,
) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(content))
	if err := decoder.Decode(&object); err != nil {
		return nil, err
	}
	for field := range object {
		if slices.Contains(knownFields, field) {
			continue
		}
		for _, knownField := range knownFields {
			if strings.EqualFold(field, knownField) {
				return nil, fmt.Errorf(
					"%w: JSON field %q must be spelled %q",
					ErrInvalid,
					field,
					knownField,
				)
			}
		}
		if !allowUnknown {
			return nil, fmt.Errorf(
				"%w: unknown JSON field %q",
				ErrInvalid,
				field,
			)
		}
	}
	return object, nil
}

// RequireEOF verifies that a decoder consumed the input's only JSON value.
func RequireEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("%w: trailing JSON value", ErrInvalid)
		}
		return err
	}
	return nil
}

func walkValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("%w: object key is not a string", ErrInvalid)
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("%w: %q", ErrDuplicateField, key)
			}
			seen[key] = struct{}{}
			if err := walkValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := walkValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return fmt.Errorf("%w: unexpected JSON delimiter", ErrInvalid)
	}
}
