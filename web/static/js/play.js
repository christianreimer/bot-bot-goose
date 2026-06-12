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
  if (streakNumEl) streakNumEl.textContent = state.streak;

  render();

  // ---- render -------------------------------------------------------------

  function render() {
    renderProgress();
    renderRound();
  }

  function renderProgress() {
    progressEl.innerHTML = '';
    for (let i = 0; i < N_ROUNDS; i++) {
      const f = document.createElement('div');
      f.className = 'feather';
      if (state.outcomes[i]) f.classList.add(state.outcomes[i]);
      if (i === state.round.index && !state.locked) f.classList.add('active');
      progressEl.appendChild(f);
    }
  }

  function renderRound() {
    const r = state.round;
    const isHunt = r.target_label === 'human' ? 'human' : 'bot';
    const subCopy = isHunt === 'human'
      ? 'Three bots, one human. Tap the <b>human</b>.'
      : 'Three humans, one bot. Tap the <b>bot</b>.';
    stage.innerHTML = `
      <div class="round-label">Round ${r.index + 1} of ${N_ROUNDS}</div>
      <div class="prompt"></div>
      <div class="sub">${subCopy}</div>
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
      ? 'Hint used — one decoy removed'
      : `🔉 Honk for a hint (removes one ${isHunt === 'human' ? 'bot' : 'human'})`;
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

    // Replace the controls with a "Next round" button.
    const ctl = document.getElementById('controls');
    ctl.innerHTML = '';
    const next = document.createElement('button');
    next.className = 'btn btn-primary';
    if (res.completed) {
      next.textContent = 'See your result ▸';
      next.onclick = () => {
        window.location.href = `/play/${res.puzzle_number}/result`;
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
    renderProgress();
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
        tag.textContent = state.round.target_label === 'human' ? 'the human' : 'the goose';
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

  let toastTimer;
  function toast(msg) {
    const t = document.getElementById('toast');
    if (!t) return;
    t.textContent = msg;
    t.classList.add('show');
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => t.classList.remove('show'), 2200);
  }
})();
