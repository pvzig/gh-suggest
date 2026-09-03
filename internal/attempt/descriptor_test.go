package attempt

import (
	"strings"
	"testing"
	"time"
)

func TestDecodeDescriptorUsesOneStrictWireContract(t *testing.T) {
	t.Parallel()

	content := validDescriptorJSON()
	descriptor, err := DecodeDescriptor([]byte(content))
	if err != nil {
		t.Fatalf("DecodeDescriptor() error = %v", err)
	}
	wantTime := time.Date(2026, time.July, 27, 12, 0, 0, 789, time.UTC)
	if descriptor.SchemaVersion != DescriptorSchemaVersion ||
		descriptor.Repository != "octo/example" ||
		descriptor.AttemptStartedAt != wantTime ||
		len(descriptor.Suggestions) != 1 {
		t.Fatalf("descriptor = %#v", descriptor)
	}

	tests := []struct {
		name    string
		content string
	}{
		{
			name:    "unknown descriptor field",
			content: strings.Replace(content, `"host":`, `"extra":true,"host":`, 1),
		},
		{
			name:    "case variant",
			content: strings.Replace(content, `"headSHA"`, `"HeadSHA"`, 1),
		},
		{
			name:    "duplicate field",
			content: strings.Replace(content, `"host":`, `"host":"github.com","host":`, 1),
		},
		{
			name:    "unknown suggestion field",
			content: strings.Replace(content, `"path":`, `"extra":true,"path":`, 1),
		},
		{
			name:    "trailing value",
			content: content + ` {}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := DecodeDescriptor([]byte(test.content)); err == nil {
				t.Fatal("DecodeDescriptor() error = nil")
			}
		})
	}
}

func validDescriptorJSON() string {
	return `{"schemaVersion":1,"host":"github.com","repository":"octo/example",` +
		`"pullRequest":42,"baseSHA":"` + strings.Repeat("a", 40) + `",` +
		`"headSHA":"` + strings.Repeat("b", 40) + `","event":"COMMENT",` +
		`"reviewBodySHA256":"` + strings.Repeat("c", 64) + `",` +
		`"suggestions":[{"path":"first.go","endLine":7,"side":"RIGHT",` +
		`"bodySHA256":"` + strings.Repeat("d", 64) + `"}],` +
		`"requestSHA256":"` + strings.Repeat("e", 64) + `",` +
		`"attemptStartedAt":"2026-07-27T12:00:00.000000789Z"}`
}
