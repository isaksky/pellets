# Pellets web components

Adoption is deliberately incremental. Five form primitives are used in production
where they closely match the existing UI. Eight composite components remain
preview candidates in the development gallery; they are not used in the app.

The existing `app.css`, `workbench.css`, and `planner.css` retain their original
rules and load order, with one targeted creation-form width guard in
`workbench.css`. `components.css` adds wrapper behavior and opt-in preview styles,
plus the native-select spacing correction documented below; it does not restyle
native buttons, text fields, or application dialogs.

## Adoption and deviations

| Pattern | Production choice | Difference from the original UI |
| --- | --- | --- |
| Text fields and checkboxes | `pl-field`, `pl-checkbox` around existing form controls | No intended visual change. Native names, labels, validation and submission remain authoritative. Planner-specific controls retain their original markup. |
| Simple creation buttons | `pl-button` around Create pellet / Create memory | No intended visual change. Existing classes and native submit buttons remain. Toolbar, row, editor-footer and planner buttons stay native. |
| Existing custom selects | `pl-select` replaces the old generated wrapper | Same native select, trigger, options and CSS. Accessible names use the associated label instead of a machine field name. Lifecycle cleanup, form reset and validation are owned by the component. Invalid required selects focus the visible trigger; the native select is clipped instead of `display:none` for validation. |
| Native creation status select | `pl-select native` | Keeps the browser dropdown. Text and a decorative chevron have matching 12px insets, correcting the browser arrow's cramped right spacing. The 31px height and 4px radius are retained in Chromium; macOS WebKit previously forced 20px height and 5px corners, so the correction also normalizes that one control and increases its form height by 11px. The earlier migration incorrectly replaced this with a custom listbox. |
| Numeric stepping | `pl-number` replaces `.number-control`'s container | Same native input, step buttons, limits, appearance and input/change events. |
| Creation forms in narrow content panes | Original disclosure and form, constrained to the heading's content width | Fixes an existing overflow when two sidebars leave less than 420px for the queue. The trigger remains right-aligned when the heading wraps. Full-width forms retain their original geometry. |
| Record, checkpoint and planning dialogs | Original native dialogs and handlers | Generic dialog adoption is deferred. Preserve each existing size, padding, radius, backdrop, dirty guard, navigation and focus behavior. Pellet descriptions and group context use the feature-owned View / Preview and Edit controls described in [Workbench](workbench-ui.md#description-reading-and-editing); the native textarea remains authoritative. |
| Quiet action links | Shared `.quiet-button` styling | Inline flex centers link and button labels vertically and horizontally; 12px text matches neighboring actions. Existing filter-specific size overrides remain. |
| Menus, disclosures and filters | Original implementations | Generic outside-click/keyboard behavior is deferred; filters retain nested top-layer handling and assignments retain their specific interactions. |
| Mermaid descriptions | Feature-owned `pl-diagram` and native diagram viewer in the shared Markdown renderer | Local strict SVG rendering with source alternatives and a viewport-sized zoom/pan modal. The viewer remaps SVG IDs/styles and preserves native nested-dialog focus and source ownership. See [Mermaid authoring and activation API](workbench-ui.md#mermaid-diagrams). The gallery includes production examples and errors. |
| Description contents | Feature-owned `pl-description-reader` in pellet and group dialogs | A document-local hierarchical navigation rail, with a collapsible list below 960px. It owns heading anchors and resize/listener cleanup; source fields still own edits. The dialog widens by the rail's width to preserve reading space. The gallery has a page-local outline fixture. See [description reading and editing](workbench-ui.md#description-reading-and-editing) for visibility and navigation rules. |
| Planning tabs and sidebar resizing | Original implementations | Keep routing, draft preservation, narrow-screen behavior, geometry and saved preferences. |
| Badges, notices and icons | Original native markup | Additional wrappers add no needed behavior here and can change child selectors or layout. |
| Review queue rows | Feature-owned native row, `details` scope disclosure and existing `.row-menu` | Replaces 42px, 11px-title dividers and bracket gutters with readable 13px titles and 12px statuses/target counts. Title/editor navigation is separate from scope expansion. Complete explicit scope and generation-bound outcomes share a bounded scroll region; no layout nesting or inferred contiguous range. Counts include reviews in the project’s active total and label the filtered composition. Native controls retain feature-specific refresh, focus, selection, draft, removal/Undo and history behavior. Generic disclosure/menu adoption remains deferred. The gallery uses the production row with local-only fixture actions. |
| Current execution | Feature-owned native status summary, message articles and tool/group disclosures | The authoritative state label, working indicator, phase and two-line operation preview remain. Commentary is expanded 14px prose using the shared safe Markdown renderer. Adjacent compatible tools share a native details/summary with operation/path counts, reported outcomes and a current/latest preview; original child disclosures remain available. Tool rows use two-line command/path previews, visible reported exits and failure impact, and complete safe paths in their expanded evidence. Groups retain the last failure alongside the current/latest operation. Grouping is feature-owned, not adoption of the preview composite. Run details collects secondary workspace/schedule/runtime information; the feed explanation has a native disclosure while history/connection warnings remain visible. The composer scrolls on phones and in windows at most 600px tall; in short windows the state summary also scrolls to leave room for content. Only the state label is a live region. See [Live execution](workbench-ui.md#live-execution) and [grouping semantics](execution-activity.md). |

The Groups addition deliberately adds one 35px row and its 2px gap to desktop project navigation
and one 19.5px group-details link below pellet metadata. The parity fixture
measures those additions, checks only their exact position/height effects, and
continues to compare all original control sizes and styles. Group dialogs and
routing separation have their own browser suite in both engines. The WebKit
comparison forces root-relative units to recalculate and restores the authored
root style before measuring; this avoids its reproducible detached-document
16px rem cache on the theme label without ignoring any style differences.

The quality audit also fixed nonvisual interaction defects: implicit and nested
select labels focus and name the visible control, option replacement/visibility
changes close stale listboxes, busy links cannot activate handlers, and enabling
a busy button takes effect when it finishes. Preview menus preserve Space
activation and skip disabled/hidden items; preview resizers cancel interrupted
drags and clamp their accessible value. Invalid forms focus their first invalid
control. The upgrade handoff records selection changes so WebKit background
tabs retain the caret as well as the draft, and restores whether an unfinished
creation form was open. Background reads are cancelled when a document leaves;
back/forward-cache restoration reconnects live updates. The planning receipts
container has a group role so its existing accessible label is valid.

The gallery marks the eight deferred component types as previews. Its generic
`pl-dialog` surface has different defaults from the app's record and planning
dialogs. The modal navigation drawer is a proposal; the real app keeps its
existing responsive navigation. Preview availability is not evidence of parity
or permission to replace an application pattern.

## Native semantics and app styling

Controls should feel like part of the app while retaining dependable native form
behavior. Use shared theme tokens and component styles for the visible surface.
Retaining an `input` or `select` does not require leaving its browser-default
decoration in place. Scope appearance overrides to the component; preserve a
visible focus indicator and a usable forced-colors treatment.

| Example | Native behavior retained | App styling and implementation choice |
| --- | --- | --- |
| Numeric stepper: `pl-number.number-control` | The `input[type="number"]` owns the value, `min`, `max`, `step`, validation, keyboard editing, and form submission. | The shared `.number-control` styles suppress browser spinner decoration and provide themed decrease/increase buttons, borders, icons, and focus treatment. Buttons use `type="button"` and `data-number-step="down\|up"`, have accessible names, and call the input's native `stepDown()`/`stepUp()`. A changed value emits one input/change event pair. Disabled and read-only inputs cannot be stepped. |
| Custom dropdown: `pl-select` | The child `select` owns options, selected value, required/disabled state, reset, and submitted data. | The existing controller renders the themed trigger and listbox, including its chevron, option spacing, selection mark, and popup surface. Reuse its labeling, keyboard/typeahead, focus, validation, and dismissal behavior; do not hand-build another trigger/listbox around the same field. |
| Styled native dropdown: `pl-select native` | The actual select stays visible and opens the browser's picker with its native interaction. | For single selects without `size`, scoped `appearance: none` replaces the closed control's browser arrow with the shared decorative chevron. Text and chevron have matching 12px visible insets; extra end padding reserves room for the icon. The icon does not intercept clicks. Forced colors restore the browser arrow. The open picker remains platform-native. |

Use these choices deliberately:

- For bounded numeric stepping, reuse `pl-number` and the existing number-control
  markup/styles. Keep the number input and give step buttons accessible names;
  adding a plain number input beside themed controls can reintroduce mismatched
  browser spinners.
- For selectors matching the app's existing custom dropdowns, reuse `pl-select`.
  Verify the actual feature's change events and focus behavior as well as its
  appearance.
- For an existing native picker, start by styling its closed control with
  `pl-select native`. Use native mode for multi-selects and list controls too;
  their browser rendering is intentionally retained. Changing a picker to a
  custom listbox is an interaction change, beyond correcting an arrow or inset.

The creation Status control illustrates the last rule: its uneven arrow spacing
was corrected without replacing the native picker. The documented macOS WebKit
height normalization is an intentional visual deviation. Verify shared changes
in Chromium and WebKit, all five themes, and narrow panes; inspect both closed
and expanded states. Keep platform differences explicit instead of assuming the
two rendering engines supply identical control decoration.

Implementation references: [component behavior](../internal/webui/assets/components.js),
[dropdown controller](../internal/webui/assets/dropdowns.js),
[native-select styling](../internal/webui/assets/components.css), and
[number-control and dropdown styling](../internal/webui/assets/workbench.css).

## Authoring

Components use **light DOM** and retain native controls as children. Put `id`,
`name`, `required`, `form`, `data-on:*`, labels, and application actions on the
native control. It remains the authoritative value and successful form control;
there is no mirrored component state or shadow form serialization.

```html
<label>Title
  <pl-field><input name="title" required></pl-field>
</label>
<pl-select>
  <select name="mode" aria-label="Run mode">
    <option value="run_one">One pellet</option>
    <option value="drain">Through matching queue</option>
  </select>
</pl-select>
<pl-button variant="primary"><button type="submit">Save</button></pl-button>
```

| Element | Native content | API and behavior |
| --- | --- | --- |
| `pl-button` | `button` or styled `a` | `variant="primary\|quiet\|icon"`, `disabled`, `busy`; `.control`, `.focus()`, `.disabled` |
| `pl-field` | `input` or `textarea` | `.value`, `.disabled`, `.readOnly`, `.form`, `.focus()`, native validity methods |
| `pl-checkbox` | checkbox input | Field API plus `.checked` and `.indeterminate` |
| `pl-select` | `select` and native options | Field API, `.refresh()`, `.open()`, `.close()`; accessible single-choice listbox, typeahead, keyboard navigation, reset and mutation support; set `native` when authoring the wrapper to retain browser interaction (also for multi-select/list controls). `.open()` and `.close()` control the custom listbox only. |
| `pl-number` | number input and step buttons | Field API, bounded native stepping, one native input/change event per change; step buttons use `data-number-step="up\|down"` |
| `pl-menu` | `details` | `.open`, `.close(restoreFocus)`; outside click, Escape, arrow keys, Home/End, typeahead |
| `pl-disclosure` | `details` | `.open`, `.close(restoreFocus)`; expandable content retains native disclosure behavior |
| `pl-dialog` | `dialog` | `.showModal(opener)`, `.close(value)`, `.requestClose(reason)`, `.open`; `variant="editor\|confirm\|drawer"`, `locked` |
| `pl-tabs` | native tab buttons in a tablist | Click and arrow/Home/End navigation; `manual` lets application code own selection and panel state |
| `pl-badge` | status/provenance span | Preview wrapper for existing status classes |
| `pl-notice` | native status/alert content | `.show(message)`, `.dismiss()`; optional `data-notice-content` target |
| `pl-resizer` | Native focusable separator | `value`, `min`, `max`, `step`, `direction`, `orientation`; pointer drag, arrow/Home/End keyboard controls, double-click reset; emits `pl-resize` with `{value, phase}` |
| `pl-icon` | `svg` | Decorative by default; `label` gives the native SVG an accessible name |

Native attributes and existing button classes remain on the native control.
Use a wrapper only after checking the target screen, its selectors and behavior. Hidden submission fields, links used for ordinary navigation, document
layout, and browser-owned unload/discard confirmations remain native HTML/browser
features. Wrapping every layout node would not add a useful component boundary.

### Live selector options and footer

Opt a native select into in-place menu reconciliation with `data-live-options`.
Call its `pl-select.refresh()` after updating options or presentation attributes.
The controller retains the open popover, current value, focused option by value,
and scroll; ordinary selectors keep their existing close-on-change behavior.

`data-menu-action="Refresh models"` adds a footer outside the listbox.
`data-menu-status` supplies nonselectable status text; `data-menu-busy="true"`
marks its action unavailable without removing keyboard focus. Activating the
footer emits bubbling `pl-select-action` from the wrapper. The application owns
requests, persistence and status. Tab reaches the footer, Shift+Tab returns to
options, and Escape closes the menu and returns focus. Arrow navigation and
selection remain owned by the native select. The gallery has a local-only live
catalog example, while the production model and effort selectors share the global
catalog documented in Workbench.

## Dialogs and tabs

```html
<pl-dialog variant="editor">
  <dialog aria-labelledby="edit-heading">
    <header><h2 id="edit-heading">Edit pellet</h2></header>
    <pl-button><button type="button" data-dialog-dismiss>Cancel</button></pl-button>
  </dialog>
</pl-dialog>
```

`pl-close-request` bubbles and is cancelable. Its `detail.reason` is `escape`,
`backdrop`, or `dismiss`. Cancel it when navigation or a dirty-record guard must
handle closure. `pl-close` reports the native `returnValue`. `locked` prevents
user dismissal during a pending operation; authoritative code may still call
`.close()`. Native dialogs provide top-layer stacking, focus containment, and
return focus. Pass the trigger to `.showModal(opener)` for consistent pointer
focus return across browsers. Backdrop dismissal requires both press and release outside the box.

`pl-tab-change` bubbles and is cancelable, with `{tab, panel}` in `detail`.
Ordinary tabs update `aria-selected`, `tabIndex`, and the controlled panel's
`hidden` state. Manual tabs emit the event and let the planner persist the choice,
load content, and manage narrow-screen inertness. IDs in `aria-controls` are
ordinary document IDs.

## Dynamic content and Datastar

For a control chosen for migration, `wrapControl(nativeElement)` returns an
explicit component wrapper. `wrapNotice` is available for preview notifications.
Do not automatically wrap dynamically created application content; retain its
existing markup unless that individual usage has been checked.

```js
import { wrapControl, refreshComponents } from "./components.js";
const button = document.createElement("button");
button.type = "button";
button.textContent = "Retry";
button.addEventListener("click", retry);
container.append(wrapControl(button));
```

Custom elements initialize on connection and abort their listeners/observers on
disconnection. Reconnecting or morphing the same control must not accumulate
handlers. Selects observe their own native options and relevant attributes;
there is no document-wide observer that discovers and upgrades arbitrary selects.
The shared listbox coordinator allows only one open listbox and cleans it up if
its owning component is removed.

For property-only native changes, call `refreshComponents(region)` after the
application restores values, or set `component.value` directly. A native
`change` event and a form `reset` also synchronize presentation. Preserve native
IDs during streamed patches so focus and draft restoration continue to identify
the exact same field. Components do not submit requests, persist preferences,
or own project/run state.

## Styling and verification

The library consumes the existing theme palette. Production control and feature
styles remain in their original stylesheets. Candidate dialog styles are scoped
to `pl-dialog`, which production does not use. Most leaf wrappers use
`display: contents`. Child-combinator selectors must account for that DOM
boundary even though the wrapper creates no layout box.

The development-only `/dev/design-system` gallery shows adopted primitives,
existing app patterns and explicitly deferred candidates across all five themes. `scripts/test-web-design-system-browser.cjs` checks component
registration, form serialization, reset, repeated reconnects, option replacement,
listbox cleanup, guarded dismissal, tabs, desktop/mobile layouts, and the release
gate. The existing application browser suites cover real Datastar mutations,
focus, dirty drafts, execution, planning, settings, checkpoint actions, and UI
version handoff. These run against compiled binaries and disposable databases.

`node scripts/test-web-model-catalog-browser.cjs` checks immediate opening with
delayed discovery, SSE invalidation without polling, live focus/selection
preservation, stale/error recovery and retry. It captures both menus in all five
themes at 1280px, 800px and 390px widths; run it in both engines.

For a before/after comparison against the committed version, run:

```sh
node scripts/test-web-components-parity-browser.cjs
```

This archives and builds `HEAD` without touching the checkout, creates a
disposable fixture, and compares 15 scenes in all five themes at 1280px, 1092px,
and 390px widths. It saves screenshots and geometry/style measurements for
creation, filters and options, record actions, checkpoint scope, assignments,
memories, and planning. Set `PELLETS_UI_BASELINE=/path/to/pl` to use an explicit
baseline executable. Expected accessible-label improvements and the documented
native-select correction are checked separately. Review rows are an intentional
presentation change: the comparison verifies the new disclosure/menu and 13px
title, unchanged explicit membership, zero bracket gutter, and only the measured
vertical displacement from taller reviews plus reclaimed gutter width on ordinary
rows. Every original control retains the remaining geometry and styles; other
differences fail. The dedicated review-row suite captures all five themes at
1280, 1092, 800 and 390px and verifies complete scope, status/count semantics and
refresh stability in both engines.

Run the gallery and application suites in both engines with
`PLAYWRIGHT_BROWSER=webkit` for the second run. Install matching browsers with
`playwright install chromium webkit`; `PLAYWRIGHT_WEBKIT_EXECUTABLE` optionally
selects a specific WebKit build. Use `NODE_PATH` when Playwright is outside the
repository. `web-components-contract.cjs` adds regressions for disabled/busy
states, fieldset disabling, dynamic options, labels, required fields, keyboard
menus and preview APIs to the gallery suite.

### Completed audit

The comparison baseline was commit
`c402741484f7889f985bdf3f77dc6625023a1504`. Checks used compiled servers and
disposable fixtures, including a real server restart for upgrade recovery.

| Check | Result |
| --- | --- |
| Full Go suite (`go test ./...`) and final web UI package checks | Passed |
| Before/after geometry and style comparisons | 150 passed: 15 scenes × five themes × Chromium/WebKit |
| Application and gallery browser suites | All ten suites passed in both engines: main app, empty state, workbench, checkpoints, settings, runtime, recovery, planning, upgrade, design system |
| Creation forms with both sidebars | 28 passed: queue/memory × seven widths (390–1280px) × two engines; the new assertion reproduces clipping in the original app |
| Upgrade drafts | Creation form visibility, values, status, focus and caret preserved in both engines; no automatic submission |
| Automated accessibility comparison | No new violations in inspected app scenes; invalid planning group label fixed; inherited color limits remain below |
| Development gate | Release builds reject both the gallery route and its assets; gallery interactions perform no application writes |

Screenshot inspection additionally covered the real preview at its current 678px
width. Remaining screenshot-only differences included native text selection and
minor rendering noise; the native-select appearance and the new narrow-pane
constraint are the intentional visual changes. This is strong evidence for the
adopted controls, not a claim that every browser or every deferred composite is
production-ready.

The workbench suite's follow-up test now waits for the implementation phase,
rather than the earlier running state whose startup revision can still change.
Its exact-revision rejection remains intact. Run just the form width regression
with `PELLETS_WORKBENCH_BROWSER_CASE=creation node scripts/test-web-workbench-browser.cjs`.

## Accessibility limits of the inherited palette

The audit preserves application colors rather than silently redesigning the
themes. The new gallery navigation and annotations have readable foregrounds,
but specimens deliberately retain the application's actual colors. Automated
WCAG AA checks still flag some pre-existing badge, notice, group-chip and active
tab text, principally in Gruvbox Light and Icy. These are inherited palette
limitations, not evidence that the library is accessibility-certified. Keyboard,
native validation, focus and form ownership receive separate browser checks.
