# Pellets web components

Adoption is deliberately incremental. Five form primitives are used in production
where they closely match the existing UI. Eight composite components remain
preview candidates in the development gallery; they are not used in the app.

The existing `app.css`, `workbench.css`, and `planner.css` retain their load
order. `workbench.css` owns the production spacing, including narrow creation
forms, compact review titles, navigation truncation, and inline selector insets. `components.css` adds wrapper behavior and opt-in preview styles,
plus the native-select spacing correction documented below; it does not restyle
native buttons, text fields, or application dialogs.

## Adoption and deviations

| Pattern | Production choice | Difference from the original UI |
| --- | --- | --- |
| Text fields and checkboxes | `pl-field`, `pl-checkbox` around existing form controls | No intended visual change. Native names, labels, validation and submission remain authoritative. Planner-specific controls retain their original markup. |
| Simple creation buttons | `pl-button` around Create pellet / Create memory | Existing classes and native submit buttons remain. The heading's quiet disclosure rule no longer reaches into these forms: both submitters retain the shared primary background, border and padding, adding 6px to their previous height. Toolbar, row, editor-footer and planner buttons stay native. |
| Existing custom selects | `pl-select` replaces the old generated wrapper | Same native select, trigger, options and CSS. Accessible names use the associated label instead of a machine field name. Lifecycle cleanup, form reset and validation are owned by the component. Invalid required selects focus the visible trigger; the native select is clipped instead of `display:none` for validation. |
| Native creation status select | `pl-select native` | Keeps the browser dropdown. Text and a decorative chevron have matching 12px insets, correcting the browser arrow's cramped right spacing. The 31px height and 4px radius are retained in Chromium; macOS WebKit previously forced 20px height and 5px corners, so the correction also normalizes that one control and increases its form height by 11px. The earlier migration incorrectly replaced this with a custom listbox. |
| Numeric stepping | `pl-number` replaces `.number-control`'s container | Same native input, step buttons, limits, appearance and input/change events. |
| Creation forms in narrow content panes | Original disclosure and form, constrained to the heading's content width | Fixes an existing overflow when two sidebars leave less than 420px for the queue. The trigger remains right-aligned when the heading wraps. Full-width forms retain their original geometry. |
| Record, checkpoint and planning dialogs | Original native dialogs and handlers | Generic dialog adoption is deferred. Preserve each existing size, padding, radius, backdrop, dirty guard, navigation and focus behavior. Pellet descriptions and group context use the feature-owned View / Preview and Edit controls described in [Workbench](workbench-ui.md#description-reading-and-editing); the native textarea remains authoritative. |
| Quiet action links | Shared `.quiet-button` styling | Inline flex centers link and button labels vertically and horizontally; 12px text matches neighboring actions. Existing filter-specific size overrides remain. |
| Menus, disclosures and filters | Original implementations | Generic composite adoption is deferred. Native menus preserve Space/Enter activation and skip hidden or disabled choices during arrow/type-ahead navigation. Navigation, row-action and assignment disclosures use `details-menu-layout.js` for viewport-bounded placement outside scrolling panes; their original dismissal, keyboard, form and draft handlers remain. Filters retain their separate nested top-layer handling. |
| Mermaid descriptions | Feature-owned `pl-diagram` and native diagram viewer in the shared Markdown renderer | Local strict SVG rendering with source alternatives and a viewport-sized zoom/pan modal. The viewer remaps SVG IDs/styles and preserves native nested-dialog focus and source ownership. See [Mermaid authoring and activation API](workbench-ui.md#mermaid-diagrams). The gallery includes production examples and errors. |
| Description contents | Feature-owned `pl-description-reader` in pellet and group dialogs | A document-local hierarchical navigation rail, with a collapsible list below 960px. It owns heading anchors and resize/listener cleanup; source fields still own edits. The dialog widens by the rail's width to preserve reading space. The gallery has a page-local outline fixture. See [description reading and editing](workbench-ui.md#description-reading-and-editing) for visibility and navigation rules. |
| Planning tabs and sidebar resizing | Original implementations | Keep routing, draft preservation, narrow-screen behavior, geometry and saved preferences. |
| Badges, notices and icons | Original native markup | Additional wrappers add no needed behavior here and can change child selectors or layout. |
| Review queue rows | Feature-owned native row, `details` scope disclosure and existing `.row-menu` | Replaces 42px, 11px-title dividers and bracket gutters with readable 13px titles and 12px statuses/target counts. Single-line titles truncate with full text in the title attribute and record editor; expanded evidence still wraps. Title/editor navigation is separate from scope expansion. Complete explicit scope and generation-bound outcomes share a bounded scroll region; no layout nesting or inferred contiguous range. Counts include reviews in the project’s active total and label the filtered composition. Native controls retain feature-specific refresh, focus, selection, draft, removal/Undo and history behavior. Generic disclosure/menu adoption remains deferred. The gallery uses the production row with local-only fixture actions. |
| Workspace navigation status | Existing native links and view-menu items reuse the authoritative execution presentation | Compact sidebar labels retain full accessible names and titles; the view menu shows the complete state. The green dot represents working execution, not ownership or an idle Watch. Long labels stay within the existing navigation width. Phone status text remains visually hidden, with the full state in the accessible link name and view menu. |
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

The editing audit keeps those native production patterns. `workbench.js` shares
cancelable backdrop requests across production dialogs, and Escape/outside-click
handling across navigation, row, assignment, creation and Settings disclosures.
Nested custom selectors retain priority. The preview `pl-dialog` owns its separate
close-request API. `app.css` reserves the inspector's Unsaved slot so record,
memory and group titles and close controls do not move when a draft becomes dirty.
The editing browser suite checks clean/dirty geometry in both engines, five themes
and desktop/intermediate/phone layouts; strict production parity checks retain
their existing assertions without new exceptions. Feature-owned memory conflicts,
checkpoint insertion guards and pending new-chat locks are covered by
`scripts/test-web-editing-browser.cjs` (`PELLETS_EDITING_CASE=headers` runs the
header matrix; `details` runs the focused dialog journeys).

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

Inline custom-select triggers have 6px horizontal insets so their hover
background does not touch text or carets. Filter and assignment fields, execution
forms, theme controls, and the planning composer retain their explicit sizing.

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

### Pellet preference forms

Feature-owned `execution-preferences.js` renders two existing `pl-select` controls
around authoritative native selects. It shares the planner's catalog loader and
refresh state. Creation, checkpoint, record, and draft forms intentionally gain
one responsive preference row; original controls keep their styles and dimensions.
The parity suite captures actual screens and measures the added row, then hides
only that row to compare every preexisting control against the baseline. The
preference workflow suite verifies persistence, clearing, missing catalog entries,
keyboard dismissal, and all five themes at desktop/intermediate/phone widths in
Chromium and WebKit (`scripts/test-web-pellet-preferences-browser.cjs`).

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

Request feedback remains native markup owned by the application. Shared
`app.js` places mutation notices inside the invoking form, execution controls, or record footer;
navigation notices stay compact and global. This preserves modal reachability
without adopting the preview `pl-notice`. See the
[feedback audit and regression recipes](workbench-ui.md#status-error-and-response-feedback-audit).

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

### Spacing regression coverage

`test-web-spacing-browser.cjs` exercises production navigation, queue/review rows,
assignment/recipient and row-action menus, planner controls, settings, records,
group cards, and empty results. It uses long names and titles across five themes
and six widths (1280, 1092, 800, 678, 601 and 390px), with keyboard, hit-testing,
checkbox sizing and draft checks. Run it in Chromium and WebKit. Planning, groups,
review-row and Workbench suites retain their deeper workflow assertions.

The parity suite captures real before/after screens, then temporarily reverses
only the 6px inline selector inset and fixed disclosure-menu placement when
comparing preexisting controls. The spacing suite checks the delivered geometry;
all other styles and positions still compare, with the previously documented
review-height displacement and preference-row accounting. Focused persistent
pairs follow [UI evidence guidance](ui-agent-guidance.md#focused-before-and-after-evidence).

Live row feedback animates only the background highlight. A row transform would
change the containing block of its fixed menu; animated opacity would create a
stacking context that can place actions beneath neighboring rows. Keep both out
of the shared `state-changed` animation, including its filled state. Menu DOM,
keyboard behavior and refresh deferral while a menu is open remain unchanged.

`test-web-row-menu-animation-browser.cjs` covers ordinary and checkpoint rows
inserted or revised through live refresh, at animation start, midpoint, end and
after completion. It checks viewport anchoring, bounds, every enabled action's
hit target, scrolling, keyboard navigation, dismissal, focus return and updates
deferred while menus are open. Run in Chromium and WebKit; it covers all five
themes at 1280, 1092 and 390px with normal motion and scrolled panes. Fixtures use
independent temporary repositories with explicitly initialized databases.
`PELLETS_MENU_BASELINE=/path/to/pl` captures baseline measurements without geometry
assertions; `PELLETS_BROWSER_ARTIFACTS=/path` retains matched before/after crops
and measurements separately from the disposable databases.

### Interaction target regression coverage

`test-web-interaction-browser.cjs` uses a fresh OS-temporary Git repository with an explicitly initialized database, a
production server and the existing deterministic planning peer. Run it in Chromium and WebKit. It checks
all five themes at 1280, 1092 and 390px: visible row/proposal actions, stable
proposal title geometry, independent checkbox labels and menu actions, native
Space/Enter activation, hover feedback, editor focus return, retained drafts,
disabled selectors, unobscured 28px proposal actions, full memory-card links,
and touch menu/checkbox activation. Memory links use native Tab traversal in
Chromium and Option+Tab in macOS WebKit.
`PELLETS_INTERACTION_BASELINE=/path/to/pl` records the original behavior without
asserting the corrected contracts; `PELLETS_BROWSER_ARTIFACTS` chooses the
persistent screenshot directory. It emits focused before/after images and
observations. Planning and execution suites retain their real busy/error,
recovery, live-update, and focus/selection scenarios.

The interaction audit found shared defects in the production native-menu
keyboard handler (Space consumed as type-ahead), row background delegation
(checkbox labels and menu padding forwarded to the record), and enabled hover
styles applied to disabled selectors/actions. Shared native disclosure styling
also lacked padded hover/focus feedback. Preview menu and component busy
semantics were already correct and remain unchanged.

Local defects were hidden ordinary-row menus, proposal dismissal columns added
only on hover/focus, whole-heading/row feedback spanning independent proposal
actions, and memory metadata/padding outside its native link. Menu targets are
now always visible and at least 20px by 28px. Proposal dismissal reserves 28px
in every state; title buttons have 6px side insets and each action owns its
feedback. Plain review, execution, metadata and settings disclosures gain 3px
vertical and 6px horizontal insets plus theme-aware hover/focus backgrounds.
Review text retains its previous available width; the collapsed row adds exactly
6px of vertical inset. Breadcrumbs, filter/assignment controls, custom selector
insets, group-card links, dialog controls, and component busy behavior already
had appropriate targets and retain their interaction model.

The parity suite compares against the supplied baseline's actual selector and
menu geometry (including baselines after the earlier spacing correction).
It restores only the specific menu/disclosure and memory-card padding properties
for strict comparison of the remaining interface. Real screenshots precede that
restoration; the interaction suite verifies the intentional target changes.

### Emphasis and copy audit

The shared `.quiet` variant (including `pl-button variant="quiet"`) and dialog
Cancel styling already provide the intended secondary treatment. The emphasis
audit reused those treatments for the selected-review Clear selection action,
proposal refinement/splitting, and new-chat cancellation; no new button variant
or icon was added. Dismiss all, row menus, group actions, assignments, settings,
record footers, and execution/recovery disclosures retain their existing roles.
The shared dialog Cancel hover rule now excludes disabled controls, including
new-chat cancellation while its preference save is pending.

A systemic duplicate label came from `description.js`: its toolbar added a
visible heading while the native source label remained visible in Edit. The
helper now visually hides only that label's text nodes, retaining native naming,
source ownership, and the single toolbar heading in creation, record, proposal,
and group editors. Repeated refreshes do not nest label wrappers.

Local template defects were the unconditional Clear filters action and memory
empty-state/metadata repetition. Clear filters is a separately patched server
fragment and appears only for clearable filters, including search. Memories keep
one creation entry point and one provenance display; secondary record metadata
uses the existing disclosure. Recovery, approval consequences, input labels, and
validation remain visible where needed. The selected-review composer's fixed
column minimums also clipped fields in narrow panes; it now sizes its columns
from available width and keeps its primary action last.

`test-web-emphasis-browser.cjs` checks these production surfaces in Chromium and
WebKit, all five persisted themes, and 1280/1092/390px layouts. It checks native
label names, quiet/focus/hover treatment, conditional reset, review form fit,
keyboard actions, and draft/source preservation. It accepts
`PELLETS_EMPHASIS_BASELINE=/path/to/pl` and `PELLETS_BROWSER_ARTIFACTS=/path` for
matched, focused before/after captures. Each capture asserts its actual theme;
the after capture uses the baseline's exact enclosing crop.

The parity suite saves actual screenshots before temporarily restoring only the
removed source-label text, hidden default-filter reset, and old memory heading
and metadata presentation for strict comparison of all other controls. The
emphasis suite verifies the delivered behavior separately. Description, planning,
Workbench filter, and group suites retain their full draft/conflict, busy/error,
retry, source-saving, and keyboard checks. Gallery tests cover unchanged shared
quiet and busy/disabled variants. All fixture entry points used by this audit
explicitly initialize their temporary database before project discovery.

### Native-control consistency audit

The shared controls and CSS load order remain unchanged. Two defects were
reproduced in production in Chromium and WebKit and fixed in `workbench.css`:

- **Shared selector scope:** `.section-heading .primary-button` also reached the
  submit buttons inside both creation forms. The quiet rule now targets only the
  creation disclosure's summary. Create pellet and Create memory use the same
  primary treatment as other form actions; the disclosure keeps its quiet style.
- **Shared field padding with a local manifestation:** generic input padding
  expanded the planning new-chat checkbox to 18×16px after its local style set
  `appearance: none`. The shared checkbox rule now resets padding to zero, keeping
  the authored 16×16px confirmation control. Native checkboxes already computed
  to zero padding; their geometry and platform rendering are unchanged.

The audit also inspected creation/detail fields, execution preferences, planning
and proposal controls, group editors, assignment/recipient menus, settings,
filters, review scope, execution access and recovery. Existing typography,
native-select chevrons, numeric step buttons, custom listboxes and quiet links
already use their matching production patterns. No new wrappers, picker changes,
or preview composite adoption were needed.

Intentional native exceptions are the open Status picker, input datalist
suggestions, native validation messages, textarea resize affordances, and the
13px platform checkboxes in settings, assignments, review scope and recovery.
Those checkboxes keep `accent-color`, native checked/indeterminate/disabled
glyphs and label activation. Planning's existing 14px proposal and 16px
confirmation checkboxes keep their themed decoration and native forced-colors
fallback. Markdown task checkboxes remain disabled native document markers.

`test-web-native-controls-browser.cjs` covers five themes at 1280, 1092, 800 and
390px in both engines. It checks the two primary actions, checkbox geometry,
keyboard/phone touch activation, native Status ownership/reset/validation,
assignment drafts across live refresh, actual creation submissions, and recovery
number limits/read-only/disabled/reset. Chromium forced-colors emulation checks
the native arrow and checkbox fallback; WebKit reports whether it supports that
emulation. Use `PELLETS_NATIVE_BASELINE=/path/to/pl` for before captures and
`PELLETS_BROWSER_ARTIFACTS=/absolute/path` for persistent paired crops and computed
observations. `PELLETS_NATIVE_CASE=behavior` runs just submissions/recovery and
forced colors. The gallery/component, parity, Workbench, planning, groups,
settings, recovery and upgrade suites cover the surrounding workflows and states.

The parity suite captures the delivered creation buttons, then restores only
their previous background, foreground, border, padding and weight to compare
every other control and form geometry strictly. The native-control suite asserts
the delivered treatment; no checkbox or broad form exclusions are applied.

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
