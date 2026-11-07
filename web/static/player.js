(() => {
  const SRC = '/hls/stream.m3u8';
  const video = document.getElementById('video');
  const overlay = document.getElementById('overlay');
  const badge = document.getElementById('badge');
  const badgeText = document.getElementById('badge-text');
  const meta = document.getElementById('meta');

  let hls = null;
  let starting = false;
  let currentFrag = null;

  function setState(state, text, overlayText) {
    badge.dataset.state = state;
    badgeText.textContent = text;
    overlay.hidden = !overlayText;
    if (overlayText) overlay.textContent = overlayText;
  }

  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

  async function waitReady() {
    for (; ;) {
      try {
        const res = await fetch('/healthz', { cache: 'no-store' });
        if (res.status === 401) { location.href = '/login'; return false; }
        const st = await res.json();
        if (st.ready) return true;
        setState('wait', 'Menunggu', st.last_error
          ? 'Kamera belum siap: ' + st.last_error
          : 'Menunggu stream siap…');
      } catch {
        setState('wait', 'Offline', 'Server tidak bisa dihubungi, mencoba lagi…');
      }
      await sleep(2000);
    }
  }

  async function start() {
    if (starting) return;
    starting = true;
    teardown();

    if (!(await waitReady())) return;

    if (window.Hls && Hls.isSupported()) {
      hls = new Hls({
        liveSyncDurationCount: 2,
        liveMaxLatencyDurationCount: 5,
        manifestLoadingMaxRetry: 4,
        levelLoadingMaxRetry: 4,
        fragLoadingMaxRetry: 4,
      });
      hls.on(Hls.Events.MANIFEST_PARSED, () => video.play().catch(() => { }));
      hls.on(Hls.Events.FRAG_CHANGED, (_, data) => {
        const f = data.frag;
        currentFrag = { name: f.url.split('/').pop().split('?')[0], start: f.start, duration: f.duration };
      });
      hls.on(Hls.Events.ERROR, (_, data) => {
        if (!data.fatal) return;
        if (data.type === Hls.ErrorTypes.MEDIA_ERROR) {
          hls.recoverMediaError();
          return;
        }
        setState('wait', 'Menyambung ulang', 'Stream terputus, menyambung ulang…');
        starting = false;
        setTimeout(start, 2000);
      });
      hls.loadSource(SRC);
      hls.attachMedia(video);
    } else if (video.canPlayType('application/vnd.apple.mpegurl')) {
      video.src = SRC;
      video.play().catch(() => { });
    } else {
      setState('off', 'Tidak didukung', 'Browser ini tidak mendukung HLS.');
      return;
    }
    starting = false;
  }

  function teardown() {
    if (hls) { hls.destroy(); hls = null; }
    currentFrag = null;
  }

  video.addEventListener('playing', () => setState('live', 'LIVE', null));
  video.addEventListener('waiting', () => setState('wait', 'Buffering', null));

  document.getElementById('btn-live').addEventListener('click', () => {
    if (hls && hls.liveSyncPosition != null) {
      video.currentTime = hls.liveSyncPosition;
    } else if (video.seekable.length) {
      video.currentTime = video.seekable.end(video.seekable.length - 1) - 1;
    }
    video.play().catch(() => { });
  });

  setInterval(() => {
    if (!hls || hls.latency == null || video.paused) { meta.textContent = ''; return; }
    meta.textContent = `Delay ±${hls.latency.toFixed(1)} detik`;
  }, 1000);

  (() => {
    const canvas = document.getElementById('boxes');
    const ctx = canvas.getContext('2d');
    const countEl = document.getElementById('count');
    const toggle = document.getElementById('btn-detect');
    const detectMeta = document.getElementById('detect-meta');

    let segments = {};
    let fps = 2;
    let enabled = false;
    let show = true;
    try { show = localStorage.getItem('showDetections') !== '0'; } catch { }
    toggle.setAttribute('aria-pressed', String(show));

    toggle.addEventListener('click', () => {
      show = !show;
      toggle.setAttribute('aria-pressed', String(show));
      try { localStorage.setItem('showDetections', show ? '1' : '0'); } catch { }
    });

    async function poll() {
      for (; ;) {
        try {
          const res = await fetch('/api/detections', { cache: 'no-store' });
          if (res.ok) {
            const d = await res.json();
            enabled = d.enabled && !!hls;
            toggle.hidden = !d.enabled;
            if (d.enabled) {
              segments = d.segments || {};
              fps = d.fps || fps;
              detectMeta.textContent = d.last_error
                ? `Deteksi error: ${d.last_error}`
                : `Deteksi ${d.inference_ms ? d.inference_ms.toFixed(0) : '–'} ms/frame`;
            } else {
              detectMeta.textContent = '';
            }
          }
        } catch { }
        await sleep(1000);
      }
    }

    function currentResult() {
      if (!currentFrag) return null;
      const frames = segments[currentFrag.name];
      if (!frames || !frames.length) return null;
      const offset = video.currentTime - currentFrag.start;
      let best = null, bestDiff = Infinity;
      for (const f of frames) {
        const diff = Math.abs(f.t - offset);
        if (diff < bestDiff) { best = f; bestDiff = diff; }
      }
      return bestDiff <= 1 / fps ? best : null;
    }

    function contentRect() {
      const cw = canvas.clientWidth, ch = canvas.clientHeight;
      const vw = video.videoWidth, vh = video.videoHeight;
      if (!vw || !vh) return null;
      const s = Math.min(cw / vw, ch / vh);
      const w = vw * s, h = vh * s;
      return { x: (cw - w) / 2, y: (ch - h) / 2, w, h };
    }

    function draw() {
      const dpr = window.devicePixelRatio || 1;
      const cw = canvas.clientWidth, ch = canvas.clientHeight;
      if (canvas.width !== Math.round(cw * dpr) || canvas.height !== Math.round(ch * dpr)) {
        canvas.width = Math.round(cw * dpr);
        canvas.height = Math.round(ch * dpr);
      }
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      ctx.clearRect(0, 0, cw, ch);

      const res = enabled && show ? currentResult() : null;
      const rect = contentRect();
      const objects = res && rect ? res.objects : [];

      const persons = objects.filter((o) => o.label === 'person').length;
      countEl.hidden = !(enabled && show && persons > 0);
      countEl.textContent = `${persons} orang`;

      if (!objects.length) return;
      ctx.lineWidth = 2;
      ctx.font = '600 12px system-ui, sans-serif';
      ctx.textBaseline = 'top';
      for (const o of objects) {
        const x = rect.x + o.x * rect.w, y = rect.y + o.y * rect.h;
        const w = o.w * rect.w, h = o.h * rect.h;
        const color = o.label === 'person' ? '#ef4444' : '#22c55e';
        ctx.strokeStyle = color;
        ctx.strokeRect(x, y, w, h);

        const text = `${o.label} ${Math.round(o.score * 100)}%`;
        const tw = ctx.measureText(text).width + 8;
        const ty = y >= 18 ? y - 18 : y;
        ctx.fillStyle = color;
        ctx.fillRect(x - 1, ty, tw, 18);
        ctx.fillStyle = '#fff';
        ctx.fillText(text, x + 3, ty + 3);
      }
    }

    function loop() {
      draw();
      requestAnimationFrame(loop);
    }

    poll();
    requestAnimationFrame(loop);
  })();

  start();
})();
