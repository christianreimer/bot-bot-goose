# Security policy

## Reporting a vulnerability

**Please do not open a public GitHub issue for security bugs.**

Report privately via [GitHub Security Advisories](https://github.com/christianreimer/bot-bot-goose/security/advisories/new). That sends the report to the maintainer with no public disclosure.

Include:

- A description of the issue.
- Steps to reproduce, ideally against a local `make up && make migrate && make seed` clone.
- The impact you believe it has (data exposure, integrity bypass, denial of service, etc.).
- Your suggested fix, if you have one.

You should hear back within **5 business days**. If you don't, please follow up on the advisory thread — it likely means the notification got lost.

## Scope

Bot Bot Goose's most sensitive surface is the **integrity backbone** — the rules in `internal/play/` that keep answer labels off the client until a guess is committed. The most welcome reports involve:

- Any way to learn which answer is the target without committing a guess.
- Any way to bypass the per-play HMAC'd token (replay, forge, cross-play).
- Any way to make the shared grid lie — by tampering with stored state, the slot permutation, or the outcome computation.
- Authentication, cookie, CSRF, or rate-limit weaknesses.
- Any way for player-submitted content (`POST /api/decoy/submit`) to break out into other users' rendered HTML (XSS in templates or `data-` attributes).
- SQL injection, including via `pgx`'s parameterized-query escape hatches.

Less interesting (but still welcome) reports:

- Information leakage in error messages or logs.
- Weak cookie / TLS configuration.
- Outdated dependencies with known CVEs.

Out of scope unless they enable one of the above:

- Issues that require an attacker already controlling the server or its environment.
- Issues in third-party services (Cloudflare, ngrok, Postmark, Anthropic) themselves.
- Self-XSS or other issues that require the victim to copy/paste hostile content into their own browser console.
- Missing security headers that don't enable a concrete attack.

## Coordinated disclosure

If you'd like attribution, say so in the report — we'll credit you in the advisory and (optionally) the release notes.

If the issue is severe enough that public disclosure would put users at risk before a fix is out, we'll work with you on a disclosure timeline. Default plan: fix → release → publish the advisory.
