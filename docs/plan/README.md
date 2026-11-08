# Plan

Sources of the planning canvases (Cursor canvas format).

| File | Contents |
|---|---|
| `end-to-end.canvas.tsx` | The complete design: Watcher, Brain, Lab, Fixer, safety, evaluation |
| `build-plan.canvas.tsx` | Build phases, their gates, and who does what |
| `first-design.canvas.tsx` | The first from-scratch design, superseded by `end-to-end` |

Decisions made after these were written:

- Same repository as v1 (v1 lives on `archive/v1`), not a new one.
- Name stays "Sentinel" for now.
- License: Apache-2.0.
- Work lands on `main` directly; no review gate.
