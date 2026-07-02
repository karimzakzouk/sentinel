package errorparse

import "testing"

func TestIsErrorLine(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{"Traceback (most recent call last):", true},
		{"KeyError: 'DATABASE_URL'", true},
		{"TypeError: can only concatenate str", true},
		{"panic: runtime error: index out of range", true},
		{"fatal error: unexpected signal", true},
		{"NullPointerException at com.acme.Foo", true},
		{"java.lang.NullPointerException", true},
		{"SIGSEGV: segmentation violation", true},
		{"ReferenceError: x is not defined", true},
		{"SyntaxError: invalid syntax", true},
		{"  at Object.<anonymous> (/app/index.js:3:10)", false},
		{"INFO: application started", false},
		{"  File \"app.py\", line 42, in main", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			got := IsErrorLine(tt.line)
			if got != tt.want {
				t.Errorf("IsErrorLine(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

func TestExtractErrorType(t *testing.T) {
	tests := []struct {
		line string
		want string
	}{
		{"KeyError: 'DATABASE_URL'", "KeyError"},
		{"TypeError: bad operand type", "TypeError"},
		{"panic: runtime error", "panic"},
		{"fatal error: unexpected", "fatal_error"},
		{"ValueError: invalid literal", "ValueError"},
		{"NullPointerException at foo", "NullPointerException"},
		{"no error here", ""},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			got := ExtractErrorType(tt.line)
			if got != tt.want {
				t.Errorf("ExtractErrorType(%q) = %q, want %q", tt.line, got, tt.want)
			}
		})
	}
}
