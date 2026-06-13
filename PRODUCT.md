# Bot Bot Goose — Product Definition

## What it is

A daily web game. One prompt, four answers, three rounds. The player taps the odd one out: a bot hiding among three human answers. After three rounds, a Bot-Dar score and a shareable emoji grid.

The cultural bet: *can you still tell a human from an AI?* is a loaded question right now, and the answer feels like an identity statement. That's what makes the grid worth posting.

## Register

**Product** for the playable surfaces (`/play`, `/me`, `/leaderboard/*`): design serves the 90-second ritual. **Brand** at the share artifacts (`/r/<short>`, `/d/<short>`, OG images): these are public-facing posters and should read as editorial objects.

Both registers share the same palette, type stack, and component vocabulary. Share pages lean more typographic.

## Scene

A person on their couch after dinner, lit by a single warm lamp, holding a phone in one hand. They want a daily ritual that takes 90 seconds and ends in something they can post. Low ambient light, low stakes, dry humor. Not a Serious App. Not a Bright Game.

This scene forces:
- A warm-dark surface (pond-evening, not navy-tech).
- Single-column, thumb-reachable, no desktop redesign.
- A voice that's funny without trying ("Around here, that's a compliment.").
- An output object — the grid — that travels well on its own.

## Users

The default player is a casual daily-puzzle person: they already do Wordle, Connections, Mini. They are not a "game player." They will spend 90 seconds and leave. They come back tomorrow because it's a small ritual, not because the game is deep.

A secondary user is the **forger** — a player who returns because they want their decoys planted in tomorrow's puzzle. Their motivation is to fool strangers and climb the forger leaderboard. The product earns this loop with the contribution flow and `/me`.

## Voice

Dry. Confident. Specific. The product talks like a clever friend, not a marketing team.

- Yes: "Around here, that's a compliment."
- Yes: "Type like a person, not a press release."
- No: "Welcome to your dashboard!" / "Let's get started!" / corporate cheer.
- No: punching-down sarcasm or low-effort meme voice.

Headlines are short and declarative. Microcopy carries the warmth.

## Strategic principles

1. **The grid is the product.** The shared emoji grid is the artifact that does the marketing. Everything in `/play` and `/result` exists to make the grid feel earned.
2. **Server owns the truth.** Labels never reach the client before commit. This is integrity, not just engineering — it's why the grid is meaningful at all.
3. **One daily issue, not a dashboard.** Every page is numbered (`#002`). Mono type carries metadata. Treat each day as an editorial object.
4. **Mobile is the only surface.** Cap the column. Don't redesign for desktop — desktop is "mobile in a window."
5. **Voice over chrome.** When in doubt, sharpen the copy. Decorative gradients and oversized emoji are easier than a good sentence; resist that trade.

## Anti-references

What this product is **not** allowed to look like:

- **A Wordle clone.** No grid-of-tiles play UI. The game is reading-and-choosing, not letter-guessing.
- **A SaaS dashboard.** No big-number hero metrics with stacked supporting stats. No gradient cards with icon + heading + body.
- **A neon AI-vs-human "battle" theme.** No glitch fonts, no scanlines, no terminal green, no "matrix" imagery.
- **An indie-cute mascot game.** The goose is a typographic mark, not a character. No animated mascot pages.
- **A community/social product.** No avatars, profile bios, follow graphs, comments. The forger leaderboard is the only social surface and it stays anonymous-by-default.

## What "good" looks like

A returning player opens `/`, sees `#012`, plays three rounds, gets their Bot-Dar score, shares the grid to one place, optionally writes a decoy, and closes the tab. The whole loop is under two minutes. They don't think about the design at all. The next day they do it again.

A new player arrives via someone else's shared grid, the OG card unfurls into something that looks like a poster, not a screenshot, and they tap through to play.
