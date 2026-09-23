# UI and design-system guidance for agents

Use this guidance for browser UI work, including design, implementation, and
review. The goal is a familiar, dependable application with reusable components
whose appearance and behavior are verified in the screens that use them.

Read [UI quality principles](ui-quality-principles.md) for recurring design
expectations and the process for distinguishing shared defects from local
instances. Apply those principles across the affected interface, not only the
first control reported.

## Establish the existing behavior

Read the adoption and deviations table and relevant APIs in
[Web components](web-components.md). For navigation, dialogs, planning, or
execution flows, also read the relevant sections of
[Workbench browser interface](workbench-ui.md).

Inspect the affected production templates, styles, and handlers, and reproduce
the relevant screen before changing it. Use the development-only
`/dev/design-system` gallery to explore controls and states. Check the real app
as well: a gallery example does not establish that a component fits a particular
application flow.

## Adopt components conservatively

Prefer established components for closely matching controls. The adoption table
in `web-components.md` is the source of truth for production usage and deferred
components. Preview availability alone does not make a component a replacement
for an existing dialog, menu, tab set, or other composite pattern.

Keep migrations small enough to compare and review. Preserve spacing, typography,
dimensions, theme colors, and interaction behavior unless the requested change
deliberately alters them. Preserve feature-specific focus handling, dismissal,
dirty guards, routing, and draft recovery. Describe intentional differences and
their reasons. Do not wrap layout nodes or replace working patterns merely to
increase web-component coverage.

Reuse theme tokens and existing variants. Extend a shared component when the
behavior belongs there; keep feature-specific behavior with its feature. Scope
CSS changes to their intended users and check descendant and child selectors
when adding wrappers. A `display: contents` wrapper still changes DOM ancestry.

## Make native controls fit the app

Native semantics and visual consistency are separate requirements. Keep the
native input or select responsible for its value and form behavior, while using
the app's shared treatment for typography, colors, borders, spacing, icons, and
focus states. Browser-default spinners, arrows, bevels, or padding should not
appear accidentally in an otherwise themed control.

Start with the existing component and its scoped styles. The numeric stepper
keeps a number input and uses themed step buttons. Dropdowns use either the
established custom selector or a styled native select that retains the browser
picker. Follow the [concrete examples and selection rules](web-components.md#native-semantics-and-app-styling)
instead of inventing another control implementation.

Preserve the current interaction model when correcting appearance. A cramped
native arrow is a spacing/styling problem; changing the entire picker also changes
keyboard, focus, and dismissal behavior and needs separate justification and
verification. Check the closed control and its expanded state, text and icon
insets, focus visibility, disabled/read-only states, and forced-colors behavior
where browser chrome is replaced. Record intentional platform-native appearance
so it is a deliberate choice.

## Preserve native and application contracts

Keep native controls authoritative for values, labels, validation, form ownership,
and submission. Preserve their IDs, names, attributes, and Datastar bindings;
avoid mirrored value state or alternate serialization. Retain the native picker
where the existing control uses one.

Components own presentation behavior. They do not submit application requests,
persist preferences, or decide project, workspace, or execution state. Preserve
the application's conflict checks and explicit-action boundaries.

Connection, disconnection, replacement, and streamed updates must not duplicate
listeners, retain stale popovers, or lose focus and drafts. Use the documented
refresh APIs after property-only value restoration. Check reset, required,
disabled, busy, and dynamically updated states for affected controls.

## Verify the affected experience

Choose checks based on the change:

- For shared controls, CSS, or wrapper changes, run the gallery/component checks
  and before/after parity comparison described in
  [Styling and verification](web-components.md#styling-and-verification), plus
  the affected application browser suites. Cover Chromium and WebKit, all five
  themes, and desktop, phone, and intermediate widths with sidebars visible.
- For feature interactions, exercise the real workflow and relevant browser
  suite. Check keyboard access, accessible names, focus return, nested Escape,
  validation, unsaved input, and recovery where the change can affect them.
- For rendering or development-gate changes, run the relevant Go web UI tests.
  Keep the gallery route and dedicated assets unavailable in release builds.
- For documentation-only changes, check links and consistency; browser suites
  are unnecessary. Reuse still-applicable test results and rerun when changes or
  failures invalidate them.

Inspect screenshots of affected app screens as well as automated assertions.
Use disposable fixtures for mutation tests. Create a fresh independent Git
repository in an OS temporary directory and run `pl --json init-db` there before
the first project command or server. Check reusable fixtures’ Git database binding
resolves to their own database before use. A nested repository or linked worktree
can discover the real shared database; a new directory alone is not isolation.
Keep screenshot artifacts separate from fixture databases. Fix fixture setup
problems and continue testing without asking again for already-authorized work.
Creating legitimate follow-up pellets for distinct out-of-scope findings is
allowed; complete in-scope work in the current pellet. Check that controls remain visible
and reachable when panes narrow or scroll. Do not weaken assertions or add broad
parity exceptions to hide regressions; expected differences need a specific
reason and evidence. Report what was verified and any remaining limits. Existing
palette contrast issues are documented and do not justify new accessibility
regressions or a claim of full accessibility conformance.

### Focused before-and-after evidence

For UI audit/fix pellets, save review evidence under
`artifacts/ui-review/<pellet-id>/<attempt>/` in the executing checkout. This
directory is gitignored. Use a unique attempt name (such as a UTC timestamp) and
retain earlier attempts. Keep the files after testing and finalization.

- Before editing, capture the affected production control or region with enough
  surrounding context to judge alignment and placement. After the fix, capture
  the same scenario. Use at least one pair per pellet and additional pairs for
  distinct fixes or materially different states; full-page screenshots alone do
  not satisfy this requirement.
- Store each pair as `<case>/before.png` and `<case>/after.png`. Match the browser
  engine, theme, viewport, device scale, fixture data, scroll position, interaction
  state, and crop. Include overlays and nearby controls when they are relevant.
  For intentional geometry changes, use the same enclosing region so the changed
  dimensions remain visible. Wait for deterministic state rather than capturing
  incidental loading or animation frames.
- Use real browser captures and disposable data. If changes already started,
  reproduce the original revision in an isolated fixture to obtain the baseline;
  do not reset the working checkout, alter images to simulate a baseline, or label
  an after image as before. Report a baseline that cannot be reproduced.
- Add an attempt-local `README.md` with a table linking each image pair, a short
  explanation of the defect and change, reproduction steps, browser/theme/viewport/
  scale/state/crop details, and source revisions (including uncommitted changes).
  If no defect remains, capture the unchanged audited scenario before and after
  verification and clearly record that no change was needed.
- Check that both images exist and are readable. Include absolute links to the
  evidence directory or index and representative pairs in the completion report.
  Preserve or copy selected test artifacts here before temporary fixtures are
  removed. When using another worktree, report its actual artifact path.

These focused pairs are review aids, not a replacement for the required browser,
theme, layout, accessibility, or interaction checks. For behavior or timing fixes,
include the relevant assertions or measurements alongside the images.

## Keep the reference current

Update the component APIs, adoption/deviation notes, and gallery examples when
their contracts change. Clearly distinguish production patterns from previews.
Gallery actions must use fixtures and must not write application data or saved
preferences. Keep detailed APIs and test recipes in `web-components.md`, these
working rules here, and the read trigger in the root `AGENTS.md`.
