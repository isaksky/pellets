package webui

import (
	"math"
	"regexp"
	"strings"
	"testing"
)

func TestMCPDeclineBypassesRequiredAnswerValidation(t *testing.T) {
	templates := embeddedText(t, "templates/main.html")
	if !strings.Contains(templates, `<button type="submit" name="action" value="decline" formnovalidate>Decline</button>`) {
		t.Fatal("MCP decline remains blocked by required answer controls")
	}
}

func TestEmbeddedUIAssetsStayOfflineAccessibleResponsiveAndStateAware(t *testing.T) {
	t.Parallel()
	css := embeddedText(t, "assets/app.css") + embeddedText(t, "assets/workbench.css")
	javascript := embeddedText(t, "assets/app.js")
	workbench := embeddedText(t, "assets/workbench.js")
	revision := embeddedText(t, "assets/ui-version.js")
	preflight := embeddedText(t, "assets/theme-preflight.js")
	templates := embeddedText(t, "templates/main.html") + embeddedText(t, "templates/workbench.html")
	datastar := embeddedText(t, "assets/datastar-1.0.3.js")
	license := embeddedText(t, "assets/DATASTAR-LICENSE.txt")
	for name, content := range map[string]string{"CSS": css, "application JavaScript": javascript, "Workbench JavaScript": workbench, "revision JavaScript": revision, "theme preflight": preflight, "templates": templates} {
		// SVG's namespace identifies its vocabulary; it never loads a resource.
		content = strings.ReplaceAll(content, "http://www.w3.org/2000/svg", "")
		for _, forbidden := range []string{"https://", "http://", "@import", "fonts.googleapis", "cdn."} {
			if strings.Contains(content, forbidden) {
				t.Fatalf("%s contains runtime external reference %q", name, forbidden)
			}
		}
	}
	for _, required := range []string{`:focus-visible`, `.state-changed`, `.status-in_progress`, `.conflict-state`, `.error-state`, `@media(max-width:600px)`, `@media(prefers-reduced-motion:reduce)`, `#record-dialog:not([open])`, `.navigation-collapsed #project-drawer`, `.execution-collapsed #execution`} {
		if !strings.Contains(compactCSS(css), compactCSS(required)) {
			t.Fatalf("CSS missing %q", required)
		}
	}
	for _, required := range []string{`new EventSource("/events?ui_revision="`, `pellets-invalidate`, `data-on-interval__duration.35s`, `id="project-drawer"`, `id="project-record"`, `data-protect-dirty`, `datastar-fetch`, `state.automatic && (dirtyInspector()`, `beforeunload`, `Discard unsaved inspector changes?`, `form.dataset.schedulePending === "true"`, `form.dataset.schedulePending = "true"`, `if (!confirmed)`, `inspectorOpener = null`} {
		if !strings.Contains(javascript+templates, required) {
			t.Fatalf("authoritative update/input protection missing %q", required)
		}
	}
	for _, required := range []string{`dialog.showModal()`, `event.preventDefault();closeRecord()`, `focus({preventScroll:true})`, `setSelectionRange(`, `stream.close()`, `pellets-activity`, `node.dataset.eventId`, `workspace.scrollTop=workspaceTop`, `localStorage.setItem("pellets-panels"`, `drafts.delete(formKey(form))`, `expansions.set(node.id,node.open)`} {
		if !strings.Contains(compactCSS(workbench), compactCSS(required)) {
			t.Fatalf("Workbench interaction/state contract missing %q", required)
		}
	}
	if !strings.Contains(preflight, `localStorage.getItem("pellets-theme")`) || strings.Index(templates, "theme-preflight.js") > strings.Index(templates, "app.css") {
		t.Fatal("theme choice must precede first stylesheet paint")
	}
	if !strings.Contains(javascript, `import { action, actions } from "./datastar-1.0.3.js"`) || !strings.Contains(javascript, `import "./workbench.js"`) {
		t.Fatal("application must import embedded Datastar and Workbench modules")
	}
	for _, forbidden := range []string{"hx-", "htmx", "HX-"} {
		if strings.Contains(templates+javascript, forbidden) {
			t.Fatalf("obsolete transport markup %q", forbidden)
		}
	}
	for _, required := range []string{`<dialog id="record-dialog"`, `aria-labelledby="inspector-title"`, `aria-live="polite"`, `aria-label="Close inspector"`, `Skip to content`, `aria-controls="project-drawer"`, `aria-controls="execution"`, `Gruvbox Light`, `Gruvbox Dark`, `>Light<`, `>Dark<`, `>Icy<`} {
		if !strings.Contains(templates, required) {
			t.Fatalf("accessible Workbench markup missing %q", required)
		}
	}
	footer := templates[strings.Index(templates, `<footer class="statusbar"`):]
	if strings.Index(footer, `id="theme-select"`) > strings.Index(footer, `id="toggle-execution"`) {
		t.Fatal("theme selector must precede the execution toggle")
	}
	if len(datastar) < 30000 || !strings.Contains(datastar, "Datastar v1.0.3") || !strings.Contains(license, "Copyright © Star Federation") {
		t.Fatal("pinned Datastar distribution or license is incomplete")
	}
}

func TestTaskRowPointerTargetKeepsOneKeyboardAccessibleNativeLink(t *testing.T) {
	t.Parallel()
	templates := embeddedText(t, "templates/main.html")
	start := strings.Index(templates, `{{else}}<article id="task-`)
	if start < 0 {
		t.Fatal("compact pellet row markup is missing")
	}
	row := templates[start:]
	end := strings.Index(row, "</article>")
	if end < 0 {
		t.Fatal("compact pellet row is incomplete")
	}
	row = row[:end]
	tag := row[:strings.Index(row, ">")+1]
	for _, forbidden := range []string{` role=`, ` tabindex=`, ` data-on:click=`, ` onclick=`} {
		if strings.Contains(tag, forbidden) {
			t.Fatalf("row became a duplicate control: %s", tag)
		}
	}
	if strings.Count(row, "<a ") != 3 || strings.Count(row, `tabindex="-1"`) != 1 || !strings.Contains(row, `class="task-title" href="{{.URL}}" data-on:click="@navigate('inspector-host')"`) {
		t.Fatal("row reference and title retain one keyboard stop to the inspector, alongside a separate group details link")
	}
	if !strings.Contains(row, `title="Open group: {{.Group}}"`) || !strings.Contains(row, `@navigate('app-content')`) {
		t.Fatal("group details must remain a distinct keyboard-accessible navigation link")
	}
	if !strings.Contains(row, `<summary aria-label="Actions for {{.Pellet.Reference}}">`) || !strings.Contains(row, `role="menu"`) {
		t.Fatal("row actions must remain independently keyboard accessible")
	}
}

func TestScheduleStartScopeSerializationNeverLeaksIntoStopForms(t *testing.T) {
	t.Parallel()
	templates := embeddedText(t, "templates/main.html")
	javascript := embeddedText(t, "assets/app.js")
	start := scheduleFormMarkup(t, templates, `action="/projects/{{$.Project.Code}}/schedules" method="post" data-schedule`)
	for _, name := range []string{"group_scope", "group", "external_id", "q", "status"} {
		if strings.Contains(start, `name="`+name+`"`) {
			t.Fatalf("browsing filter %s leaked into assigned execution start: %s", name, start)
		}
	}
	if !strings.Contains(start, `name="workspace_id"`) || !strings.Contains(start, `name="mode"`) || !strings.Contains(start, `Workspace group assignments`) {
		t.Fatal("start must explicitly select workspace and intent under saved assignments")
	}
	for _, action := range []string{"stop-after", "stop-now"} {
		form := scheduleFormMarkup(t, templates, `/`+action+`" method="post" data-schedule`)
		for _, name := range []string{"group_scope", "group", "external_id", "workspace_id", "mode"} {
			if strings.Contains(form, `name="`+name+`"`) {
				t.Fatalf("%s form carries unrelated %s", action, name)
			}
		}
	}
	// Exact-group choices still exist on explicit recovery forms and must be
	// normalized only when that form actually contains a scope field.
	for _, required := range []string{`fields.has("group_scope") && fields.get("group_scope") !== "ungrouped"`, `fields.set("group_scope", fields.get("group") ? "value" : "any");`} {
		if !strings.Contains(javascript, required) {
			t.Fatalf("exact recovery filter contract missing %q", required)
		}
	}
}

func scheduleFormMarkup(t *testing.T, templates, marker string) string {
	t.Helper()
	start := strings.Index(templates, marker)
	if start < 0 {
		t.Fatalf("schedule form marker missing: %q", marker)
	}
	end := strings.Index(templates[start:], "</form>")
	if end < 0 {
		t.Fatalf("schedule form is incomplete: %q", marker)
	}
	return templates[start : start+end+len("</form>")]
}

func TestTaskTitleColumnUsesAvailableWidthBeforeTruncating(t *testing.T) {
	t.Parallel()
	css := embeddedText(t, "assets/workbench.css")
	for _, required := range []string{`grid-template-columns:63px 27px minmax(0,1fr)`, `grid-template-columns:62px 24px minmax(0,1fr)`, `.task-title{display:block;white-space:nowrap;overflow:hidden;text-overflow:ellipsis`, `.group-name,.owner-label{`} {
		if !strings.Contains(compactCSS(css), compactCSS(required)) {
			t.Fatalf("compact row must give title flexible space at both widths: %q", required)
		}
	}
	for _, forbidden := range []string{`max-width:34ch`, `max-width:38ch`, `max-width: 34ch`, `max-width: 38ch`} {
		if strings.Contains(css, forbidden) {
			t.Fatalf("title retained fixed character cap %q", forbidden)
		}
	}
}

func TestTaskDescriptionEditorUsesBoundedViewportResponsiveHeight(t *testing.T) {
	t.Parallel()
	css := embeddedText(t, "assets/app.css")
	templates := embeddedText(t, "templates/main.html")

	const taskEditor = `<textarea class="task-description-editor" name="description" rows="4">`
	if !strings.Contains(templates, taskEditor) || strings.Count(templates, `task-description-editor`) != 1 {
		t.Fatal("only the task inspector description textarea must opt into responsive sizing")
	}
	if !strings.Contains(css, `.task-description-editor { height: auto; min-height: 7rem; max-height: 40vh; field-sizing: content; }`) {
		t.Fatal("task inspector description height must respond to the viewport within useful bounds")
	}
	if !strings.Contains(css, `textarea { resize: vertical; min-height: 3.2rem; }`) {
		t.Fatal("task description editor must retain vertical user resizing")
	}
}

func TestThemeTextAndPrimaryControlsMeetWCAGAAContrast(t *testing.T) {
	t.Parallel()
	css := embeddedText(t, "assets/workbench.css")
	themes := regexp.MustCompile(`(?:html|:root)\[data-theme="([^"]+)"\]\s*\{([^}]+)\}`).FindAllStringSubmatch(css, -1)
	if len(themes) != 5 {
		t.Fatalf("expected five actual theme palettes, got %d", len(themes))
	}
	for _, theme := range themes {
		tokens := map[string]string{}
		for _, token := range regexp.MustCompile(`--([a-z-]+):\s*(#[a-f0-9]{6});`).FindAllStringSubmatch(theme[2], -1) {
			tokens[token[1]] = token[2]
		}
		for _, pair := range [][2]string{{"ink", "bg"}, {"muted", "pane"}, {"dim", "bg"}, {"dim", "pane"}, {"theme-on-primary", "theme-primary"}} {
			ratio := contrastRatio(parseHexColor(t, tokens[pair[0]]), parseHexColor(t, tokens[pair[1]]))
			if ratio < 4.5 {
				t.Errorf("%s %s/%s contrast = %.2f:1, want at least 4.5:1", theme[1], pair[0], pair[1], ratio)
			}
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

func compactCSS(value string) string { return regexp.MustCompile(`\s+`).ReplaceAllString(value, "") }
