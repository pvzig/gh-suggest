package jsoninput

import (
	"errors"
	"testing"
)

func TestRejectDuplicateFieldsWalksNestedValues(t *testing.T) {
	t.Parallel()

	if err := RejectDuplicateFields([]byte(`{"outer":[{"value":1}]}`)); err != nil {
		t.Fatalf("RejectDuplicateFields() error = %v", err)
	}
	for _, content := range []string{
		`{"outer":1,"outer":2}`,
		`{"outer":[{"value":1,"value":2}]}`,
	} {
		if err := RejectDuplicateFields([]byte(content)); !errors.Is(err, ErrDuplicateField) {
			t.Fatalf("RejectDuplicateFields(%q) error = %v", content, err)
		}
	}
}

func TestValidateObjectFieldsPreservesExactCaseWithAdditiveFields(t *testing.T) {
	t.Parallel()

	if _, err := ValidateObjectFields(
		[]byte(`{"schemaVersion":1,"future":true}`),
		[]string{"schemaVersion"},
		true,
	); err != nil {
		t.Fatalf("ValidateObjectFields() error = %v", err)
	}
	if _, err := ValidateObjectFields(
		[]byte(`{"SchemaVersion":1}`),
		[]string{"schemaVersion"},
		true,
	); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ValidateObjectFields() error = %v", err)
	}
}
