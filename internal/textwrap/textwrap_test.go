package textwrap

import (
	"strings"
	"testing"
)

func TestDedent(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name: "Basic indentation",
			input: `
\t\t\tHello
\t\t\t\tWorld
\t\t\t  Foo
\t\t\t    Bar
\t\t\t`,
			expected: `
Hello
\tWorld
  Foo
    Bar
`,
		},
		{
			name: "No common indentation",
			input: `
Hello
\tWorld
  Foo
    Bar
`,
			expected: `
Hello
\tWorld
  Foo
    Bar
`,
		},
		{
			name: "All lines indented",
			input: `
\tHello
\tWorld
\tFoo
\tBar
\t`,
			expected: `
Hello
World
Foo
Bar
`,
		},
		{
			name: "Mixed tabs and spaces",
			input: `
\t\tHello
\t  \tWorld
\t\t  Foo
\t\t \tBar
\t Baz
\tQux
\t`,
			expected: `
\tHello
  \tWorld
\t  Foo
\t \tBar
 Baz
Qux
`,
		},
		{
			name: "Tabs and spaces aren't interchangable",
			input: `
\t\tHello
\t World
\t `,
			expected: `
\tHello
 World
`,
		},
		{
			name: "Empty lines",
			input: `
\tHello

\tWorld

\tFoo
\tBar
\t`,
			expected: `
Hello

World

Foo
Bar
`,
		},
		{
			name: "Only whitespace lines",
			input: `
\t\t\t
\t\t\t\t
\t\t\t  
\t\t\t`,
			expected: `



`,
		},
		{
			name:     "Single line",
			input:    "    Hello World",
			expected: "Hello World",
		},
		{
			name: "Dedent more than common prefix",
			input: `
\t\tHello
\tWorld
\t\tFoo
\tBar
\t`,
			expected: `
\tHello
World
\tFoo
Bar
`,
		},
		{
			name:     "Empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "String with only newlines",
			input:    "\n\n\n",
			expected: "\n\n\n",
		},
		{
			name:     "String with line feed",
			input:    "\n\r\n",
			expected: "\n\r\n",
		},
		{
			name: "Indentation with non-space/tab characters",
			input: `
\u00a0Hello
\u00a0~\tWorld
\u00a0\tFoo
\u00a0\tBar
\u00a0`,
			expected: `
\u00a0Hello
\u00a0~\tWorld
\u00a0\tFoo
\u00a0\tBar
\u00a0`,
		},
		{
			name: "Very long lines",
			input: `
\tThis is a very long line that goes on and on and on and on and on and on and on and on and on and on and on and on and on and on and on
\t\tThis is another very long line that goes on and on and on and on and on and on and on and on and on and on and on and on and on and on
\tThis is a third very long line that goes on and on and on and on and on and on and on and on and on and on and on and on and on and on and on
\t`,
			expected: `
This is a very long line that goes on and on and on and on and on and on and on and on and on and on and on and on and on and on and on
\tThis is another very long line that goes on and on and on and on and on and on and on and on and on and on and on and on and on and on
This is a third very long line that goes on and on and on and on and on and on and on and on and on and on and on and on and on and on and on
`,
		},
		{
			name: "Lines with different indentation at the end",
			input: `
\tHello
\tWorld
\tFoo
\t\tBar
\tBaz
\t`,
			expected: `
Hello
World
Foo
\tBar
Baz
`,
		},
	}

	mappings := map[string]string{
		"\t":     `\t`,
		"\r":     `\r`,
		"\u00a0": `\u00a0`,
	}
	escape := func(s string) string {
		for actual, escaped := range mappings {
			s = strings.ReplaceAll(s, actual, escaped)
		}
		return s
	}
	unescape := func(s string) string {
		for actual, escaped := range mappings {
			s = strings.ReplaceAll(s, escaped, actual)
		}
		return s
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Dedent(unescape(tt.input))
			if got != unescape(tt.expected) {
				t.Errorf("Dedent() = %q, want %q", escape(got), tt.expected)
			}
		})
	}
}
