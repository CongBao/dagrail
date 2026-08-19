# ADR 0023: hierarchical DAG Explorer contract

- Status: Accepted
- Date: 2026-08-19

## Context

Flattening execution Nodes, retry cycles, review work, Effects, and governance repair
into one canvas makes a valid large DAG unreadable. Search can locate an item, but it
cannot restore the top-level work structure or preserve context while inspecting one
subgraph. The Explorer needs a product-level hierarchy without changing lifecycle
authority or learning any adopter-specific vocabulary.

## Decision

The v0.26 Explorer implements the following indivisible, project-neutral contract:

1. A Group expands inline as a compound Node in the Project DAG.
2. Expansion is an accordion: exactly one Group may be expanded at a time.
3. The selected Group or Node and its one-hop neighborhood remain emphasized; unrelated
   items stay visible at low contrast.
4. The default layout is stable, top-down, compound-aware, layered, and orthogonally
   routed rather than a fixed grid.
5. Expansion centers and fits the compound plus its neighborhood; collapse or Escape
   restores the prior viewport, zoom, and keyboard focus.
6. Collapsed views draw a deterministic dependency backbone. Selection retrieves the
   complete snapshot-bound one-hop edge set; expansion restores exact internal edges.
7. Canvas cards show stable identity and a text status. Full title, progress, health,
   and execution detail live in the Inspector.
8. The desktop Inspector is docked and resizable; narrow screens use a bottom sheet.
9. One Navigator combines locate search, hierarchy tree, and interactive minimap, and
   synchronizes selection with canvas, Inspector, and URL.
10. Navigator starts as a narrow rail, expands on demand, and can be pinned.
11. Standard actions use a locked 18 px Lucide subset. Icon-only controls have a
    tooltip, `aria-label`, and distinct focus, busy, selected, and disabled states.
12. Canvas Group disclosure uses four-corner maximize/minimize; hierarchy uses chevrons
    and canvas panning uses hand/grab semantics.
13. Hierarchy comes from generic Graph Groups, `parentGroupId`, `groupId`, `laneId`, and
    `summaryNodeId`; it is orthogonal to dependency edges and lifecycle history.
14. An ungrouped legacy Graph remains valid and opens in Execution Detail. Project
    adapters may propose explicit generic Groups during import or through GraphPatch;
    the core never infers hierarchy from adopter-specific names or metadata keys.
15. Group rollup separates lifecycle from health. `summaryNodeId` controls terminal
    meaning, while ready work, active Attempts, open Incidents, uncertain Effects, and
    unclosed Resources follow deterministic precedence and cannot be guessed complete.
16. Collapsed edges map endpoints to the nearest visible ancestor, hide internal edges,
    and aggregate by visible endpoints and predicate/outcome class.
17. ELK layered layout uses stable IDs, model order, compound structure, and cancellable
    worker execution so an unchanged snapshot is deterministic and interaction never
    blocks the main thread.
18. All UI surfaces share one verified snapshot. Head polling is cheap and optional;
    refresh requests cancel and deduplicate, preserve the last good view on failure, and
    Node selection loads detail directly.
19. Render caps never omit a top-level Group. Members and dense edge indexes are
    snapshot-bound, paginated, stale-failing, and tested with generic 1,000+ Node and
    100+ Group fixtures. External adopter projects are validation only.
20. The visual language is Calm Collaboration: warm neutral light canvas, neutral
    charcoal dark canvas, restrained violet-blue navigation, low-saturation state
    colors, semantic theme tokens, sentence case, and reduced-motion support.

## Consequences

The Project DAG is a readable projection, not a graph editor and not a replacement for
Execution Detail. Grouping, layout state, spotlight, theme, and pagination never append
journal events or modify Attempt, Effect, Incident, Resource, or Graph authority. No
project name, Stage convention, requirements system, or migration fixture is a built-in
grouping rule.
