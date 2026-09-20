package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"unicode"

	"pellets/internal/domain"
)

// Text output never emits ANSI styling, so NO_COLOR and redirected streams
// require no special case. Escape embedded controls supplied in record content.
func plainHuman(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			fmt.Fprintf(&b, "\\u%04x", r)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// WriteHuman writes text with terminal controls escaped, preserving newlines
// and tabs. Use it for human output that is not a rendered command result.
func WriteHuman(w io.Writer, text string) error {
	return write(w, []byte(plainHuman(text)))
}

func wrapHuman(s string, width int) string {
	s = plainHuman(s)
	if width <= 0 {
		return s
	}
	var b strings.Builder
	col := 0
	for _, r := range s {
		if r == '\n' {
			b.WriteRune(r)
			col = 0
			continue
		}
		n := 1
		if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
			n = 0
		} else if r >= 0x1100 && (r <= 0x115f || r >= 0x2e80 && r <= 0xa4cf || r >= 0xac00 && r <= 0xd7a3 || r >= 0xf900 && r <= 0xfaff || r >= 0xfe10 && r <= 0xfe6f || r >= 0xff01 && r <= 0xff60 || r >= 0x1f300) {
			n = 2
		}
		if r == '\t' {
			n = 4 - col%4
		}
		if col > 0 && col+n > width {
			b.WriteByte('\n')
			col = 0
		}
		if r == '\t' {
			b.WriteString(strings.Repeat(" ", n))
		} else {
			b.WriteRune(r)
		}
		col += n
	}
	return b.String()
}

// WriteHumanError preserves actionable structured details as labeled text.
func WriteHumanError(w io.Writer, err error, width int) error {
	public := domain.PublicError(err)
	var b bytes.Buffer
	fmt.Fprintf(&b, "Error: %s (%s)\n", public.Message, public.Code)
	humanValue(&b, reflect.ValueOf(public.Details), "  ")
	return write(w, []byte(wrapHuman(b.String(), width)))
}

func humanValue(w io.Writer, value reflect.Value, indent string) {
	if !value.IsValid() {
		fmt.Fprintln(w, "none")
		return
	}
	for value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		if value.IsNil() {
			fmt.Fprintln(w, "none")
			return
		}
		value = value.Elem()
	}
	switch value.Kind() {
	case reflect.Map:
		keys := value.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j]) })
		for _, key := range keys {
			child := value.MapIndex(key)
			if fmt.Sprint(key) == "retry_argv" {
				if args, ok := child.Interface().([]string); ok {
					fmt.Fprintf(w, "%sRetry command: %s\n", indent, ShellCommand(args))
					continue
				}
			}
			fmt.Fprintf(w, "%s%s: ", indent, strings.ReplaceAll(fmt.Sprint(key), "_", " "))
			unwrapped := child
			for unwrapped.IsValid() && (unwrapped.Kind() == reflect.Interface || unwrapped.Kind() == reflect.Pointer) && !unwrapped.IsNil() {
				unwrapped = unwrapped.Elem()
			}
			if unwrapped.IsValid() && unwrapped.Kind() == reflect.Map {
				fmt.Fprintln(w)
			}
			humanValue(w, child, indent+"  ")
		}
	case reflect.Slice, reflect.Array:
		if value.Len() == 0 {
			fmt.Fprintln(w, "none")
			return
		}
		fmt.Fprintln(w)
		for i := 0; i < value.Len(); i++ {
			fmt.Fprint(w, indent)
			humanValue(w, value.Index(i), indent+"  ")
		}
	default:
		fmt.Fprintln(w, value.Interface())
	}
}

// RenderDetails uses the same complete public fields as JSON, with text labels.
func RenderDetails(w io.Writer, data any) error {
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	var fields map[string]any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&fields); err != nil {
		return err
	}
	var b bytes.Buffer
	humanValue(&b, reflect.ValueOf(fields), "")
	_, err = w.Write(b.Bytes())
	return err
}

// ShellCommand formats a copyable POSIX-shell automation example.
func ShellCommand(args []string) string {
	result := make([]string, len(args))
	for i, arg := range args {
		if arg != "" && !strings.ContainsAny(arg, " \t\n\r'\"`$;|&<>()*?[]{}!\\") {
			result[i] = arg
		} else {
			result[i] = "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
		}
	}
	return strings.Join(result, " ")
}
