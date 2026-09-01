package webui

import (
	"math"
	"strings"
	"testing"
)

func TestEmbeddedUIAssetsStayOfflineAccessibleResponsiveAndStateAware(t *testing.T) {
	t.Parallel()
	css := embeddedText(t, "assets/app.css")
	javascript := embeddedText(t, "assets/app.js")
	preflight := embeddedText(t, "assets/theme-preflight.js")
	templates := embeddedText(t, "templates/main.html")
	htmx := embeddedText(t, "assets/htmx-2.0.4.min.js")
	license := embeddedText(t, "assets/HTMX-LICENSE.txt")

	for name, content := range map[string]string{"CSS": css, "application JavaScript": javascript, "theme preflight": preflight, "templates": templates} {
		for _, forbidden := range []string{"https://", "http://", "@import", "fonts.googleapis", "cdn."} {
			if strings.Contains(content, forbidden) {
				t.Fatalf("%s contains runtime external reference %q", name, forbidden)
			}
		}
	}
	for _, required := range []string{
		`:root[data-theme="dark"]`, `@media (max-width: 760px)`, `@media (prefers-reduced-motion: reduce)`,
		`:focus-visible`, `.state-changed`, `.status-in_progress`, `.conflict-state`, `.error-state`, `.drawer-scrim[hidden]`, `.inspector-host.has-inspector`, `.inspector-host:has(.error-state)`,
		`.task-row { position: relative; cursor: pointer; }`, `.task-row:has(.row-link:focus-visible)`, `.row-link::after { content: ""; position: absolute; inset: 0; }`,
	} {
		if !strings.Contains(css, required) {
			t.Fatalf("CSS missing %q", required)
		}
	}
	for _, required := range []string{
		`new EventSource("/events")`, `pellets-invalidate`, `every 35s`, `id="project-drawer"`, `id="workspace-strip"`, `id="project-record"`, `data-protect-dirty`,
		`htmx:beforeSwap`, `target.id === "project-drawer"`, `target.id === "project-record"`,
		`scope.matches("[data-inspector]")`, `document.querySelector("[data-inspector], .error-state")`, `classList.toggle("has-inspector", hasInspector)`,
		`closest("form.dirty-track")`, `event.detail.elt === region`,
		`document.querySelector("#task-list a, #memory-list a, #main")`, `inspectorOpener = null`,
		`beforeunload`, `Discard unsaved inspector changes?`, `event.key === "Escape"`,
		`event.key !== "Tab"`, `drawerFocusable`, `closeDrawer(true)`, `aria-modal`, `prefers-color-scheme`, `htmx:historyRestore`,
	} {
		if !strings.Contains(javascript+templates+preflight, required) {
			t.Fatalf("UI assets missing %q", required)
		}
	}
	responseHandling := `window.htmx.config.responseHandling = [
      {code: "204", swap: false},
      {code: "409", swap: true, error: true},
      {code: "422", swap: true, error: true},
      {code: "[23]..", swap: true},
      {code: "[45]..", swap: false, error: true}
    ];`
	if !strings.Contains(javascript, responseHandling) {
		t.Fatal("application JavaScript must swap only intentional 409/422 application errors before the safe 4xx/5xx default")
	}
	if !strings.Contains(preflight, `localStorage.getItem("pellets-theme")`) || strings.Index(templates, "theme-preflight.js") > strings.Index(templates, "app.css") {
		t.Fatal("theme choice is not applied before first stylesheet paint")
	}
	if strings.Index(templates, "htmx-2.0.4.min.js") > strings.Index(templates, "app.js") {
		t.Fatal("HTMX must load before application JavaScript configures response handling")
	}
	if strings.Count(templates, `hx-target="#inspector-host" hx-swap="innerHTML" hx-push-url="true"`) != 2 {
		t.Fatal("task and memory inspector links must override inherited outerHTML swaps")
	}
	if strings.Count(templates, `class="icon-button"`) != 2 {
		t.Fatal("task and memory inspectors must share the corrected close control")
	}
	if !strings.Contains(css, `.icon-button { display: inline-grid; place-items: center; width: 36px; height: 36px;`) {
		t.Fatal("inspector close control must center its glyph in the 36px touch target")
	}
	for _, required := range []string{
		`role="dialog"`, `aria-labelledby="inspector-title"`, `aria-live="polite"`,
		`aria-label="Close inspector"`, `aria-label="Close projects"`, `Skip to content`, `System`, `Light`, `Dark`,
	} {
		if !strings.Contains(templates, required) {
			t.Fatalf("markup missing %q", required)
		}
	}
	if len(htmx) < 45_000 || !strings.Contains(htmx, "var htmx=") || !strings.Contains(license, "Zero-Clause BSD") {
		t.Fatal("pinned HTMX distribution or license is incomplete")
	}
}

func TestTaskRowPointerTargetKeepsOneKeyboardAccessibleNativeLink(t *testing.T) {
	t.Parallel()
	css := embeddedText(t, "assets/app.css")
	templates := embeddedText(t, "templates/main.html")

	rowStart := strings.Index(templates, `<tr class="task-row`)
	if rowStart < 0 {
		t.Fatal("task row markup is missing")
	}
	rowEnd := strings.Index(templates[rowStart:], ">")
	if rowEnd < 0 {
		t.Fatal("task row start tag is incomplete")
	}
	rowTag := templates[rowStart : rowStart+rowEnd+1]
	for _, forbidden := range []string{` role=`, ` tabindex=`, ` hx-get=`, ` onclick=`} {
		if strings.Contains(rowTag, forbidden) {
			t.Fatalf("task row must not become a duplicate interactive control: %s", rowTag)
		}
	}
	if strings.Count(templates, `class="row-link"`) != 1 || !strings.Contains(templates, `<a class="row-link" href="{{.URL}}" hx-get="{{.URL}}" hx-target="#inspector-host" hx-swap="innerHTML" hx-push-url="true">`) {
		t.Fatal("each rendered task row must retain one native, keyboard-accessible inspector link with the complete HTMX and history contract")
	}
	for _, required := range []string{
		`.task-row { position: relative; cursor: pointer; }`,
		`.row-link::after { content: ""; position: absolute; inset: 0; }`,
		`.task-row:has(.row-link:focus-visible)`,
	} {
		if !strings.Contains(css, required) {
			t.Fatalf("task row pointer/focus contract missing %q", required)
		}
	}
}

func TestTaskTitleColumnUsesAvailableWidthBeforeTruncating(t *testing.T) {
	t.Parallel()
	css := embeddedText(t, "assets/app.css")
	templates := embeddedText(t, "templates/main.html")

	if strings.Count(templates, `class="task-title-column"`) != 2 {
		t.Fatal("task title header and cells must share the flexible column sizing rule")
	}
	for _, required := range []string{
		`.task-title-column { width: 100%; max-width: 0; }`,
		`.task-title { display: block; width: 100%; overflow: hidden; text-overflow: ellipsis;`,
		`.task-row small { display: block; width: 100%; overflow: hidden; text-overflow: ellipsis;`,
	} {
		if !strings.Contains(css, required) {
			t.Fatalf("task title sizing contract missing %q", required)
		}
	}
	for _, forbidden := range []string{`.task-title { display: block; max-width:`, `max-width: 34ch`, `max-width: 38ch`} {
		if strings.Contains(css, forbidden) {
			t.Fatalf("task title or owner retained a fixed character cap %q", forbidden)
		}
	}
}

func TestTaskDescriptionEditorUsesBoundedViewportResponsiveHeight(t *testing.T) {
	t.Parallel()
	css := embeddedText(t, "assets/app.css")
	templates := embeddedText(t, "templates/main.html")

	const taskEditor = `<textarea class="task-description-editor" name="description" rows="9">`
	if !strings.Contains(templates, taskEditor) || strings.Count(templates, `task-description-editor`) != 1 {
		t.Fatal("only the task inspector description textarea must opt into responsive sizing")
	}
	if !strings.Contains(css, `.task-description-editor { height: clamp(12rem, 32vh, 24rem); }`) {
		t.Fatal("task inspector description height must respond to the viewport within useful bounds")
	}
	if !strings.Contains(css, `textarea { resize: vertical; min-height: 3.2rem; }`) {
		t.Fatal("task description editor must retain vertical user resizing")
	}
}

func TestThemeTextAndPrimaryControlsMeetWCAGAAContrast(t *testing.T) {
	t.Parallel()
	for _, pair := range []struct {
		name       string
		foreground string
		background string
		minimum    float64
	}{
		{name: "light body text", foreground: "#202124", background: "#f7f7f8", minimum: 4.5},
		{name: "light muted text", foreground: "#646872", background: "#ffffff", minimum: 4.5},
		{name: "light primary button", foreground: "#ffffff", background: "#5b5bd6", minimum: 4.5},
		{name: "dark body text", foreground: "#ececf0", background: "#111216", minimum: 4.5},
		{name: "dark muted text", foreground: "#a6a8b0", background: "#18191e", minimum: 4.5},
		{name: "dark primary button", foreground: "#111216", background: "#8b8cf6", minimum: 4.5},
		{name: "light warning badge", foreground: "#8a5b14", background: "#fff7e5", minimum: 4.5},
		{name: "dark warning badge", foreground: "#f0bf67", background: "#3b301d", minimum: 4.5},
	} {
		ratio := contrastRatio(parseHexColor(t, pair.foreground), parseHexColor(t, pair.background))
		if ratio < pair.minimum {
			t.Errorf("%s contrast = %.2f:1, want at least %.1f:1", pair.name, ratio, pair.minimum)
		}
	}
}

func embeddedText(t *testing.T, name string) string {
	t.Helper()
	content, err := embeddedFiles.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

type rgbColor struct{ red, green, blue float64 }

func parseHexColor(t *testing.T, value string) rgbColor {
	t.Helper()
	if len(value) != 7 || value[0] != '#' {
		t.Fatalf("invalid test color %q", value)
	}
	decode := func(pair string) float64 {
		var result uint64
		for _, digit := range pair {
			result *= 16
			switch {
			case digit >= '0' && digit <= '9':
				result += uint64(digit - '0')
			case digit >= 'a' && digit <= 'f':
				result += uint64(digit-'a') + 10
			default:
				t.Fatalf("invalid test color %q", value)
			}
		}
		return float64(result) / 255
	}
	return rgbColor{decode(value[1:3]), decode(value[3:5]), decode(value[5:7])}
}

func contrastRatio(left, right rgbColor) float64 {
	luminance := func(color rgbColor) float64 {
		linear := func(channel float64) float64 {
			if channel <= 0.04045 {
				return channel / 12.92
			}
			return math.Pow((channel+0.055)/1.055, 2.4)
		}
		return 0.2126*linear(color.red) + 0.7152*linear(color.green) + 0.0722*linear(color.blue)
	}
	first, second := luminance(left), luminance(right)
	if first < second {
		first, second = second, first
	}
	return (first + 0.05) / (second + 0.05)
}
