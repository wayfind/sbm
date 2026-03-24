package internal

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// JSONMode is set by main.go when --json flag is present.
// Commands check this to decide output format.
var JSONMode bool

// PrintJSON outputs v as indented JSON to stdout. Only call when JSONMode is true.
func PrintJSON(v interface{}) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "json marshal error: %v\n", err)
		return
	}
	fmt.Println(string(data))
}

// PrintErrorJSON outputs a JSON error object and exits with code 1.
func PrintErrorJSON(msg string) {
	PrintJSON(map[string]string{"error": msg})
}

// PrintJSONTo writes indented JSON to a writer (for HTTP responses).
func PrintJSONTo(w io.Writer, v interface{}) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}
