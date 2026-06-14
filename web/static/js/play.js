// Bot Bot Goose — play loop client.
//
// Hydrates from <script id="bbg-state"> and drives the server-authoritative
// loop. The client only ever knows: my play_id, the current round's prompt,
// the shuffled answer text, and a play_token. It never sees which answer is
// the target until /api/play/round/N/guess responds.
(function() {
  'use strict';

  const stateEl = document.getElementById('bbg-state');
  if (!stateEl) return;
  const initial = JSON.parse(stateEl.textContent);
  if (initial.completed) {
    // Result page is server-rendered; nothing for play.js to do.
    return;
  }

  const csrf = readCookie('bbg_csrf_v1') || '';
  const N_ROUNDS = 3;

  // ---- view state ---------------------------------------------------------
  let state = {
    playId: initial.play_id,
    mode: initial.mode,
    puzzleNumber: initial.puzzle_number,
    round: initial.round,
    outcomes: initial.outcomes || [],
    streak: initial.streak || 0,
    locked: false,
  };

  const stage = document.getElementById('stage');
  const progressEl = document.getElementById('progress');
  const streakNumEl = document.getElementById('streakNum');
  if (streakNumEl) {
    streakNumEl.textContent = state.streak;
    const streakEl = streakNumEl.closest('.streak');
    if (streakEl) streakEl.hidden = !state.streak;
  }

  render();
  maybeShowIntro();

  // ---- render -------------------------------------------------------------

  function render() {
    renderProgress();
    renderRound();
  }

  function renderProgress() {
    progressEl.innerHTML = '';
    progressEl.setAttribute('role', 'list');
    progressEl.setAttribute('aria-label', 'Round progress');
    for (let i = 0; i < N_ROUNDS; i++) {
      const f = document.createElement('div');
      f.className = 'feather';
      f.setAttribute('role', 'listitem');
      const outcome = state.outcomes[i];
      const isActive = i === state.round.index && !state.locked;
      if (outcome) f.classList.add(outcome);
      if (isActive) f.classList.add('active');
      const statusWord = outcome === 'green' ? 'correct'
        : outcome === 'yellow' ? 'after a hint'
        : outcome === 'red' ? 'wrong'
        : isActive ? 'in progress'
        : 'locked';
      f.setAttribute('aria-label', `Round ${i + 1} of ${N_ROUNDS}: ${statusWord}`);
      progressEl.appendChild(f);
    }
  }

  function renderRound() {
    const r = state.round;
    stage.innerHTML = `
      <div class="round-label">Round ${r.index + 1} of ${N_ROUNDS} <span class="hunt">· Catch the bot</span></div>
      <div class="prompt"></div>
      <div class="answers" id="answers"></div>
      <div class="controls" id="controls"></div>`;
    stage.querySelector('.prompt').textContent = r.prompt;
    const wrap = document.getElementById('answers');
    r.answers.forEach((txt, i) => {
      const b = document.createElement('button');
      b.className = 'answer';
      if (r.removed_slot === i) b.classList.add('removed');
      b.textContent = txt;
      b.disabled = state.locked || r.removed_slot === i;
      b.onclick = () => guess(i);
      wrap.appendChild(b);
    });

    const ctl = document.getElementById('controls');
    const hint = document.createElement('button');
    hint.className = 'btn btn-ghost';
    hint.textContent = r.hint_used
      ? 'Hint used. One human removed.'
      : 'Honk for a hint (removes one human)';
    hint.disabled = !!r.hint_used || state.locked;
    hint.onclick = useHint;
    ctl.appendChild(hint);
  }

  // ---- actions ------------------------------------------------------------

  async function useHint() {
    const r = state.round;
    if (r.hint_used || state.locked) return;
    try {
      const res = await postJSON(`/api/play/round/${r.index}/hint`, { token: r.token });
      r.hint_used = true;
      r.removed_slot = res.removed_slot;
      r.token = res.token;
      render();
    } catch (e) {
      toast('Hint failed: ' + e.message);
    }
  }

  async function guess(slot) {
    if (state.locked) return;
    state.locked = true;
    const r = state.round;
    let res;
    try {
      res = await postJSON(`/api/play/round/${r.index}/guess`, { token: r.token, slot });
    } catch (e) {
      state.locked = false;
      toast('Guess failed: ' + e.message);
      return;
    }

    state.outcomes = res.outcomes;
    revealRound(slot, res.target_slots, res.outcome);

    // Replace the controls with a vote prompt + a Next button that's
    // disabled until the player casts their realest pick. The pick is
    // the gate to advancing — armRealestVote enables Next on success.
    const ctl = document.getElementById('controls');
    ctl.innerHTML = '';

    const voteHint = document.createElement('div');
    voteHint.className = 'vote-hint';
    voteHint.id = 'voteHint';
    voteHint.textContent = 'Which one felt most human?';
    ctl.appendChild(voteHint);

    const next = document.createElement('button');
    next.className = 'btn btn-primary';
    next.disabled = true;
    if (res.completed) {
      next.textContent = 'See your result ▸';
      next.onclick = () => {
        // After completion, send the player back to "/", which renders the
        // result branch when the server sees the play is done. The puzzle
        // number isn't in the URL anymore (deliberate: today is today).
        window.location.href = '/';
      };
    } else {
      next.textContent = 'Next round ▸';
      next.onclick = () => {
        state.locked = false;
        state.round = res.next_round;
        render();
      };
    }
    ctl.appendChild(next);
    armRealestVote(res.target_slots, next, voteHint);
    renderProgress();
  }

  // Wire up "felt most human" voting. Only the three human cards are
  // tappable — the goose card has already been outed and is not votable.
  // One vote per round, final once cast — mirrors the bot-guess pick.
  // The chosen card's chip is the visual confirmation; we hide the
  // prompt and unlock the Next button so advancing reads as a fresh
  // affordance, not a second nag.
  function armRealestVote(targetSlots, nextBtn, voteHint) {
    const targets = new Set(targetSlots || []);
    const r = state.round;
    const btns = document.querySelectorAll('.answer');
    let voted = false;

    btns.forEach((b, idx) => {
      if (targets.has(idx)) return; // bot card not votable
      b.classList.add('votable');
      b.classList.remove('dimmed');
      b.disabled = false;
      // Tap-to-vote on the human cards. The earlier reveal logic already
      // disabled them; we re-enable for the vote phase.
      b.addEventListener('click', async () => {
        if (voted) return;
        voted = true;

        // Drop the chip on the chosen card and lock every human card so
        // no further taps register. The bot card was never enabled.
        btns.forEach((other, otherIdx) => {
          if (targets.has(otherIdx)) return;
          other.classList.remove('votable');
          other.disabled = true;
          if (otherIdx === idx) {
            other.classList.add('voted');
            const chip = document.createElement('span');
            chip.className = 'tag human';
            chip.textContent = 'most human';
            other.appendChild(chip);
          }
        });
        if (voteHint) voteHint.hidden = true;
        if (nextBtn) nextBtn.disabled = false;
        try {
          await postJSON(`/api/play/round/${r.index}/realest`, {
            token: r.token,
            slot: idx,
          });
        } catch (e) {
          if (voteHint) {
            voteHint.hidden = false;
            voteHint.textContent = 'Vote failed: ' + e.message;
          }
        }
      });
    });
  }

  function revealRound(yourSlot, targetSlots, outcome) {
    const btns = document.querySelectorAll('.answer');
    const target = new Set(targetSlots || []);
    btns.forEach((b, idx) => {
      b.disabled = true;
      if (target.has(idx)) {
        b.classList.add('goose');
        const tag = document.createElement('span');
        tag.className = 'tag goose';
        tag.textContent = 'the goose';
        b.appendChild(tag);
        const pop = document.createElement('span');
        pop.className = 'honk-pop';
        pop.textContent = 'HONK!';
        b.appendChild(pop);
      }
      if (idx === yourSlot) {
        if (outcome === 'red') {
          b.classList.add('you-wrong');
          const t = document.createElement('span');
          t.className = 'tag wrong';
          t.textContent = 'fooled you';
          b.appendChild(t);
        } else {
          b.classList.add('you-right');
          const t = document.createElement('span');
          t.className = 'tag right';
          t.textContent = 'you got it';
          b.appendChild(t);
        }
      }
      if (!target.has(idx) && idx !== yourSlot) {
        b.classList.add('dimmed');
      }
    });
  }

  // ---- helpers ------------------------------------------------------------

  function readCookie(name) {
    return document.cookie.split('; ')
      .map(c => c.split('='))
      .filter(p => p[0] === name)
      .map(p => decodeURIComponent(p[1]))[0];
  }

  async function postJSON(url, body) {
    const r = await fetch(url, {
      method: 'POST',
      credentials: 'same-origin',
      headers: {
        'Content-Type': 'application/json',
        'X-CSRF-Token': csrf,
      },
      body: JSON.stringify(body),
    });
    if (!r.ok) {
      let msg = r.statusText;
      try { const j = await r.json(); msg = j.code || j.error || msg; } catch(e){}
      throw new Error(msg);
    }
    return r.json();
  }

  // First-visit "how to play" modal. Pure client-side gate: localStorage
  // key "bbg_seen_intro_v1" persists the dismissal. Anyone with a stale
  // browser (cleared storage, incognito, new device) sees it again, which
  // is acceptable for an under-1-minute primer. If storage is blocked we
  // still show + dismiss the modal; the user just sees it next visit too.
  function maybeShowIntro() {
    const KEY = 'bbg_seen_intro_v1';
    let seen = false;
    try { seen = localStorage.getItem(KEY) === '1'; } catch (_) {}
    if (seen) return;

    const ov = document.getElementById('introOverlay');
    if (!ov) return;
    ov.hidden = false;

    const dismiss = () => {
      ov.hidden = true;
      try { localStorage.setItem(KEY, '1'); } catch (_) {}
      document.removeEventListener('keydown', onKey);
    };
    const onKey = (e) => { if (e.key === 'Escape') dismiss(); };

    document.getElementById('introDismiss')?.addEventListener('click', dismiss);
    document.getElementById('introScrim')?.addEventListener('click', dismiss);
    document.addEventListener('keydown', onKey);

    // Focus the primary action so a keyboard user can press Enter to dismiss.
    // requestAnimationFrame lets the layout settle first.
    requestAnimationFrame(() => document.getElementById('introDismiss')?.focus());
  }

  // Visual dismissal lives in CSS (@keyframes toastShow). See app.css.
  let toastTimer;
  function toast(msg) {
    const t = document.getElementById('toast');
    if (!t) return;
    t.textContent = msg;
    t.classList.remove('show');
    void t.offsetWidth;
    t.classList.add('show');
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => t.classList.remove('show'), 2700);
  }
})();
