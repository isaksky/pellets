# UI quality principles

Pellets should be a compact, quiet tool whose controls make their purpose clear.
The interface should expose useful work and state, support familiar interactions,
and remain consistent across screens. These principles capture recurring product
review findings for future design, implementation, and audits.

Use this document with [agent workflow and verification guidance](ui-agent-guidance.md),
the [component adoption table and APIs](web-components.md), and the
[current Workbench behavior](workbench-ui.md). The examples describe failure
patterns to check, not a claim that those defects remain in the current build.
Feature-specific contracts and intentional exceptions remain documented in those
references; changing them requires an explicit design decision and verification.

## 1. Spacing, alignment, and information density

- Keep task rows, proposal trays, headers, and toolbars compact. Reduce oversized
  controls and structural gaps that displace useful content.
- Align labels, values, icons, carets, and activity indicators with their visual
  neighbors. Center button contents consistently regardless of native element.
- Give text and icons enough inset inside a visible background. Compact layout
  does not mean backgrounds should touch glyphs or checkboxes.
- Keep list titles on one line with ellipsis where the established row design
  calls for it; retain access to the complete title. Allow prose and editable
  content to wrap appropriately.
- Check narrow content panes with both sidebars visible, not just the viewport
  width. Controls must remain reachable without accidental overflow or wrapping.

## 2. Hover, focus, and click areas

- Match the interactive area to the visible object. A navigable card should
  include its content and padding; a row action should have an understandable
  target. Do not make only a small text fragment appear interactive by accident.
- Treat a label and its caret as one control. Apply coherent background, spacing,
  and focus treatment across that control.
- Whole-row hover must respect independent checkboxes, menus, links, and other
  actions. Avoid nested interactive elements and ambiguous competing targets.
- Secondary actions may become more prominent on hover, but remain discoverable
  and operable with keyboard focus and touch. Avoid shifting surrounding content.
- Use theme-appropriate hover and visible focus states, including disabled and
  busy variants. The cursor must agree with whether an action is available.

## 3. Visual emphasis and concise content

- Reserve prominent buttons for the primary action. Prefer quiet text or icon
  actions for secondary operations where their meaning remains clear.
- Remove repeated context and redundant label/value combinations. Do not repeat
  an entity name when the surrounding title already identifies it.
- Show controls when they serve a purpose: an already-empty conversation does
  not need a redundant reset action, for example.
- Use plain operational language. Avoid decorative slogans, example prompts,
  and explanatory copy that compete with the work itself.
- Keep essential labels, accessible names, validation, and recovery guidance.
  Reducing clutter must not obscure the meaning or consequences of an action.

## 4. Direct editing and familiar interactions

- Avoid a separate read-only step or nested dialog for a simple property edit
  when editing in context is sufficient. Keep title editing in its existing
  position without moving the header around it.
- Retain deliberate reading experiences for rich descriptions. The current
  Markdown View / Preview and Edit modes are an intentional feature contract,
  not an instruction to make every surface editable by default.
- Dialogs and popovers should dismiss through their established outside-click
  and Escape behavior, with predictable focus return. Nested controls consume
  dismissal at the appropriate level.
- Preserve unsaved-edit guards, validation, conflict handling, and drafts.
  Simplifying an interaction must not turn dismissal into silent data loss.
- Opening, expanding, switching modes, or editing should preserve context and
  avoid unnecessary layout movement or extra steps.

## 5. Placement, labels, and domain meaning

- Put an action close to the object it modifies and a selector close to the
  action it governs. Dialog footers and toolbars need a clear action hierarchy.
- Distinguish browsing from execution. Queue filters, group navigation, workspace
  assignments, and scheduler selection are separate concepts; controls should
  communicate and preserve those distinctions.
- Make the displayed entity clear: database location, project, workspace, owned
  task, and active execution are not interchangeable labels or states.
- Display useful path context with an understandable relative base. Prefer
  concise location information to internal database filenames, `.git` suffixes,
  or duplicate project/path labels; retain complete paths where needed.
- Fix misleading presentation at the appropriate layer. Do not change routing,
  ownership, persistence, or execution rules merely to simplify a label.

## 6. Consistent controls and conservative component adoption

- Native inputs, selects, checkboxes, and number controls should fit the app's
  typography, colors, spacing, icons, and focus treatment. Native semantics do
  not require accidental browser-default decoration.
- Reuse the documented component or established pattern that matches the
  interaction. Preserve native value ownership, validation, keyboard behavior,
  and submission. A spacing correction alone does not justify a new picker.
- Inspect shared defaults, scoped styles, wrappers, and CSS inheritance before
  adding another feature-specific override. Check selector ancestry and both
  native links and buttons when they share a visual treatment.
- Adopt components where they closely match existing production behavior.
  Preview availability is not a reason to replace a working composite pattern.
- Verify the real application as well as the gallery. A successful refactor or
  isolated example does not establish visual or behavioral parity in context.

## 7. Useful, timely state and feedback

- Make working, waiting, interrupted, failed, and finished states legible from
  authoritative application state. Task ownership or an old activity entry does
  not establish that a process is running now.
- Explain unavailable actions near the relevant control or incomplete record.
  Surface actionable errors while preserving input and offering the supported
  correction or retry path.
- Check predictable prerequisites when the user invokes an action, before
  starting work where possible. Retain necessary execution checks; do not remove
  safeguards, change access modes, or replay work to avoid an error message.
- Open ordinary controls promptly. Use existing cached data and truthful loading
  or refresh feedback where asynchronous discovery is needed.
- Keep views current through the established Datastar update flow, preserving
  drafts, focus, selection, expanded details, and reading position.
- Group repetitive compatible activity while retaining useful details, failures,
  questions, approvals, and operation boundaries. Never invent progress or
  successful outcomes. Empty states should explain a useful next action.

## Diagnose the cause, then cover the affected interface

1. Reproduce a concrete instance in the current app. Identify the relevant
   principle and expected behavior; do not assume an old example is still broken.
2. Trace the behavior through production markup, shared components, theme tokens,
   CSS, handlers, and application state. Determine whether the cause is systemic
   (such as an unsuitable shared default) or local (such as incorrect markup or
   a feature-specific override). A category may contain both kinds of defect.
3. For a systemic cause, fix the responsible shared layer and verify its consumers.
   Avoid repeated local overrides that leave the default wrong. Keep intentional
   differences explicit and do not broaden component adoption unnecessarily.
4. Where there is no systemic cause, audit the applicable production surfaces for
   every instance of the pattern and fix those instances. Also check for local
   exceptions left behind after a shared fix.
5. Cover navigation/status areas, queue and review rows, record dialogs, groups,
   workspace assignments and filters, planning/proposals, execution and recovery,
   and settings as applicable. Exercise normal, empty, busy, error, disabled,
   expanded, and edited states. Record intentional exceptions and unaffected areas.
6. Verify according to [UI agent guidance](ui-agent-guidance.md#verify-the-affected-experience).
   Inspect screenshots of the affected production screens, not only assertions.
   Shared presentation changes require Chromium and WebKit, all five themes, and
   desktop, intermediate, and phone layouts, including narrow sidebar panes.
   Use disposable fixtures for mutation checks and preserve accessibility.
7. Report the root-cause classification, audited surfaces, fixes, intentional
   exceptions, and verification. If the current interface already satisfies the
   principle, report concrete coverage rather than manufacturing a change.

An audit is complete when the common cause and remaining instances are addressed,
not merely when the first highlighted control looks correct. Add meaningful
regression coverage for shared contracts or interaction failures, and update the
component or Workbench reference when its documented behavior changes.
