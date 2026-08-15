# DAGrail logo system

DAGrail's mark combines two ideas in one minimal gesture: a path branches once between
two rails. The rails express durable machine constraints without implying that DAGrail
chooses the route.

## Asset inventory

| Asset | Purpose |
| --- | --- |
| `assets/logo.svg` | Primary mark on light surfaces |
| `assets/logo-dark.svg` | Primary mark on dark surfaces |
| `assets/composer-icon.svg` | Compact plugin and composer surfaces |
| `skills/*/assets/icon-large.svg` | Skill listing and detail surfaces |
| `skills/*/assets/icon-small.svg` | Compact skill pickers and menus |

## Host application

Codex consumes the primary assets through `.codex-plugin/plugin.json` and the
per-skill assets through each `agents/openai.yaml`. Claude Code consumes the stable
`displayName` but does not currently expose plugin image fields. Copilot CLI likewise
does not expose image fields in its plugin manifest. Those manifests intentionally omit
unknown visual keys; all three host projections still carry the same immutable SVG
assets in the linked public bundle.

The three skill marks share the same two rails and change only the central gesture:

- **Govern DAG** shows one controlled branch.
- **Execute DAG Node** highlights the active node on a bounded path.
- **Review DAG Node** places an inspection ring around the reviewed node before its
  possible outcomes branch.

## Color

| Token | Value | Meaning |
| --- | --- | --- |
| Governance blue | `#2563EB` | Directed work and stable actions |
| Checkpoint cyan | `#38BDF8` | Active nodes, checkpoints, and inspection |
| Rail slate | `#334155` / `#475569` | Durable machine constraints |
| Dark-surface rail | `#CBD5E1` | Rail contrast on dark backgrounds |

Keep the mark flat and transparent. Do not add arrows, extra nodes, gradients, shadows,
a containing tile, or typography inside the symbol. Use the supplied compact asset
below 48 px rather than mechanically shrinking the primary mark. Preserve clear space
equal to one node radius around every side of the mark.
