(() => {
  const SRC = '/hls/stream.m3u8';
  const video = document.getElementById('video');
  const overlay = document.getElementById('overlay');
  const badge = document.getElementById('badge');
  const badgeText = document.getElementById('badge-text');
  const meta = document.getElementById('meta');

  let hls = null;
  let starting = false;

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

  start();
})();
