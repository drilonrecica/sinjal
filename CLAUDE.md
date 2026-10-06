# Claude Code Instructions

You are implementing **Sinjal**.

Before making changes, read:

- `AGENTS.md`
- `docs/00_MASTER_SPEC.md`
- `docs/23_IMPLEMENTATION_PLAN.md`
- `docs/24_ACCEPTANCE_CRITERIA.md`
- the domain-specific document for the area you are changing

## Working method

1. Work milestone-by-milestone from `docs/23_IMPLEMENTATION_PLAN.md`.
2. Do not skip foundational tests to get to UI faster.
3. Keep a short implementation log in `docs/IMPLEMENTATION_CHECKLIST.md`.
4. Before each milestone:
   - identify relevant specs
   - identify acceptance criteria
   - state which files/packages will change
5. After each milestone:
   - run unit tests
   - run integration tests
   - run linters/formatters
   - run benchmark checks if a hot path changed
   - update checklist
6. Do not begin the next milestone while current milestone acceptance criteria are failing.
7. Never broaden the scope without explicit owner approval.

## Design authority

When the UI needs a decision:
- follow `docs/03_INFORMATION_ARCHITECTURE.md`
- follow `docs/04_DESIGN_SYSTEM.md`
- follow `docs/05_THEMES.md`
- do not invent a stock admin template

## Architecture authority

When the backend needs a decision:
- follow `docs/06_MONITORING_ENGINE.md`
- follow `docs/07_SCHEDULER.md`
- follow `docs/08_DATA_MODEL.md`
- follow `docs/09_DATABASE.md`

## Performance authority

Any implementation that violates the budgets in `docs/18_PERFORMANCE.md` must be reconsidered before it is accepted.

## Important

A smaller clear implementation is preferred to a more generic one.
