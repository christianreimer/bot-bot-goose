# Bot Bot Goose — Design System

## Core idea

A warm, dark pond at evening. The player is the observer at the edge. The goose is the mark. Orange is the only saturated color and it's reserved for what matters: the brand, the verdict, the streak, the call-to-action.

This is a **Restrained** color strategy: tinted neutrals + one committed accent. Don't drift toward Committed or Full-palette.

## Color

Tokens are already hex in `web/static/css/app.css`. OKLCH equivalents are documented here for future additions and tooling. Keep the hex names — they read better than `--accent-2` ever will.

| Token | Hex | OKLCH (approx) | Role |
|---|---|---|---|
| `--pond-deep` | `#0e1a20` | `oklch(17% 0.02 222)` | Base background floor |
| `--pond` | `#14232b` | `oklch(22.5% 0.02 220)` | Default surface field |
| `--surface` | `#1d3540` | `oklch(28% 0.03 225)` | Cards, answer choices |
| `--surface-2` | `#244553` | `oklch(33% 0.035 225)` | Hover lift |
| `--line` | `rgba(244,239,227,.10)` | — | Borders, dividers |
| `--ink` | `#f4efe3` | `oklch(95% 0.015 85)` | Primary text (warm cream — tinted toward honk hue, never `#fff`) |
| `--muted` | `#8fa6ae` | `oklch(67% 0.025 215)` | Metadata, secondary text |
| `--honk` | `#f4a23b` | `oklch(75% 0.16 70)` | Accent — brand wordmark, primary button, streak fire, score number, verdict highlights |
| `--reed` | `#6fb36a` | `oklch(70% 0.13 140)` | Success — correct guess, "live" decoy state |
| `--miss` | `#e0604f` | `oklch(63% 0.18 32)` | Error — wrong guess, "rejected" decoy state |

### Rules

- `--honk` covers no more than ~10% of any screen's pixel area. It's a punctuation color, not a background. The radial gradient on `body` is the one allowed exception (light bleed at the top of the pond).
- `--reed` and `--miss` only appear after a verdict. They are state colors, not decorative.
- `--ink` is the only text color for body copy. `--muted` is metadata only.
- No pure `#000` or `#fff` anywhere, ever. The hex map above is the allowlist.
- No gradients on text. The single body radial-gradient is the only allowed gradient. Replace the `.payoff` and `.decoy` top-tint gradients with a flat `rgba(244,162,59,.06)` fill or a 2px top border in `--honk` — pick one, commit to it across both.

## Theme

Dark only. The scene sentence forces it. No `prefers-color-scheme: light` variant — adding one would invent a surface that doesn't match the brand.

If a daytime-readability concern surfaces, the answer is contrast within the dark theme, not a light variant.

## Typography

Three families, hard limit:

| Family | Use | CSS var |
|---|---|---|
| Bricolage Grotesque (600/700/800) | Display: brand, headlines, prompts, buttons, scores | `--display` |
| Inter (regular/medium) | Body: answer copy, paragraphs, microcopy | `--body` |
| Space Mono (400/700) | Metadata: puzzle number, streak count, tier labels, the emoji grid | `--mono` |

**Fix the silent fallback:** `--body` lists `'Inter'` but the stylesheet only loads Bricolage + Space Mono. Either add Inter to the `@import` URL or remove it from the stack. Currently body type falls through to system-ui without anyone knowing.

### Scale

A 1.25 ratio between steps. Don't flatten.

| Use | Size | Family | Weight |
|---|---|---|---|
| Page H1 (result, /me) | 28–32px | display | 800 |
| Prompt (play) | 24px | display | 700 |
| Card heading (decoy, payoff) | 17–18px | display | 700 |
| Body | 15px | body | regular |
| Microcopy | 13px | body | regular |
| Metadata (puzzle #, streak, tier) | 11–12px | mono | regular/700 |
| Tags (goose/right/wrong) | 10px | mono | 700, uppercase, +.1em tracking |

Headlines tighten letter-spacing to `-.02em`. Microcopy and metadata loosen to `+.08em` to `+.18em` for uppercase labels. Never use uppercase on body type.

Body line length caps at 65–75ch. The 520px column gets this for free; don't widen it.

## Layout

- **Single column.** `.app` max-width is `520px`. Don't add a desktop layout; the current behavior (centered narrow column on wide screens) is the correct interpretation.
- **Vertical rhythm:** group spacing is `18px` body padding, `14px` between header and content, `22px` between sections, `11px` between answer cards. Don't flatten to a single spacing constant.
- **Safe-area insets:** add `env(safe-area-inset-bottom)` to `.app` padding-bottom and to the `.toast` `bottom` offset. iPhone home-indicator collisions are the current bug.
- **Cards are used only when they're the affordance.** Answer choices are the right place for a card (it's the tappable thing). Stacked metadata boxes are not — use dividers or pill rows instead. No nested cards anywhere.
- **No global containers around everything.** `.app` is the only outer container. Sections should sit directly on the pond.

## Components

### Brand mark
`🪿` emoji at 26px + "Bot Bot **Goose**" wordmark in Bricolage 800, the *Goose* word colored `--honk`. Small drop-shadow on the emoji to lift it off dark surfaces. This mark is the only place the goose appears at scale; pages should not lead with a 54px goose emoji as a hero.

### Answer card
The most important component. `--surface` fill, `1.5px` `--line` border, `14px` radius, `15px` body type, `15px 16px` padding. On verdict:
- The chosen-correct card: `--reed` border + `rgba(reed,.12)` fill + `RIGHT` tag.
- The chosen-wrong card: `--miss` border + `rgba(miss,.12)` fill + `WRONG` tag.
- The reveal-bot card: `--honk` border + soft top-down honk gradient + `GOOSE` tag.
- Other cards: `.dimmed` 40% opacity.

Minimum tap target: `48px` total height. Current padding gets close but doesn't guarantee it on a single-line answer. Set `min-height: 56px` on `.answer`.

### Buttons

- `.btn-primary`: honk fill, dark text (`#241400`), Bricolage 700, `16px`, `14px` padding, `13px` radius. Minimum height: `52px`.
- `.btn-ghost`: transparent, `1.5px --line` border, ink text, body 600, `14px`, `12px` padding. Minimum height: `48px` — bump from current `11px` padding.
- `.btn-share` (inline): mono uppercase pill, `--honk` text, transparent fill, ghost border. Stays small (~32px).

### Progress feathers
Pill-shaped indicators of round progress and outcome. Currently `38×6px` — too small to read as state on mobile. Bump to `44×8px` and add `aria-label` per feather ("Round 1: correct" / "Round 2: in progress" / "Round 3: locked").

### Grid (the totem)
Space Mono, `26px`, letter-spacing `6px`, line-height `1.5`. Centered. This is the artifact — treat it as a typographic stamp, not a graph. Don't decorate it; don't put it in a frame more elaborate than `.scorecard`.

### Tags
Mono `10px`, uppercase, `+.1em` tracking, pill radius. One word: `GOOSE`, `RIGHT`, `WRONG`. Position absolute, `-9px` top, `12px` right. On mobile with 11px between cards, the tag overlaps the prior card edge — increase between-card gap to `14px` or reposition the tag inside the card.

### Pills (decoy status)
Same mono treatment as tags. States: `pending` (honk tint), `live` (reed tint), `rejected` (miss tint), `retired` (muted tint). Same as currently implemented.

## Motion

- **Honk pop** on correct guess: `0.5s cubic-bezier(.2,1.4,.4,1)`. The one allowed pop in the system — celebrates the verdict moment. Don't reuse this curve elsewhere.
- **Card press:** `transform: scale(.99)` over `.12s ease`. Hover and active lifts only.
- **Fade in** on result: `0.4s ease`, `8px` rise. Used once, on result reveal.
- **No layout animation.** Never animate width/height/padding/margin. Don't add bounce or elastic.
- **Default curve for new motion:** `cubic-bezier(0.22, 1, 0.36, 1)` (ease-out-quart). Exponential ease-out, no overshoot except the one honk-pop.
- `prefers-reduced-motion: reduce` already disables everything. Keep that.

## Voice and copy

- Em dashes are banned. The current templates use them in several places ("plant one — your words become tomorrow's trap", "spot the AI hiding among real human answers" — none currently). Replace each with a period, comma, colon, or full stop. Search the templates and remove.
- The footer tagline ("one bot hides among the humans") is the brand line. Don't restate it as `.sub` content on the play page; that's redundant.
- Numbers are written numerically (`#002`, `47%`, `3 rounds`). Don't spell them out.
- "Bot-Dar" and "Human-Dar" are proper nouns. Capitalize them. The grid is "the grid," lowercase.

## What we deliberately don't have

- No avatars, no profile photos. Handles are typographic.
- No onboarding flow. The first round is the onboarding.
- No settings page beyond what `/me` does. Theme, font-size, language: not now.
- No light mode.
- No desktop-specific layout.
- No animated mascot, no goose character art beyond the emoji.

## The slop test

This product fails the AI-slop test if any of these are true:
- It looks like a Wordle clone (grid-of-tiles, green/yellow/grey palette in the play UI).
- It looks like a SaaS dashboard (big number + supporting stats + gradient card).
- It looks like a "neon AI" themed product (terminal green, scanlines, glitch type, matrix imagery).
- The goose emoji appears at 48px+ on three or more pages in a row (becomes a template tic).
- Any gradient text exists anywhere.
- Any side-stripe colored border exists on a card or callout.

Run the check before shipping any new surface.
