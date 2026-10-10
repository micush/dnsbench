"use strict";
// Anyname web GUI. No framework, no innerHTML: every value from the server goes
// through textContent, so nothing it returns can inject markup.

(() => {
  const POLL_MS = 2000;
  const state = { topo: { list: [], gid: null, action: null }, session: null, tab: "topology", timer: null, cfg: null, cfgMode: "form", cfgInfo: null,
    target: null, nodes: [], clustered: false }; // target: address of the node being driven, null = this one
  const root = document.getElementById("app");

  // ── DOM helper ────────────────────────────────────────────────────────────
  function h(tag, attrs, ...kids) {
    const el = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs || {})) {
      if (v === false || v == null) continue;
      if (k === "class") el.className = v;
      else if (k.startsWith("on")) { el.addEventListener(k.slice(2), v); (el.__on = el.__on || {})[k.slice(2)] = v; }
      else if (k === "value") el.value = v;
      else if (v === true) el.setAttribute(k, "");
      else el.setAttribute(k, v);
    }
    for (const kid of kids.flat(Infinity)) {
      if (kid == null || kid === false) continue;
      el.append(kid.nodeType ? kid : document.createTextNode(String(kid)));
    }
    return el;
  }
  const clear = (el) => { while (el.firstChild) el.removeChild(el.firstChild); return el; };

  // morph makes the children of dst the same as the children of src, reusing the elements that are already there (only
  // their text and attributes are patched).  A page that is redrawn on a timer uses it so that what the browser keeps
  // about an element survives the redraw: the position of a horizontal scroll bar, a text selection.  Emptying a
  // container and building it again, which these pages did, destroys every scroll box in it, so a scroll bar that had been
  // moved sprang back and the bars flashed on every redraw.  Only for markup without event handlers: the handlers are
  // not copied.  An inline style is skipped too (the page's policy allows it only through the CSSOM, so the caller sets it).
  function morph(dst, src) {
    const from = Array.from(src.childNodes);
    from.forEach((s, i) => {
      const d = dst.childNodes[i];
      if (!d) { dst.appendChild(s); return; }
      if (d.nodeType !== s.nodeType || d.nodeName !== s.nodeName) { dst.replaceChild(s, d); return; }
      if (s.nodeType !== 1) { if (d.nodeValue !== s.nodeValue) d.nodeValue = s.nodeValue; return; }
      for (const at of Array.from(d.attributes)) if (at.name !== "style" && !s.hasAttribute(at.name)) d.removeAttribute(at.name);
      for (const at of Array.from(s.attributes)) if (at.name !== "style" && d.getAttribute(at.name) !== at.value) d.setAttribute(at.name, at.value);
      // handlers: the new element's replace the old ones (they close over the new data)
      const dOn = d.__on || {}, sOn = s.__on || {};
      for (const ev of Object.keys(dOn)) if (dOn[ev] !== sOn[ev]) d.removeEventListener(ev, dOn[ev]);
      for (const ev of Object.keys(sOn)) if (dOn[ev] !== sOn[ev]) d.addEventListener(ev, sOn[ev]);
      d.__on = sOn;
      // form state is a property, not an attribute; what the person is typing into is left alone
      if (d.nodeName === "INPUT" && d !== document.activeElement) { if (d.type === "checkbox" || d.type === "radio") d.checked = s.checked; else if (d.value !== s.value) d.value = s.value; }
      if (d.nodeName === "INPUT" || d.nodeName === "BUTTON") d.disabled = s.disabled;
      morph(d, s);
    });
    while (dst.childNodes.length > from.length) dst.removeChild(dst.lastChild);
  }
  // fill(el, ...kids): what `clear(el).append(...kids)` does, but el keeps the elements it already has (see morph), so the
  // scroll boxes inside it are not destroyed and re-created by every poll.
  const fill = (el, ...kids) => { morph(el, h("div", {}, ...kids)); return el; };
  // Width (in chart units) the y-axis labels need: the chart's left margin grows with the longest label so big
  // numbers are never cut off.  ~6.6 units a character at the 11px axis font, plus the gap and a little edge room.
  const axisMargin = (labels, min) => Math.max(min, Math.ceil(Math.max(0, ...labels.map((t) => String(t).length)) * 6.6) + 14);
  const pill = (text, kind) => h("span", { class: "pill " + (kind || "") }, text);
  const fmtMs = (n) => (n == null ? "–" : Number(n).toFixed(2));

  // ── API ───────────────────────────────────────────────────────────────────
  // Everything follows the node picked in the top bar (relayed through the node
  // you are logged in to) except the login itself.  The picker's own list of
  // nodes is asked of the login node explicitly (api(..., true)).
  const LOCAL_API = /^\/api\/(login|logout|session|proxy|tshoot\/download)(\/|\?|$)/;
  // The Node menu's "Cluster" entry: Monitor ▸ Statistics and Monitor ▸ Host show every node's numbers added together
  // (the node you are logged in to asks the others).  Only those two pages offer it, and only their two requests change;
  // anything else a page asks goes to this node as before.
  const CLUSTER = "*cluster";
  const CLUSTER_TABS = ["stats", "host", "capture"];
  const CLUSTER_API = [[/^\/api\/qstats(\?|$)/, "/api/clusterstats"], [/^\/api\/host(\?|$)/, "/api/clusterhost"], [/^\/api\/qstats\/clear$/, "/api/clusterstats"]];
  function route(path) {
    if (state.target === CLUSTER) {
      for (const [re, to] of CLUSTER_API) if (re.test(path)) return path.replace(/^\/api\/[a-z]+/, to);
      return path;
    }
    if (!state.target || LOCAL_API.test(path)) return path;
    return "/api/proxy?node=" + encodeURIComponent(state.target) + "&path=" + encodeURIComponent(path);
  }
  async function api(method, path, body, local) {
    const orig = path;
    if (!local) path = route(path);
    const headers = {};
    if (body !== undefined) headers["Content-Type"] = "application/json";
    if (state.session && method !== "GET") headers["X-CSRF-Token"] = state.session.csrf;
    const res = await fetch(path, {
      method, headers, credentials: "same-origin",
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    let data = null;
    try { data = await res.json(); } catch (_) { /* non-JSON error */ }
    if (res.status === 401 && orig !== "/api/login") {
      stopAllTimers();
      state.session = null;
      showLogin();
      throw new Error("unauthenticated");
    }
    if (!res.ok) {
      const err = new Error((data && data.error) || res.statusText || "request failed");
      err.status = res.status;
      err.data = data;
      throw err;
    }
    return data;
  }

  // Upload raw bytes (release archive); the CSRF token still applies.
  async function apiUpload(path, file) {
    const res = await fetch(route(path), {
      method: "POST", credentials: "same-origin",
      headers: { "Content-Type": "application/octet-stream", "X-CSRF-Token": state.session.csrf },
      body: file,
    });
    let data = null;
    try { data = await res.json(); } catch (_) { /* non-JSON error */ }
    if (res.status === 401) { stopAllTimers(); state.session = null; showLogin(); throw new Error("unauthenticated"); }
    if (!res.ok) throw new Error((data && data.error) || res.statusText || "upload failed");
    return data;
  }

  // Download a file the daemon builds (a capture): fetched with the session, named by the server's Content-Disposition.
  async function apiDownload(path, fallback) {
    const res = await fetch(route(path), { credentials: "same-origin" });
    if (res.status === 401) { stopAllTimers(); state.session = null; showLogin(); throw new Error("unauthenticated"); }
    if (!res.ok) {
      let data = null;
      try { data = await res.json(); } catch (_) { /* not JSON */ }
      throw new Error((data && data.error) || res.statusText || "download failed");
    }
    const m = /filename="?([^";]+)"?/.exec(res.headers.get("Content-Disposition") || "");
    const a = h("a", { href: URL.createObjectURL(await res.blob()), download: m ? m[1] : fallback });
    document.body.append(a); a.click(); a.remove();
    setTimeout(() => URL.revokeObjectURL(a.href), 4000);
  }

  // ── login ─────────────────────────────────────────────────────────────────
  function showLogin(message) {
    stopAllTimers();
    // Already showing the form (a late 401 from a request still in flight): leave
    // it alone, so nothing being typed is wiped.
    if (!message && root.querySelector("form input[name=username]")) return;
    clear(root);
    const err = h("div", { class: "notice bad hidden", role: "alert" });
    if (message) { err.textContent = message; err.classList.remove("hidden"); }
    const user = h("input", { type: "text", name: "username", autocomplete: "username", required: true, autofocus: true, autocapitalize: "none", spellcheck: "false" });
    const pass = h("input", { type: "password", name: "password", autocomplete: "current-password", required: true });
    const btn = h("button", { class: "btn primary", type: "submit" }, "Log in");
    let lockTimer = null;
    const setLocked = (on) => { user.disabled = pass.disabled = btn.disabled = on; };
    // Locked out: disable the form and count down to the end of the lockout.
    function lockFor(secs) {
      const end = Date.now() + secs * 1000;
      setLocked(true);
      clearInterval(lockTimer);
      const tick = () => {
        const left = Math.ceil((end - Date.now()) / 1000);
        if (!form.isConnected) { clearInterval(lockTimer); return; }
        if (left <= 0) {
          clearInterval(lockTimer);
          setLocked(false);
          err.classList.add("hidden");
          user.focus();
          return;
        }
        err.textContent = "Too many failed attempts.";
        err.classList.remove("hidden");
      };
      tick();
      lockTimer = setInterval(tick, 1000);
    }
    const form = h("form", {
      onsubmit: async (e) => {
        e.preventDefault();
        btn.disabled = true;
        err.classList.add("hidden");
        try {
          state.session = await api("POST", "/api/login", { username: user.value, password: pass.value });
          pass.value = "";
          showShell();
        } catch (ex) {
          pass.value = "";
          const d = ex.data || {};
          if (ex.status === 429 && d.retry_after) { lockFor(d.retry_after); return; }
          err.textContent = ex.message;
          err.classList.remove("hidden");
          pass.focus();
          btn.disabled = false;
        }
      },
    },
      h("label", { class: "f" }, "Username", user),
      h("label", { class: "f" }, "Password", pass),
      btn);
    root.append(h("div", { class: "login-wrap" }, h("div", { class: "login" },
      h("div", { class: "card" }, h("div", { class: "body" },
        h("h1", {}, "Anyname DNS Director"),
        h("p", { class: "sub" }, "Sign in with your system account"),
        err, form)))));
    user.focus();
    // already locked out (page reloaded)? show it before anything is typed
    fetch("/api/login/state", { credentials: "same-origin" }).then((r) => r.json()).then((d) => {
      if (d.locked && d.retry_after) lockFor(d.retry_after);
    }).catch(() => { /* the form still works */ });
  }

  // ── shell / navigation ────────────────────────────────────────────────────
  // Sidebar groups: one open at a time; the group holding the current page is open.
  // Topology comes first; its items are the gateways themselves (filled in from the daemon).
  const TOPO = "Topology";
  const NAV_GROUPS = [
    ["Monitor", [["stats", "Statistics"], ["host", "Host"], ["gateways", "Gateways"], ["nodes", "Cluster"], ["dns", "DNS"], ["anycaststatus", "Anycast"], ["capture", "Capture"], ["log", "Log"]]],
    ["Configure", [["cfg_general", "General"], ["cfg_groups", "Gateway groups"], ["cfg_dns", "DNS proxy"], ["cfg_web", "Web GUI"], ["cfg_cluster", "Cluster"], ["anycast", "Anycast"], ["users", "Users"], ["history", "History"]]],
    ["Operate", [["node", "Node"], ["cluster", "Cluster"], ["anycastop", "Anycast"], ["updates", "Upgrade"]]],
  ];
  const TABS = [["topology", TOPO]].concat(NAV_GROUPS.flatMap(([, items]) => items));
  const groupOf = (id) => (id === "topology" ? TOPO : (NAV_GROUPS.find(([, items]) => items.some(([i]) => i === id)) || [null])[0]);
  const bareIP = (a) => (a || "").split("/")[0];

  // The gateways listed under Topology: bare address, a status dot, nothing else.
  async function refreshTopoNav() {
    try {
      // always the node you signed in to, and the gateway's state for the cluster as a whole (from its nodes), so the dot does not change with the Node menu
      const r = await api("GET", "/api/canvas?own=1", undefined, true);
      state.topo.list = (r.data || []).map((g) => ({ id: g.group_id, label: g.name || bareIP(g.vip4) || bareIP(g.vip6) || "gateway " + g.group_id,
        status: g.cluster_status || g.status, title: "Group " + g.group_id + (g.name ? " · " + (bareIP(g.vip4) || bareIP(g.vip6)) : "") + " — " + (g.cluster_status ? g.cluster_detail : g.detail) }));
    } catch (_) { return; }
    if (!state.topo.list.some((g) => g.id === state.topo.gid)) state.topo.gid = state.topo.list.length ? state.topo.list[0].id : null;
    renderTopoNav();
  }
  function renderTopoNav() {
    const box = root.querySelector('.nav-group[data-group="' + TOPO + '"] .nav-items');
    if (!box) return;
    const here = state.tab === "topology";
    clear(box).append(...state.topo.list.map((g) => h("button", { type: "button", class: "nav-item", title: g.title, "data-gw": g.id,
        "aria-current": here && g.id === state.topo.gid ? "page" : null,
        onclick: () => { state.topo.gid = g.id; selectTab("topology"); },
        oncontextmenu: rowMenu([["Rename…", () => { state.topo.gid = g.id; state.topo.action = "rename"; selectTab("topology"); }],
          ["Delete", () => { state.topo.gid = g.id; state.topo.action = "delete"; selectTab("topology"); }, "danger"]]) },
      h("span", { class: "dot st-" + g.status }), g.label)),
      h("button", { type: "button", class: "nav-item add", onclick: () => { state.topo.action = "new"; selectTab("topology"); } }, "＋ New gateway…"));
  }

  function openNavGroup(name) {
    for (const g of root.querySelectorAll(".nav-group")) {
      const open = g.dataset.group === name;
      g.querySelector(".nav-group-label").classList.toggle("open", open);
      g.querySelector(".nav-group-label").setAttribute("aria-expanded", String(open));
      g.querySelector(".nav-items").classList.toggle("hidden", !open);
    }
  }

  function showShell() {
    stopAllTimers();
    clear(root);
    const s = state.session;
    const groups = [[TOPO, []]].concat(NAV_GROUPS).map(([name, items]) => h("div", { class: "nav-group", "data-group": name },
      h("button", { type: "button", class: "nav-group-label", "aria-expanded": "false",
        onclick: () => { if (name === TOPO) selectTab("topology"); else openNavGroup(name); } },
        h("span", { class: "nav-chevron" }), name),
      h("div", { class: "nav-items hidden" },
        items.map(([id, label]) => h("button", { type: "button", class: "nav-item", "data-tab": id, onclick: () => selectTab(id) }, label)))));
    const nav = h("nav", { class: "side", "aria-label": "Sections" },
      h("div", { class: "mark" }, h("div", { class: "brand" }, "Anyname DNS Director"),
        h("div", { class: "muted small" }, "v" + s.version + (s.hostname && s.hostname.toLowerCase() !== "ddgw" ? " · " + s.hostname : ""))),
      h("div", { class: "nav-section" }, groups),
      h("div", { class: "nav-footer" },
        h("div", { class: "who" }, "Signed in as ", h("strong", {}, s.user)),
        h("button", { class: "nav-signout", type: "button", onclick: logout }, "Sign out")));
    root.append(helpControls());
    root.append(h("div", { class: "shell" }, nav,
      h("div", { class: "content" },
        h("div", { id: "topbar", class: "topbar hidden" }),
        h("div", { id: "banner", class: "banner" }),
        h("main", { id: "main" }))));
    state.target = null;
    state.nodes = [];
    state.clustered = false;
    document.body.classList.remove("remote");
    certBanner();
    refreshTopoNav();
    refreshNodes();
    state.topoTimer = setInterval(() => { if (!document.hidden) refreshTopoNav(); }, 4000);
    state.nodesTimer = setInterval(() => { if (!document.hidden) refreshNodes(); }, 6000);
    let wanted = location.hash.slice(1);
    if (wanted === "canvas") wanted = "topology"; // the page's old name: keep old bookmarks working
    if (wanted === "neighbors") wanted = "nodes"; // likewise
    if (wanted === "power" || wanted === "gateway") wanted = "node"; // Power and the short-lived Gateway page became Operate ▸ Node
    if (wanted === "bgpstatus") wanted = "anycaststatus"; // Monitor ▸ BGP became Monitor ▸ Anycast
    if (wanted === "bgp") wanted = "anycast"; // likewise Configure ▸ BGP
    if (wanted === "certificate") wanted = "cfg_web"; // moved under Configure ▸ Web GUI
    if (wanted === "config") wanted = "cfg_general"; // Configure ▸ Settings was split into one page per tab
    selectTab(TABS.some(([id]) => id === wanted) ? wanted : "topology");
  }

  async function logout() {
    try { await api("POST", "/api/logout", {}); } catch (_) { /* already gone */ }
    stopAllTimers();
    state.target = null;
    document.body.classList.remove("remote");
    state.session = null;
    showLogin();
  }

  function selectTab(id) {
    state.tab = id;
    if (state.target === CLUSTER && !CLUSTER_TABS.includes(id)) state.target = null;   // the other pages are about one node: back to this one
    renderTopbar();   // the Cluster entry is on the menu for Statistics and Host only
    history.replaceState(null, "", "#" + id);
    for (const b of root.querySelectorAll("nav.side .nav-item[data-tab]")) {
      if (b.dataset.tab === id) b.setAttribute("aria-current", "page"); else b.removeAttribute("aria-current");
    }
    const grp = groupOf(id);
    if (grp) openNavGroup(grp);
    renderTopoNav();
    stopPolling();
    const main = clear(document.getElementById("main"));
    const view = VIEWS[id];
    view.mount(main);
    if (document.getElementById("help-panel") && document.getElementById("help-panel").classList.contains("open")) fillHelp();
    if (view.poll) {
      startPolling(view.poll);
    }
  }

  // ── help: the "?" in the top right, a slide-out panel for the current page ──
  // Content is data in help.js (DDGW_HELP); this builds it with DOM calls only.
  function richText(str) {
    const out = [];
    for (const part of String(str).split(/(\*\*[^*]+\*\*|`[^`]+`|\*[^*]+\*)/)) {
      if (!part) continue;
      if (part.startsWith("**")) out.push(h("strong", {}, part.slice(2, -2)));
      else if (part.startsWith("`")) out.push(h("code", {}, part.slice(1, -1)));
      else if (part.startsWith("*") && part.length > 2) out.push(h("em", {}, part.slice(1, -1)));
      else out.push(part);
    }
    return out;
  }
  function helpBody(topic) {
    const t = DDGW_HELP[topic] || DDGW_HELP._default;
    const kids = t.body.map((blk) => {
      if (typeof blk === "string") return h("p", {}, richText(blk));
      if (blk[0] === "h") return h("h3", {}, blk[1]);
      if (blk[0] === "ul") return h("ul", {}, blk[1].map((li) => h("li", {}, richText(li))));
      if (blk[0] === "fields") return h("dl", { class: "help-fields" }, blk[1].flatMap(([name, text]) => [h("dt", {}, name), h("dd", {}, richText(text))]));
      if (blk[0] === "cli") return h("div", {}, h("h3", {}, "Command line"), h("pre", { class: "help-cli" }, blk[1]));
      return null;
    });
    return [t.title, kids];
  }
  function helpControls() {
    const overlay = h("div", { class: "help-overlay hidden", id: "help-overlay" });
    const title = h("h2", { id: "help-title" });
    const body = h("div", { class: "help-panel-body", id: "help-body" });
    const panel = h("aside", { class: "help-panel", id: "help-panel", "aria-hidden": "true", "aria-label": "Help" },
      h("div", { class: "help-panel-header" }, title), body);
    const btn = h("button", { class: "help-btn", id: "help-btn", type: "button", title: "Help for this page", "aria-label": "Help",
      onclick: () => setHelp(!panel.classList.contains("open")) }, "?");
    overlay.addEventListener("click", () => setHelp(false));
    return h("div", {}, btn, overlay, panel);
  }
  // Opening (and every page change while open) fills the panel for the current page.
  function setHelp(open) {
    const panel = document.getElementById("help-panel"), overlay = document.getElementById("help-overlay");
    if (!panel) return;
    if (open) fillHelp();
    panel.classList.toggle("open", !!open);
    overlay.classList.toggle("hidden", !open);
    panel.setAttribute("aria-hidden", open ? "false" : "true");
    const b = document.getElementById("help-btn");
    b.setAttribute("aria-label", open ? "Close help" : "Help");
    b.title = open ? "Close help (Esc)" : "Help for this page";
  }
  function fillHelp() {
    // the DNS proxy page has a help page for each of its tabs
    const topic = state.tab === "cfg_dns" && DDGW_HELP["config_dns" + state.dnsTab] ? "config_dns" + state.dnsTab : state.tab.startsWith("cfg_") ? "config" : state.tab;
    const [t, kids] = helpBody(topic);
    document.getElementById("help-title").textContent = t;
    const body = clear(document.getElementById("help-body"));
    body.append(...kids);
    body.scrollTop = 0;
  }
  document.addEventListener("keydown", (ev) => { if (ev.key === "Escape") setHelp(false); });

  // ── node picker (top right) ────────────────────────────────────────────────
  const selfNode = () => state.nodes.find((n) => n.self) || null;
  const currentNode = () => state.nodes.find((n) => n.addr === state.target) || null;
  // the host name, or the address too when two nodes share a name
  const nodeLabel = (n) => {
    if (!n) return "this node";
    if (!n.hostname) return n.addr;
    return state.nodes.filter((x) => x.hostname === n.hostname).length > 1 ? n.hostname + " (" + n.addr + ")" : n.hostname;
  };

  async function refreshNodes() {
    let v;
    try { v = (await api("GET", "/api/cluster", undefined, true)).data; } catch (_) { return; }
    state.clustered = !!(v && v.enabled && (v.peers || []).length > 1);
    state.nodes = state.clustered ? v.peers : [];
    if (state.target === CLUSTER ? state.nodes.length < 2 : state.target && !state.nodes.some((n) => n.addr === state.target && !n.self)) setTarget(null);
    renderTopbar();
  }

  function renderTopbar() {
    const bar = document.getElementById("topbar");
    if (!bar) return;
    clear(bar);
    bar.classList.toggle("hidden", state.nodes.length < 2);
    if (state.nodes.length < 2) return;
    const sel = h("select", { id: "node-pick", "aria-label": "Node to configure and monitor", onchange: (e) => setTarget(e.target.value || null) });
    const self = selfNode();
    sel.append(h("option", { value: "" }, nodeLabel(self) + " (this node)"));
    for (const n of state.nodes.filter((x) => !x.self)) {
      const o = h("option", { value: n.addr }, nodeLabel(n) + (n.is_primary ? " · primary" : "") + (n.reachable ? "" : " · unreachable"));
      if (!n.reachable && n.addr !== state.target) o.disabled = true;
      sel.append(o);
    }
    if (CLUSTER_TABS.includes(state.tab)) sel.append(h("option", { value: CLUSTER }, "Cluster (" + state.nodes.length + ")"));
    sel.value = state.target || "";
    bar.append(h("label", { for: "node-pick", class: "muted small" }, "Node"), sel);
  }

  function setTarget(addr) {
    if ((addr || null) === state.target) return;
    state.target = addr || null;
    document.body.classList.toggle("remote", !!state.target && state.target !== CLUSTER);   // another node's page is marked; the cluster's is no node
    renderTopbar();
    state.topo.action = null;   // the gateway picked stays picked: the sidebar list is the signed-in node's, whichever node is driven
    refreshTopoNav();
    certBanner();
    if (document.getElementById("main")) selectTab(state.tab);
  }

  function startPolling(fn) {
    const tick = async () => { if (!document.hidden) { try { await fn(); } catch (_) { /* shown by view */ } } };
    tick();
    state.timer = setInterval(tick, POLL_MS);
  }
  function stopPolling() { if (state.timer) clearInterval(state.timer); state.timer = null; }
  // Every timer the signed-in shell owns.  A session that ends by itself (an
  // update restarts the daemon, an idle timeout) must stop the sidebar and node
  // pollers too: they kept getting 401 and redrew the login form every few
  // seconds, wiping what was being typed (it looked like the form blinking).
  function stopAllTimers() {
    stopPolling();
    if (state.topoTimer) clearInterval(state.topoTimer);
    if (state.nodesTimer) clearInterval(state.nodesTimer);
    state.topoTimer = state.nodesTimer = null;
  }
  document.addEventListener("visibilitychange", () => {
    if (!document.hidden && state.timer && VIEWS[state.tab].poll) VIEWS[state.tab].poll().catch(() => {});
  });

  function errorBox(el, msg) {
    clear(el).append(h("div", { class: "notice bad", role: "alert" }, msg));
  }

  // ── views ─────────────────────────────────────────────────────────────────
  const VIEWS = {};


  // Topology  (CLI: ddgw --canvas …)
  // One tab per gateway.  Circle = the gateway and its shared address, squares =
  // the DNS servers it forwards to, trapezoids = the domains each server must
  // answer to count as healthy.  Every change is saved as soon as it is made.
  VIEWS.topology = (() => {
    const SVGNS = "http://www.w3.org/2000/svg";
    const sv = (tag, attrs, ...kids) => {
      const el = document.createElementNS(SVGNS, tag);
      for (const [k, v] of Object.entries(attrs || {})) {
        if (v == null || v === false) continue;
        if (k.startsWith("on")) el.addEventListener(k.slice(2), v); else el.setAttribute(k, v);
      }
      for (const kid of kids.flat(Infinity)) if (kid != null) el.append(kid.nodeType ? kid : document.createTextNode(String(kid)));
      return el;
    };
    const QTYPES = ["A", "AAAA", "NS", "MX", "TXT", "SOA", "CNAME"];
    const CV_ZOOM_MIN = 0.2, CV_ZOOM_MAX = 3;   // what the mouse wheel can zoom the drawing to
    const CV_MIN_SCALE = 0.65;   // the drawing shrinks to fit the window down to this, then scrolls
    const cv = { cfg: null, orig: null, gid: null, sel: null, view: [], loaded: false, drawn: null, ro: null };
    let svgHost, noteEl, msgEl, legendEl, pending = 0;

    const normAddr = (a) => {
      a = a.trim().toLowerCase();
      if (/^\[.*\]:\d+$/.test(a)) return a;
      if (a.split(":").length > 2) return "[" + a.replace(/^\[|\]$/g, "") + "]:53";
      if (/:\d+$/.test(a)) return a;
      return a + ":53";
    };
    const shortAddr = (a) => a.replace(/:53$/, "").replace(/^\[(.*)\]$/, "$1");
    // the optional name of a server (dns.server_names), shown under its address
    const srvName = (g, addr) => ((g.dns || cv.cfg.dns || {}).server_names || {})[addr] || "";
    const srvTitle = (g, addr) => (srvName(g, addr) ? srvName(g, addr) + " (" + shortAddr(addr) + ")" : shortAddr(addr));
    const trunc = (t, n) => (t.length > n ? t.slice(0, n - 1) + "…" : t);
    // cut a text to a pixel width (measured in the font the drawing uses), ending in … when it had to be cut
    let measureCtx = null;
    const fitPx = (t, maxPx, font) => {
      try { measureCtx = measureCtx || document.createElement("canvas").getContext("2d"); } catch (e) { measureCtx = null; }
      if (!measureCtx) return trunc(t, Math.floor(maxPx / 7.5));
      measureCtx.font = font;
      if (measureCtx.measureText(t).width <= maxPx) return t;
      let lo = 0, hi = t.length;
      while (lo < hi) { const m = (lo + hi + 1) >> 1; if (measureCtx.measureText(t.slice(0, m) + "…").width <= maxPx) lo = m; else hi = m - 1; }
      return t.slice(0, lo).trimEnd() + "…";
    };
    const groups = () => cv.cfg.groups;
    const curGroup = () => groups().find((g) => g.group_id === cv.gid) || null;
    // the cluster nodes currently serving a gateway, for the circle's tooltip
    // "Online for 2h 5m - 1 failure": the daemon notes when each item last flipped between working and
    // failing and how many failures it has seen (u = {up, since, failures}); nothing for grey or paused items
    const spanText = (secs) => {
      const s = Math.max(0, Math.floor(secs)), d = Math.floor(s / 86400), hh = Math.floor(s / 3600) % 24, m = Math.floor(s / 60) % 60;
      return d ? d + "d " + hh + "h" : hh ? hh + "h " + m + "m" : m ? m + "m " + (s % 60) + "s" : s + "s";
    };
    const upText = (u) => !u ? "" : (u.up ? "Online for " : "Down for ") + spanText(Date.now() / 1000 - u.since) + " - " + (u.failures || 0) + ((u.failures || 0) === 1 ? " failure" : " failures");
    const upLine = (u) => (u ? "\n" + upText(u) : "");
    const membersText = (ms) => "Serving nodes: " + (ms.length || "none yet");   // how many, not which: each node's own tooltip says what it is
    // The gateway circle's tooltip: its name, how long it has been up, each address family's state, and how many
    // nodes serve it.  The overall status line is added only when it says more than the family lines do (a
    // pause, a gateway with one family, a start still waiting); it is the same words as the two together otherwise.
    const vmacWords = { delivered: "delivered", "not-delivered": "NOT delivered", inconclusive: "inconclusive" };
    const vmacLine = (r) => "Virtual MACs: " + (vmacWords[r.verdict] || r.verdict) + " — " + r.detail;
    const gwTip = (g, st, dual) => {
      const fam = (af) => (st.fams[af] ? st.fams[af].detail : "not running");
      const together = dual ? "IPv4: " + fam("v4") + "; IPv6: " + fam("v6") : null;
      return ["Gateway " + gwLabel(g), st.why && st.why !== together ? st.why : "", upText(st.uptime),
        ...(dual ? [["IPv4", "v4"], ["IPv6", "v6"]].map(([n, af]) => n + " — " + fam(af)) : []),
        (g.more_vip4 || []).length + (g.more_vip6 || []).length ? "Also answers on " + [...(g.more_vip4 || []), ...(g.more_vip6 || [])].join(", ") : "",
        membersText(st.members), st.vmac && st.vmac.verdict !== "off" && !(st.why || "").startsWith("Virtual MACs:") ? vmacLine(st.vmac) : ""].filter(Boolean).join("\n");
    };
    const gwAddr = (g) => bareIP(g.vip4) || bareIP(g.vip6) || "gateway " + g.group_id;
    const gwLabel = (g) => g.name || gwAddr(g);
    const effQueries = (g, addr) => {
      const d = g.dns || {};
      return (d.server_queries && d.server_queries[addr]) || d.queries || [];
    };
    const ownQueries = (g, addr) => {
      const d = g.dns;
      d.server_queries = d.server_queries || {};
      if (!d.server_queries[addr]) d.server_queries[addr] = (d.queries || []).map((q) => ({ ...q }));
      return d.server_queries[addr];
    };

    // The daemon works out every colour (the CLI shows the same view); anything
    // not in it yet — an unsaved shape — is grey.
    function statusOf(g) {
      const v = (cv.view || []).find((x) => x.group_id === g.group_id);
      const servers = {};
      if (v) for (const x of v.servers) servers[x.addr] = x;
      const fams = {};
      if (v) for (const f of v.families || []) fams[f.af] = f;
      const members = v ? v.members || [] : [];
      let circle = v ? v.status : "idle", why = v ? v.detail : "Starting — not running yet";
      // what is staged wins over the last live view, so the pause / resume shows at once
      const via = !!(v && v.via);   // this node does not serve the gateway: the picture is that of a node that does, so nothing local colours it
      const nodePaused = !via && !!(v && v.node_paused);
      const gwPaused = !via && !!(g.paused || g.paused_all);
      const me = ((v && v.nodes) || []).find((n) => n.self);
      const removed = !via && !!(me && me.node_id && (g.excluded_nodes || []).includes(me.node_id));   // this node was removed from the gateway
      if (gwPaused || nodePaused || removed) { circle = "paused"; why = g.paused_all ? "Paused on all nodes — nothing is serving it until resumed" : removed && !g.paused && !nodePaused ? "This node was removed from the gateway — it does not serve it; the other nodes carry on" : nodePaused && !g.paused ? "This node is paused (Operate ▸ Node) — it is not serving; the other nodes carry on" : "Paused on this node — it is not serving; the other nodes carry on"; }
      else if (!via && circle === "paused") { circle = "idle"; why = "Resuming…"; }
      if (via) why = "As " + v.via + " sees it (it serves the gateway; this node does not): " + why;
      return { via: (v && v.via) || "", circle, why, servers, fams, members, anycast: v ? v.anycast || [] : [], nodes: v ? v.nodes || [] : [], vmac: v && !via ? v.vmac || null : null, uptime: v && !gwPaused && !nodePaused && !removed ? v.uptime : null };
    }
    // Where a server / domain is paused: "all" (shared, every node), "node" (this node's own settings) or "".
    const srvPausedScope = (g, addr) => (((g.dns || cv.cfg.dns || {}).paused_servers || []).includes(addr) ? "all" : (cv.cfg.paused_servers_here || []).includes(addr) ? "node" : "");
    const pausedSrv = (g, addr) => !!srvPausedScope(g, addr);
    const qKey = (addr, q) => addr + "|" + String(q.name).replace(/\.$/, "").toLowerCase() + "|" + String(q.type).toUpperCase();
    const qPausedScope = (g, addr, q) => ((((g.dns || cv.cfg.dns || {}).paused_queries || []).includes(qKey(addr, q))) ? "all" : (cv.cfg.paused_queries_here || []).includes(qKey(addr, q)) ? "node" : "");
    const scopeWord = (sc) => (sc === "all" ? "all nodes" : "this node");
    function serverState(g, addr, st, via) {
      const sc = via ? "" : srvPausedScope(g, addr);   // seen from a serving node: what it says, not this node's own pauses
      if (sc) return { c: "paused", why: "Paused on " + scopeWord(sc) + " — not queried or probed until resumed" };
      if (st && st.status === "paused") return via ? { c: "paused", why: st.detail } : { c: "idle", why: "Resuming…" };
      return st ? { c: st.status, why: st.detail } : { c: "idle", why: "Not probed yet" };
    }
    // Pause / resume: saved at once like every other change.
    function quick(change) { change(); commit(); }
    // Toggle one entry in a list held on `obj` (deleting the list when it empties).
    function toggleIn(obj, key, val, on) {
      const l = (obj[key] || []).filter((x) => x !== val);
      obj[key] = on ? l.concat(val) : l;
      if (!obj[key].length) delete obj[key];
    }
    function pauseGateway(g, scope, pause) {
      const k = scope === "all" ? "paused_all" : "paused";
      if (!pause) { quick(() => { delete g[k]; }); return; }
      confirmDialog("Pause gateway " + gwLabel(g) + " on " + scopeWord(scope) + "?",
        scope === "all"
          ? "Every node stops serving the gateway — it gives up its address and stops answering DNS. Resume it when you are done."
          : "This node stops serving the gateway — it gives up its address and stops answering DNS — so it can be taken offline. The other cluster nodes carry on. Resume it here when you are done.",
        "Pause gateway", () => quick(() => { g[k] = true; }), "btn primary");
    }
    // "Test virtual MACs…": asks this node to check that replies addressed to a virtual MAC reach it, and shows what it found.
    // Nothing changes unless the person presses "Use real MAC addresses".
    async function vmacTest(g) {
      const body = h("div", {}, h("p", {}, "Testing… this takes a few seconds."));
      const toolbar = h("div", { class: "toolbar" }, h("button", { class: "btn", type: "button", onclick: closeEditor }, "Close"));
      openDialog("Virtual MACs: " + gwLabel(g), body, toolbar);
      try {
        const r = await api("POST", "/api/vmactest", { group: g.group_id });
        const x = (r.data || [])[0];
        if (!x) throw new Error("no result");
        clear(body).append(h("p", {}, vmacLine(x)), ...(x.families || []).map((f) => h("p", { class: "hint" }, f.af + ": " + f.detail)));
        if (x.verdict === "not-delivered") {
          toolbar.prepend(h("button", { class: "btn primary", type: "button", onclick: () => { closeEditor(); quick(() => { g.real_macs = true; }); } }, "Use real MAC addresses"));
        }
      } catch (e) {
        clear(body).append(h("p", {}, "The test could not run: " + e.message));
      }
    }
    function pauseServer(g, addr, scope, pause) {
      quick(() => {
        if (scope === "all") { ensureDNS(g); toggleIn(g.dns, "paused_servers", addr, pause); }
        else toggleIn(cv.cfg, "paused_servers_here", addr, pause);
      });
    }
    function pauseDomain(g, addr, q, scope, pause) {
      quick(() => {
        if (scope === "all") { ensureDNS(g); toggleIn(g.dns, "paused_queries", qKey(addr, q), pause); }
        else toggleIn(cv.cfg, "paused_queries_here", qKey(addr, q), pause);
      });
    }

    // ── drawing ──────────────────────────────────────────────────────────
    function draw() {
      cv.stale = 0;
      // the drawing is rebuilt on every poll: keep where the person had scrolled it to (the same gateway only)
      const oldWrap = svgHost.querySelector(".cv-wrap");
      const keep = oldWrap && cv.drawn === cv.gid ? { x: oldWrap.scrollLeft, y: oldWrap.scrollTop } : { x: 0, y: 0 };
      const g = curGroup();
      if (!g) { clear(svgHost); clear(noteEl); svgHost.append(h("div", { class: "empty" }, "No gateway yet. Click “＋ New gateway…” under Topology in the sidebar (or right-click here) to draw one.")); return; }
      const st = statusOf(g);
      const servers = (g.dns && g.dns.servers) || [];
      const COLW = 190, SW = 156, DW = 156, DH = 34, DG = 46;
      const maxDoms = Math.max(0, ...servers.map((a) => effQueries(g, a).length));
      // The circle shows bare addresses (no /prefix) and grows until the longest one fits,
      // up to a full 39-character IPv6 address (monospace 12px ≈ 7.3px per character).
      const ip4 = (g.vip4 || "").split("/")[0], ip6 = (g.vip6 || "").split("/")[0];
      const dual = !!(ip4 && ip6);
      const textW = Math.max(ip4.length, ip6.length, 9) * 7.3;
      const R = Math.max(50, Math.ceil(Math.hypot((textW + 22) / 2, dual ? 24 : 14)));
      const anys = g.extra_vips || [], AH = 40, AG = 8;      // anycast pills sit to the right of the circle, the cluster's nodes to the left
      const AW = Math.max(170, Math.ceil(Math.max(0, ...anys.map((a) => a.length)) * 7.3 + 28));  // wide enough for the longest address in full (monospace 12px)
      const waiting = cv.nodeWait || {};
      // a node removed from the gateway is not drawn (right-click the gateway ▸ Add node brings it back); the saved list decides, so it goes at once
      const nodes = (st.nodes || []).filter((n) => !(g.excluded_nodes || []).includes(n.node_id)).map((n) => {   // a pause or resume just asked for, not yet reported back
        const w = waiting[n.addr];
        if (!w) return n;
        if (!!n.node_paused === w.want || Date.now() > w.until) { delete waiting[n.addr]; return n; }
        return { ...n, status: w.want ? "paused" : "idle", label: w.want ? "pausing…" : "resuming…" };
      }), NW = 190, NSL = 14;      // a node is a parallelogram: its name and what it is doing
      // each side is four rows tall, or three when a four-row side would have a server's line run through a box; the next ones go in a new column further out
      const CG = 25, SIDE_GAP = 70;   // SIDE_GAP: room between the circle and the nodes on its left, the anycast pills on its right
      const SH = servers.some((a) => srvName(g, a)) ? 62 : 46;   // a named server has a third line, so every square in the row is taller
      const CY = R + 12;
      const layout = (PER, extra = 0) => {
        const sideH = (n) => Math.min(PER, n) * (AH + AG) - (n ? AG : 0);
        const aCols = Math.ceil(anys.length / PER), nCols = Math.ceil(nodes.length / PER);
        const anyH = sideH(anys.length), nodeH = sideH(nodes.length);
        const anyTop = Math.max(8, CY - anyH / 2), nodeTop = Math.max(8, CY - nodeH / 2);
        const anyAt = (k) => ({ x: R + SIDE_GAP + Math.floor(k / PER) * (AW + CG), y: anyTop + (k % PER) * (AH + AG) });   // x from the circle's centre
        const nodeAt = (k) => ({ x: -(R + SIDE_GAP + NW + Math.floor(k / PER) * (NW + CG)), y: nodeTop + (k % PER) * (AH + AG) });
        const rightX = anys.length ? R + SIDE_GAP + aCols * AW + (aCols - 1) * CG : 0, leftX = nodes.length ? R + SIDE_GAP + nCols * NW + (nCols - 1) * CG : 0;
        const sideBottom = Math.max(anys.length ? anyTop + anyH : 0, nodes.length ? nodeTop + nodeH : 0);
        // extra is spare height to use.  It goes into the gaps (circle to servers, servers to their first domain, domain to
        // domain) so that they come out as even as the drawing allows: the smallest gaps grow first, a gap that is already
        // larger than the rest keeps its size.
        const dm = Math.max(maxDoms - 1, 0), SY0 = Math.max(CY + R + 80, sideBottom ? sideBottom + 84 : 0);
        const floors = [SY0 - (CY + R), 34, ...Array(dm).fill(DG - DH)];
        let gaps = floors;
        if (extra > 0) {
          const order = floors.map((f, i) => i).sort((x, y) => floors[y] - floors[x]);   // largest first
          let left = floors.reduce((a, b) => a + b, 0) + extra, k = floors.length;
          for (const i of order) { if (floors[i] > left / k) { left -= floors[i]; k--; } else break; }
          gaps = floors.map((f) => Math.max(f, left / k));
        }
        const eTop = gaps[0] - floors[0], eMid = gaps[1] - floors[1], eDom = dm ? gaps[2] - floors[2] : 0;
        const DGE = DG + eDom;   // distance from one domain to the next
        const SY = SY0 + eTop, DY0 = SY + SH + 34 + eMid;   // servers' top, first domain's top
        const W = Math.max(Math.max(servers.length, 1) * COLW + 20, 2 * R + 140, 2 * Math.max(rightX, leftX) + 20), H = DY0 + (Math.max(maxDoms, 1) - 1) * DGE + DG + 6;
        const cx = W / 2, rowX = (W - Math.max(servers.length, 1) * COLW) / 2;   // the row of servers is centred under the circle
        // does a line from the circle to a server pass through a node or a pill?
        const boxes = [...anys.map((_, k) => ({ ...anyAt(k), w: AW })), ...nodes.map((_, k) => ({ ...nodeAt(k), w: NW }))];
        let clash = false;
        servers.forEach((_, i) => {
          const x2 = rowX + i * COLW + COLW / 2;
          for (let t = 0; t <= 1 && !clash; t += 0.02) {
            const px = cx + (x2 - cx) * t - cx, py = CY + R + (SY - CY - R) * t;
            if (boxes.some((b) => px > b.x - 3 && px < b.x + b.w + 3 && py > b.y - 3 && py < b.y + AH + 3)) clash = true;
          }
        });
        return { PER, anyAt, nodeAt, SY, DY0, DGE, W, H, cx, rowX, clash };
      };
      let lay = layout(4);
      if (lay.clash) lay = layout(3);
      // When the window is taller than the drawing at the size it will be shown, the rows are spread apart to use the
      // height (never more than twice as tall as drawn).
      if (svgHost.isConnected) {   // the zoom does not come into it: zooming scales this layout, it must not change it
        const hostW = oldWrap && svgHost.contains(oldWrap) ? oldWrap.clientWidth : svgHost.clientWidth;
        if (hostW > 0) {
          const s0 = Math.max(CV_MIN_SCALE, Math.min(1, hostW / lay.W));   // the size it is fitted to, whatever the zoom is
          const after = svgHost.nextElementSibling;   // the legend under the card
          let top = 0;   // where the card is on the page, not on the screen: scrolling the page (it scrolls when zoomed in) must not move the layout
          for (let el = svgHost; el; el = el.offsetParent) top += el.offsetTop;
          const room = window.innerHeight - top - (after ? after.offsetHeight + 24 : 0) - 16;
          const extra = Math.min(room / s0 - lay.H, lay.H, 600);
          if (extra > 20) { const taller = layout(lay.PER, extra); if (!taller.clash) lay = taller; }
        }
      }
      const { anyAt, nodeAt, SY, DY0, DGE, W, H, cx, rowX } = lay;
      const svg = sv("svg", { class: "cv", viewBox: `0 0 ${W} ${H}`, width: W, height: H, role: "group", "aria-label": "Gateway diagram", tabindex: "0",
        onkeydown: (e) => { if ((e.key === "Delete" || e.key === "Backspace") && cv.sel && !/INPUT|SELECT|TEXTAREA/.test(e.target.tagName)) { e.preventDefault(); delSel(); } },
        onclick: (e) => { if (e.target === svg) { cv.sel = null; refresh(); } } });
      const sel = (kind, addr, di) => cv.sel && cv.sel.kind === kind && cv.sel.addr === addr && cv.sel.di === di;
      const pick = (kind, addr, di) => (e) => {
        e.stopPropagation(); cv.sel = { kind, addr, di }; refresh();
        const el = svgHost.querySelector("svg"); if (el) el.focus({ preventScroll: true }); // keep the Delete key working after the redraw
      };
      const label = (x, y, t, cls) => sv("text", { x, y, "text-anchor": "middle", class: cls || "" }, t);
      const lines = [];
      const shapes = [];
      // Drag a server left/right, a domain or an anycast address up/down to another place.  The shape follows the pointer
      // (a server takes its domains with it), a dashed outline shows where it will land, and letting go saves the new
      // order at once.  Nothing is redrawn while it is held (the poll skips cv.drag); Escape puts it back.
      const dragStart = (sp) => (e) => {
        if (e.button !== 0 || cv.drag || sp.n < 2) return;
        const shape = e.currentTarget, x0 = e.clientX, y0 = e.clientY;
        const els = sp.group ? [...svg.querySelectorAll("[data-col]")].filter((el) => el.getAttribute("data-col") === sp.group) : [shape];
        let on = false, target = sp.index, ghost = null;
        const unitsPerPx = () => 1 / ((svg.getBoundingClientRect().width / W) || 1);
        const lift = els.filter((el) => el.classList.contains("shape"));   // the shapes that travel: drawn last, so on top
        const setGhost = (idx) => { if (ghost) ghost.remove(); ghost = sp.ghost(idx); svg.insertBefore(ghost, lift[0]); };   // over the others, under the held one
        const end = () => { window.removeEventListener("pointermove", move); window.removeEventListener("pointerup", up); window.removeEventListener("pointercancel", cancel); window.removeEventListener("keydown", key, true); };
        const move = (ev) => {
          if (!on) {
            if (Math.hypot(ev.clientX - x0, ev.clientY - y0) < 6) return;
            on = true; cv.drag = true;
            shape.classList.add("dragging"); svg.classList.add("dragging");
            for (const el of lift) svg.appendChild(el);
            setGhost(target);
          }
          let t;
          if (sp.axis === "xy") {   // a grid of slots (the anycast addresses, four to a column): the pointer picks the nearest slot
            const u = unitsPerPx(), dx = (ev.clientX - x0) * u, dy = (ev.clientY - y0) * u;
            for (const el of els) el.style.transform = `translate(${dx}px, ${dy}px)`;
            let best = Infinity; t = 0;
            for (let k = 0; k < sp.n; k++) { const p = sp.slot(k), dist = Math.hypot(p.x - (sp.pos.x + dx), p.y - (sp.pos.y + dy)); if (dist < best) { best = dist; t = k; } }
          } else {
            const d = (sp.axis === "x" ? ev.clientX - x0 : ev.clientY - y0) * unitsPerPx();
            t = Math.max(0, Math.min(sp.n - 1, Math.round((sp.pos + d - sp.origin) / sp.step)));
            for (const el of els) el.style.transform = sp.axis === "x" ? `translate(${d}px, 0)` : `translate(0, ${d}px)`;
          }
          if (t !== target) { target = t; setGhost(t); }
        };
        const finish = (drop) => {
          end();
          if (!on) return;
          cv.drag = false;
          // the click that follows the button release must not select (and redraw) anything
          const swallow = (ev) => { ev.stopPropagation(); ev.preventDefault(); };
          window.addEventListener("click", swallow, true);
          setTimeout(() => window.removeEventListener("click", swallow, true), 0);
          if (drop && target !== sp.index) sp.drop(target); else draw();
        };
        const up = () => finish(true);
        const cancel = () => finish(false);
        const key = (ev) => { if (ev.key === "Escape") { ev.stopPropagation(); finish(false); } };
        window.addEventListener("pointermove", move);
        window.addEventListener("pointerup", up);
        window.addEventListener("pointercancel", cancel);
        window.addEventListener("keydown", key, true);
      };
      const reorder = (list, from, to) => { const [it] = list.splice(from, 1); list.splice(to, 0, it); };
      shapes.push(sv("g", { class: "shape st-" + st.circle + (sel("gw") ? " sel" : ""), tabindex: "0", role: "button", "aria-label": "Gateway " + gwLabel(g), onclick: pick("gw"), oncontextmenu: rightClick("gw"),
        onkeydown: (e) => { if (e.key === "Enter") pick("gw")(e); } },
        sv("title", {}, gwTip(g, st, dual)),
        sv("circle", { cx, cy: CY, r: R }),
        ...(dual
          ? [label(cx, CY - 14, ip4, "ip"), label(cx, CY + 4, ip6, "ip"), label(cx, CY + 24, g.name ? g.name.slice(0, 22) : g.interface + " · group " + g.group_id, "t2")]
          : [label(cx, CY - 2, ip4 || ip6, "ip"), label(cx, CY + 18, g.name ? g.name.slice(0, 22) : g.interface + " · group " + g.group_id, "t2")])));
      anys.forEach((addr, i) => {
        const at = anyAt(i), x = cx + at.x, y = at.y;
        const a = st.anycast.find((z) => z.addr === addr);
        const noSrv = !(g.dns && (g.dns.servers || []).length);
        // A pill is never more alarming than its gateway: green while announced,
        // otherwise it takes the gateway's own colour only when that is amber, red or paused.
        const pzAll = (g.paused_vips || []).includes(addr), pzHere = !st.via && (g.paused_vips_here || []).includes(addr), pz = pzAll || pzHere;
        const c = pz ? "paused" : noSrv || !a ? "idle" : a.up ? (a.status === "warn" || a.status === "bad" ? a.status : "ok") : ["warn", "bad", "paused"].includes(st.circle) ? st.circle : "idle";
        const why = pz ? "Paused on " + (pzAll ? "all nodes" : "this node") + ": not announced until resumed" : noSrv ? "Add a DNS server to this gateway: the address is only announced while a server answers"
          : !st.via && (g.paused || g.paused_all) ? "Gateway paused on " + (g.paused_all ? "all nodes" : "this node") + ": not announced"
          : !a ? "Applying…" : a.up ? "Announced from " + (st.via || "this node") + " (on lo)" + (a.carried && a.carried.length ? " — kept up by " + a.carried.map((n) => { const x = groups().find((z) => z.group_id === n); return x ? gwLabel(x) : "group " + n; }).join(", ") + (a.reason ? ", because this gateway's DNS cannot answer here (" + a.reason + ")" : "") : "") + (a.detail ? " — " + a.detail : "") : "Withdrawn on " + (st.via || "this node") + (a.reason ? ": " + a.reason : "");
        lines.push(sv("line", { x1: cx + R, y1: CY, x2: x, y2: y + AH / 2, class: "edge" }));
        shapes.push(sv("g", { class: "shape drag st-" + c + (sel("any", addr) ? " sel" : ""), tabindex: "0", role: "button", "aria-label": "Anycast address " + addr, onclick: pick("any", addr), oncontextmenu: rightClick("any", addr),
          onpointerdown: dragStart({ axis: "xy", n: anys.length, index: i, pos: { x, y }, slot: (k) => ({ x: cx + anyAt(k).x, y: anyAt(k).y }),
            ghost: (k) => sv("rect", { class: "dropslot", x: cx + anyAt(k).x, y: anyAt(k).y, width: AW, height: AH, rx: 18 }),
            drop: (k) => { reorder(g.extra_vips, i, k); cv.sel = { kind: "any", addr }; commit(); } }),
          onkeydown: (e) => { if (e.key === "Enter") pick("any", addr)(e); } },
          sv("title", {}, "Anycast " + addr + " — " + why + (a && !noSrv && !g.paused && !g.paused_all && !pz ? upLine(a.uptime) : "")),
          sv("rect", { x, y, width: AW, height: AH, rx: 18 }),
          label(x + AW / 2, y + 17, addr, "ip"),
          label(x + AW / 2, y + 32, a && a.up && !noSrv && !pz && a.status === "warn" ? "anycast · neighbor down" : a && a.up && !noSrv && !pz && a.status === "bad" ? (a.bgp === "disabled" ? "anycast · BGP disabled" : "anycast · no BGP session") : c === "ok" ? "anycast · announced" : c === "paused" && pz ? "paused · " + (pzAll ? "all nodes" : "this node") : c === "paused" ? "anycast · paused" : a && !a.up && !noSrv ? "anycast · withdrawn" : "anycast", "t2")));
      });
      // the cluster's nodes, left of the circle, as parallelograms (read-only: they cannot be dragged or deleted here)
      nodes.forEach((n, i) => {
        const at = nodeAt(i), x = cx + at.x, y = at.y, mid = x + NW / 2;
        lines.push(sv("line", { x1: cx - R, y1: CY, x2: x + NW - NSL / 2, y2: y + AH / 2, class: "edge" + (n.status === "bad" ? " down" : "") }));
        shapes.push(sv("g", { class: "shape st-" + n.status, tabindex: "0", role: "button", "aria-label": "Cluster node " + n.name, oncontextmenu: nodeMenu(n), onkeydown: (e) => { if (e.key === "Enter") nodeMenu(n)(e); } },
          sv("title", {}, nodeTip(n)),
          sv("polygon", { points: `${x + NSL},${y} ${x + NW},${y} ${x + NW - NSL},${y + AH} ${x},${y + AH}` }),
          label(mid, y + 17, fitPx(n.name + (n.self ? " *" : ""), NW - 2 * NSL - 16, "600 13px system-ui, sans-serif"), "t1"),
          label(mid, y + 32, fitPx(n.label, NW - 2 * NSL - 12, "400 11px system-ui, sans-serif"), "t2")));
      });
      servers.forEach((addr, i) => {
        const x = rowX + i * COLW + (COLW - SW) / 2, mx = x + SW / 2;
        const ss = serverState(g, addr, st.servers[addr], st.via);
        const info = st.servers[addr];
        lines.push(sv("line", { x1: cx, y1: CY + R, x2: mx, y2: SY, class: "edge" + (info && info.in_band ? " spread" : "") }));
        shapes.push(sv("g", { class: "shape drag st-" + ss.c + (sel("srv", addr) ? " sel" : ""), "data-col": addr, tabindex: "0", role: "button", "aria-label": "DNS server " + srvTitle(g, addr), onclick: pick("srv", addr), oncontextmenu: rightClick("srv", addr),
          onpointerdown: dragStart({ axis: "x", n: servers.length, index: i, pos: x, origin: rowX + (COLW - SW) / 2, step: COLW, group: addr,
            ghost: (k) => sv("rect", { class: "dropslot", x: rowX + (COLW - SW) / 2 + k * COLW, y: SY, width: SW, height: SH, rx: 4 }),
            drop: (k) => { ensureDNS(g); reorder(g.dns.servers, i, k); cv.sel = { kind: "srv", addr }; commit(); } }),
          onkeydown: (e) => { if (e.key === "Enter") pick("srv", addr)(e); } },
          sv("title", {}, srvTitle(g, addr) + " — " + ss.why + (info && ss.c !== "paused" && ss.c !== "idle" ? upLine(info.uptime) : "")),
          sv("rect", { x, y: SY, width: SW, height: SH, rx: 4 }),
          label(mx, SY + (srvName(g, addr) ? 19 : 20), fitPx(shortAddr(addr), SW - 44, "600 13px system-ui, sans-serif"), "t1"),
          ...(srvName(g, addr) ? [label(mx, SY + 35, fitPx(srvName(g, addr), SW - 44, "400 11px system-ui, sans-serif"), "t2")] : []),
          label(mx, SY + (srvName(g, addr) ? 51 : 35), info && (ss.c === "ok" || ss.c === "warn") ? info.ms + " ms" + (info.rank ? " · #" + info.rank : "") : ss.c === "bad" ? "down" : ss.c === "paused" ? "paused" : "", "t2")));
        const qs = effQueries(g, addr);
        qs.forEach((q, di) => {
          const y = DY0 + di * DGE;
          const t = info && (info.tests || []).find((z) => z.name === q.name && z.type === q.type);
          const qsc = st.via ? "" : qPausedScope(g, addr, q);
          const sp = ss.c === "paused" || !!qsc;
          const c = sp ? "paused" : t && t.status !== "paused" ? t.status : st.via && t ? "paused" : "idle";
          const why = ss.c === "paused" ? "Server paused" : qsc ? "Paused on " + scopeWord(qsc) + " — not asked until resumed" : t && t.status !== "paused" ? t.detail : "Not tested yet";
          lines.push(sv("line", { x1: mx, y1: di ? y - DGE + DH : SY + SH, x2: mx, y2: y, class: "edge", "data-col": addr }));
          shapes.push(sv("g", { class: "shape drag st-" + c + (sel("dom", addr, di) ? " sel" : ""), "data-col": addr, tabindex: "0", role: "button", "aria-label": "Domain " + q.name, onclick: pick("dom", addr, di), oncontextmenu: rightClick("dom", addr, di),
          onpointerdown: dragStart({ axis: "y", n: qs.length, index: di, pos: y, origin: DY0, step: DGE,
            ghost: (k) => { const gy = DY0 + k * DGE; return sv("polygon", { class: "dropslot", points: `${mx - DW / 2 + 14},${gy} ${mx + DW / 2 - 14},${gy} ${mx + DW / 2},${gy + DH} ${mx - DW / 2},${gy + DH}` }); },
            drop: (k) => { ensureDNS(g); reorder(ownQueries(g, addr), di, k); cv.sel = { kind: "dom", addr, di: k }; commit(); } }),
            onkeydown: (e) => { if (e.key === "Enter") pick("dom", addr, di)(e); } },
            sv("title", {}, q.name + " (" + q.type + ") — " + why + (t && !sp && c !== "idle" ? upLine(t.uptime) : "")),
            sv("polygon", { points: `${mx - DW / 2 + 14},${y} ${mx + DW / 2 - 14},${y} ${mx + DW / 2},${y + DH} ${mx - DW / 2},${y + DH}` }),
            label(mx, y + 21, fitPx(q.name + (q.type === "A" ? "" : " " + q.type), DW - 46, "400 13px system-ui, sans-serif"), "t1 dn")));
        });
      });
      svg.append(...lines, ...shapes);
      // The scrolling box (.cv-wrap) is made once and kept: only the drawing inside it is swapped on each refresh, so its
      // scrollbars neither blink nor get pulled from under the pointer while they are being dragged.
      let wrap = oldWrap && svgHost.contains(oldWrap) ? oldWrap : null;
      if (!wrap) {
        clear(svgHost);
        wrap = h("div", { class: "cv-wrap" });
        svgHost.append(wrap);
        wrap.addEventListener("wheel", (e) => { if (cv.zoomBy) cv.zoomBy(e); }, { passive: false });
        // A double-click on the empty drawing fits it again. Timed by hand from the two presses: the first click redraws
        // the drawing (it deselects), and the browser then does not report the pair as a double-click.
        let lastDown = null;
        wrap.addEventListener("dragstart", (e) => e.preventDefault());   // never a native drag of selected text or an image
        // Drag the empty drawing to move around it, in any direction (the box scrolls sideways, the page up and down).
        wrap.addEventListener("pointerdown", (e) => {
          const t = e.target;
          if (e.button !== 0 || cv.drag || !(t === wrap || (t.tagName && t.tagName.toLowerCase() === "svg"))) return;   // only the empty background
          const now = Date.now();
          if (lastDown && now - lastDown.t < 400 && Math.hypot(e.clientX - lastDown.x, e.clientY - lastDown.y) < 6 && cv.zoom && cv.unzoom) { lastDown = null; cv.unzoom(); return; }
          lastDown = { t: now, x: e.clientX, y: e.clientY };
          let vert = null;
          for (let el = wrap; el; el = el.parentElement) {
            const oy = getComputedStyle(el).overflowY;
            if (el.scrollHeight > el.clientHeight && (oy === "auto" || oy === "scroll")) { vert = el; break; }
          }
          const x0 = e.clientX, y0 = e.clientY, sl0 = wrap.scrollLeft, st0 = vert ? vert.scrollTop : 0;
          let moved = false;
          const move = (ev) => {
            if (!moved) {
              if (Math.hypot(ev.clientX - x0, ev.clientY - y0) < 4) return;
              moved = true; wrap.classList.add("panning");
            }
            wrap.scrollLeft = sl0 - (ev.clientX - x0);
            if (vert) vert.scrollTop = st0 - (ev.clientY - y0);
          };
          const end = () => {
            window.removeEventListener("pointermove", move); window.removeEventListener("pointerup", end); window.removeEventListener("pointercancel", end);
            wrap.classList.remove("panning");
            if (!moved) return;
            // the click that follows the release must not deselect (and redraw) anything
            const swallow = (ev) => { ev.stopPropagation(); ev.preventDefault(); };
            window.addEventListener("click", swallow, true);
            setTimeout(() => window.removeEventListener("click", swallow, true), 0);
          };
          window.addEventListener("pointermove", move); window.addEventListener("pointerup", end); window.addEventListener("pointercancel", end);
        });
        if (cv.ro) cv.ro.disconnect();
        if (window.ResizeObserver) { cv.ro = new ResizeObserver(() => { if (cv.fit) cv.fit(); }); cv.ro.observe(wrap); }
      }
      // The size: fitted to the window it is in (never larger than drawn, never smaller than CV_MIN_SCALE: a wider one
      // scrolls), until the mouse wheel zooms it; then cv.zoom (kept across the refreshes, reset for another gateway)
      // holds the scale, and a double-click on the empty drawing goes back to fitting.
      if (cv.zoomGid !== cv.gid) { cv.zoom = null; cv.zoomGid = cv.gid; }
      const fitScale = () => Math.max(CV_MIN_SCALE, Math.min(1, (wrap.clientWidth || W) / W));
      const fit = () => {
        const s = cv.zoom || fitScale();
        svg.style.width = Math.round(W * s) + "px"; svg.style.height = Math.round(H * s) + "px";   // CSSOM, allowed by the CSP
        wrap.style.minHeight = Math.round(H * fitScale()) + "px";   // zooming out must not shrink the box out from under the pointer
      };
      if (!cv.resizeBound) {   // a taller or shorter window changes how far the rows are spread: draw again once it has settled
        cv.resizeBound = true;
        let t = null;
        window.addEventListener("resize", () => { clearTimeout(t); t = setTimeout(() => { if (svgHost && svgHost.isConnected && cv.loaded && !cv.drag) draw(); }, 200); });
      }
      fit();                       // sized before it goes in, so the box never sees it at another size (that would clamp the scroll)
      wrap.replaceChildren(svg);   // one step: no frame without a drawing
      cv.fit = fit;
      cv.zoomBy = (e) => {
        e.preventDefault();   // the wheel over the drawing zooms it instead of scrolling the page
        const unit = e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? 100 : 1;
        const r0 = svg.getBoundingClientRect();
        const s0 = r0.width / W || 1;
        const s1 = Math.max(CV_ZOOM_MIN, Math.min(CV_ZOOM_MAX, s0 * Math.exp((-e.deltaY * unit) * 0.0015)));
        if (Math.abs(s1 - s0) < 0.001) return;
        const ux = (e.clientX - r0.left) / s0, uy = (e.clientY - r0.top) / s0;   // the point under the pointer, in drawing units
        cv.zoom = s1; fit();
        const r1 = svg.getBoundingClientRect();
        wrap.scrollLeft += r1.left + ux * s1 - e.clientX;   // keep that point under the pointer
        let rest = r1.top + uy * s1 - e.clientY;             // … vertically: the drawing grows taller than its box, so the page scrolls
        for (let el = wrap; el && rest; el = el.parentElement) {
          if (el.scrollHeight <= el.clientHeight) continue;
          const oy = getComputedStyle(el).overflowY;
          if (oy !== "auto" && oy !== "scroll") continue;
          const was = el.scrollTop; el.scrollTop += rest; rest -= el.scrollTop - was;
        }
      };
      cv.unzoom = () => { cv.zoom = null; fit(); wrap.scrollLeft = 0; wrap.scrollTop = 0; };
      wrap.scrollLeft = keep.x; wrap.scrollTop = keep.y;
      cv.drawn = cv.gid;
      clear(noteEl); noteEl.className = "cv-status";   // no status line above the drawing: the colours and tooltips say it (a message from an action can still appear here)
    }

    // ── popup dialogs and the right-click menu ───────────────────────────
    let dlg = null, menuEl = null;
    function closeEditor() {
      if (!dlg) return;
      const d = dlg; dlg = null;
      try { d.close(); } catch (_) { /* already closed */ }
      d.remove();
    }
    function openDialog(title, ...kids) {
      closeEditor(); closeMenu();
      const d = h("dialog", { class: "cv-dialog", "aria-label": title }, h("h2", {}, title), ...kids);
      d.addEventListener("close", () => { if (dlg === d) { dlg = null; d.remove(); } });
      dlg = d;
      root.append(d);
      d.showModal();
      return d;
    }
    function form(title, fields, onOk, okLabel) {
      const els = {}, hints = {};
      const grid = h("div", { class: "grid" }, fields.map((f) => {
        els[f.k] = f.options
          ? h("select", {}, f.options.map((o) => h("option", { value: o, selected: o === f.v }, o)))
          : h("input", { type: "text", autocomplete: "off", spellcheck: "false", value: f.v || "", disabled: f.disabled, placeholder: f.ph || "" });
        hints[f.k] = h("span", { class: "hint" }, f.hint || "");
        // a field may react when it is left (f.on): it gets the fields, and note(key, text) to say something under a field
        if (f.on) els[f.k].addEventListener("change", () => f.on(els, (k, t) => { const o = fields.find((x) => x.k === k); hints[k].textContent = (o && o.hint ? o.hint : "") + (t ? (o && o.hint ? " " : "") + t : ""); }));
        return h("label", { class: "f" + (fields.length === 1 ? " wide" : "") }, f.l, els[f.k], f.hint || f.on ? hints[f.k] : null);
      }));
      for (const f of fields) if (f.on && f.initOn) f.on(els, () => {});
      const err = h("div", { "aria-live": "polite" });
      const submit = (e) => {
        e.preventDefault();
        const vals = {};
        for (const f of fields) vals[f.k] = els[f.k].value.trim();
        const m = onOk(vals);
        if (m) { errorBox(err, m); return; }
        closeEditor(); commit();
      };
      openDialog(title, h("form", { onsubmit: submit }, grid, err,
        h("div", { class: "toolbar" }, h("button", { class: "btn primary", type: "submit" }, okLabel || "OK"),
          h("button", { class: "btn", type: "button", onclick: closeEditor }, "Cancel"))));
      const first = fields.find((f) => !f.disabled);
      if (first) els[first.k].focus();
    }
    function confirmDialog(title, text, okLabel, onOk, cls) {
      const go = h("button", { class: cls || "btn danger", type: "button", onclick: () => { closeEditor(); onOk(); } }, okLabel);
      openDialog(title, h("p", {}, text),
        h("div", { class: "toolbar" }, go, h("button", { class: "btn", type: "button", onclick: closeEditor }, "Cancel")));
      go.focus();
    }

    function closeMenu() {
      if (!menuEl) return;
      closeSub();
      menuEl.remove(); menuEl = null;
      document.removeEventListener("click", outsideClick, true);
      document.removeEventListener("keydown", menuKey, true);
      window.removeEventListener("blur", closeMenu);
      window.removeEventListener("resize", closeMenu);
    }
    let subEl = null;
    function closeSub() {
      if (subEl) { subEl.remove(); subEl = null; }
      // the parent item's "open" highlight must go with its submenu, or it stays lit after the pointer moves on
      if (menuEl) menuEl.querySelectorAll("button.open").forEach((b) => b.classList.remove("open"));
    }
    const outsideClick = (e) => { if (!(menuEl && menuEl.contains(e.target)) && !(subEl && subEl.contains(e.target))) closeMenu(); };
    function menuKey(e) {
      if (!menuEl) return;
      if (e.key === "Escape") { e.preventDefault(); closeMenu(); return; }
      const cur = document.activeElement;
      if (e.key === "ArrowRight" && cur && cur.dataset && cur.dataset.sub) { e.preventDefault(); cur.click(); return; }
      if (e.key === "ArrowLeft" && subEl && subEl.contains(cur)) { e.preventDefault(); const p = menuEl.querySelector("button.open"); closeSub(); if (p) p.focus(); return; }
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        const box = subEl && subEl.contains(cur) ? subEl : menuEl;
        const items = [...box.querySelectorAll("button")];
        const i = items.indexOf(cur);
        items[(i + (e.key === "ArrowDown" ? 1 : items.length - 1)) % items.length].focus();
      }
    }
    // items: [label, handler, "danger"?, subitems?]. An item with subitems opens a second menu beside the first
    // when the pointer rests on it (or on click / the right arrow key); the sub-items are [label, handler] too.
    let menuAt = { x: 0, y: 0 };
    function openMenu(e, items) {
      e.preventDefault(); e.stopPropagation();
      closeMenu();
      menuAt = { x: e.clientX || 0, y: e.clientY || 0 };
      const openSub = (btn, subs) => {
        closeSub();
        menuEl.querySelectorAll("button.open").forEach((b) => b.classList.remove("open"));
        btn.classList.add("open");
        subEl = h("div", { class: "cv-menu cv-sub", role: "menu" }, subs.map(([label, fn]) =>
          h("button", { type: "button", role: "menuitem", onclick: () => { closeMenu(); fn(); } }, label)));
        root.append(subEl);
        const pr = menuEl.getBoundingClientRect(), br = btn.getBoundingClientRect(), sr = subEl.getBoundingClientRect();
        const left = pr.right + sr.width + 4 <= window.innerWidth ? pr.right - 2 : Math.max(4, pr.left - sr.width + 2);
        subEl.style.left = left + "px";
        subEl.style.top = Math.max(4, Math.min(br.top - 4, window.innerHeight - sr.height - 4)) + "px";
      };
      menuEl = h("div", { class: "cv-menu", role: "menu" }, items.map(([label, fn, cls, subs]) => {
        if (!subs) {
          return h("button", { type: "button", role: "menuitem", class: cls || "", onmouseenter: closeSub, onclick: () => { closeMenu(); fn(); } }, label);
        }
        const btn = h("button", { type: "button", role: "menuitem", class: "has-sub", "aria-haspopup": "true", "data-sub": "1", onclick: () => { openSub(btn, subs); subEl.querySelector("button").focus(); } }, label, h("span", { class: "arrow" }, "▸"));
        btn.addEventListener("mouseenter", () => openSub(btn, subs));
        return btn;
      }));
      root.append(menuEl);
      const r = menuEl.getBoundingClientRect();
      // keyboard-opened menus report 0,0: fall back to the element's corner
      const x = e.clientX || (e.target.getBoundingClientRect ? e.target.getBoundingClientRect().left : 0);
      const y = e.clientY || (e.target.getBoundingClientRect ? e.target.getBoundingClientRect().bottom : 0);
      menuEl.style.left = Math.max(4, Math.min(x, window.innerWidth - r.width - 4)) + "px";   // CSSOM, allowed by the CSP
      menuEl.style.top = Math.max(4, Math.min(y, window.innerHeight - r.height - 4)) + "px";
      menuEl.querySelector("button").focus();
      document.addEventListener("click", outsideClick, true);
      document.addEventListener("keydown", menuKey, true);
      window.addEventListener("blur", closeMenu);
      window.addEventListener("resize", closeMenu);
    }
    // ── a server's statistics (right-click ▸ Statistics…; CLI: --server-stats) ──
    const niceMaxV = (v, floor) => {
      if (v <= floor) return floor;
      const p = Math.pow(10, Math.floor(Math.log10(v)));
      for (const m of [1, 2, 2.5, 5, 10]) if (m * p >= v) return m * p;
      return 10 * p;
    };
    const stampV = (sec, step, long) => {
      const d = new Date(sec * 1000);
      const hm = d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });
      return step >= 3600 || long ? d.toLocaleDateString([], { month: "short", day: "numeric" }) + " " + hm : hm;
    };
    function tsChart(title, d, series, o) {
      const n = series[0].values.length;
      const peak = Math.max(0, ...series.flatMap((s) => s.values));
      const max = o.fixedMax || niceMaxV(peak, o.floor || 10);
      const W = 620, H = 230, L = axisMargin([0, 1, 2, 3, 4].map((g) => o.axis((max * g) / 4)), 62), R = 12, T = 12, B = 28, pw = W - L - R, ph = H - T - B;
      const x = (i) => L + (n <= 1 ? pw / 2 : (pw * i) / (n - 1));
      const y = (v) => T + ph - (ph * Math.min(v, max)) / max;
      const svg = sv("svg", { class: "qchart", viewBox: `0 0 ${W} ${H}`, role: "img", "aria-label": title + " over time" });
      for (let g = 0; g <= 4; g++) {
        const v = (max * g) / 4;
        svg.append(sv("line", { class: "qgrid", x1: L, x2: W - R, y1: y(v), y2: y(v) }), sv("text", { class: "qax", x: L - 6, y: y(v) + 4, "text-anchor": "end" }, o.axis(v)));
      }
      const ticks = Math.min(8, n);
      const long = d.step >= 600 && d.to - d.from > 20 * 3600;
      for (let k = 0; k < ticks; k++) {
        const i = ticks === 1 ? 0 : Math.round((k * (n - 1)) / (ticks - 1));
        svg.append(sv("text", { class: "qax", x: x(i), y: H - 8, "text-anchor": k === 0 ? "start" : k === ticks - 1 ? "end" : "middle" }, stampV(d.start + i * d.step, d.step, long)));
      }
      for (const s of series) {
        let path = "", pen = false;
        s.values.forEach((v, i) => { if (v < 0) { pen = false; return; } path += (pen ? "L" : "M") + x(i).toFixed(1) + "," + y(v).toFixed(1); pen = true; });
        if (path) svg.append(sv("path", { class: "qline " + s.cls, d: path }));
        // a minute with data between two without has no line to show it: a dot, so it is not mistaken for nothing
        s.values.forEach((v, i) => { if (v >= 0 && !(i > 0 && s.values[i - 1] >= 0) && !(i < n - 1 && s.values[i + 1] >= 0)) svg.append(sv("circle", { class: "qpt " + s.cls, r: 2.5, cx: x(i).toFixed(1), cy: y(v).toFixed(1) })); });
      }
      const cross = sv("line", { class: "qcross hidden", y1: T, y2: T + ph });
      const dots = series.map((s) => sv("circle", { class: "qdot hidden " + s.cls, r: 4, cx: 0, cy: 0 }));
      const tip = sv("g", { class: "qtip hidden" });
      const tipBg = sv("rect", { class: "qtipbg", rx: 6, width: 250, height: 20 + 18 * (series.length + 1) });
      const tipHead = sv("text", { class: "qtiph", x: 10, y: 17 });
      const tipRows = series.map((s, k) => sv("g", { transform: `translate(10 ${36 + 18 * k})` }, sv("rect", { class: s.cls + " sw", width: 10, height: 10, y: -9, rx: 2 }), sv("text", { class: "qtipt", x: 16, y: 0 }, "")));
      tip.append(tipBg, tipHead, ...tipRows);
      const hit = sv("rect", { class: "qhit", x: L, y: T, width: pw, height: ph,
        onmousemove: (e) => {
          const r = svg.getBoundingClientRect();
          const px = ((e.clientX - r.left) / r.width) * W;
          const i = Math.max(0, Math.min(n - 1, Math.round(((px - L) / pw) * (n - 1))));
          cross.setAttribute("x1", x(i)); cross.setAttribute("x2", x(i)); cross.classList.remove("hidden");
          series.forEach((s, k) => { const v = s.values[i]; dots[k].classList.toggle("hidden", v < 0); dots[k].setAttribute("cx", x(i)); dots[k].setAttribute("cy", v < 0 ? 0 : y(v)); });
          tipHead.textContent = new Date((d.start + i * d.step) * 1000).toLocaleString();
          series.forEach((s, k) => { tipRows[k].lastChild.textContent = s.label + ": " + (s.values[i] < 0 ? "no data" : s.text(i)); });
          const tx = x(i) > W / 2 ? x(i) - 260 : x(i) + 12;
          tip.setAttribute("transform", `translate(${tx} ${T + 6})`); tip.classList.remove("hidden");
        },
        onmouseleave: () => { cross.classList.add("hidden"); tip.classList.add("hidden"); } });
      svg.append(cross, ...dots, hit, tip);
      const legend = series.length > 1 || o.legend ? h("div", { class: "qlegend" }, series.map((s) => h("span", { class: "qchip " + s.cls }, h("span", { class: "sw" }), s.label))) : null;
      return h("div", { class: "tsc" }, h("h3", {}, title), legend, svg);
    }

    // The statistics dialog for a server, a gateway or one domain on a server.  It is built once; choosing another
    // range only swaps the numbers and charts in place (the old charts stay until the new ones arrive), so the box
    // never shrinks and grows again.
    const pctAxis = (v) => (Number.isInteger(v) ? v : v.toFixed(1)) + "%";
    const msAxis = (v) => (Number.isInteger(v) ? v : v.toFixed(1)) + " ms";
    async function statsDialog(spec, rng) {   // spec: { key, title, kind }
      rng = rng || "1h";
      let d = dlg && dlg.dataset.stats === spec.key ? dlg : null;
      if (!d) {
        const sum = h("p", { class: "hint" }, "Loading…");
        const body = h("div", {});
        const tabs = h("div", { class: "toolbar" }, h("span", { class: "segs", role: "group", "aria-label": "Time range" }, [["1h", "Last hour"], ["1d", "Last day"], ["7d", "Last week"]].map(([k, l]) =>
          h("button", { class: "seg", type: "button", "data-r": k, onclick: () => statsDialog(spec, k) }, l))));
        const nodes = h("span", { class: "hint stats-nodes" });   // a gateway's graphs: which nodes' history this is (filled in below)
        tabs.append(nodes);
        d = openDialog("Statistics — " + spec.title, tabs, sum, body);
        d.prepend(h("button", { class: "dlg-x", type: "button", "aria-label": "Close", title: "Close", onclick: closeEditor }, "✕"));   // first, so it floats to the top-right corner and sticks there
        d.dataset.stats = spec.key;
        d.style.width = "min(46rem, calc(100vw - 2rem))";   // CSSOM, allowed by the CSP
        d.sum = sum; d.body = body; d.nodes = nodes;
      }
      for (const b of d.querySelectorAll(".segs .seg")) b.setAttribute("aria-pressed", b.dataset.r === rng ? "true" : "false");
      d.rng = rng;
      const sum = d.sum, body = d.body;
      try {
        const r = (await api("GET", "/api/serverstats?addr=" + encodeURIComponent(spec.key) + "&from=" + rng)).data;
        if (dlg !== d || d.rng !== rng) return;   // closed, or another range was chosen meanwhile
        if (!r.known) { clear(body); sum.hidden = false; sum.textContent = "Nothing recorded for this " + spec.kind + " yet (the daemon keeps 7 days, and only once it has something to count)."; return; }
        d.nodes.textContent = r.kind !== "gateway" ? "" : r.nodes_off > 0 ? "Nodes counted: " + r.nodes + " of " + (r.nodes + r.nodes_off) + " (" + r.nodes_off + " not answering)" : r.nodes > 1 ? "All " + r.nodes + " nodes counted together" : "";
        const f1 = (v) => (v < 0 ? "no data" : v.toFixed(2));
        const dd = { start: r.start, step: r.step, from: r.from, to: r.to };
        const lat = (name) => tsChart(name, dd, [
          { label: "Average", cls: "t1", values: r.latency, text: (i) => f1(r.latency[i]) + " ms" },
          { label: "Worst", cls: "t3", values: r.latency_max, text: (i) => f1(r.latency_max[i]) + " ms" }], { floor: 1, axis: msAxis, legend: true });
        let charts;
        if (r.kind === "gateway") {
          charts = [
            lat("Answer time (as clients see it)"),
            tsChart("Errors", dd, [{ label: "Errors", cls: "t6", values: r.loss, text: (i) => r.loss[i].toFixed(2) + "% of " + r.queries[i] + " queries" }], { floor: 1, axis: pctAxis }),
            tsChart("Availability", dd, [{ label: "Available", cls: "t3", values: r.avail, text: (i) => r.avail[i].toFixed(2) + "% of the time" }], { fixedMax: 100, axis: pctAxis }),
            tsChart("Queries per second", dd, [
              { label: "All", cls: "t1", values: r.qps, text: (i) => r.qps[i].toFixed(1) + " per second" },
              { label: "From the cache", cls: "t4", values: r.hit_qps, text: (i) => r.hit_qps[i].toFixed(1) + " per second" }], { floor: 1, axis: (v) => (Number.isInteger(v) ? v : v.toFixed(1)) + "/s", legend: true })];
        } else if (r.kind === "domain") {
          charts = [lat("Latency"),
            tsChart("Failed probes", dd, [{ label: "Failed probes", cls: "t6", values: r.loss, text: (i) => r.loss[i].toFixed(2) + "% of the probes" }], { floor: 1, axis: pctAxis })];
        } else {
          charts = [lat("Latency"),
            tsChart("Failed probes", dd, [{ label: "Failed probes", cls: "t6", values: r.loss, text: (i) => r.loss[i].toFixed(2) + "% of the probes" }], { floor: 1, axis: pctAxis }),
            tsChart("Failed queries", dd, [{ label: "Failed queries", cls: "t6", values: r.query_loss || [], text: (i) => r.query_loss[i].toFixed(2) + "% of " + r.queries[i] + " queries" }], { floor: 1, axis: pctAxis })];
        }
        sum.hidden = true;   // the graphs say it all; the line is only for "loading", "nothing recorded" and errors
        clear(body).append(...charts);
      } catch (e) { if (dlg === d) { sum.hidden = false; sum.textContent = "Could not load the statistics: " + (e.message || e); } }
    }
    const serverStats = (g, addr) => statsDialog({ key: addr, title: srvTitle(g, addr), kind: "server" });
    const gatewayStats = (g) => statsDialog({ key: "gw:" + g.group_id, title: "gateway " + gwLabel(g), kind: "gateway" });
    const domainStats = (g, addr, di) => {
      const q = effQueries(g, addr)[di];
      if (q) statsDialog({ key: "dom:" + addr + "|" + q.name + "|" + q.type, title: q.name + " " + q.type + " on " + srvTitle(g, addr), kind: "domain" });
    };

    // "Pause ▸ This node / All nodes" and "Resume ▸ …": a menu that opens beside this one while the pointer is on the item.
    // `scopeOn(scope)` says whether the thing is paused in that scope; only the scopes that can still change are offered.
    const pauseItems = (noun, scopeOn, act) => {
      const subs = (pause) => [["node", "This node"], ["all", "All nodes"]].filter(([sc]) => scopeOn(sc) !== pause).map(([sc, t]) => [t, () => act(sc, pause)]);
      const items = [];
      // never both: a paused item offers only Resume (for the scopes it is paused in), otherwise only Pause
      if (scopeOn("node") || scopeOn("all")) items.push(["Resume " + noun, null, "", subs(false)]);
      else items.push(["Pause " + noun, null, "", subs(true)]);
      return items;
    };
    const gwItems = (g) => [["Statistics…", () => gatewayStats(g)], ["Edit gateway…", () => gatewayForm(g)], ...(g.real_macs ? [] : [["Test virtual MACs…", () => vmacTest(g)]]), ["Add DNS server…", () => serverForm(g)], ["Add anycast address…", () => anycastForm(g)], ...addNodeItems(g),
      ...pauseItems("gateway", (sc) => !!g[sc === "all" ? "paused_all" : "paused"], (sc, p) => pauseGateway(g, sc, p)), ["Delete gateway", delSel, "danger"]];
    const srvItems = (g, addr) => [["Statistics…", () => serverStats(g, addr)], ["Edit server…", () => serverForm(g, addr)], ["Add domain…", () => domainForm(g, addr, null)],
      ...pauseItems("server", (sc) => ((sc === "all" ? (g.dns || cv.cfg.dns || {}).paused_servers : cv.cfg.paused_servers_here) || []).includes(addr), (sc, p) => pauseServer(g, addr, sc, p)), ["Delete server", delSel, "danger"]];
    // Pause / resume announcing one anycast address: on this node only (kept in this node's own settings) or on every node (shared)
    const anyPauseList = (g, scope) => (scope === "all" ? "paused_vips" : "paused_vips_here");
    const anyPaused = (g, addr, scope) => (g[anyPauseList(g, scope)] || []).includes(addr);
    function pauseAnycast(g, addr, scope, pause) {
      quick(() => {
        const k = anyPauseList(g, scope), l = (g[k] || []).filter((x) => x !== addr);
        g[k] = pause ? l.concat(addr) : l;
        if (!g[k].length) delete g[k];
      });
    }
    const anyItems = (g, addr) => [["Edit address…", () => anycastForm(g, addr)],
      ...pauseItems("anycast", (sc) => anyPaused(g, addr, sc), (sc, p) => pauseAnycast(g, addr, sc, p)), ["Delete address", delSel, "danger"]];
    const domItems = (g, addr, di) => {
      const q = effQueries(g, addr)[di];
      const on = (sc) => ((sc === "all" ? (g.dns || cv.cfg.dns || {}).paused_queries : cv.cfg.paused_queries_here) || []).includes(qKey(addr, q));
      return [["Statistics…", () => domainStats(g, addr, di)], ["Edit domain…", () => domainForm(g, addr, di)], ["Add domain…", () => domainForm(g, addr, null)],
        ...(q ? pauseItems("domain", on, (sc, p) => pauseDomain(g, addr, q, sc, p)) : []), ["Delete domain", delSel, "danger"]];
    };
    // the cluster's nodes (the parallelograms left of the circle)
    const nodeTip = (n) => [
      n.name + (n.self ? " (this node)" : "") + " — " + n.detail,
      "Address: " + n.addr + (n.hostname && n.hostname !== n.name ? " (host " + n.hostname + ")" : ""),
      ...(n.addrs || []).filter((i) => (i.v4 || []).length + (i.v6 || []).length).map((i) => i.name + ": " + [...(i.v4 || []), ...(i.v6 || [])].join(", ")),   // Ethernet interfaces: IPv4 and IPv6 GUA
      "Role: " + (n.role === "primary" ? "primary" : "replica"),
      n.self ? "Last seen: just now" : n.reachable ? "Last seen: " + (n.last_seen ? spanText(Date.now() / 1000 - n.last_seen) + " ago" : "just now") : "",
      n.version ? "Version: " + n.version + (n.version_differs ? " (not the same as this node's)" : "") : "",
      n.reachable || n.self ? (n.behind ? "Settings: behind the primary's" : "Settings: up to date") : "",
      n.host ? "Host: " + (n.host.cpu_pct >= 0 ? "CPU " + Math.round(n.host.cpu_pct) + "%, " : "") + "memory " + Math.round(n.host.mem_pct) + "%" + (n.host.disk_pct >= 0 ? ", disk " + Math.round(n.host.disk_pct) + "%" + (n.host.disk_mount ? " (" + n.host.disk_mount + ")" : "") : "") + " — amber above 85%" : "",
      n.updating ? "An update is in progress" : "", n.update_failed ? "Update failed: " + n.update_failed : "",
    ].filter(Boolean).join("\n");
    // open another node (the Node menu, top right), optionally on one page; this node's own address stands for "this node"
    const openNode = (n, tab) => { const me = selfNode(); setTarget(me && n.addr === me.addr ? null : n.addr); if (tab) selectTab(tab); };
    // Pause or resume any reachable node from its shape: this node directly, another through the cluster relay (the same call the
    // Node page makes on the node picked in the Node menu).  Until that node reports back (up to a sync interval) the shape shows
    // "pausing…" / "resuming…" so the click is seen at once.
    const nodeCall = (n, method, path, body) => {
      const me = selfNode(), to = n.self || (me && n.addr === me.addr) ? null : n.addr;   // this node: straight, never through the relay
      return api(method, to ? "/api/proxy?node=" + encodeURIComponent(to) + "&path=" + encodeURIComponent(path) : path, body, true);
    };
    function pauseNode(n) {
      const want = !n.node_paused;
      const go = async () => {
        try { await nodeCall(n, "POST", "/api/nodepause", { paused: want }); }
        catch (e) { clear(noteEl).append("Could not " + (want ? "pause " : "resume ") + n.name + ": " + (e.message || e)); return; }
        cv.nodeWait = cv.nodeWait || {};
        cv.nodeWait[n.addr] = { want, until: Date.now() + 15000 };
        draw(); refresh();
      };
      if (!want) { go(); return; }
      confirmDialog("Pause " + (n.self ? "this node" : n.name) + "?", "All of its gateways stop serving and the other nodes take over. Clients are not interrupted as long as another node is serving. Resume it when you are done.", "Pause node", go, "btn primary");
    }
    // Make a node the gateway controller for all groups (the Node page's button, aimed at the node whose shape was clicked): it takes
    // the role first, then the node that held it is asked to step down, so clients lose nothing.
    function makeController(n) {
      const go = async () => {
        let said;
        try {
          const r = await nodeCall(n, "POST", "/api/assert-agc", {});
          said = r.ok ? "Asked " + (n.self ? "this node" : n.name) + " to take over: " + (r.messages || []).join(" · ") : (r.error || "Could not make " + n.name + " the gateway controller");
        } catch (e) { said = "Could not make " + n.name + " the gateway controller: " + (e.message || e); }
        draw(); refresh();
        clear(noteEl).append(said);   // after the redraw, which clears the line
      };
      confirmDialog("Make " + (n.self ? "this node" : n.name) + " the gateway controller?", "It takes over the controller role for all groups, and the node that answers for the shared address now is asked to step down once it has. Clients should lose nothing.", "Make controller", go, "btn primary");
    }
    // Restart or shut down the host of a node, now (the Node page's Host section, aimed at the node whose shape was clicked).  When a
    // gateway would lose its last serving member the daemon says so, and the admin can go ahead anyway.
    function powerNode(n, action) {
      const verb = action === "restart" ? "Restart" : "Shut down", who = n.self ? "this node" : n.name;
      const run = async (force) => {
        try {
          const r = (await nodeCall(n, "POST", "/api/power", force ? { action, when: "now", force: true } : { action, when: "now" })).data;
          clear(noteEl).append(verb + " of " + who + " " + (r && r.when ? r.when : "requested") + ".");
        } catch (e) {
          if (!force && /^not safe to /.test(e.message || "")) {
            confirmDialog("Go ahead anyway?", String(e.message).replace(/ \(to go ahead anyway.*$/, "") + " Clients of that gateway will be interrupted.", verb + " anyway", () => run(true));
            return;
          }
          clear(noteEl).append("Could not " + verb.toLowerCase() + " " + who + ": " + (e.message || e));
        }
      };
      confirmDialog(verb + " the host of " + who + "?", (action === "restart" ? "The host reboots" : "The host powers off and stays off until it is powered on again") +
        (n.self ? (action === "restart" ? ", and you lose access to this page until it is back. " : ". You lose access to this page. ") : ". ") +
        "Its gateways stop serving and the other nodes take over.", verb + " host", () => run(false));
    }
    // Remove a node from the gateway shown (a shared setting, kept in the gateway's own settings): it serves nothing of it, whichever
    // node you look from, and is no longer drawn.  The gateway's right-click ▸ Add node brings it back.  The last node serving it
    // cannot be removed (pause the gateway instead).
    function nodeMember(n) {
      const g = curGroup();
      if (!g) return;
      const left = (statusOf(g).nodes || []).filter((x) => x.node_id && x.node_id !== n.node_id && !(g.excluded_nodes || []).includes(x.node_id)).length;
      if (!left) { errorBox(msgEl, "That would leave no node serving gateway " + gwLabel(g) + ". Pause it on all nodes instead."); return; }
      confirmDialog("Remove " + (n.self ? "this node" : n.name) + " from gateway " + gwLabel(g) + "?",
        "It stops serving the gateway — it gives up the address and stops answering DNS — and the other nodes carry on. Other gateways are not affected. Add it back from the same menu.",
        "Remove node", () => quick(() => toggleIn(g, "excluded_nodes", n.node_id, true)), "btn primary");
    }
    const memberItem = (n) => (curGroup() && n.node_id ? [["Remove from this gateway…", () => nodeMember(n)]] : []);
    // the nodes removed from a gateway, by name (an entry for a node that has left the cluster can still be cleared)
    const removedNodes = (g) => {
      const nodes = statusOf(g).nodes || [];
      return (g.excluded_nodes || []).map((id) => {
        const n = nodes.find((x) => x.node_id === id);
        return { id, label: n ? n.name + (n.self ? " (this node)" : "") : "A node that left the cluster (" + id.slice(0, 8) + ")" };
      });
    };
    const addNodeItems = (g) => {
      const l = removedNodes(g);
      return l.length ? [["Add node", null, "", l.map((r) => [r.label, () => quick(() => toggleIn(g, "excluded_nodes", r.id, false))])]] : [];
    };
    const nodeMenu = (n) => (e) => openMenu(e, n.self
      ? [["Host statistics…", () => selectTab("host")], ["Make controller…", () => makeController(n)], [n.node_paused ? "Resume node" : "Pause node…", () => pauseNode(n)], ["Restart…", () => powerNode(n, "restart")], ["Shut down…", () => powerNode(n, "shutdown")], ...memberItem(n)]
      : n.reachable ? [["Open this node", () => openNode(n)], ["Host statistics…", () => openNode(n, "host")], ["Make controller…", () => makeController(n)], [n.node_paused ? "Resume node" : "Pause node…", () => pauseNode(n)], ["Restart…", () => powerNode(n, "restart")], ["Shut down…", () => powerNode(n, "shutdown")], ...memberItem(n)]
      : [["Cluster page…", () => selectTab("cluster")], ...memberItem(n)]);
    // right-click on a shape: select it, then show what can be done with it
    const rightClick = (kind, addr, di) => (e) => {
      cv.sel = { kind, addr, di }; refresh();
      const g = curGroup();
      openMenu(e, kind === "gw" ? gwItems(g) : kind === "srv" ? srvItems(g, addr) : kind === "any" ? anyItems(g, addr) : domItems(g, addr, di));
    };

    function nextGroupId() { const used = new Set(groups().map((g) => g.group_id)); for (let i = 1; i < 256; i++) if (!used.has(i)) return i; return 0; }

    // the gateway's own load-balancing values only show while "own values" is chosen
    function showLB(els) {
      const own = els.lbmode.value === LB_OWN;
      for (const k of ["lb_spread", "lb_band", "lb_down", "lb_fail", "lb_attempts", "lb_alpha"]) els[k].closest("label").style.display = own ? "" : "none";
    }
    function renameGateway(g) {
      form("Rename gateway", [{ k: "name", l: "Name (optional)", v: g.name || "", ph: "e.g. Office DNS" }], (v) => {
        if ((v.name || "").length > 40) return "The name can be at most 40 characters.";
        if (v.name.trim()) g.name = v.name.trim(); else delete g.name;
        return null;
      }, "Rename");
    }
    function gatewayForm(g) {
      const base = g || groups()[0];
      form(g ? "Edit gateway" : "New gateway", [
        { k: "id", l: "Group number", v: String(g ? g.group_id : nextGroupId()), disabled: !!g },
        { k: "name", l: "Name (optional)", v: g ? (g.name || "") : "", ph: "e.g. Office DNS" },
        { k: "vip4", l: "Shared IPv4 address / prefix", v: g ? g.vip4 : "", ph: "10.0.0.1/24" },
        { k: "vip6", l: "Shared IPv6 address / prefix (optional)", v: g ? g.vip6 : "", ph: "2001:db8::1/64" },
        { k: "more4", l: "More IPv4 addresses (same subnet, optional)", v: g ? (g.more_vip4 || []).join(", ") : "", ph: "10.0.0.2, 10.0.0.3" },
        { k: "more6", l: "More IPv6 addresses (same subnet, optional)", v: g ? (g.more_vip6 || []).join(", ") : "", ph: "2001:db8::2" },
        { k: "iface", l: "Network interface", v: g ? g.interface : (base ? base.interface : "eth0") },
        { k: "ecs", l: "Tell the DNS servers which network the client is on (ECS)", v: ecsOn(g) ? "on" : "off", options: ["off", "on"] },
        { k: "fb", l: "Fallback DNS servers (optional)", v: fallbackOf(g).join(", "), ph: "e.g. 1.1.1.1, 9.9.9.9" },
        { k: "lbmode", l: "Load balancing", v: lbNow(g).own ? LB_OWN : LB_FOLLOW, options: [LB_FOLLOW, LB_OWN], initOn: true, on: (els) => showLB(els) },
        { k: "lb_spread", l: "Spread queries over servers of similar speed", v: lbNow(g).lb.spread ? "on" : "off", options: ["on", "off"] },
        { k: "lb_band", l: "Spread band (% of fastest)", v: String(lbNow(g).lb.spread_band) },
        { k: "lb_down", l: "Down at (% of tests failing)", v: String(lbNow(g).lb.down_percent) },
        { k: "lb_fail", l: "Failures before down", v: String(lbNow(g).lb.fail_threshold) },
        { k: "lb_attempts", l: "Max servers tried", v: String(lbNow(g).lb.max_attempts) },
        { k: "lb_alpha", l: "Latency smoothing (0–1]", v: String(lbNow(g).lb.latency_alpha) },
      ], (v) => {
        let lbOwn = null;
        if (v.lbmode === LB_OWN) {
          lbOwn = { spread: v.lb_spread === "on", spread_band: Number(v.lb_band), down_percent: Number(v.lb_down), fail_threshold: Number(v.lb_fail), max_attempts: Number(v.lb_attempts), latency_alpha: Number(v.lb_alpha) };
          const intIn = (n, lo, hi) => Number.isInteger(n) && n >= lo && n <= hi;
          if (!intIn(lbOwn.spread_band, 1, 1000)) return "The spread band is a whole number from 1 to 1000.";
          if (!intIn(lbOwn.down_percent, 1, 100)) return "Down at is a whole number from 1 to 100.";
          if (!intIn(lbOwn.fail_threshold, 1, 1000000)) return "Failures before down is a whole number, at least 1.";
          if (!intIn(lbOwn.max_attempts, 1, 1000000)) return "Max servers tried is a whole number, at least 1.";
          if (!(lbOwn.latency_alpha > 0 && lbOwn.latency_alpha <= 1)) return "Latency smoothing is above 0 and at most 1.";
        }
        const fb = (v.fb || "").split(/[\s,;]+/).filter(Boolean);
        if (fb.some((a) => !/^[A-Za-z0-9_.\-:\[\]\/]+$/.test(a))) return "A fallback server needs to be an IP address or host name.";
        if (new Set(fb).size !== fb.length) return "A fallback server is listed twice.";
        const own = new Set(((g && g.dns && g.dns.servers) || []).map(shortAddr));
        const dup = fb.find((a) => own.has(shortAddr(normAddr(a))) || own.has(a));
        if (dup) return dup + " is already one of this gateway's servers.";
        if ((v.name || "").length > 40) return "The name can be at most 40 characters.";
        if (!v.vip4 && !v.vip6) return "Give the gateway an IPv4 or IPv6 address, e.g. 10.0.0.1/24.";
        if (v.vip4 && !/^\d+\.\d+\.\d+\.\d+\/\d+$/.test(v.vip4)) return "The IPv4 address needs a prefix length, e.g. 10.0.0.1/24.";
        if (v.vip6 && !(v.vip6.includes(":") && /^[0-9a-fA-F:.]+\/\d+$/.test(v.vip6))) return "The IPv6 address needs a prefix length, e.g. 2001:db8::1/64.";
        if (!v.iface) return "Choose the network interface the gateway runs on.";
        const more4 = (v.more4 || "").split(/[\s,;]+/).filter(Boolean), more6 = (v.more6 || "").split(/[\s,;]+/).filter(Boolean);
        if (more4.some((a) => !/^\d+\.\d+\.\d+\.\d+$/.test(a))) return "More IPv4 addresses are plain addresses without a prefix, e.g. 10.0.0.2, 10.0.0.3.";
        if (more6.some((a) => !a.includes(":") || !/^[0-9a-fA-F:.]+$/.test(a))) return "More IPv6 addresses are plain addresses without a prefix, e.g. 2001:db8::2.";
        if ((more4.length && !v.vip4) || (more6.length && !v.vip6)) return "Further addresses need the gateway's own address of the same kind first.";
        const taken = groups().find((x) => x !== g && [x.vip4, x.vip6, ...(x.more_vip4 || []), ...(x.more_vip6 || [])].some((a) => a && [...more4, ...more6].includes(a.split("/")[0])));
        if (taken) return "Gateway " + gwLabel(taken) + " already uses one of those addresses.";
        const clash = groups().find((x) => x !== g && [x.vip4, x.vip6].some((a) => a && [v.vip4, v.vip6].some((b) => b && a.split("/")[0] === b.split("/")[0])));
        if (clash) return "Gateway " + gwLabel(clash) + " already uses that address.";
        if (g) {
          g.vip4 = v.vip4; g.vip6 = v.vip6; g.interface = v.iface;
          if (more4.length) g.more_vip4 = more4; else delete g.more_vip4;
          if (more6.length) g.more_vip6 = more6; else delete g.more_vip6;
          if (v.name.trim()) g.name = v.name.trim(); else delete g.name;
          if ((v.ecs === "on") !== ecsOn(g)) { ensureDNS(g); g.dns.ecs = v.ecs === "on"; }
          if (fb.join(",") !== fallbackOf(g).join(",")) { ensureDNS(g); if (fb.length) g.dns.fallback_servers = fb; else delete g.dns.fallback_servers; }
          if (lbOwn) { ensureDNS(g); g.dns.lb = lbOwn; }
          else if (g.dns) { delete g.dns.lb; Object.assign(g.dns, LB_DEFAULTS); }   // back to following Settings
          return null;
        }
        const id = Number(v.id);
        if (!(id >= 1 && id <= 255) || groups().some((x) => x.group_id === id)) return "Pick an unused group number between 1 and 255.";
        groups().push({ ...(v.name.trim() ? { name: v.name.trim() } : {}), group_id: id, interface: v.iface, vip4: v.vip4, vip6: v.vip6, ...(more4.length ? { more_vip4: more4 } : {}), ...(more6.length ? { more_vip6: more6 } : {}), real_macs: true, dns: { servers: [], server_queries: {}, ecs: v.ecs === "on", ...(fb.length ? { fallback_servers: fb } : {}), ...(lbOwn ? { lb: lbOwn } : {}) } });
        setGid(id); cv.sel = { kind: "gw" };
        return null;
      }, g ? "Save" : "Add gateway");
    }

    const fallbackOf = (g) => ((g ? (g.dns || cv.cfg.dns || {}) : {}).fallback_servers || []);
    const ecsOn = (g) => !!(g ? (g.dns || cv.cfg.dns || {}).ecs : (cv.cfg.dns || {}).ecs); // a new gateway starts from the shared setting

    // The load-balancing settings a gateway's own pool starts from, so that it follows Settings until it is given its own
    // (the daemon treats a pool at other values as the gateway's own).
    const LB_DEFAULTS = { spread: true, spread_band: 20, down_percent: 100, fail_threshold: 2, max_attempts: 3, latency_alpha: 0.3 };
    const LB_FOLLOW = "follow Settings", LB_OWN = "own values";
    const lbShared = () => { const d = cv.cfg.dns || {}; return { spread: d.spread !== false, spread_band: d.spread_band, down_percent: d.down_percent, fail_threshold: d.fail_threshold, max_attempts: d.max_attempts, latency_alpha: d.latency_alpha }; };
    const lbText = (l) => "spread " + (l.spread ? "on" : "off") + ", band " + l.spread_band + "%, down at " + l.down_percent + "%, " + l.fail_threshold + " failures, " + l.max_attempts + " servers tried, smoothing " + l.latency_alpha;
    // what a gateway runs with now (the daemon works it out: cv.view) and whether that is its own
    const lbNow = (g) => {
      const v = g && (cv.view || []).find((x) => x.group_id === g.group_id);
      return v && v.lb ? { lb: v.lb, own: !!v.lb_own } : { lb: lbShared(), own: false };
    };

    function ensureDNS(g) {
      if (!g.dns) {
        const d = cv.cfg.dns || {};
        g.dns = JSON.parse(JSON.stringify(d));
        delete g.dns.lb;
        Object.assign(g.dns, LB_DEFAULTS);
        g.dns.server_queries = g.dns.server_queries || {};
      }
      g.dns.server_queries = g.dns.server_queries || {};
    }

    // Fill in one of the address and the name and the other follows: an address is looked up in reverse (PTR) for its
    // name, a name for its address (both asked of the node being configured).  Only a field that is empty, or still
    // holds what a lookup put there, is ever filled; once you type your own name or address it is left alone.
    const IPISH = /^(\[[0-9A-Fa-f:.]+\](:\d+)?|[0-9A-Fa-f:]*:[0-9A-Fa-f:.]*|\d{1,3}(\.\d{1,3}){3}(:\d+)?)$/;
    function lookupHooks() {
      const auto = { addr: "", label: "" };
      const ask = async (q) => (await api("GET", "/api/dnslookup?lookup=" + encodeURIComponent(q))).data;
      const canFill = (el, k) => !el.value.trim() || el.value.trim() === auto[k];
      return {
        addr: async (els, note) => {
          const q = els.addr.value.trim();
          if (!q || !IPISH.test(q) || !canFill(els.label, "label")) { note("label", ""); return; }
          note("label", "Looking up the name…");
          try {
            const r = await ask(q);
            if (els.addr.value.trim() !== q || !canFill(els.label, "label")) return;
            if (r.found) { els.label.value = r.name; auto.label = r.name; note("label", "Name found by reverse lookup; change it if you like."); }
            else {
              if (els.label.value.trim() === auto.label) { els.label.value = ""; auto.label = ""; }   // the old fill belongs to the old address
              note("label", r.error + " — type a name if you want one.");
            }
          } catch (e) { note("label", "Lookup failed: " + (e.message || e)); }
        },
        label: async (els, note) => {
          const n = els.label.value.trim();
          if (n === auto.label) return;                       // our own fill, not something typed
          if (!n || !/^[A-Za-z0-9_\-]+(\.[A-Za-z0-9_\-]+)*$/.test(n) || IPISH.test(n) || !canFill(els.addr, "addr")) { note("addr", ""); return; }
          note("addr", "Looking up the address…");
          try {
            const r = await ask(n);
            if (els.label.value.trim() !== n || !canFill(els.addr, "addr")) return;
            if (r.found) { els.addr.value = r.addr; auto.addr = r.addr; note("addr", "Address found for " + n + "."); }
            else note("addr", r.error + ".");
          } catch (e) { note("addr", "Lookup failed: " + (e.message || e)); }
        },
      };
    }
    function serverForm(g, edit) {
      const lk = lookupHooks();
      form(edit ? "Edit DNS server" : "Add DNS server", [
        { k: "addr", l: "IP address or host name", v: edit ? shortAddr(edit) : "", ph: "8.8.8.8", hint: "Add :port for a port other than 53. Write tls://host for DNS over TLS (port 853) or https://host/path for DNS over HTTPS (port 443).", on: lk.addr },
        { k: "label", l: "Name (optional)", v: edit ? srvName(g, edit) : "", ph: "e.g. dns-a", hint: "Shown under the address on the diagram. Fill in the address and its name is looked up; fill in a host name here and its address is.", on: lk.label },
        ...(edit ? [] : [
          { k: "name", l: "Domain to ask it about", v: "", ph: "google.com", hint: "A server is checked by asking it a question; add more domains afterwards." },
          { k: "type", l: "Record type", v: "A", options: QTYPES }]),
      ], (v) => {
        if (!v.addr) return "Enter an IP address or host name.";
        if (!edit && !/^[A-Za-z0-9_.\-]+$/.test(v.name)) return "Enter a domain name such as google.com.";
        if (!/^[A-Za-z0-9_.\-:\[\]]+$/.test(v.addr)) return "That doesn't look like an IP address or host name.";
        const nm = (v.label || "").trim();
        if ([...nm].length > 40) return "The name can be at most 40 characters.";
        if (/[\u0000-\u001f\u007f]/.test(nm)) return "The name must not contain control characters.";
        ensureDNS(g);
        const a = normAddr(v.addr);
        if (g.dns.servers.includes(a) && a !== edit) return "That server is already on this gateway.";
        if (edit) {
          const i = g.dns.servers.indexOf(edit);
          g.dns.servers[i] = a;
          if (a !== edit) {
            if (g.dns.server_names) delete g.dns.server_names[edit];
            g.dns.server_queries[a] = ownQueries(g, edit); delete g.dns.server_queries[edit];
            if ((g.dns.paused_servers || []).includes(edit)) g.dns.paused_servers = g.dns.paused_servers.map((x) => (x === edit ? a : x));
          }
          cv.sel = { kind: "srv", addr: a };
        } else {
          g.dns.servers.push(a);
          g.dns.server_queries[a] = [{ name: v.name, type: v.type }];
          cv.sel = { kind: "srv", addr: a };
        }
        if (nm) { g.dns.server_names = g.dns.server_names || {}; g.dns.server_names[a] = nm; }
        else if (g.dns.server_names) { delete g.dns.server_names[a]; if (!Object.keys(g.dns.server_names).length) delete g.dns.server_names; }
        return null;
      }, edit ? "Save" : "Add server");
    }

    function anycastForm(g, edit) {
      form(edit ? "Edit anycast address" : "Add anycast address", [
        { k: "addr", l: "Anycast address", v: edit || "", ph: "203.0.113.53 or 2001:db8:53::1",
          hint: "Any subnet. Held on every node while a DNS server answers. Other gateways may carry it too." },
      ], (v) => {
        const a = (v.addr || "").trim().replace(/\/(32|128)$/, "").toLowerCase();
        if (!a) return "Enter an IPv4 or IPv6 address.";
        if (!/^[0-9a-f:.]+$/.test(a) || !(a.includes(":") || /^\d+\.\d+\.\d+\.\d+$/.test(a))) return "That has to be a plain IPv4 or IPv6 address, without a subnet.";
        if (/^(127\.|0\.|22\d\.|23\d\.)/.test(a) || /^(::1?|fe80:|ff)/.test(a)) return "Loopback, link-local and multicast addresses cannot be used.";
        // the same anycast address on several gateways is how an anycast service is run; only this gateway's own list and every gateway's shared address are refused
        const own = (g.extra_vips || []).includes(a) && !(a === edit) ? g : null;
        const shared = groups().find((x) => [x.vip4, x.vip6, ...(x.more_vip4 || []), ...(x.more_vip6 || [])].some((z) => z && z.split("/")[0].toLowerCase() === a));
        if (own || shared) return "Gateway " + gwLabel(own || shared) + " already uses that address.";
        g.extra_vips = g.extra_vips || [];
        if (edit) g.extra_vips[g.extra_vips.indexOf(edit)] = a; else g.extra_vips.push(a);
        cv.sel = { kind: "any", addr: a };
        return null;
      }, edit ? "Save" : "Add address");
    }

    function domainForm(g, addr, di) {
      const q = di == null ? null : effQueries(g, addr)[di];
      form(q ? "Edit domain" : "Add domain to " + shortAddr(addr), [
        { k: "name", l: "Domain to ask about", v: q ? q.name : "", ph: "google.com", hint: "The server must answer this to be counted healthy." },
        { k: "type", l: "Record type", v: q ? q.type : "A", options: QTYPES },
      ], (v) => {
        if (!/^[A-Za-z0-9_.\-]+$/.test(v.name)) return "Enter a domain name such as google.com.";
        const list = ownQueries(g, addr);
        if (list.some((x, i) => x.name.toLowerCase() === v.name.toLowerCase() && x.type === v.type && i !== di)) return "That domain is already tested on this server.";
        if (q) { list[di] = { name: v.name, type: v.type }; } else { list.push({ name: v.name, type: v.type }); cv.sel = { kind: "dom", addr, di: list.length - 1 }; }
        return null;
      }, q ? "Save" : "Add domain");
    }

    function delSel() {
      const g = curGroup(), s = cv.sel;
      if (!g || !s) return;
      const finish = () => { cv.sel = null; closeEditor(); commit(); };
      if (s.kind === "gw") {
        const servers = (g.dns && g.dns.servers) || [];
        const doms = servers.reduce((n, a) => n + effQueries(g, a).length, 0);
        confirmDialog("Delete gateway " + gwLabel(g) + "?",
          "This removes its shared address" + (servers.length ? ", its " + servers.length + " DNS server(s) and their " + doms + " domain test(s)" : "") +
          " from the configuration. You can restore it later from History.", "Delete gateway", () => {
            cv.cfg.groups = groups().filter((x) => x !== g);
            setGid(cv.cfg.groups.length ? cv.cfg.groups[0].group_id : null);
            finish();
          });
      } else if (s.kind === "srv") {
        const n = effQueries(g, s.addr).length;
        const del = () => {
          g.dns.servers = g.dns.servers.filter((a) => a !== s.addr);
          if (g.dns.server_queries) delete g.dns.server_queries[s.addr];
          if (g.dns.server_names) { delete g.dns.server_names[s.addr]; if (!Object.keys(g.dns.server_names).length) delete g.dns.server_names; }
          if (g.dns.paused_servers) g.dns.paused_servers = g.dns.paused_servers.filter((a) => a !== s.addr);
          finish();
        };
        if (n) confirmDialog("Delete DNS server " + srvTitle(g, s.addr) + "?", "Its " + n + " domain test(s) are deleted with it.", "Delete server", del);
        else del();
      } else if (s.kind === "any") {
        g.extra_vips = (g.extra_vips || []).filter((a) => a !== s.addr);
        if (!g.extra_vips.length) delete g.extra_vips;
        finish();
      } else if (s.kind === "dom") {
        if (ownQueries(g, s.addr).length <= 1) {
          openDialog("A server needs a domain", h("p", {}, "A DNS server is checked by asking it about a domain, so " + shortAddr(s.addr) +
            " must keep at least one. Add another domain first, edit this one, or delete the server."),
            h("div", { class: "toolbar" }, h("button", { class: "btn primary", type: "button", onclick: closeEditor }, "OK")));
          return;
        }
        ownQueries(g, s.addr).splice(s.di, 1);
        finish();
      }
    }

    // ── saving ───────────────────────────────────────────────────────────
    // Every change is saved as soon as it is made.  If the daemon refuses it, the
    // picture goes back to what is saved and the reason is shown.
    function setGid(id) { cv.gid = id; state.topo.gid = id; }
    const fresh = (cfg) => {
      cv.orig = JSON.parse(JSON.stringify(cfg));
      if (pending === 0) { cv.cfg = cfg; cv.sel = cv.sel && curGroup() ? cv.sel : null; }
      if (!groups().some((g) => g.group_id === cv.gid)) setGid(groups().length ? groups()[0].group_id : null);
    };
    async function save() {
      const sent = JSON.stringify(groups().map((g) => g.group_id));
      try {
        const r = await api("PUT", "/api/config", { config: cv.cfg, note: "Topology" });
        pending--; fresh(r.config); clear(msgEl);
      } catch (ex) {
        pending--;
        if (ex.message === "unauthenticated") return;
        if (ex.status) { // refused: show what is really saved
          if (pending === 0) { cv.cfg = JSON.parse(JSON.stringify(cv.orig)); cv.sel = null; if (!curGroup()) setGid(groups().length ? groups()[0].group_id : null); }
          errorBox(msgEl, ex.message);
        } else {
          // The connection dropped (a gateway change can briefly reset the network).
          // Look at what the node actually has before reporting a failure.
          try {
            const r = await api("GET", "/api/config");
            if (JSON.stringify(r.config.groups.map((g) => g.group_id)) === sent) { fresh(r.config); clear(msgEl); }
            else errorBox(msgEl, "The connection was interrupted and the change may not have been saved. Reload the page to check.");
          } catch (_) { errorBox(msgEl, "The connection was interrupted and the change may not have been saved. Reload the page to check."); }
        }
      }
      refresh();
      refreshTopoNav();
      poll().catch(() => {});
    }
    let queue = Promise.resolve();
    function commit() { refresh(); pending++; queue = queue.then(save); }

    function refresh() { draw(); if (legendEl) legendEl.classList.toggle("hidden", !groups().length); }

    async function load() {
      const r = await api("GET", "/api/config");
      cv.cfg = r.config; cv.orig = JSON.parse(JSON.stringify(r.config));
      cv.loaded = true;
      cv.gid = state.topo.gid;
      if (!groups().some((g) => g.group_id === cv.gid)) setGid(groups().length ? groups()[0].group_id : null);
    }

    async function poll() {
      if (!cv.loaded) return;
      try {
        const r = await api("GET", "/api/canvas");
        cv.view = r.data || [];
      } catch (ex) { if (ex.message === "unauthenticated") throw ex; }
      // Redrawing replaces every shape, and the browser's tooltip (the shape's <title>) only appears after the
      // pointer has rested on the same element for about a second: a redraw every poll while the pointer is on a
      // shape kept resetting it.  So leave the drawing alone while a shape is hovered and catch up when the
      // pointer leaves (the next poll, or the mouseout below, whichever comes first) — but never keep a stale
      // picture for more than 10 s, so colours stay true for a pointer left resting on the canvas.
      if (cv.drag) return;   // a shape is being dragged: leave the drawing alone
      if (svgHost && svgHost.querySelector(".shape:hover")) {
        if (!cv.stale) cv.stale = Date.now();
        if (Date.now() - cv.stale < 10000) return;
      }
      draw();
    }

    return {
      mount(main) {
        noteEl = h("div", { class: "cv-status" });
        svgHost = h("div", { class: "cv-card",
          onmouseout: (e) => { if (cv.stale && !cv.drag && !(e.relatedTarget && e.relatedTarget.closest && e.relatedTarget.closest(".shape"))) draw(); },
          // right-click on empty canvas (shapes and tabs handle their own menus)
          oncontextmenu: (e) => { cv.sel = null; refresh(); const g = curGroup();
            openMenu(e, [["New gateway…", () => gatewayForm(null)]].concat(g ? [["Add DNS server…", () => serverForm(g)]] : [])); } });
        msgEl = h("div", { "aria-live": "polite" });
        const legend = legendEl = h("div", { class: "cv-legend muted small" },
          h("span", { class: "dot st-ok" }), "working ", h("span", { class: "dot st-warn" }), "degraded ", h("span", { class: "dot st-bad" }), "down ", h("span", { class: "dot st-idle" }), "not yet known ", h("span", { class: "dot st-paused" }), "paused ", h("span", { class: "dot line-spread" }), "active");
        main.append(msgEl, noteEl, svgHost, legend);
        cv.loaded = false;
        load().then(() => {
          cv.loaded = true; refresh(); poll().catch(() => {});
          if (state.topo.action === "new") { state.topo.action = null; gatewayForm(null); }
          else if (state.topo.action === "rename") { state.topo.action = null; const g = curGroup(); if (g) renameGateway(g); }
          else if (state.topo.action === "delete") { state.topo.action = null; if (curGroup()) { cv.sel = { kind: "gw" }; delSel(); } }
        })
          .catch((ex) => { if (ex.message !== "unauthenticated") errorBox(msgEl, ex.message); });
      },
      poll,
    };
  })();

  // Gateways  (CLI: --show-gateways, --show-neighbors)
  VIEWS.gateways = (() => {
    let body;
    return {
      mount(main) {
        body = h("div", {});
        main.append(body, h("p", { class: "hint" }, "★ marks this node"));
      },
      async poll() {
        const r = await api("GET", "/api/gateways");
        const out = h("div", {});   // built aside, then patched into the page (see morph)
        if (!r.data.length) {
          out.append(h("div", { class: "card" }, h("div", { class: "empty" }, "No gateway groups running.")));
          morph(body, out);
          return;
        }
        for (const g of r.data) {
          const rows = g.members.map((m) => h("tr", { class: m.local ? "local" : "" },
            h("td", { class: "nname", title: m.name || null }, m.name || "–", m.local ? " ★" : ""),
            h("td", { class: "mono" }, m.ip),
            h("td", { class: "num" }, m.priority),
            h("td", { class: "num" }, m.slot || "–"),
            h("td", { class: "num" }, m.weight),
            h("td", {}, pill(m.role, m.role === "AGC" ? "ok" : m.role === "AFN" ? "info" : "")),
            h("td", {}, pill(m.state.toUpperCase(), m.state === "expired" ? "bad" : m.state === "active" || m.state === "forward" ? "ok" : "warn"),
              m.preempt ? " " : null, m.preempt ? pill("preempt") : null),
            h("td", { class: "num" }, m.local ? "local" : m.age_ms),
            h("td", { class: "mono" }, m.vmac || "–"),
            h("td", {}, pill(m.dns_listening ? "answering" : "not answering", m.dns_listening ? "ok" : "warn"))));
          out.append(h("div", { class: "card" },
            h("header", {}, h("h2", {}, g.name ? g.name + " · group " + g.group_id : "Group " + g.group_id),
              h("span", { class: "mono muted" }, "VIP " + g.vip),
              g.agc ? h("span", { class: "muted" }, "AGC ", h("span", { class: "mono" }, g.agc)) : null),
            h("div", { class: "scroll" }, h("table", { class: "tight" },
              h("thead", {}, h("tr", {}, ["Node name", "Node IP", "Pri", "Slot", "Weight", "Role", "State", "Age (ms)", "vMAC", "DNS"].map((t, i) => h("th", { class: [2, 3, 4, 7].includes(i) ? "num" : "" }, t)))),
              h("tbody", {}, rows)))));
        }
        morph(body, out);
      },
    };
  })();

  // Nodes  (CLI: --cluster-status): the cluster's members (the gateway protocol's neighbours are on Gateways)
  VIEWS.nodes = (() => {
    let body;
    return {
      mount(main) { body = h("div", {}); main.append(body, h("p", { class: "hint" }, "★ marks this node")); },
      async poll() {
        const c = await api("GET", "/api/cluster").catch(() => null);
        const out = h("div", {});
        if (c && c.data) {
          const v = c.data;
          out.append(h("div", { class: "card" },
            h("header", {}, h("h2", {}, "Cluster members"), h("span", { class: "muted" }, "Nodes sharing these settings."),
              v.conflict ? pill("conflict", "bad") : null),
            membersTable(v, null, true)));
        }
        morph(body, out);
      },
    };
  })();

  // DNS  (CLI: --show-dns)
  VIEWS.dns = (() => {
    let body;
    const stat = (k, v, small) => h("div", { class: "stat" }, h("div", { class: "k" }, k), h("div", { class: "v" + (small ? " small" : "") }, v));
    return {
      mount(main) { body = h("div", {}); main.append(body); },
      async poll() {
        const r = await api("GET", "/api/dns");
        const out = h("div", {});
        if (!r.ok) { out.append(h("div", { class: "notice info" }, r.error)); morph(body, out); return; }
        const d = r.data;
        const pools = d.pools && d.pools.length ? d.pools : [{ key: 0, groups: [], servers: d.servers, down_percent: d.down_percent, probes: d.probes }];
        out.append(h("div", { class: "stats" }, stat("Queries", d.queries), stat("Answered", d.answered), stat("SERVFAIL", d.servfail)));
        // "group 1 192.168.0.5:53" -> where each gateway answers, shown on its own card
        const addrsOf = {};
        for (const l of d.listeners || []) { const m = /^group (\d+) (.+)$/.exec(l); if (m) (addrsOf[m[1]] = addrsOf[m[1]] || []).push(m[2]); }
        const fills = [];
        for (const p of pools) {
          const maxE = Math.max(1, ...p.servers.filter((s) => s.healthy).map((s) => s.ewma_ms));
          out.append(h("div", { class: "card" },
            h("header", {}, h("h2", {}, p.key ? "Gateway " + p.key + (p.name ? " · " + p.name : "") : "Shared pool" + (p.groups.length ? " (group " + p.groups.join(", ") + ")" : "")),
              h("span", { class: "muted" }, p.probes + " test" + (p.probes === 1 ? "" : "s") + " per round, down at " + p.down_percent + "% failing"),
              p.ecs ? pill("client subnet sent · " + p.ecs_sent, "info") : h("span", { class: "muted small" }, "servers see this node's address"),
              p.using_fallback ? pill("every server is down: using the fallback servers", "warn") : null,
              p.client_rate ? pill("limit " + p.client_rate + "/s per client" + (p.limited ? " · " + p.limited.toLocaleString() + " turned away" : ""), p.limited ? "warn" : "info") : null,
              p.allowed_clients ? pill("allowed clients · " + p.allowed_clients + " network" + (p.allowed_clients === 1 ? "" : "s") + (p.denied ? " · " + p.denied.toLocaleString() + " refused" : ""), p.denied ? "warn" : "info") : null,
              p.dot_port ? pill("DoT · " + p.dot_port, "info") : null,
              p.doh_port ? pill("DoH · " + p.doh_port, "info") : null,
              p.tls_insecure ? pill("tls:// and https:// certificates not checked", "warn") : null,
              p.spread ? h("span", { class: "muted small", title: "Servers within " + p.spread_band + "% of the fastest one's latency take turns; slower ones are fallbacks." }, "spread within " + p.spread_band + "%") : null,
              (() => {
                const c = p.cache, n0 = (n) => Number(n || 0).toLocaleString();
                if (!c || !c.on) return h("span", { class: "muted small" }, "cache off");
                const t = c.hits + c.misses;
                return h("span", { class: "muted small", title: "Answers kept: " + n0(c.entries) + " of " + n0(c.max) + ". Not cacheable (never kept): " + n0(c.bypassed) + ". Pushed out when full: " + n0(c.evicted) + ". Kept at most " + n0(c.max_ttl) + " s." },
                  "cache " + (t ? (100 * c.hits / t).toFixed(0) + "% hits" : "empty") + " · " + n0(c.entries) + " kept");
              })(),
              h("span", { class: "spacer" }),
              (() => {
                const on = (p.groups.length ? p.groups : [p.key]).flatMap((g) => addrsOf[g] || []);
                return on.length ? h("span", { class: "mono small" }, "answering on " + on.join(" · ")) : h("span", { class: "muted small" }, "not answering on this node");
              })()),
            h("div", { class: "scroll" }, h("table", { class: "dns" },
              h("thead", {}, h("tr", {}, ["Rank", "Server name", "Server IP", "State", "EWMA (ms)", "Last (ms)", "OK", "Fail", "Served", "Last error"].map((t, i) => h("th", { class: i >= 4 && i <= 8 ? "num" : "" }, t)))),
              h("tbody", {}, p.servers.map((s) => h("tr", {},
                h("td", {}, s.healthy && s.rank ? s.rank : "–"),
                h("td", { class: "sname", title: s.name || null }, s.name || "–"),   // a long name is cut short; the whole is the tooltip
                h("td", { class: "mono" }, s.addr, s.fallback ? [h("span", { class: "muted small fb-tag", title: "Used only while every other server is down" }, "fallback")] : null),
                h("td", {}, pill(s.healthy ? "up" : "down", s.healthy ? "ok" : "bad")),
                h("td", { class: "num" }, s.healthy ? h("span", { class: "bar-wrap" }, h("span", { class: "bar-fill" })) : null, s.healthy ? fmtMs(s.ewma_ms) : "–"),
                h("td", { class: "num" }, s.last_ms > 0 ? fmtMs(s.last_ms) : "–"),
                h("td", { class: "num" }, s.successes), h("td", { class: "num" }, s.failures), h("td", { class: "num" }, s.served),
                h("td", { class: "wrap muted" }, s.last_error || ""))))))));
          for (const s of p.servers) if (s.healthy) fills.push([s, maxE]);
        }
        morph(body, out);
        // widths are set via the CSSOM (allowed under the page's CSP)
        const els = body.querySelectorAll(".bar-fill");
        fills.forEach(([s, maxE], i) => { els[i].style.width = Math.max(4, (s.ewma_ms / maxE) * 100) + "%"; });
      },
    };
  })();

  // ── configuration  (CLI: --show-config, --configure) ──────────────────────
  const LB = ["roundrobin", "weighted", "hostpinned", "failover"];
  const GROUP_FIELDS = [
    { shared: true, k: "name", l: "Name", t: "text", hint: "optional label shown instead of the address" },
    { shared: true, k: "group_id", l: "Group ID", t: "int", min: 1, max: 255 },
    { k: "interface", l: "Interface", t: "text" },
    { shared: true, k: "vip4", l: "IPv4 VIP/prefix", t: "text", hint: "e.g. 10.0.0.1/24 — empty to disable" },
    { shared: true, k: "vip6", l: "IPv6 VIP/prefix", t: "text", hint: "e.g. 2001:db8::1/64 — empty to disable" },
    { shared: true, k: "more_vip4", l: "More IPv4 addresses", t: "list", hint: "Same subnet as the VIP, one per line, no prefix length." },
    { shared: true, k: "more_vip6", l: "More IPv6 addresses", t: "list", hint: "Same subnet as the VIP, one per line, no prefix length." },
    { k: "priority", l: "Priority", t: "int", min: 0, max: 255 },
    { shared: true, k: "lb_method", l: "LB method", t: "select", options: LB },
    { k: "weight", l: "Weight", t: "int", min: 0, max: 255 },
    { shared: true, k: "hello_ms", l: "Hello interval (ms)", t: "int", min: 1 },
    { shared: true, k: "hold_ms", l: "Hold time (ms)", t: "int", min: 1 },
    { shared: true, k: "max_afns", l: "Max forwarders", t: "int", min: 1, max: 255 },
    { shared: true, k: "key", l: "HMAC shared key", t: "text" },
    { k: "preempt", l: "Preemption", t: "bool" },
    { shared: true, k: "real_macs", l: "Use real MAC addresses (no virtual MACs)", t: "bool", hint: "On for a gateway you add now (most networks need it: a VMware port group that is not promiscuous, a cloud with one MAC per interface, a switch with port security); gateways that already existed keep their setting. Off is the older way with a virtual MAC per node. On: the VIP is on every node's lo and the controller answers ARP with the real MAC of the node it picks. Failover then depends on the neighbors honoring an unsolicited ARP. Restarts the gateway; set it on every node's cluster together." },
    { shared: true, k: "neighbors", l: "Neighbors (unicast mode)", t: "list", wide: true, hint: "Every node, one IP per line. Empty: multicast." },
  ];
  const DNS_FIELDS = [
    { section: "Upstream servers", note: "The default settings for every gateway that has no servers of its own (a gateway drawn on the Topology page can have its own)." },
    { shared: true, k: "servers", l: "Servers", t: "list", wide: true, hint: "One per line: 10.0.0.53, 10.0.1.53:5353, [2001:db8::53]:53, tls://dns.example.com (DNS over TLS, port 853), https://dns.example.com/dns-query (DNS over HTTPS)" },
    { shared: true, k: "fallback_servers", l: "Fallback servers", t: "list", wide: true, hint: "Same forms. Used only while no server above is in service (all down or paused), and dropped as soon as one answers again. They are never probed and always count as up, so a public resolver that knows nothing of your internal names is fine." },
    { shared: true, k: "queries", l: "Probe queries", t: "queries", wide: true, hint: "Every server is asked each of these to check it works. One per line: \"example.com\" or \"example.com AAAA\"" },
    { shared: true, k: "tls_insecure", l: "Accept any certificate from tls:// and https:// servers (insecure)", t: "bool" },
    { section: "Load Balancing", cols: 3, note: "Applies to every gateway unless that gateway has its own load balancing (Topology ▸ right-click the gateway ▸ Edit gateway)." },
    { shared: true, k: "spread", l: "Spread queries over servers of similar speed (round-robin)", t: "bool" },
    { shared: true, k: "spread_band", l: "Spread band (% of fastest)", t: "int", min: 1, max: 1000 },
    { shared: true, k: "down_percent", l: "Down at (% of tests failing)", t: "int", min: 1, max: 100 },
    { shared: true, k: "fail_threshold", l: "Failures before down", t: "int", min: 1 },
    { shared: true, k: "max_attempts", l: "Max servers tried", t: "int", min: 1 },
    { shared: true, k: "latency_alpha", l: "Latency smoothing (0–1]", t: "float" },
    { section: "Listeners", note: "Plain DNS is always on. DNS over TLS (usually 853) and DNS over HTTPS (usually 443, served at /dns-query) use the GUI certificate (Configure ▸ Web GUI); 0 turns one off." },
    { shared: true, k: "listen_port", l: "DNS port (UDP and TCP)", t: "int", min: 1, max: 65535 },
    { shared: true, k: "dot_port", l: "DNS over TLS port", t: "int", min: 0, max: 65535 },
    { shared: true, k: "doh_port", l: "DNS over HTTPS port", t: "int", min: 0, max: 65535 },
    { shared: true, k: "forward_updates", l: "Forward dynamic DNS updates to the zone's primary server", t: "bool" },
    { section: "Sort List" },
    { shared: true, k: "sortlist_on", l: "Sort list enabled", t: "bool" },
    { shared: true, k: "sortlist", l: "Sort list", t: "list", wide: true },
    { section: "Policy-Based Resolution" },
    { shared: true, k: "policy_on", l: "Policy-Based Resolution enabled", t: "bool" },
    { shared: true, k: "policy_log", l: "Log policy matches", t: "bool" },
    { shared: true, k: "policy", l: "Policy rows", t: "policy", wide: true },
    { section: "Client Rate Limiting", note: "Applies to every gateway that uses these settings, over UDP, TCP, DoT and DoH, before the cache. This node itself is always allowed and never limited. Counters are per node." },
    { shared: true, k: "allowed_clients", l: "Allowed clients", t: "list", wide: true, hint: "Networks that may use the proxy, one per line: 10.0.0.0/8, 192.168.1.5, 2001:db8::/32. Empty = everyone. Anyone else is answered REFUSED (dynamic updates too)." },
    { shared: true, k: "client_rate", l: "Queries per second", t: "int", min: 0, max: 10000000, hint: "0 = no limit. A client is one IPv4 address or one IPv6 /64." },
    { shared: true, k: "client_burst", l: "Burst (queries at once)", t: "int", min: 0, max: 100000000, hint: "0 = twice the rate, at least 10." },
    { shared: true, k: "client_action", l: "Over the limit", t: "select", options: ["drop", "truncate", "refused"] },
    { shared: true, k: "client_exempt", l: "Never limited", t: "list", wide: true, hint: "Networks exempt from the rate limit (not from the allowed list), same forms." },
    { section: "Client network (ECS)" },
    { shared: true, k: "ecs", l: "Pass the client's network to the servers", t: "bool" },
    { shared: true, k: "ecs_prefix4", l: "IPv4 prefix bits", t: "int", min: 1, max: 32 },
    { shared: true, k: "ecs_prefix6", l: "IPv6 prefix bits", t: "int", min: 1, max: 128 },
    { section: "Cache" },
    { shared: true, k: "cache", l: "Answer repeated queries from a cache", t: "bool" },
    { shared: true, k: "cache_entries", l: "Most answers kept", t: "int", min: 100, max: 1000000 },
    { shared: true, k: "cache_max_ttl", l: "Cache TTL (s)", t: "int", min: 1, max: 604800 },
    { section: "Timing" },
    { shared: true, k: "probe_interval_ms", l: "Probe interval (ms)", t: "int", min: 1 },
    { shared: true, k: "probe_timeout_ms", l: "Probe timeout (ms)", t: "int", min: 1 },
    { shared: true, k: "query_timeout_ms", l: "Forward timeout (ms)", t: "int", min: 1 },
  ];
  const WEB_FIELDS = [
    { k: "listen", l: "Listen address", t: "text", hint: "host:port, default :53853" },
    { k: "group", l: "Login group", t: "text" },
    { k: "pam_service", l: "PAM service", t: "text" },
    { k: "session_idle_minutes", l: "Session idle timeout (min)", t: "int", min: 1 },
    { k: "max_failed_logins", l: "Failed logins before lockout", t: "int", min: 1 },
    { k: "failed_login_window_minutes", l: "…within this many minutes", t: "int", min: 1 },
    { k: "lockout_minutes", l: "Lockout lasts (minutes)", t: "int", min: 1 },
    { k: "min_password_length", l: "Minimum password length", t: "int", min: 0, max: 128, hint: "0 = 8" },
  ];
  const CLUSTER_FIELDS = [
    { k: "listen", l: "Binding address", t: "text" },
    { k: "self", l: "Advertised address", t: "text" },
    { k: "sync_interval_sec", l: "Sync interval (s)", t: "int", min: 1, max: 3600 },
  ];
  const GENERAL_FIELDS = [{ k: "log_level", l: "Log level", t: "select", options: ["debug", "info", "warning", "error"] }];

  // Shown only when this node is part of a multi-node cluster.
  const sharedTag = (f) => (f.shared && state.clustered
    ? h("span", { class: "pill info tiny", title: "Replicated from the cluster's primary. Changes made here are accepted by the primary first." }, "shared") : null);

  // Policy-Based Resolution: a table of rows (source client, source name, destination servers, destination name) edited
  // like a spreadsheet.  The rows live in a list and the table shows one page of it, optionally narrowed by a filter, so
  // thousands of rows stay quick.  The rows are read from the top and the first match wins, so their order is kept and
  // numbered.  Right-click a row for Edit, Copy, Paste, Add, Move and Delete; drag the handle to reorder on a page.
  let policyClip = null;
  const POLICY_LOCAL = /^(A|AAAA|CNAME|TXT|TTL)\s/i;
  function policyTable(rows) {
    const NAMES = ["Source client", "Source name", "Destination servers", "Destination name"];
    // a local answer is shown in the destination name column, wherever it was written
    const data = rows.map((r) => {
      const sv = r.servers || [];
      return sv.length === 1 && POLICY_LOCAL.test(sv[0]) ? { v: [r.client, r.name, "", sv[0]] } : { v: [r.client, r.name, sv.join(", "), r.dest || ""] };
    });
    let page = 0, size = 50, dragging = null;

    const tbody = h("tbody", {});
    const tbl = h("table", { class: "policy" },
      h("thead", {}, h("tr", {}, h("th", { "aria-label": "Row number and order" }, "#"), ...NAMES.map((n) => h("th", {}, n)))), tbody);
    const empty = h("div", { class: "policy-empty" }, "");
    const wrap = h("div", { class: "policy-wrap" }, tbl, empty);

    const filter = h("input", { type: "text", class: "policy-filter", placeholder: "Filter rows…", "aria-label": "Filter policy rows", autocomplete: "off", spellcheck: "false" });
    const count = h("span", { class: "policy-count muted" }, "");
    const sizeSel = h("select", { "aria-label": "Rows per page" }, [25, 50, 100, 250, 500].map((n) => h("option", { value: String(n) }, n + " per page")));
    sizeSel.value = String(size);
    const mkBtn = (label, title) => h("button", { type: "button", class: "btn small", title, "aria-label": title }, label);
    const first = mkBtn("«", "First page"), prev = mkBtn("‹", "Previous page"), next = mkBtn("›", "Next page"), last = mkBtn("»", "Last page");
    const pageIn = h("input", { type: "number", min: "1", class: "policy-page", "aria-label": "Page number" });
    const pages = h("span", { class: "muted" }, "");
    const pager = h("span", { class: "policy-pager" }, first, prev, pageIn, pages, next, last);
    const bar = h("div", { class: "policy-bar" }, filter, count, h("span", { class: "grow" }), pager, sizeSel);
    const box = h("div", { class: "policy-box" }, bar, wrap);
    // the filter and paging controls are not settings: they must not trigger a save
    bar.addEventListener("change", (e) => e.stopPropagation());

    const changed = () => tbl.dispatchEvent(new Event("change", { bubbles: true }));
    const visible = () => {
      const q = filter.value.trim().toLowerCase();
      return q ? data.filter((r) => r.v.some((x) => x.toLowerCase().includes(q))) : data;
    };
    const mark = (tr, row) => { tr.classList.toggle("incomplete", !row.v[2].trim() && !row.v[3].trim()); };
    const copyText = (vals) => vals.join("\t");
    const parseText = (txt) => txt.split(/\r?\n/).map((l) => l.split("\t").map((x) => x.trim())).filter((c) => c.length > 1 && c.some(Boolean))
      .map((c) => [c[0] || "*", c[1] || "*", c[2] || "", c[3] || ""]);
    const clearMarks = () => tbody.querySelectorAll(".drop-above, .drop-below").forEach((x) => x.classList.remove("drop-above", "drop-below"));
    const trOf = (row) => [...tbody.children].find((t) => t._row === row);

    // show the page; with `follow`, the page that holds that row
    function render(follow) {
      const vis = visible();
      const n = Math.max(1, Math.ceil(vis.length / size));
      if (follow) { const i = vis.indexOf(follow); if (i >= 0) page = Math.floor(i / size); }
      page = Math.min(Math.max(page, 0), n - 1);
      const from = page * size;
      tbody.replaceChildren(...vis.slice(from, from + size).map(mk));
      const filtered = vis.length !== data.length;
      count.textContent = vis.length
        ? `${from + 1}–${Math.min(from + size, vis.length)} of ${vis.length}${filtered ? ` (filtered from ${data.length})` : ""}`
        : (filtered ? `0 of ${data.length}` : "0 rows");
      pager.hidden = n < 2;
      pageIn.value = String(page + 1);
      pageIn.max = String(n);
      pages.textContent = "of " + n;
      first.disabled = prev.disabled = page === 0;
      next.disabled = last.disabled = page >= n - 1;
      empty.hidden = vis.length > 0;
      empty.textContent = data.length ? "No rows match the filter." : "No rows. Right-click here ▸ Add row.";
    }

    function moveTo(row, target, after) {
      if (row === target) return;
      data.splice(data.indexOf(row), 1);
      const j = data.indexOf(target);
      data.splice(after ? j + 1 : j, 0, row);
      render(row);
      changed();
    }
    function mk(row) {
      const tr = h("tr", {});
      tr._row = row;
      const grip = h("span", { class: "grip-h", draggable: "true", title: "Drag to reorder", "aria-hidden": "true" }, "⋮⋮");
      tr.append(h("td", { class: "grip" }, h("span", { class: "rownum" }, String(data.indexOf(row) + 1)), grip));
      for (let i = 0; i < 4; i++) {
        const inp = h("input", { type: "text", autocomplete: "off", spellcheck: "false", "aria-label": NAMES[i] });
        inp.value = row.v[i] || "";
        inp.addEventListener("input", () => { row.v[i] = inp.value; mark(tr, row); });
        tr.append(h("td", {}, inp));
      }
      grip.addEventListener("dragstart", (e) => {
        dragging = row;
        e.dataTransfer.effectAllowed = "move";
        try { e.dataTransfer.setData("text/plain", "policy-row"); e.dataTransfer.setDragImage(tr, 0, 0); } catch (x) { /* a browser without drag images */ }
        tr.classList.add("dragging");
      });
      grip.addEventListener("dragend", () => { dragging = null; tr.classList.remove("dragging"); clearMarks(); });
      tr.addEventListener("dragover", (e) => {
        if (!dragging || dragging === row) return;
        e.preventDefault();
        e.dataTransfer.dropEffect = "move";
        const r = tr.getBoundingClientRect();
        clearMarks();
        tr.classList.add(e.clientY > r.top + r.height / 2 ? "drop-below" : "drop-above");
      });
      tr.addEventListener("drop", (e) => {
        if (!dragging || dragging === row) return;
        e.preventDefault();
        const below = tr.classList.contains("drop-below");
        clearMarks();
        const d = dragging;
        dragging = null;
        moveTo(d, row, below);
      });
      tr.addEventListener("contextmenu", (e) => rowMenu(menuFor(row))(e)); // the items depend on where the row is now
      mark(tr, row);
      return tr;
    }
    // add a row after `after` (the end when none); the filter is cleared so the new row shows
    function insert(vals, after) {
      const row = { v: [vals[0] || "", vals[1] || "", vals[2] || "", vals[3] || ""] };
      data.splice(after ? data.indexOf(after) + 1 : data.length, 0, row);
      filter.value = "";
      render(row);
      return row;
    }
    const focusRow = (row) => { const t = trOf(row); if (t) t.querySelector("input").focus(); };
    async function paste(after) {
      let list = [];
      try { list = parseText(await navigator.clipboard.readText()); } catch (e) { /* no permission: the last copied row is used */ }
      if (!list.length && policyClip) list = [policyClip];
      if (!list.length) return;
      let at = after;
      for (const v of list) at = insert(v, at);
      changed();
    }
    function menuFor(row) {
      const vis = visible(), i = vis.indexOf(row), di = data.indexOf(row);
      return [
        ["Edit", () => focusRow(row)],
        ["Copy", () => { policyClip = row.v.map((x) => x.trim()); try { navigator.clipboard.writeText(copyText(policyClip)); } catch (e) { /* the row stays in the page's own clipboard */ } }],
        ["Paste", () => paste(row)],
        ["Add", () => focusRow(insert(["*", "*", "", ""], row))],
        ...(i > 0 ? [["Move up", () => moveTo(row, vis[i - 1], false)]] : []),
        ...(i >= 0 && i < vis.length - 1 ? [["Move down", () => moveTo(row, vis[i + 1], true)]] : []),
        ...(di > 0 ? [["Move to top", () => moveTo(row, data[0], false)]] : []),
        ...(di < data.length - 1 ? [["Move to bottom", () => moveTo(row, data[data.length - 1], true)]] : []),
        ["Delete", () => { data.splice(data.indexOf(row), 1); render(); changed(); }, "danger"],
      ];
    }
    wrap.addEventListener("contextmenu", rowMenu([
      ["Add", () => focusRow(insert(["*", "*", "", ""], null))],
      ["Paste", () => paste(data[data.length - 1])],
    ]));

    filter.addEventListener("input", () => { page = 0; render(); });
    sizeSel.addEventListener("change", () => { size = +sizeSel.value || 50; page = 0; render(); });
    first.addEventListener("click", () => { page = 0; render(); });
    prev.addEventListener("click", () => { page--; render(); });
    next.addEventListener("click", () => { page++; render(); });
    last.addEventListener("click", () => { page = 1e9; render(); });
    pageIn.addEventListener("change", () => { page = (parseInt(pageIn.value, 10) || 1) - 1; render(); });
    pageIn.addEventListener("keydown", (e) => { if (e.key === "Enter") { e.preventDefault(); pageIn.blur(); } });
    filter.addEventListener("keydown", (e) => { if (e.key === "Enter") e.preventDefault(); });
    render();
    return {
      el: box,
      // a row with neither servers nor a destination name is not sent (it is marked until it has one); a blank client or source name is "*"
      get: () => data.map((r) => r.v.map((x) => x.trim())).filter((c) => c[2] || c[3])
        .map((c) => ({ client: c[0] || "*", name: c[1] || "*", servers: POLICY_LOCAL.test(c[2]) ? [c[2]] : c[2].split(/[\s,]+/).filter(Boolean), ...(c[3] && c[3] !== "*" ? { dest: c[3] } : {}) })),
    };
  }

  // Build inputs for one object; returns {el, get()} where get() reads values back with types.
  // With asCards, every {section} spec starts a card of its own (title in the header, optional note above the fields).
  function fieldset(specs, obj, asCards) {
    const getters = {};
    let grid = h("div", { class: "grid" });
    const wrap = asCards ? h("div", {}) : grid;
    for (const f of specs) {
      if (f.section) {
        if (!asCards) { // a heading inside the one grid
          grid.append(h("div", { class: "fsec" }, h("h3", {}, f.section), f.note ? h("p", { class: "hint nomargin" }, f.note) : null));
          continue;
        }
        grid = h("div", { class: f.cols === 3 ? "grid c3" : "grid" });
        wrap.append(h("div", { class: "card" }, h("header", {}, h("h2", {}, f.section)),
          h("div", { class: "body" }, f.note ? h("p", { class: "hint" }, f.note) : null, grid)));
        continue;
      }
      const v = obj[f.k];
      let input, get;
      if (f.t === "bool") {
        input = h("input", { type: "checkbox", checked: !!v });
        get = () => input.checked;
        grid.append(h("label", { class: "f check" }, input, f.l, sharedTag(f)));
        getters[f.k] = get;
        continue;
      }
      if (f.t === "policy") {
        const pol = policyTable(v || []);
        getters[f.k] = pol.get;
        grid.append(h("div", { class: "f wide" }, h("span", {}, f.l, sharedTag(f)), pol.el));
        continue;
      }
      if (f.t === "select") {
        input = h("select", {}, f.options.map((o) => h("option", { value: o, selected: o === v }, o)));
        get = () => input.value;
      } else if (f.t === "list" || f.t === "queries") {
        const lines = f.t === "queries" ? (v || []).map((q) => q.name + " " + q.type) : (v || []);
        input = h("textarea", { rows: 3, spellcheck: "false" });
        input.value = lines.join("\n");
        get = () => {
          const ls = input.value.split("\n").map((s) => s.trim()).filter(Boolean);
          if (f.t === "list") return ls;
          return ls.map((s) => { const p = s.split(/\s+/); return { name: p[0], type: (p[1] || "A").toUpperCase() }; });
        };
      } else if (f.t === "int" || f.t === "float") {
        input = h("input", { type: "number", step: f.t === "float" ? "any" : "1", min: f.min, max: f.max });
        input.value = v == null ? "" : v;
        get = () => (input.value === "" ? 0 : Number(input.value));
      } else {
        input = h("input", { type: "text", autocomplete: "off", spellcheck: "false" });
        input.value = v == null ? "" : v;
        get = () => input.value.trim();
      }
      getters[f.k] = get;
      grid.append(h("label", { class: "f" + (f.wide ? " wide" : "") }, h("span", {}, f.l, sharedTag(f)), input, f.hint ? h("span", { class: "hint" }, f.hint) : null));
    }
    return { el: wrap, get: () => Object.fromEntries(Object.entries(getters).map(([k, g]) => [k, g()])) };
  }

  // Configure ▸ General, Gateway groups, DNS proxy, Web GUI and Cluster: one page each, all made from the one form
  // (every page shows its own panel and saves through the same call).
  const makeConfigView = (only) => {
    let holder, status, collect = null;
    const tab = only;

    // Action results ("Saved", "Restored" …) are not announced; only errors, warnings and hints are.
    const say = (kind, ...msg) => kind === "ok" ? clear(status) : clear(status).append(h("div", { class: "notice " + kind, role: kind === "bad" ? "alert" : "status" }, ...msg));

    function renderForm(cfg) {
      const general = fieldset(GENERAL_FIELDS, cfg);
      const web = fieldset(WEB_FIELDS, cfg.web);
      const dns = fieldset(DNS_FIELDS, cfg.dns, true);
      const cluster = fieldset(CLUSTER_FIELDS, cfg.cluster || {});
      // the DNS proxy page is six tabs; every card stays in the page (a hidden one is still read when saving)
      const dnsTabs = () => {
        const TABS = [
          ["Servers", ["Upstream servers"]],
          ["Health & balancing", ["Load Balancing", "Timing"]],
          ["Listeners", ["Listeners"]],
          ["Resolution", ["Policy-Based Resolution", "Sort List"]],
          ["Cache & ECS", ["Cache", "Client network (ECS)"]],
          ["Clients", ["Client Rate Limiting"]],
        ];
        const titleOf = (c) => ((c.querySelector("header h2") || {}).textContent || "");
        const panes = TABS.map(() => h("div", { role: "tabpanel" }));
        for (const c of [...dns.el.children]) {
          const i = TABS.findIndex(([, names]) => names.includes(titleOf(c)));
          panes[i < 0 ? TABS.length - 1 : i].append(c);
        }
        const buttons = TABS.map(([name], i) => h("button", { type: "button", role: "tab", class: "dtab", onclick: () => show(i) }, name));
        function show(i, focus) {
          state.dnsTab = i;
          panes.forEach((p, j) => { p.hidden = j !== i; });
          buttons.forEach((b, j) => { b.setAttribute("aria-selected", j === i ? "true" : "false"); b.tabIndex = j === i ? 0 : -1; b.classList.toggle("on", j === i); });
          if (focus) buttons[i].focus();
          if (document.getElementById("help-panel") && document.getElementById("help-panel").classList.contains("open")) fillHelp();
        }
        const strip = h("div", { class: "dtabs", role: "tablist", "aria-label": "DNS proxy settings" }, buttons);
        strip.addEventListener("keydown", (e) => {
          const d = e.key === "ArrowRight" ? 1 : e.key === "ArrowLeft" ? -1 : 0;
          if (!d) return;
          e.preventDefault();
          show((state.dnsTab + d + TABS.length) % TABS.length, true);
        });
        show(state.dnsTab >= 0 && state.dnsTab < TABS.length ? state.dnsTab : 0);
        return h("div", {}, strip, ...panes);
      };
      const groupsEl = h("div", {});
      const groups = []; // {el, get}
      const addGroup = (g) => {
        const fs = fieldset(GROUP_FIELDS, g);
        const entry = { fs, orig: g };
        const title = h("h3", {}, "Group");
        const card = h("div", { class: "card" },
          h("header", {}, title),
          h("div", { class: "body" },
            g.dns ? h("p", { class: "hint" }, "This gateway has its own DNS servers and domains — edit them on the Topology page.") : null,
            fs.el));
        entry.card = card;
        const gid = fs.el.querySelector("input[type=number]");
        const retitle = () => { title.textContent = "Group " + gid.value; };
        gid.addEventListener("input", retitle); retitle();
        groups.push(entry);
        groupsEl.append(card);
      };
      cfg.groups.forEach(addGroup);
      const section = (title, ...kids) => h("div", { class: "card" }, h("header", {}, h("h2", {}, title)), h("div", { class: "body" }, ...kids));
      collect = () => ({
        ...general.get(),
        // keep what this form has no fields for (a gateway's own DNS pool, per-server domains)
        groups: groups.map((g) => ({ ...(g.orig.dns ? { dns: g.orig.dns } : {}), ...(g.orig.paused ? { paused: true } : {}), ...(g.orig.paused_all ? { paused_all: true } : {}), ...(g.orig.excluded_nodes ? { excluded_nodes: g.orig.excluded_nodes } : {}), ...(g.orig.paused_vips ? { paused_vips: g.orig.paused_vips } : {}), ...(g.orig.paused_vips_here ? { paused_vips_here: g.orig.paused_vips_here } : {}), ...(g.orig.extra_vips ? { extra_vips: g.orig.extra_vips } : {}), ...g.fs.get() })),
        dns: { ...(cfg.dns.server_queries ? { server_queries: cfg.dns.server_queries } : {}), ...(cfg.dns.server_names ? { server_names: cfg.dns.server_names } : {}), ...(cfg.dns.paused_servers ? { paused_servers: cfg.dns.paused_servers } : {}), ...(cfg.dns.paused_queries ? { paused_queries: cfg.dns.paused_queries } : {}), ...dns.get() },
        // the certificate is managed under Web GUI; keep any file paths already in the config
        web: { cert_file: cfg.web.cert_file || "", key_file: cfg.web.key_file || "", ...web.get() },
        cluster: cluster.get(),
        ...(cfg.bgp ? { bgp: cfg.bgp } : {}), // edited on the Anycast page
        ...(cfg.node_paused ? { node_paused: true } : {}), // set on the Node page
        ...(cfg.paused_servers_here ? { paused_servers_here: cfg.paused_servers_here } : {}), // set on the Topology page
        ...(cfg.paused_queries_here ? { paused_queries_here: cfg.paused_queries_here } : {}),
      });
      // every committed edit (leaving a field, picking an option, ticking a box) is saved and applied
      // Automatic updates are not part of the config file: switching them is its own call, made at once
      const updatesCard = () => {
        if (state.autoUpdate == null) return null;
        const cb = h("input", { type: "checkbox", checked: state.autoUpdate });
        cb.addEventListener("change", async (ev) => {
          ev.stopPropagation(); // not a config edit
          const on = cb.checked;
          if (on && !confirm("Turn automatic updates on?\n\nEvery node will build and install any newer staged release, one node at a time.")) { cb.checked = false; return; }
          try { await api("POST", "/api/update/auto", { enabled: on }); state.autoUpdate = on; say("ok"); }
          catch (e) { cb.checked = !on; say("bad", "Not changed: " + (e.message || e)); }
        });
        return section("Upgrade", h("label", { class: "f check" }, cb, "Update every node automatically when a newer release is staged (one node at a time)"),
          h("p", { class: "hint" }, "Upload a release on the Upgrade page; with this on, each node then builds and installs it in turn and rolls back by itself if the new version does not stay up."));
      };
      // the GUI certificate lives under Web GUI; its own controls save through their own calls, never through the settings form
      const certBox = h("div", {});
      certBox.addEventListener("change", (e) => e.stopPropagation());
      VIEWS.certificate.mount(certBox);
      // one card per tab; every tab stays in the page (the hidden ones are just not shown) so a save reads all of them
      const panels = [
        ["general", "General", h("div", {}, section("General", general.el), updatesCard())],
        ["groups", "Gateway groups", h("div", {}, groupsEl)],
        ["dns", "DNS proxy", dnsTabs()],
        ["web", "Web GUI", h("div", {}, section("Web GUI", web.el), certBox)],
        ["cluster", "Cluster", section("Cluster", cluster.el)],
      ];
      for (const [pid, , el] of panels) el.hidden = pid !== tab;
      const form = h("div", {}, ...panels.map((p) => p[2]));
      form.addEventListener("change", () => autosave());
      return form;
    }

    function draw() {
      clear(holder).append(renderForm(state.cfg));
    }

    async function load() {
      try {
        const c = await api("GET", "/api/cluster");
        state.clustered = !!(c.data.enabled && c.data.peers.length > 1);
      } catch (e) { if (e.message === "unauthenticated") throw e; }
      try { state.autoUpdate = !!(await api("GET", "/api/update")).data.intent.auto_all; } catch (e) { if (e.message === "unauthenticated") throw e; state.autoUpdate = null; }
      const r = await api("GET", "/api/config");
      state.cfg = r.config;
      state.cfgInfo = r;
      clear(status);
      if (!r.exists) say("info", "No config file at " + r.path + " yet — the first change creates it.");
      draw();
    }

    // Saving is automatic: there is no Save or Apply button.  One save runs at a
    // time; an edit made while one is running triggers another straight after.
    let busy = false, again = false;
    async function autosave() {
      if (busy) { again = true; return; }
      busy = true;
      try { do { again = false; await save(); } while (again); } finally { busy = false; }
    }

    async function save() {
      let cfg;
      try { cfg = collect(); } catch (e) { say("bad", e.message); return; }
      const was = state.cfg && state.cfg.web ? state.cfg.web : {};
      if (cfg.web.listen !== was.listen &&
        !confirm("Change the GUI listen address to " + cfg.web.listen + "?\n\nThe GUI restarts and every session ends. If the address is wrong you will lose access to this page.")) {
        draw(); // put the old value back
        return;
      }
      try {
        const r = await api("PUT", "/api/config", { config: cfg });
        state.cfg = r.config;
        clear(status);
        state.cfgInfo.exists = true;
      } catch (e) { if (e.message !== "unauthenticated") say("bad", "Not saved: " + e.message); }
    }

    return {
      mount(main) {
        status = h("div", { "aria-live": "polite" });
        holder = h("div", {});
        main.append(status, holder);
        load().catch((e) => { if (e.message !== "unauthenticated") say("bad", e.message); });
      },
    };
  };
  for (const id of ["general", "groups", "dns", "web", "cluster"]) VIEWS["cfg_" + id] = makeConfigView(id);

  // ── shared helpers for the management pages ───────────────────────────────
  // What the memory guard did (Statistics and Host): it drops the oldest history when memory use reaches its limit.
  const guardNote = (g) => (g && g.trims ? [h("br"), h("span", { class: "warnline" }, "Memory guard: the oldest history was dropped " + g.trims + " time" + (g.trims === 1 ? "" : "s") + " to keep memory use under " + g.limit_pct + "%. Last: " + new Date(g.last * 1000).toLocaleString() + " — " + g.last_note)] : []);
  // Nothing is said when every node answered (the Node menu says "Cluster"); when one could not, which and why, since the
  // numbers are then missing its share
  const clusterNote = (c) => {
    const lost = c && c.nodes ? c.nodes.filter((n) => !n.ok) : [];
    if (!lost.length) return [];
    return [h("div", { class: "notice warn" }, "Not included in these numbers: " + lost.map((n) => n.name + " (" + n.error + ")").join(", "))];
  };
  // A right-click menu for a table row: items are [label, handler, "danger"?]. Reuses the Topology menu's look.
  let rowMenuEl = null;
  const closeRowMenu = () => {
    if (!rowMenuEl) return;
    rowMenuEl.remove(); rowMenuEl = null;
    document.removeEventListener("click", closeRowMenu, true);
    document.removeEventListener("keydown", rowMenuKey, true);
    window.removeEventListener("blur", closeRowMenu);
    window.removeEventListener("resize", closeRowMenu);
  };
  const rowMenuKey = (e) => {
    if (e.key === "Escape") { e.preventDefault(); closeRowMenu(); return; }
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
    e.preventDefault();
    const items = [...rowMenuEl.querySelectorAll("button")], i = items.indexOf(document.activeElement);
    items[(i + (e.key === "ArrowDown" ? 1 : items.length - 1)) % items.length].focus();
  };
  const rowMenu = (items) => (e) => {
    e.preventDefault(); e.stopPropagation();
    closeRowMenu();
    rowMenuEl = h("div", { class: "cv-menu", role: "menu" }, items.map(([label, fn, cls]) =>
      h("button", { type: "button", role: "menuitem", class: cls || "", onclick: () => { closeRowMenu(); fn(); } }, label)));
    document.body.append(rowMenuEl);
    const r = rowMenuEl.getBoundingClientRect(), b = e.target.getBoundingClientRect ? e.target.getBoundingClientRect() : { left: 0, bottom: 0 };
    const x = e.clientX || b.left, y = e.clientY || b.bottom;
    rowMenuEl.style.left = Math.max(4, Math.min(x, window.innerWidth - r.width - 4)) + "px";
    rowMenuEl.style.top = Math.max(4, Math.min(y, window.innerHeight - r.height - 4)) + "px";
    rowMenuEl.querySelector("button").focus();
    document.addEventListener("click", closeRowMenu, true);
    document.addEventListener("keydown", rowMenuKey, true);
    window.addEventListener("blur", closeRowMenu);
    window.addEventListener("resize", closeRowMenu);
  };
  const when = (t) => (t && !String(t).startsWith("0001") ? new Date(t).toLocaleString() : "–");
  // The cluster's member table: the Cluster page passes a remove handler (right-click a node ▸ Remove), the Monitor page none.
  // The Monitor page (named) has the node's name and its addresses in two columns; the cluster address is the name's tooltip.
  const membersTable = (v, onRemove, named) => {
    const roleOf = (p) => (p.is_primary ? "primary" : (p.role || "replica"));
    const rows = v.peers.map((p) => h("tr", Object.assign({ class: p.self ? "local" : "" }, onRemove && !p.self ? { tabindex: "0", oncontextmenu: rowMenu([["Remove", () => onRemove(p), "danger"]]) } : {}),
      named ? h("td", { class: "nname", title: p.addr }, p.hostname || p.addr, p.self ? " ★" : "") : h("td", { class: "mono" }, p.addr, p.self ? " ★" : ""),
      named ? h("td", { class: "mono" }, (p.ips && p.ips.length) ? p.ips.map((ip) => h("div", {}, ip)) : "–") : null,
      h("td", {}, pill(roleOf(p), p.is_primary ? "ok" : "info")),
      h("td", {}, p.reachable ? pill("yes", "ok") : pill("NO", "bad"), p.updating ? [" ", pill("updating", "warn")] : null),
      h("td", { class: "num" }, p.reachable ? p.epoch : "–"),
      h("td", { class: "num" }, p.version || "–"), h("td", { class: "num" }, p.source_version || "–"),
      h("td", {}, p.self ? "now" : when(p.last_seen)),
      h("td", { class: "wrap muted" }, p.error || "")));
    return h("div", { class: "scroll" }, h("table", { class: named ? "tight" : null },
      h("thead", {}, h("tr", {}, (named ? ["Node name", "Node IP"] : ["Node"]).concat(["Role", "Reachable", "Epoch", "Running", "Source", "Last seen", ""]).map((t, i) => h("th", { class: i >= (named ? 4 : 3) && i <= (named ? 6 : 5) ? "num" : "" }, t)))),
      h("tbody", {}, rows)));
  };
  const say = (el, kind, ...msg) => kind === "ok" ? clear(el) : clear(el).append(h("div", { class: "notice " + kind, role: kind === "bad" ? "alert" : "status" }, ...msg));
  const fail = (el) => (e) => { if (e.message !== "unauthenticated") say(el, "bad", e.message); };
  const kv = (rows) => h("dl", { class: "kv" }, rows.filter(Boolean).map(([k, v]) => [h("dt", {}, k), h("dd", {}, v)]));
  const section = (title, ...kids) => h("div", { class: "card" }, h("header", {}, h("h2", {}, title)), h("div", { class: "body" }, ...kids));
  const copyBtn = (getText) => h("button", { class: "btn small", type: "button", onclick: async (ev) => {
    try { await navigator.clipboard.writeText(getText()); ev.target.textContent = "Copied"; setTimeout(() => { ev.target.textContent = "Copy"; }, 1500); }
    catch (_) { ev.target.textContent = "Select and copy manually"; }
  } }, "Copy");
  function readFileInto(input, target) {
    input.addEventListener("change", () => {
      const f = input.files && input.files[0];
      if (!f) return;
      const rd = new FileReader();
      rd.onload = () => { target.value = String(rd.result); };
      rd.readAsText(f);
    });
  }
  function saveText(name, text) {
    const a = h("a", { href: URL.createObjectURL(new Blob([text], { type: "text/plain" })), download: name });
    document.body.append(a); a.click(); a.remove();
  }
  // Expiry warning under the tabs (certificate state is cheap to check once per login).
  async function certBanner() {
    const slot = document.getElementById("banner");
    if (!slot) return;
    try {
      const c = (await api("GET", "/api/tls")).data;
      clear(slot);
      if (c.expired) slot.append(h("div", { class: "notice bad", role: "alert" }, "The GUI certificate has expired. Install a new one under Configure ▸ Web GUI."));
      else if (c.expires_soon && c.source !== "self-signed") slot.append(h("div", { class: "notice warn" }, "The GUI certificate expires in " + c.days_left + " day(s)."));
    } catch (_) { /* not critical */ }
  }

  // ── History  (CLI: --versions, --version-show/-diff/-snapshot/-restore/-export, --config-import) ──
  VIEWS.history = (() => {
    let tbody, panel, status, picked = [], list = [], sel = null, actBtns = [];
    const actor = (v) => v.actor + (v.manual ? " · manual" : "");

    // The actions work on the highlighted row, so the table has no buttons of its own.
    function select(id) {
      sel = id;
      for (const tr of tbody.querySelectorAll("tr[data-id]")) tr.classList.toggle("sel", tr.dataset.id === id);
      for (const b of actBtns) b.disabled = !b.dataset.always && (id === null || (b.dataset.needs === "previous" && !previous()));
    }
    const current = () => list.find((v) => v.id === sel) || null;
    const previous = () => { const i = list.findIndex((v) => v.id === sel); return i >= 0 && i + 1 < list.length ? list[i + 1] : null; };
    function download(id) {
      const a = h("a", { href: route("/api/versions/download?id=" + encodeURIComponent(id)), download: "" });
      document.body.append(a); a.click(); a.remove();
    }

    function drawDiff(d) {
      const rows = (d.lines || []).map((l) => h("tr", { class: "dl " + ({ "+": "add", "-": "del", "@": "skip" }[l.op] || "ctx") },
        h("td", { class: "op" }, l.op === "@" ? "" : l.op), h("td", { class: "mono ln" }, l.op === "@" ? "… " + l.text : l.text)));
      clear(panel).append(h("div", { class: "card" },
        h("header", {}, h("h2", {}, "Changes"), h("span", { class: "mono muted" }, d.a.id + " → " + d.b.id),
          h("span", { class: "spacer" }), h("button", { class: "btn small", type: "button", onclick: () => clear(panel) }, "Close")),
        h("div", { class: "body" },
          d.same ? h("div", { class: "empty" }, "The two versions are identical.") : [
            h("ul", { class: "changes" }, (d.sections || []).map((s) => h("li", {}, h("strong", {}, s.label + ": "), s.detail))),
            h("div", { class: "scroll" }, h("table", { class: "diff" }, h("tbody", {}, rows)))])));
      panel.scrollIntoView({ block: "nearest" });
    }
    async function diff(a, b) {
      try { drawDiff((await api("GET", "/api/versions/diff?a=" + encodeURIComponent(a) + "&b=" + encodeURIComponent(b))).data); }
      catch (e) { fail(status)(e); }
    }
    async function view(id) {
      try {
        const r = (await api("GET", "/api/versions/get?id=" + encodeURIComponent(id))).data;
        clear(panel).append(h("div", { class: "card" },
          h("header", {}, h("h2", {}, "Version " + id), h("span", { class: "muted" }, when(r.meta.at) + " · " + r.meta.actor),
            h("span", { class: "spacer" }), h("button", { class: "btn small", type: "button", onclick: () => clear(panel) }, "Close")),
          h("div", { class: "body" }, h("pre", { class: "help" }, JSON.stringify(r.config, null, 2)))));
      } catch (e) { fail(status)(e); }
    }
    async function restore(v) {
      if (!confirm("Restore version " + v.id + " (" + when(v.at) + ") and apply it now?\n\nThe current configuration is saved as a new version first, so you can come back.")) return;
      try { await api("POST", "/api/versions/restore", { id: v.id }); say(status, "ok", "Version " + v.id + " restored and applied."); await load(); }
      catch (e) { fail(status)(e); }
    }
    async function load() {
      list = (await api("GET", "/api/versions")).data;
      picked = picked.filter((id) => list.some((v) => v.id === id));
      if (sel !== null && !list.some((v) => v.id === sel)) sel = null;
      clear(tbody);
      if (!list.length) { tbody.append(h("tr", {}, h("td", { colspan: 4, class: "empty" }, "Nothing recorded yet. A version is saved every time the configuration changes."))); select(null); return; }
      list.forEach((v, i) => {
        const cb = h("input", { type: "checkbox", "aria-label": "Compare version " + v.id, checked: picked.includes(v.id), onchange: () => {
          picked = cb.checked ? [...picked, v.id].slice(-2) : picked.filter((x) => x !== v.id);
          for (const o of tbody.querySelectorAll("input[type=checkbox]")) o.checked = picked.includes(o.dataset.id);
        } });
        cb.dataset.id = v.id;
        const tr = h("tr", { "data-id": v.id, tabindex: "0", "aria-label": "Version " + v.id },
          h("td", {}, cb),
          h("td", { class: "nowrap" }, when(v.at), i === 0 ? [" ", pill("latest", "info")] : null),
          h("td", {}, actor(v)),
          h("td", { class: "wrap" }, v.summary, v.note ? h("div", { class: "muted" }, "“" + v.note + "”") : null));
        tr.addEventListener("click", () => select(v.id));
        tr.addEventListener("keydown", (e) => { if (e.key === "Enter" || e.key === " ") { if (e.target === tr) { e.preventDefault(); select(v.id); } } });
        tbody.append(tr);
      });
      select(sel);
    }
    return {
      mount(main) {
        status = h("div", { "aria-live": "polite" });
        panel = h("div", {});
        tbody = h("tbody", {});
        const file = h("input", { type: "file", accept: ".json,application/json", class: "hidden", "aria-label": "Configuration file" });
        file.addEventListener("change", async () => {
          const f = file.files[0]; file.value = "";
          if (!f) return;
          let cfg;
          try { cfg = JSON.parse(await f.text()); } catch (e) { say(status, "bad", "Not valid JSON: " + e.message); return; }
          if (!confirm("Replace the whole configuration with " + f.name + " and apply it now?")) return;
          try { await api("POST", "/api/versions/upload", { config: cfg, note: "uploaded " + f.name }); say(status, "ok", "Configuration from " + f.name + " applied."); await load(); }
          catch (e) { fail(status)(e); }
        });
        const act = (label, needs, fn) => { const b = h("button", { class: "btn small", type: "button", disabled: true, onclick: fn }, label); if (needs) b.dataset.needs = needs; return b; };
        actBtns = [
          h("button", { class: "btn small", type: "button", "data-always": "1", title: "Record the current configuration as a version now", onclick: async () => {
            try { await api("POST", "/api/versions/snapshot", {}); say(status, "ok", "Snapshot saved."); await load(); } catch (e) { fail(status)(e); }
          } }, "Snapshot now"),
          act("View", null, () => { if (sel) view(sel); }),
          act("Diff vs live", null, () => { if (sel) diff(sel, "current"); }),
          act("Diff vs previous", "previous", () => { const p = previous(); if (p) diff(p.id, sel); }),
          h("button", { class: "btn small", type: "button", "data-always": "1", title: "Tick two versions to compare them", onclick: () => { if (picked.length === 2) diff(picked[1], picked[0]); else say(status, "info", "Tick two versions to compare them."); } }, "Compare ticked"),
          act("Restore", null, () => { const v = current(); if (v) restore(v); }),
          act("Download", null, () => { if (sel) download(sel); }),
          h("button", { class: "btn small", type: "button", "data-always": "1", onclick: () => file.click() }, "Upload"),
        ];
        sel = null;
        main.append(
          file,
          status,
          // the buttons belong to the list: they act on the highlighted version
          h("div", { class: "card" },
            h("header", { class: "bar", role: "group", "aria-label": "Versions" }, ...actBtns),
            h("div", { class: "scroll" }, h("table", {},
              h("thead", {}, h("tr", {}, ["", "When", "Who", "What changed"].map((t) => h("th", {}, t)))), tbody))),
          panel);
        load().catch(fail(status));
      },
    };
  })();

  // ── Certificate  (CLI: --tls-status/-install/-csr/-revert/-regenerate) ─────
  VIEWS.certificate = (() => {
    let info, status, csrBox, tools;
    const sourceLabel = { files: "Files set in the configuration file", installed: "Uploaded on this page", cluster: "Copied from the cluster's main node", "self-signed": "Created automatically by ddgw" };
    const fold = (title, ...kids) => h("details", { class: "card fold" }, h("summary", {}, title), h("div", { class: "body" }, ...kids));

    function drawInfo(c) {
      if (tools) tools.classList.toggle("hidden", c.source === "files");
      const left = c.expired ? pill("Expired", "bad") : pill(c.days_left + " days left", c.expires_soon ? "warn" : "ok");
      clear(info).append(h("div", { class: "card" },
        h("header", {}, h("h2", {}, "Current certificate"), pill(c.source === "self-signed" ? "Not trusted by browsers" : "Custom", c.source === "self-signed" ? "warn" : "ok"), c.error ? pill("problem", "bad") : left),
        h("div", { class: "body" }, c.error ? h("div", { class: "notice bad" }, c.error) : kv([
          ["Where it comes from", sourceLabel[c.source] || c.source],
          ["Issued to", c.subject], ["Issued by", c.issuer + (c.self_signed ? " (itself)" : "")],
          ["Valid for", [...new Set([...c.dns_names, ...c.ip_addresses])].join(", ") || "–"],
          ["Valid", when(c.not_before) + " → " + when(c.not_after)],
          ["SHA-256", h("span", { class: "mono wrapall" }, c.fingerprint)],
          c.source === "files" && c.cert_file ? ["Files", h("span", { class: "mono" }, c.cert_file + ", " + c.key_file)] : null,
          c.installed_by ? ["Installed", "by " + c.installed_by + " at " + when(c.installed_at)] : null,
          ["Installed certificates in chain", String(c.chain_length)]]),
          c.source === "self-signed" ? h("p", { class: "hint" }, "Your browser shows a security warning for this certificate. Upload one from your own certificate authority below to get rid of it.") : null,
          c.source === "files" ? h("p", { class: "hint" }, "This certificate comes from files named in the configuration file, which override anything uploaded here. To manage it on this page instead, remove web.cert_file and web.key_file from the configuration file.") : null)));
      clear(csrBox);
      if (c.pending_csr) {
        const ta = h("textarea", { rows: 8, readonly: true, class: "pem", "aria-label": "Pending CSR" });
        ta.value = c.pending_csr;
        csrBox.append(h("div", { class: "notice info" }, "A certificate request is waiting. Send the text below to your certificate authority, then upload the certificate they send back (above) — leave the private key box empty."),
          ta, h("div", { class: "toolbar" }, copyBtn(() => ta.value),
            h("button", { class: "btn small", type: "button", onclick: () => saveText("ddgw.csr", ta.value) }, "Download"),
            h("button", { class: "btn small", type: "button", onclick: async () => {
              if (!confirm("Throw away the pending request?")) return;
              try { await api("POST", "/api/tls/csr/cancel", {}); await load(); } catch (e) { fail(status)(e); }
            } }, "Discard")));
      }
    }
    async function load() { drawInfo((await api("GET", "/api/tls")).data); }

    return {
      mount(main) {
        status = h("div", { "aria-live": "polite" });
        info = h("div", {});
        csrBox = h("div", {});
        const cert = h("textarea", { rows: 7, spellcheck: "false", class: "pem", placeholder: "-----BEGIN CERTIFICATE-----  (your certificate first, then any intermediate ones)", "aria-label": "Certificate PEM" });
        const key = h("textarea", { rows: 5, spellcheck: "false", class: "pem", placeholder: "-----BEGIN PRIVATE KEY-----  (leave empty if you created a request below)", "aria-label": "Private key PEM", autocomplete: "off" });
        const certFile = h("input", { type: "file", accept: ".pem,.crt,.cer,.txt", "aria-label": "Certificate file" });
        const keyFile = h("input", { type: "file", accept: ".pem,.key,.txt", "aria-label": "Key file" });
        readFileInto(certFile, cert); readFileInto(keyFile, key);
        const cn = h("input", { type: "text", placeholder: "gw.example.com", autocomplete: "off", "aria-label": "Common name" });
        const names = h("textarea", { rows: 3, spellcheck: "false", placeholder: "gw2.example.com", "aria-label": "Other names" });
        const ips = h("textarea", { rows: 2, spellcheck: "false", placeholder: "10.0.0.5", "aria-label": "IP addresses" });
        const lines = (t) => t.value.split("\n").map((s) => s.trim()).filter(Boolean);
        tools = h("div", {},
          section("Upload your own certificate",
            h("p", { class: "hint" }, "Paste the certificate and its private key, or choose the files. It is used for the next connection — nothing restarts and nobody is logged out. Nodes in a cluster get the same certificate automatically."),
            h("div", { class: "grid2" },
              h("label", { class: "f" }, "Certificate", cert, certFile),
              h("label", { class: "f" }, "Private key", key, keyFile)),
            h("div", { class: "toolbar" }, h("button", { class: "btn primary", type: "button", onclick: async (ev) => {
              ev.target.disabled = true;
              try {
                const r = (await api("POST", "/api/tls/install", { cert_pem: cert.value, key_pem: key.value })).data;
                cert.value = ""; key.value = "";
                say(status, r.warnings && r.warnings.length ? "warn" : "ok", "Certificate installed. ", ...(r.warnings || []).map((w) => h("div", {}, "• " + w)));
                drawInfo(r.info);
              } catch (e) { fail(status)(e); } finally { ev.target.disabled = false; }
            } }, "Use this certificate"))),
          fold("Don't have a certificate yet? Create a request for one",
            h("p", { class: "hint" }, "Your certificate authority needs a request first. Fill this in and send the result to them. The private key is created and kept on this server; when the signed certificate comes back, upload it above without a key."),
            h("div", { class: "grid" }, h("label", { class: "f" }, "Main name", cn), h("label", { class: "f wide" }, "Other names (one per line)", names), h("label", { class: "f wide" }, "IP addresses (one per line, optional)", ips)),
            h("div", { class: "toolbar" }, h("button", { class: "btn", type: "button", onclick: async () => {
              try { await api("POST", "/api/tls/csr", { cn: cn.value.trim(), dns: lines(names), ips: lines(ips) }); say(status, "ok", "Request created."); await load(); } catch (e) { fail(status)(e); }
            } }, "Create request")), csrBox),
          fold("Go back to the automatic certificate",
            h("p", { class: "hint" }, "Anyname can make its own certificate. Browsers do not trust it, but connections are still encrypted."),
            h("div", { class: "toolbar" },
              h("button", { class: "btn warn", type: "button", onclick: async () => {
                if (!confirm("Stop using the uploaded certificate and go back to the automatic one?")) return;
                try { await api("POST", "/api/tls/revert", {}); say(status, "ok", "Back to the automatic certificate."); await load(); } catch (e) { fail(status)(e); }
              } }, "Stop using the uploaded certificate"),
              h("button", { class: "btn", type: "button", onclick: async () => {
                try { await api("POST", "/api/tls/regenerate", {}); say(status, "ok", "A fresh automatic certificate was created."); await load(); } catch (e) { fail(status)(e); }
              } }, "Make a fresh automatic certificate"))));
        main.append(status, info, tools);
        load().catch(fail(status));
      },
    };
  })();

  // ── Cluster  (CLI: --cluster-status/-token/-join/-promote/-remove/-unremove/-leave/-sync) ──
  VIEWS.cluster = (() => {
    let dyn, status, tokenBox, joinBox;

    async function act(path, body, confirmText, okText) {
      if (confirmText && !confirm(confirmText)) return;
      try { await api("POST", path, body || {}); say(status, "ok", okText); await VIEWS.cluster.poll(); } catch (e) { fail(status)(e); }
    }

    function draw(v) { const real = dyn; dyn = h("div", {}); try { build(v); } finally { morph(real, dyn); dyn = real; } }
    // built into a detached copy and morphed in: the page keeps its elements (and so its scroll boxes) from one poll to the next
    function build(v) {
      if (v.conflict) dyn.append(h("div", { class: "notice bad", role: "alert" }, "Conflict: " + v.conflict));
      for (const w of v.warnings) dyn.append(h("div", { class: "notice warn" }, w));
      if (v.last_sync_error) dyn.append(h("div", { class: "notice warn" }, "Last sync failed: " + v.last_sync_error));
      const stat = (k, val, small) => h("div", { class: "stat" }, h("div", { class: "k" }, k), h("div", { class: "v" + (small ? " small" : "") }, val));
      dyn.append(h("div", { class: "stats" },
        stat("This node", v.self, true), stat("Role", v.role.toUpperCase()), stat("Epoch", v.epoch),
        stat("Primary", v.primary_addr, true), stat("Shared settings rev.", v.shared_rev)));
      dyn.append(h("div", { class: "card" },
        h("header", {}, h("h2", {}, "Members")),
        membersTable(v, (p) => act("/api/cluster/peers/remove", { addr: p.addr }, "Remove " + p.addr + " from the cluster? It resets itself to a single-node cluster.", p.addr + " removed."))));
      if (v.removed.length) dyn.append(h("div", { class: "card" }, h("header", {}, h("h2", {}, "Removed nodes")), h("div", { class: "body" },
        h("p", { class: "hint" }, "These cannot be re-added by gossip. Lift the block to let one join again with a new code."),
        v.removed.map((a) => h("div", { class: "toolbar" }, h("span", { class: "mono" }, a),
          h("button", { class: "btn small", type: "button", onclick: () => act("/api/cluster/peers/unremove", { addr: a }, null, "Block lifted for " + a) }, "Allow again"))))));
      // join form only makes sense for an untouched node
      joinBox.classList.toggle("hidden", !v.joinable);
      leaveBtn.classList.toggle("hidden", v.joinable);
      promoteBtn.classList.toggle("hidden", v.role === "primary");
      lastView = v;
    }
    let leaveBtn, promoteBtn, lastView = null;

    return {
      mount(main) {
        status = h("div", { "aria-live": "polite" });
        dyn = h("div", {});
        tokenBox = h("div", {});
        const code = h("textarea", { rows: 3, spellcheck: "false", class: "pem", placeholder: "ddgw-join-v1:…", "aria-label": "Join code" });
        joinBox = section("Join an existing cluster",
          h("p", { class: "hint" }, "Paste a join code created on a member (“Create join code” there). This node's shared settings — DNS block, VIPs, keys and timers — are replaced by the cluster's; its interface, priority, weight, preemption and neighbours stay its own. A snapshot of the current configuration is saved first."),
          code, h("div", { class: "toolbar" }, h("button", { class: "btn primary", type: "button", onclick: async (ev) => {
            if (!confirm("Join this node to the cluster? Its shared settings will be replaced by the cluster's.")) return;
            ev.target.disabled = true;
            try { await api("POST", "/api/cluster/join", { code: code.value.trim() }); code.value = ""; say(status, "ok", "Joined the cluster."); await VIEWS.cluster.poll(); }
            catch (e) { fail(status)(e); } finally { ev.target.disabled = false; }
          } }, "Join cluster")));
        leaveBtn = h("button", { class: "btn danger", type: "button", onclick: () => act("/api/cluster/leave", {}, "Leave the cluster? This node keeps its settings and becomes its own single-node cluster.", "Left the cluster.") }, "Leave cluster");
        promoteBtn = h("button", { class: "btn warn", type: "button", onclick: () => act("/api/cluster/promote", {}, "Promote THIS node to primary?\n\nDo this only if the current primary is gone for good (or you are moving the role on purpose). Every other member follows the new primary.", "This node is now the primary.") }, "Promote this node");
        main.append(status,
          h("div", { class: "toolbar" },
            h("button", { class: "btn", type: "button", onclick: async () => {
              try {
                const r = (await api("POST", "/api/cluster/token", {})).data;
                const ta = h("textarea", { rows: 4, readonly: true, class: "pem", "aria-label": "Join code" }); ta.value = r.code;
                clear(tokenBox).append(h("div", { class: "card" }, h("header", {}, h("h2", {}, "Join code"), h("span", { class: "muted" }, "single use, valid until " + when(r.expires))),
                  h("div", { class: "body" }, ta, h("div", { class: "toolbar" }, copyBtn(() => ta.value),
                    h("span", { class: "hint" }, "Paste it on the new node (Cluster → Join) or run: ddgw --cluster-join '<code>'")))));
              } catch (e) { fail(status)(e); }
            } }, "Create join code"),
            h("button", { class: "btn", type: "button", onclick: () => act("/api/cluster/sync", {}, null, "Synced.") }, "Sync now"),
            h("span", { class: "spacer" }), promoteBtn, leaveBtn),
          tokenBox, dyn, h("p", { class: "hint" }, "★ marks this node"), joinBox);
        joinBox.classList.add("hidden"); leaveBtn.classList.add("hidden"); promoteBtn.classList.add("hidden");
      },
      async poll() { draw((await api("GET", "/api/cluster")).data); },
    };
  })();

  // ── Upgrade  (CLI: --update-status/-upload/-apply/-push/-cancel/-auto/-history) ──
  VIEWS.updates = (() => {
    let dyn, status, stats, selected = new Set(), histPage = 0;
    const HIST_PAGE = 25;
    const stat = (k, val, small) => h("div", { class: "stat" }, h("div", { class: "k" }, k), h("div", { class: "v" + (small ? " small" : "") }, val));

    async function act(path, body, confirmText, okText) {
      if (confirmText && !confirm(confirmText)) return;
      try { await api("POST", path, body || {}); say(status, "ok", okText); await VIEWS.updates.poll(); } catch (e) {
        // updating this node now would leave a gateway unserved: say so and let the admin decide
        if (path === "/api/update/apply" && /^not safe to update/.test(e.message || "") && confirm(e.message.replace(/ \(to update anyway.*$/, "") + "\n\nUpdate anyway? Clients of that gateway will be interrupted.")) {
          try { await api("POST", path, { force: true }); say(status, "ok", okText); await VIEWS.updates.poll(); } catch (e2) { fail(status)(e2); }
          return;
        }
        fail(status)(e);
      }
    }

    function draw(v) { const real = dyn; dyn = h("div", {}); try { build(v); } finally { morph(real, dyn); dyn = real; } }
    // built into a detached copy and morphed in: the page keeps its elements (and so its scroll boxes) from one poll to the next
    function build(v) {
      if (v.notice) dyn.append(h("div", { class: "notice warn", role: "alert" }, v.notice));
      if (!v.toolchain) dyn.append(h("div", { class: "notice warn" }, "No Go toolchain (≥ 1.24) was found on this node, so it cannot build updates itself. Re-run install.sh, which keeps one under /usr/local/share/ddgw/go."));
      const newer = v.source_version && Number(v.source_version) > Number(v.running);
      fill(stats, 
        stat("Running", "v" + v.running), stat("Staged source", v.source_version ? "v" + v.source_version : "none"),
        stat("Auto-update", v.intent.auto_all ? "ON" : "off"),
        stat("This node", v.busy ? (v.phase || "working…") : newer ? "update available" : "up to date", true));
      if (v.waiting) dyn.append(h("p", { class: "hint" }, "This node is holding back its queued update: " + v.waiting + "."));
      const addrs = new Set(v.nodes.map((n) => n.addr));
      for (const a of [...selected]) if (!addrs.has(a)) selected.delete(a);
      const picked = v.clustered ? [...selected] : [];
      const go = h("button", { class: "btn primary hdr", type: "button", disabled: v.intent.auto_all || !newer || v.busy || !v.toolchain, title: v.intent.auto_all ? "Automatic updates are on: every node updates itself. Turn them off in Configure ▸ General ▸ Updates to update by hand." : null, onclick: () => picked.length
        ? act("/api/update/push", { nodes: picked }, "Update " + picked.length + " selected node" + (picked.length === 1 ? "" : "s") + " to v" + v.source_version + "?\n\nThey build and restart one at a time.", "Queued " + picked.length + " node(s).")
        : act("/api/update/apply", {}, "Build v" + v.source_version + " on this node and restart into it?\n\nThe node is briefly unavailable. If the new version fails to stay up it is rolled back automatically.", "Building v" + v.source_version + " — this node restarts when it is done.") },
        picked.length ? "Update " + picked.length + " selected" : "Update this node now");
      const rows = v.nodes.map((n) => {
        const state = n.updating ? pill("updating", "warn") : n.failed ? pill("failed v" + n.failed, "bad") : n.behind ? pill("behind", "warn") : pill("ok", "ok");
        const cb = h("td", {}, h("input", { type: "checkbox", checked: selected.has(n.addr), disabled: v.intent.auto_all, "aria-label": "Select " + n.addr, onchange: (ev) => { if (ev.target.checked) selected.add(n.addr); else selected.delete(n.addr); VIEWS.updates.poll(); } }));
        return h("tr", { class: n.self ? "local" : "" }, v.clustered ? cb : null,
          h("td", { class: "mono" }, n.addr, n.self ? " ★" : ""),
          h("td", {}, n.reachable ? pill("yes", "ok") : pill("NO", "bad")),
          h("td", { class: "num" }, n.running || "–"), h("td", { class: "num" }, n.source || "–"),
          h("td", {}, n.queued ? pill(v.intent.auto_all ? "auto" : "queued", "info") : ""), h("td", {}, state));
      });
      const heads = (v.clustered ? [""] : []).concat(["Node", "Reachable", "Running", "Source", "Queued", "State"]);
      const off = v.clustered ? 1 : 0;
      dyn.append(h("div", { class: "card" }, h("header", { class: "bar" }, h("h2", {}, "Nodes"), h("span", { class: "grow" }), go),
        h("div", { class: "scroll" }, h("table", {}, h("thead", {}, h("tr", {}, heads.map((t, i) => h("th", { class: i === 2 + off || i === 3 + off ? "num" : "" }, t)))), h("tbody", {}, rows)))));
      const row = (e) => h("tr", {}, h("td", {}, when(e.at)), h("td", { class: "mono" }, e.node), h("td", {}, pill(e.kind, e.kind === "failed" || e.kind === "rolled-back" ? "bad" : e.kind === "applied" ? "ok" : "info")),
        h("td", { class: "num" }, (e.from ? e.from + " → " : "") + (e.to || "")), h("td", { class: "wrap" }, [e.detail, e.by ? " (by " + e.by + ")" : ""].join("")));
      const pages = Math.max(1, Math.ceil(v.history.length / HIST_PAGE));
      histPage = Math.min(Math.max(histPage, 0), pages - 1);   // newest first; page 0 is the newest
      const shown = v.history.slice(histPage * HIST_PAGE, (histPage + 1) * HIST_PAGE);
      const go2 = (d) => () => { histPage += d; VIEWS.updates.poll(); };
      const pager = pages > 1 ? h("span", { class: "pager" },
        h("span", { class: "muted small" }, "Events " + (histPage * HIST_PAGE + 1) + "–" + (histPage * HIST_PAGE + shown.length) + " of " + v.history.length),
        h("button", { class: "btn small", type: "button", disabled: histPage === 0, onclick: go2(-1) }, "‹ Newer"),
        h("span", { class: "muted small" }, "Page " + (histPage + 1) + " of " + pages),
        h("button", { class: "btn small", type: "button", disabled: histPage >= pages - 1, onclick: go2(1) }, "Older ›")) : null;
      dyn.append(h("div", { class: "card" }, h("header", { class: "bar" }, h("h2", {}, "History"), h("span", { class: "grow" }), pager),
        shown.length ? h("div", { class: "scroll" }, h("table", {}, h("thead", {}, h("tr", {}, ["When", "Node", "Event", "Version", "Detail"].map((t) => h("th", {}, t)))), h("tbody", {}, shown.map(row))))
          : h("div", { class: "empty" }, "No update activity yet.")));
    }

    return {
      mount(main) {
        status = h("div", { "aria-live": "polite" });
        dyn = h("div", {});
        stats = h("div", { class: "stats" });
        const file = h("input", { type: "file", accept: ".tgz,.tar.gz,.zip,application/gzip,application/zip", "aria-label": "Release archive" });
        const up = h("button", { class: "btn primary hdr", type: "button", onclick: async (ev) => {
          const f = file.files[0];
          if (!f) { say(status, "info", "Choose a release archive first."); return; }
          ev.target.disabled = true;
          try { const r = await apiUpload("/api/update/upload", f); file.value = ""; say(status, "ok", "Source v" + r.data.version + " staged."); await VIEWS.updates.poll(); }
          catch (e) { fail(status)(e); } finally { ev.target.disabled = false; }
        } }, "Upload");
        main.append(stats, status,
          h("div", { class: "card" }, h("header", { class: "bar" }, h("h2", {}, "Upload a release"), h("span", { class: "grow" }), up),
            h("div", { class: "body" },
              h("p", { class: "hint" }, "Select a release archive. It is checked and staged on this node; other cluster members pull it from here."),
              h("div", { class: "toolbar tight" }, file))),
          dyn);
      },
      async poll() { draw((await api("GET", "/api/update")).data); },
      poll_ms: 2000,
    };
  })();

  // ── Statistics, Monitor  (CLI: --stats) ──────────────────────────────────────
  // Query totals by response code over time, query types, transports, top clients and
  // domains.  The daemon keeps them for 30 days (in memory, saved to disk every 5 minutes).  The picked node (top-bar
  // Node menu) is the one shown.
  VIEWS.stats = (() => {
    const SVGNS = "http://www.w3.org/2000/svg";
    const sv = (tag, attrs, ...kids) => {
      const el = document.createElementNS(SVGNS, tag);
      for (const [k, v] of Object.entries(attrs || {})) {
        if (v == null || v === false) continue;
        if (k.startsWith("on")) el.addEventListener(k.slice(2), v); else el.setAttribute(k, v);
      }
      for (const kid of kids.flat(Infinity)) if (kid != null) el.append(kid.nodeType ? kid : document.createTextNode(String(kid)));
      return el;
    };
    // Series in the validated colour order; the colour belongs to the entity.
    const SERIES = [
      { key: "total", label: "Total", cls: "q-total" },
      { key: "no_error", label: "No Error", cls: "q-ok" },
      { key: "nx_domain", label: "NX Domain", cls: "q-nx" },
      { key: "refused", label: "Refused", cls: "q-ref" },
      { key: "other", label: "Other", cls: "q-oth" },
      { key: "updates", label: "Updates", cls: "q-upd" },
      { key: "server_failure", label: "Server Failure", cls: "q-fail" },
    ];
    // a record type keeps its colour whatever its rank; the rest fold into "Other"
    const TYPE_CLS = { A: "t1", AAAA: "t2", HTTPS: "t3", PTR: "t4", SRV: "t5", TXT: "t6", SOA: "t7", MX: "t8" };
    const PROTO_CLS = { UDP: "t1", TCP: "t2" };
    const RANGES = [["1h", "Last Hour"], ["1d", "Last Day"], ["7d", "Last Week"], ["30d", "Last Month"], ["custom", "Custom"]];
    const REFRESH_MS = 10000;
    const n0 = (n) => Number(n || 0).toLocaleString();
    const pct = (n, t) => (t <= 0 ? "–" : n === 0 ? "0%" : (100 * n / t).toFixed(n / t < 0.001 ? 2 : 1) + "%");

    let status, bar, cnote, tiles, chartBox, pies, tops, upds, foot, rangeBtns, fromIn, toIn, customRow;
    let range = "1h", qpsView = false, sel = "", pickClient = "", pickDomain = "", hidden = new Set(), last = null, lastAt = 0, seq = 0, clientsMore = false, domainsMore = false;

    // ── fetching ──
    // the picked node (Node menu in the top bar) answers; pick another member there
    // sel: the answer kind a tile picked ("" = everything); the daemon limits the donuts and
    // top lists to it, the chart just shows that one line
    const q = (from, to) => "/api/qstats?from=" + encodeURIComponent(from) + (to ? "&to=" + encodeURIComponent(to) : "") + (sel ? "&rcode=" + sel : "") + (pickClient ? "&client=" + encodeURIComponent(pickClient) : "") + (pickDomain ? "&domain=" + encodeURIComponent(pickDomain) : "");
    const KINDS = { no_error: "noerror", server_failure: "servfail", nx_domain: "nxdomain", refused: "refused", updates: "update" };
    async function fetchData() {
      let from = range, to = "";
      if (range === "custom") {
        const a = new Date(fromIn.value), b = new Date(toIn.value);
        if (isNaN(a) || isNaN(b) || a >= b) throw new Error("Pick a start that is before the end.");
        from = String(Math.floor(a / 1000)); to = String(Math.floor(b / 1000));
      }
      return (await api("GET", q(from, to))).data;
    }

    // ── drawing ──
    const when2 = (sec, step) => {
      const d = new Date(sec * 1000);
      const hm = d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });
      return step >= 3600 || state.statsLong ? d.toLocaleDateString([], { month: "short", day: "numeric" }) + " " + hm : hm;
    };
    const niceMax = (v) => {
      if (v <= 5) return 5;
      const p = Math.pow(10, Math.floor(Math.log10(v)));
      for (const m of [1, 2, 2.5, 5, 10]) if (m * p >= v) return m * p;
      return 10 * p;
    };

    function drawTiles(d) {
      const t = d.sums.total;
      // a tile with a kind is a button: it limits the lists and the chart to that answer; Total Queries clears it
      const tile = (cls, label, value, sub, key, tip) => h(key === undefined ? "div" : "button", key === undefined ? { class: "qtile " + cls, title: tip || null } :
        { type: "button", class: "qtile pick " + cls, "aria-pressed": (key === "qps" ? qpsView : (KINDS[key] || "") === sel && !qpsView) ? "true" : "false",
          title: key === "qps" ? (tip || "") : key ? "Show only " + label + " in the chart and lists" : "Show everything",
          onclick: () => (key === "qps" ? toggleQps() : pick(KINDS[key] || "")) },
        (() => { const huge = typeof value !== "string" && value >= 1e12, text = typeof value === "string" ? value : huge ? new Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 1 }).format(value) : n0(value), qv = h("div", { class: "qv", title: huge ? n0(value) : null }, text); qv.style.setProperty("--len", String(Math.max(1, text.length - (text.match(/[,.\s]/g) || []).length / 2))); return qv; })(), // CSSOM, allowed by the CSP
        h("div", { class: "qs" }, sub || "\u00a0"), h("div", { class: "ql" }, label));
      clear(tiles).append(
        tile("q-total", "Total Queries", t, t ? "100%" : "", ""),
        // queries per second: now = the last finished bucket (the newest one is still filling), with the average and the peak over the range
        (() => { const b = d.total || [], st = d.step || 60, f = (v) => (v >= 100 ? n0(Math.round(v)) : v.toFixed(1)),
            rate = b.map((v) => v / st), cur = rate.length > 1 ? rate[rate.length - 2] : rate.length ? rate[0] : 0,
            avg = rate.length ? rate.reduce((a, v) => a + v, 0) / rate.length : 0, peak = rate.length ? Math.max(...rate) : 0;
          return tile("q-qps", "QPS", f(cur), rate.length ? "peak " + f(peak) : "", "qps", "Click to draw queries per second in the chart. Queries per second in the last finished interval. Over the chosen range: average " + f(avg) + ", peak " + f(peak)); })(),
        tile("q-ok", "No Error", d.sums.no_error, pct(d.sums.no_error, t), "no_error"),
        tile("q-fail", "Server Failure", d.sums.server_failure, pct(d.sums.server_failure, t), "server_failure"),
        tile("q-nx", "NX Domain", d.sums.nx_domain, pct(d.sums.nx_domain, t), "nx_domain"),
        tile("q-ref", "Refused", d.sums.refused, pct(d.sums.refused, t), "refused"),
        // dynamic DNS updates forwarded to a primary (they are part of Total too)
        tile("q-upd", "Updates", d.sums.updates, d.sums.updates ? (d.sums.updates_failed ? n0(d.sums.updates_failed) + " failed" : "none failed") : "", "updates"),
        tile("q-clients", "Clients", d.sums.clients, ""),
        // answers served from the cache, out of the lookups that could be cached (the cache's own hit rate, over the chosen range)
        (() => { const hit = d.sums.cache_hits || 0, look = hit + (d.sums.cache_misses || 0);
          return tile("q-cache", "Cache Hits", look ? pct(hit, look) : "–", look ? n0(hit) + " of " + n0(look) : "off or unused", undefined, "Answered from the cache, out of the lookups that could be cached in this range"); })());
    }

    function drawChart(d) {
      clear(chartBox);
      const only = Object.keys(KINDS).find((k) => KINDS[k] === sel); // the one line a tile picked
      if (qpsView) d = Object.assign({}, d, { qps: (d[only || "total"] || d.total).map((v) => v / (d.step || 60)) });   // the rate of what is picked (all queries when nothing is)
      const fq = (v) => (v >= 10 ? n0(Math.round(v)) : Number(v).toFixed(1));
      const qs = SERIES.find((s) => s.key === (only || "total"));
      const avail = qpsView ? [{ key: "qps", label: "QPS" + (only ? " — " + qs.label : ""), cls: qs.cls }] : SERIES.filter((s) => only ? s.key === only : (s.key !== "other" || d.sums.other > 0) && (s.key !== "updates" || d.sums.updates > 0));
      const shown = avail.filter((s) => only || !hidden.has(s.key));
      const legend = h("div", { class: "qlegend", role: "group", "aria-label": "Series" }, avail.map((s) =>
        h("button", { type: "button", class: "qchip " + s.cls, "aria-pressed": hidden.has(s.key) && !only ? "false" : "true", disabled: only || qpsView ? "" : null,
          onclick: () => { if (hidden.has(s.key)) hidden.delete(s.key); else hidden.add(s.key); drawChart(d); } },
          h("span", { class: "sw" }), s.label)));
      const n = d.total.length;
      const top = Math.max(qpsView ? 0.1 : 1, ...shown.flatMap((s) => d[s.key])), max = qpsView ? 4 * niceMax(top / 4) : niceMax(top);   // a rate gets four round steps
      const yLabels = [0, 1, 2, 3, 4].map((g) => (qpsView ? fq((max * g) / 4) : n0(Math.round((max * g) / 4))));
      const W = 1000, H = 300, L = axisMargin(yLabels, 52), R = 14, T = 12, B = 28, pw = W - L - R, ph = H - T - B;
      const x = (i) => L + (n <= 1 ? pw / 2 : (pw * i) / (n - 1));
      const y = (v) => T + ph - (ph * v) / max;
      const svg = sv("svg", { class: "qchart", viewBox: `0 0 ${W} ${H}`, role: "img", "aria-label": qpsView ? "Queries per second over time" : "Queries over time" });
      for (let g = 0; g <= 4; g++) {
        const v = (max * g) / 4;
        svg.append(sv("line", { class: "qgrid", x1: L, x2: W - R, y1: y(v), y2: y(v) }), sv("text", { class: "qax", x: L - 6, y: y(v) + 4, "text-anchor": "end" }, yLabels[g]));
      }
      const ticks = Math.min(8, n);
      for (let k = 0; k < ticks; k++) {
        const i = ticks === 1 ? 0 : Math.round((k * (n - 1)) / (ticks - 1));
        svg.append(sv("text", { class: "qax", x: x(i), y: H - 8, "text-anchor": k === 0 ? "start" : k === ticks - 1 ? "end" : "middle" }, when2(d.start + i * d.step, d.step)));
      }
      for (const s of shown) {
        const pts = d[s.key].map((v, i) => x(i).toFixed(1) + "," + y(v).toFixed(1)).join(" ");
        svg.append(sv("polyline", { class: "qline " + s.cls, points: pts }));
      }
      // hover: a crosshair, a dot per series and one tooltip with the values
      const cross = sv("line", { class: "qcross hidden", y1: T, y2: T + ph });
      const dots = shown.map((s) => sv("circle", { class: "qdot " + s.cls, r: 4, cx: 0, cy: 0 }));
      const tip = sv("g", { class: "qtip hidden" });
      const tipBg = sv("rect", { class: "qtipbg", rx: 6, width: 210, height: 20 + 18 * (shown.length + 1) });
      const tipHead = sv("text", { class: "qtiph", x: 10, y: 17 });
      const tipRows = shown.map((s, k) => sv("g", { transform: `translate(10 ${36 + 18 * k})` }, sv("rect", { class: s.cls + " sw", width: 10, height: 10, y: -9, rx: 2 }), sv("text", { class: "qtipt", x: 16, y: 0 }, "")));
      tip.append(tipBg, tipHead, ...tipRows);
      const hit = sv("rect", { class: "qhit", x: L, y: T, width: pw, height: ph,
        onmousemove: (e) => {
          const r = svg.getBoundingClientRect();
          const px = ((e.clientX - r.left) / r.width) * W;
          const i = Math.max(0, Math.min(n - 1, Math.round(((px - L) / pw) * (n - 1))));
          cross.setAttribute("x1", x(i)); cross.setAttribute("x2", x(i)); cross.classList.remove("hidden");
          shown.forEach((s, k) => { dots[k].setAttribute("cx", x(i)); dots[k].setAttribute("cy", y(d[s.key][i])); });
          tipHead.textContent = when((d.start + i * d.step) * 1000);
          shown.forEach((s, k) => { tipRows[k].lastChild.textContent = s.label + ": " + (qpsView ? fq(d[s.key][i]) : n0(d[s.key][i])); });
          const tx = x(i) > W / 2 ? x(i) - 220 : x(i) + 12;
          tip.setAttribute("transform", `translate(${tx} ${T + 6})`); tip.classList.remove("hidden");
        },
        onmouseleave: () => { cross.classList.add("hidden"); tip.classList.add("hidden"); } });
      svg.append(cross, ...dots, hit, tip);
      chartBox.append(legend, svg);
    }

    function donut(title, items, clsOf, keep) {
      const total = items.reduce((a, e) => a + e.count, 0);
      const card = h("div", { class: "qdonut" }, h("h3", {}, title));
      if (!total) return card.appendChild(h("div", { class: "empty" }, "No queries in this range.")), card;
      // fold everything beyond the named, coloured ones into "Other"
      const rows = [];
      let other = 0;
      for (const e of items) { if (clsOf[e.name] && rows.length < keep) rows.push(e); else other += e.count; }
      if (other) rows.push({ name: "Other", count: other, cls: "t-other" });
      const S = 180, c = S / 2, ro = 84, ri = 52;
      const svg = sv("svg", { class: "qpie", viewBox: `0 0 ${S} ${S}`, role: "img", "aria-label": title });
      let a0 = -Math.PI / 2;
      for (const e of rows) {
        const frac = e.count / total, a1 = a0 + frac * 2 * Math.PI;
        const cls = e.cls || clsOf[e.name];
        const tt = sv("title", {}, e.name + ": " + n0(e.count) + " (" + pct(e.count, total) + ")");
        if (frac > 0.9999) {
          svg.append(sv("circle", { class: "qseg ring " + cls, cx: c, cy: c, r: (ro + ri) / 2, "stroke-width": ro - ri }, tt));
        } else {
          const p = (r, a) => (c + r * Math.cos(a)).toFixed(2) + " " + (c + r * Math.sin(a)).toFixed(2);
          const big = a1 - a0 > Math.PI ? 1 : 0;
          svg.append(sv("path", { class: "qseg " + cls, "stroke-width": 2, d: `M ${p(ro, a0)} A ${ro} ${ro} 0 ${big} 1 ${p(ro, a1)} L ${p(ri, a1)} A ${ri} ${ri} 0 ${big} 0 ${p(ri, a0)} Z` }, tt));
        }
        a0 = a1;
      }
      svg.append(sv("text", { class: "qpiet", x: c, y: c + 5, "text-anchor": "middle" }, n0(total)));
      card.append(h("div", { class: "qrow" }, svg, h("ul", { class: "qkey" }, rows.map((e) => h("li", {}, h("span", { class: "sw " + (e.cls || clsOf[e.name]) }), h("span", { class: "kn" }, e.name), h("span", { class: "kc" }, n0(e.count) + " · " + pct(e.count, total)))))));
      return card;
    }

    // Tooltips: a client shows every reverse-DNS name the daemon has for it; a domain asks the
    // daemon for its whois data the first time it is hovered (the daemon caches and asks the
    // registry from the picked node), then keeps the answer here too.
    const whoisSeen = new Map();
    const whoisText = (w) => {
      if (w.kind === "ip") {
        if (!w.found) return w.domain + " — " + w.note;
        return [w.domain, w.network && "Network: " + w.network, w.netname && "Name: " + w.netname, w.org && "Organization: " + w.org, w.country && "Country: " + w.country,
          w.origin && "Origin AS: " + w.origin, "From: " + w.server].filter(Boolean).join("\n");
      }
      const addrs = w.name ? [w.name, w.ipv4 && "IPv4: " + w.ipv4, w.ipv6 && "IPv6: " + w.ipv6, !w.ipv4 && !w.ipv6 && "No address found", ""].filter((x) => x !== false && x !== undefined && x !== null && x !== 0).join("\n") + "\n" : "";
      if (!w.found) return addrs + w.domain + " — " + w.note;
      return addrs + [w.domain, w.registrar && "Registrar: " + w.registrar, w.registrant && "Registrant: " + w.registrant, w.registered && "Registered: " + w.registered,
        w.updated && "Updated: " + w.updated, w.expires && "Expires: " + w.expires, (w.name_servers || []).length && "Name servers: " + w.name_servers.join(", "), "From: " + w.server].filter(Boolean).join("\n");
    };
    // what a redraw gives the button before the next hover: the answer already looked up, if there is one, so a refresh does not wipe it
    function cachedWhois(name, head, tail) {
      const hit = whoisSeen.get((state.target || "") + "|" + name);
      return hit && hit.text && Date.now() - hit.at < (hit.ok ? 600000 : 60000) ? (head ? head + "\n\n" : "") + hit.text + tail : "";
    }
    function whoisFor(btn, name, tail, head) {
      head = head ? head + "\n\n" : "";
      const key = (state.target || "") + "|" + name;
      const hit = whoisSeen.get(key);
      if (hit && Date.now() - hit.at < (hit.ok ? 600000 : 60000)) { btn.title = head + hit.text + tail; return; }
      if (hit && hit.busy) return;
      btn.title = head + "Looking up whois…" + tail;
      const mine = { busy: true, at: Date.now() };
      whoisSeen.set(key, mine);
      if (whoisSeen.size > 500) whoisSeen.delete(whoisSeen.keys().next().value);
      api("GET", "/api/whois?domain=" + encodeURIComponent(name)).then((r) => {
        whoisSeen.set(key, { text: whoisText(r.data), ok: true, at: Date.now() });
      }, (e) => {
        whoisSeen.set(key, { text: "whois: " + (e.message || "failed"), ok: false, at: Date.now() });
      }).then(() => { if (btn.getAttribute("data-name") === name) btn.title = head + whoisSeen.get(key).text + tail; });   // the list is redrawn in place: this button may show another name by now
    }

    // A row is a button: picking a client lists what it asked for in Top domains, picking a
    // domain lists who asked for it in Top clients.  o.picked is this list's picked row,
    // o.note says the list is filtered, o.onClear drops that filter.
    // Clear: forget the counts and the top lists (of every node when the Node menu says Cluster)
    async function clearStats() {
      const all = state.target === CLUSTER;
      if (!confirm("Clear the statistics" + (all ? " of every node in the cluster" : " of this node") + "?\n\nThe counts, the chart and the top lists start again from nothing. This cannot be undone.")) return;
      try {
        const r = (await api("POST", "/api/qstats/clear", {})).data || {};
        const bad = (r.nodes || []).filter((n) => !n.ok);
        await load();
        if (bad.length) say(status, "warn", "Cleared, except on: " + bad.map((n) => n.name + " (" + n.error + ")").join(", "));
        else say(status, "info", "The statistics are cleared.");
      } catch (e) { if (e.message !== "unauthenticated") say(status, "bad", "Not cleared: " + e.message); }
    }

    // Scan: an nmap of a client, run on the picked node; the report goes into the client's tooltip
    const scanSeen = new Map();   // address → { state, text, at }
    const scanKey = (a) => (state.target || "") + "|" + a;
    function scanText(addr) {
      const s = scanSeen.get(scanKey(addr));
      if (!s) return "";
      if (s.state === "running") return "nmap: scanning…";
      const when = new Date((s.end || s.at) * 1000).toLocaleString();
      return "nmap scan, " + when + (s.state === "error" ? " (failed)" : "") + ":\n" + (s.text.length > 2500 ? s.text.slice(0, 2500) + "\n…" : s.text);
    }
    async function scanClient(addr) {
      try {
        let j = (await api("POST", "/api/scan", { client: addr })).data;
        scanSeen.set(scanKey(addr), j);
        if (last) draw(last);   // (a redraw clears the message line, so the message comes after it)
        say(status, "info", "Scanning " + addr + " with nmap… (up to two minutes; hover the client afterwards to read the result)");
        while (j.state === "running") {
          await new Promise((r) => setTimeout(r, 3000));
          if (!bar.isConnected) return;   // the page was left
          j = (await api("GET", "/api/scan?client=" + encodeURIComponent(addr))).data;
        }
        scanSeen.set(scanKey(addr), j);
        if (last) draw(last);
        say(status, j.state === "done" ? "info" : "warn", "The scan of " + addr + (j.state === "done" ? " is finished: hover the client to read it." : " failed: hover the client to read why."));
      } catch (e) { scanSeen.delete(scanKey(addr)); if (last) draw(last); if (e.message !== "unauthenticated") say(status, "bad", "Scan of " + addr + ": " + e.message); }
    }

    // adds a row "any client / name / keyword" at the top of the shared Policy-Based Resolution table and saves it
    async function addPolicyRow(name, keyword, client, dest) {
      client = client || "*";
      try {
        const cfg = (await api("GET", "/api/config")).config;
        cfg.dns = cfg.dns || {};
        const list = cfg.dns.policy || [];
        const same = list.findIndex((r) => (r.client || "*") === client && String(r.name || "*").toLowerCase() === name.toLowerCase());
        if (same >= 0) { say(status, "warn", "There already is a policy row for " + (client === "*" ? name : client + " asking for " + name) + " (row " + (same + 1) + ", " + ((list[same].servers || []).join(", ") || "no servers") + "). Edit it on Configure ▸ DNS proxy ▸ Resolution."); return; }
        list.unshift(dest ? { client, name, servers: [], dest } : { client, name, servers: [keyword] });
        cfg.dns.policy = list;
        await api("PUT", "/api/config", { config: cfg, note: "Policy row from Statistics: " + (dest ? "rewrite to " + dest : keyword) + " for " + (client === "*" ? name : client + " / " + name) });
        await load();
        say(status, cfg.dns.policy_on === false ? "warn" : "info", "Added policy row 1: " + (client === "*" ? name : client + " asking for " + name) + " → " + (dest || keyword) + "." + (cfg.dns.policy_on === false ? " Policy-Based Resolution is switched off, so it does nothing until you switch it on (Configure ▸ DNS proxy ▸ Resolution)." : ""));
      } catch (e) { if (e.message !== "unauthenticated") say(status, "bad", "Not added: " + e.message); }
    }

    // the row Block writes: this client, any name, answered NODATA
    let blocked = new Set();   // clients with a Block row, for the icon after their name
    let blockedNames = new Map();   // domains answered by a keyword (any client): lower-case name -> keyword
    const KEYWORD_ROWS = ["null", "nxdomain", "nodata", "refused"];
    let rewrittenNames = new Map();   // domains answered with a Destination name (any client): lower-case name -> destination
    const isRewriteRow = (r, name) => (r.client || "*") === "*" && String(r.name || "").toLowerCase() === name.toLowerCase() && !!String(r.dest || "").trim() && String(r.dest).trim() !== "*" &&
      ((r.servers || []).length === 0 || ((r.servers || []).length === 1 && String(r.servers[0]).toLowerCase() === "pool"));
    const rewrittenNamesOf = (cfg) => {
      const m = new Map();
      for (const r of ((cfg.dns || {}).policy || [])) {
        const n = String(r.name || "").toLowerCase();
        if (n && n !== "*" && !m.has(n) && isRewriteRow(r, n)) m.set(n, String(r.dest).trim());
      }
      return m;
    };
    const blockedNamesOf = (cfg) => {
      const m = new Map();
      for (const r of ((cfg.dns || {}).policy || [])) {
        const s = r.servers || [], k = s.length === 1 ? String(s[0]).toLowerCase() : "";
        if ((r.client || "*") === "*" && r.name && r.name !== "*" && KEYWORD_ROWS.includes(k) && !m.has(String(r.name).toLowerCase())) m.set(String(r.name).toLowerCase(), k);
      }
      return m;
    };
    const blockedOf = (cfg) => new Set(((cfg.dns || {}).policy || []).filter((r) => isBlockRow(r, r.client || "*") && (r.client || "*") !== "*").map((r) => r.client));
    const isBlockRow = (r, client) => (r.client || "*") === client && String(r.name || "*") === "*" && (r.servers || []).length === 1 && String(r.servers[0]).toLowerCase() === "nodata";
    async function unblockClient(client) {
      try {
        const cfg = (await api("GET", "/api/config")).config;
        const list = (cfg.dns && cfg.dns.policy) || [];
        const keep = list.filter((r) => !isBlockRow(r, client));
        if (keep.length === list.length) { say(status, "warn", client + " is not blocked any more."); return; }
        cfg.dns.policy = keep;
        await api("PUT", "/api/config", { config: cfg, note: "Policy row removed from Statistics: unblock " + client });
        await load();
        say(status, "info", "Unblocked " + client + ": its policy row was removed.");
      } catch (e) { if (e.message !== "unauthenticated") say(status, "bad", "Not unblocked: " + e.message); }
    }
    // a domain: the rows that answer exactly this name with a keyword for every client
    const isNameRow = (r, name) => (r.client || "*") === "*" && String(r.name || "").toLowerCase() === name.toLowerCase() && (r.servers || []).length === 1 && KEYWORD_ROWS.includes(String(r.servers[0]).toLowerCase());
    async function unblockName(name, rewrite) {
      try {
        const cfg = (await api("GET", "/api/config")).config;
        const list = (cfg.dns && cfg.dns.policy) || [];
        const keep = list.filter((r) => !(rewrite ? isRewriteRow(r, name) : isNameRow(r, name)));
        if (keep.length === list.length) { say(status, "warn", name + " has no such policy row any more."); return; }
        cfg.dns.policy = keep;
        await api("PUT", "/api/config", { config: cfg, note: "Policy row removed from Statistics: " + (rewrite ? "remove rewrite of " : "unblock ") + name });
        await load();
        say(status, "info", (rewrite ? "Rewrite of " + name + " removed" : "Unblocked " + name) + ": its policy row was removed.");
      } catch (e) { if (e.message !== "unauthenticated") say(status, "bad", "Not unblocked: " + e.message); }
    }
    // Rewrite…: a small dialog for the Destination name of the row (an address, records, or another name to ask for)
    function askRewrite(name) {
      const input = h("input", { type: "text", autocomplete: "off", spellcheck: "false", placeholder: "10.5.5.5   or   other.example.com", "aria-label": "Answer with" });
      const err = h("div", { "aria-live": "polite" });
      const d = h("dialog", { class: "cv-dialog", "aria-label": "Rewrite " + name });
      const close = () => { try { d.close(); } catch (_) { /* closed */ } d.remove(); };
      d.addEventListener("close", () => d.remove());
      d.append(h("h2", {}, "Rewrite " + name),
        h("form", { onsubmit: (ev) => {
          ev.preventDefault();
          const v = input.value.trim();
          if (!v) { errorBox(err, "Write an address, or the name to ask for instead."); return; }
          if (v.length > 253 || /[\s]{2,}/.test(v)) { errorBox(err, "That is too long or not valid."); return; }
          close(); addPolicyRow(name, "", "*", v);
        } },
        h("label", { class: "f wide" }, "Answer with", input,
          h("span", { class: "hint" }, "An address (10.5.5.5, or 10.5.5.5, 2001:db8::5) makes the name that address. A name (other.example.com) asks the servers for that name instead and answers as if it were " + name + ". Records such as A 10.5.5.5; TTL 300 or CNAME host.example.com work too.")),
        err,
        h("div", { class: "toolbar" }, h("button", { class: "btn primary", type: "submit" }, "Rewrite"), h("button", { class: "btn", type: "button", onclick: close }, "Cancel"))));
      root.append(d);
      d.showModal();
      input.focus();
    }
    async function domainMenu(e, name) {
      e.preventDefault(); e.stopPropagation();
      let has = false, rewritten = false;
      try {
        const list = (((await api("GET", "/api/config")).config.dns || {}).policy || []);
        has = list.some((r) => isNameRow(r, name)); rewritten = list.some((r) => isRewriteRow(r, name));
      } catch (_) { /* offer Block and Rewrite */ }
      rowMenu(rewritten ? [["Remove rewrite", () => unblockName(name, true)]]
        : [has ? ["Unblock", () => unblockName(name)] : ["Block", () => addPolicyRow(name, "nodata")], ["Rewrite…", () => askRewrite(name)]])(e);
    }
    async function clientMenu(e, addr) {
      e.preventDefault(); e.stopPropagation();
      let blocked = false;
      try { const list = ((await api("GET", "/api/config")).config.dns || {}).policy || []; blocked = list.some((r) => isBlockRow(r, addr)); } catch (_) { /* offer Block */ }
      rowMenu([["Scan", () => scanClient(addr)],
        blocked ? ["Unblock", () => unblockClient(addr)] : ["Block", () => addPolicyRow("*", "nodata", addr)]])(e);
    }

    function topTable(title, items, total, more, onMore, withHost, o) {
      const rows = (more ? items : items.slice(0, 10));
      const mine = withHost ? "Client" : "Domain";
      return h("div", { class: "card" }, h("header", { class: "bar" }, h("h2", {}, title), h("span", { class: "grow" }),
        items.length > 10 ? h("button", { class: "btn", type: "button", onclick: onMore }, more ? "Fewer" : "More") : null),
        h("div", { class: "hint qnote" }, o.note ? [o.note, " ", h("button", { class: "qlink", type: "button", onclick: o.onClear }, "Show all")] : "\u00a0"),
        h("div", { class: "body flush" }, rows.length
          ? h("div", { class: "scroll" }, h("table", { class: "qtop" }, h("thead", {}, h("tr", {}, h("th", {}, mine), h("th", { class: "num" }, "Queries"), h("th", { class: "num" }, "Share"))),
            h("tbody", {}, rows.map((e) => h("tr", { class: e.name === o.picked ? "picked" : null,
              // right-click a domain: add a Policy-Based Resolution row for it
              oncontextmenu: e.name === "(others)" ? null : (ev) => (withHost ? clientMenu(ev, e.name) : domainMenu(ev, e.name)) },
              h("td", {}, e.name === "(others)" ? h("div", { class: "muted" }, e.name)
                : (() => {
                  const tail = withHost ? "\n\nClick: show what this client asked for" : "\n\nClick: show who asked for this domain";
                  const head = withHost ? (e.hosts && e.hosts.length ? e.hosts.join("\n") : "No reverse DNS name known (yet)") + (scanText(e.name) ? "\n\n" + scanText(e.name) : "") : "";
                  // the handlers are properties of the new element, so that morphing the list into the old one gives the reused button
                  // this row's name (a listener added with addEventListener would stay on it and look up the name it had before)
                  const go = (ev) => whoisFor(ev.currentTarget, e.name, tail, head);
                  const b = h("button", { type: "button", class: "qlink mono", "data-name": e.name, "aria-pressed": e.name === o.picked ? "true" : "false",
                    title: cachedWhois(e.name, head, tail) || (withHost ? (head + "\n" + e.name + tail) : "Hover for whois" + tail),
                    onclick: () => o.onPick(e.name === o.picked ? "" : e.name), onmouseenter: go, onfocus: go }, e.name);
                  const kw = withHost ? null : blockedNames.get(e.name.toLowerCase());
                  const rw = withHost || kw ? null : rewrittenNames.get(e.name.toLowerCase());
                  return withHost && blocked.has(e.name)
                    ? h("div", { class: "qname" }, b, h("span", { class: "qblocked", role: "img", "aria-label": "blocked",
                      title: "Blocked: this client gets no answers (a Policy-Based Resolution row). Right-click ▸ Unblock to remove it." }, "🚫"))
                    : kw ? h("div", { class: "qname" }, b, h("span", { class: "qblocked", role: "img", "aria-label": "in the Policy-Based Resolution table",
                      title: "Answered by the Policy-Based Resolution table: " + kw.toUpperCase() + " for every client. Edit it on Configure ▸ DNS proxy ▸ Resolution." }, "🚫"))
                    : rw ? h("div", { class: "qname" }, b, h("span", { class: "qblocked", role: "img", "aria-label": "rewritten",
                      title: "Rewritten by the Policy-Based Resolution table: answered with " + rw + " for every client. Right-click ▸ Remove rewrite to undo it." }, "✏️"))
                    : b;
                })(),
                e.host ? h("div", { class: "muted small" }, e.host) : null),
              h("td", { class: "num" }, n0(e.count)), h("td", { class: "num" }, pct(e.count, total)))))))
          : h("div", { class: "empty" }, "No queries in this range.")));
    }

    const shareBase = (d) => { const k = Object.keys(KINDS).find((x) => KINDS[x] === sel); return k ? d.sums[k] : d.sums.total; };
    const kindLabel = () => { const s = SERIES.find((x) => KINDS[x.key] === sel); return s ? " — " + s.label : ""; };
    // picking a client clears the domain and the other way round: one drill-down at a time
    function pickC(c) { if (c === pickClient) return; pickClient = c; pickDomain = ""; load(); }
    function pickD(n) { if (n === pickDomain) return; pickDomain = n; pickClient = ""; load(); }
    function pick(kind) {
      const was = qpsView;
      qpsView = false;   // any other tile goes back to counting queries
      if (kind === sel) { if (was && last) draw(last); return; }
      sel = kind;
      load();
    }
    function toggleQps() { qpsView = !qpsView; if (last) draw(last); }

    function draw(d) {
      last = d;
      state.statsLong = d.step >= 600 && d.to - d.from > 20 * 3600;
      clear(status);
      drawTiles(d);
      drawChart(d);
      clear(pies).append(donut("Query types" + kindLabel(), d.types, TYPE_CLS, 8), donut("Transport" + kindLabel(), d.protos, PROTO_CLS, 2));
      const sumOf = (l) => l.reduce((a, e) => a + e.count, 0);
      fill(tops, 
        topTable("Top clients" + kindLabel(), d.clients, pickDomain ? sumOf(d.clients) : shareBase(d), clientsMore, () => { clientsMore = !clientsMore; draw(last); }, true,
          { picked: pickClient, onPick: pickC, note: pickDomain ? "Clients that asked for " + pickDomain : "", onClear: () => pickD("") }),
        topTable("Top domains" + kindLabel(), d.domains, pickClient ? sumOf(d.domains) : shareBase(d), domainsMore, () => { domainsMore = !domainsMore; draw(last); }, false,
          { picked: pickDomain, onPick: pickD, note: pickClient ? "Domains that " + pickClient + " asked for" : "", onClear: () => pickC("") }));
      clear(cnote).append(...clusterNote(d.cluster));   // which nodes are added together, when the Node menu says Cluster
      const gn = guardNote(d.guard).slice(1);   // only the memory-guard warning, when there is one
      clear(foot).append(...gn); foot.hidden = !gn.length;
    }

    // the recent dynamic DNS updates (always the latest ones, whatever the range)
    function drawUpdates(list) {
      const at = (u) => { const d = new Date(u.at * 1000); return [h("div", { class: "small" }, d.toLocaleTimeString()), h("div", { class: "muted small" }, d.toLocaleDateString())]; };
      fill(upds, h("div", { class: "card" }, h("header", { class: "bar" }, h("h2", {}, "Recent dynamic updates"), h("span", { class: "grow" }),
        h("span", { class: "muted small" }, list.length ? "the last " + list.length + " of this node, newest first" : "")),
        h("div", { class: "body flush" }, list.length
          ? h("div", { class: "scroll" }, h("table", {}, h("thead", {}, h("tr", {}, ["When", "Client", "Zone", "Changes", "Primary", "Result"].map((c) => h("th", {}, c)))),
            h("tbody", {}, list.map((u) => h("tr", {},
              h("td", {}, at(u)),
              h("td", { class: "mono small" }, u.client),
              h("td", { class: "mono small" }, u.zone),
              h("td", {}, u.changes.map((c) => h("div", { class: "mono small" }, c)), u.more ? h("div", { class: "muted small" }, "… and " + n0(u.more) + " more") : null),
              h("td", { class: "wrap" }, u.addr ? [h("div", { class: "small" }, u.primary), h("div", { class: "muted small mono" }, u.addr)] : h("div", { class: "muted small" }, u.note || "not delivered")),
              h("td", {}, h("span", { class: "pill " + (u.ok ? "ok" : "bad") }, u.result)))))))
          : h("div", { class: "empty" }, "No dynamic DNS updates yet."))));
    }

    async function load() {
      const my = ++seq;
      try {
        const wantUpd = sel === "update"; // the card of recent updates belongs to the Updates tile
        const [d, u] = await Promise.all([fetchData(), wantUpd ? api("GET", "/api/dnsupdates").then((r) => r.data.updates, () => null) : null,
          api("GET", "/api/config").then((r) => { blocked = blockedOf(r.config); blockedNames = blockedNamesOf(r.config); rewrittenNames = rewrittenNamesOf(r.config); }, () => null)]);
        if (my !== seq) return;
        lastAt = Date.now();
        draw(d);
        if (wantUpd && u) drawUpdates(u); else clear(upds);
      } catch (e) { fail(status)(e); }
    }

    function setRange(r) {
      range = r;
      for (const [id, b] of rangeBtns) b.setAttribute("aria-pressed", id === r ? "true" : "false");
      customRow.classList.toggle("hidden", r !== "custom");
      if (r === "custom" && !fromIn.value) {
        const f = (d) => new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
        toIn.value = f(new Date()); fromIn.value = f(new Date(Date.now() - 6 * 3600 * 1000));
      }
      if (r !== "custom") load();
    }

    return {
      mount(main) {
        status = h("div", { "aria-live": "polite" });
        rangeBtns = new Map(RANGES.map(([id, label]) => [id, h("button", { type: "button", class: "seg", "aria-pressed": id === range ? "true" : "false", onclick: () => setRange(id) }, label)]));
        fromIn = h("input", { type: "datetime-local", "aria-label": "From" });
        toIn = h("input", { type: "datetime-local", "aria-label": "To" });
        customRow = h("span", { class: "qcustom hidden" }, fromIn, " to ", toIn,
          h("button", { class: "btn", type: "button", onclick: load }, "Apply"), h("span", { class: "muted small" }, " only the last 30 days exist"));
        bar = h("div", { class: "toolbar qbar" }, h("span", { class: "segs", role: "group", "aria-label": "Time range" }, [...rangeBtns.values()]),
          h("button", { class: "btn", type: "button", title: "Forget the counts and top lists", onclick: clearStats }, "Clear"), customRow);
        tiles = h("div", { class: "qtiles" });
        chartBox = h("div", { class: "card qchartcard" });
        pies = h("div", { class: "qpies" });
        tops = h("div", { class: "grid2" });
        upds = h("div", { class: "qupd" });
        foot = h("p", { class: "hint" });
        cnote = h("div", {});
        main.append(status, bar, cnote, tiles, h("div", { class: "card" }, h("div", { class: "body" }, chartBox)), pies, tops, upds, foot);
        setRange(range);
      },
      async poll() {
        if (range === "custom") return;
        if (Date.now() - lastAt >= REFRESH_MS) await load();
      },
    };
  })();

  // ── Host, Monitor  (CLI: --host) ─────────────────────────────────────────────
  // CPU, memory, disk and network use of the machine, sampled every 10 s by the daemon and
  // kept in memory for 30 days.  The picked node (top-bar Node menu) is the one shown.
  VIEWS.host = (() => {
    const SVGNS = "http://www.w3.org/2000/svg";
    const sv = (tag, attrs, ...kids) => {
      const el = document.createElementNS(SVGNS, tag);
      for (const [k, v] of Object.entries(attrs || {})) {
        if (v == null || v === false) continue;
        if (k.startsWith("on")) el.addEventListener(k.slice(2), v); else el.setAttribute(k, v);
      }
      for (const kid of kids.flat(Infinity)) if (kid != null) el.append(kid.nodeType ? kid : document.createTextNode(String(kid)));
      return el;
    };
    const RANGES = [["1h", "Last Hour"], ["1d", "Last Day"], ["7d", "Last Week"], ["30d", "Last Month"], ["custom", "Custom"]];
    const REFRESH_MS = 10000;
    const pc = (v) => (v == null || v < 0 ? "–" : v.toFixed(v < 10 ? 1 : 0) + "%");
    const bits = (v) => (v == null || v < 0 ? "–" : v >= 1e9 ? (v / 1e9).toFixed(2) + " Gb/s" : v >= 1e6 ? (v / 1e6).toFixed(v >= 1e8 ? 0 : 2) + " Mb/s" : v >= 1e3 ? (v / 1e3).toFixed(v >= 1e5 ? 0 : 1) + " kb/s" : Math.round(v) + " b/s");
    const bytes = (v) => { let f = Number(v) || 0; for (const u of ["B", "KiB", "MiB", "GiB", "TiB"]) { if (f < 1024 || u === "TiB") return (u === "B" ? f : f.toFixed(1)) + " " + u; f /= 1024; } return ""; };
    const niceMax = (v, floor) => {
      if (v <= floor) return floor;
      const p = Math.pow(10, Math.floor(Math.log10(v)));
      for (const m of [1, 2, 2.5, 5, 10]) if (m * p >= v) return m * p;
      return 10 * p;
    };
    const stamp = (sec, step, long) => {
      const d = new Date(sec * 1000);
      const hm = d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });
      return step >= 3600 || long ? d.toLocaleDateString([], { month: "short", day: "numeric" }) + " " + hm : hm;
    };

    let status, bar, cnote, tiles, charts, tables, foot, rangeBtns, fromIn, toIn, customRow;
    let range = "1h", lastAt = 0, seq = 0;

    // One line chart: series = [{ label, cls, values, text(i) }]; values below 0 are gaps.
    function lineChart(title, d, series, o) {
      const n = d.cpu.length;
      const peak = Math.max(0, ...series.flatMap((s) => s.values));
      const max = o.fixedMax || niceMax(peak, o.floor || 10);
      const W = 620, H = 230, L = axisMargin([0, 1, 2, 3, 4].map((g) => o.axis((max * g) / 4)), 62), R = 12, T = 12, B = 28, pw = W - L - R, ph = H - T - B;
      const x = (i) => L + (n <= 1 ? pw / 2 : (pw * i) / (n - 1));
      const y = (v) => T + ph - (ph * Math.min(v, max)) / max;
      const svg = sv("svg", { class: "qchart", viewBox: `0 0 ${W} ${H}`, role: "img", "aria-label": title + " over time" });
      for (let g = 0; g <= 4; g++) {
        const v = (max * g) / 4;
        svg.append(sv("line", { class: "qgrid", x1: L, x2: W - R, y1: y(v), y2: y(v) }), sv("text", { class: "qax", x: L - 6, y: y(v) + 4, "text-anchor": "end" }, o.axis(v)));
      }
      const ticks = Math.min(8, n);
      const long = d.step >= 600 && d.to - d.from > 20 * 3600;
      for (let k = 0; k < ticks; k++) {
        const i = ticks === 1 ? 0 : Math.round((k * (n - 1)) / (ticks - 1));
        svg.append(sv("text", { class: "qax", x: x(i), y: H - 8, "text-anchor": k === 0 ? "start" : k === ticks - 1 ? "end" : "middle" }, stamp(d.start + i * d.step, d.step, long)));
      }
      for (const s of series) {
        let path = "", pen = false;
        s.values.forEach((v, i) => { if (v < 0) { pen = false; return; } path += (pen ? "L" : "M") + x(i).toFixed(1) + "," + y(v).toFixed(1); pen = true; });
        if (path) svg.append(sv("path", { class: "qline " + s.cls, d: path }));
      }
      const cross = sv("line", { class: "qcross hidden", y1: T, y2: T + ph });
      const dots = series.map((s) => sv("circle", { class: "qdot hidden " + s.cls, r: 4, cx: 0, cy: 0 }));
      const tip = sv("g", { class: "qtip hidden" });
      const tipBg = sv("rect", { class: "qtipbg", rx: 6, width: 250, height: 20 + 18 * (series.length + 1) });
      const tipHead = sv("text", { class: "qtiph", x: 10, y: 17 });
      const tipRows = series.map((s, k) => sv("g", { transform: `translate(10 ${36 + 18 * k})` }, sv("rect", { class: s.cls + " sw", width: 10, height: 10, y: -9, rx: 2 }), sv("text", { class: "qtipt", x: 16, y: 0 }, "")));
      tip.append(tipBg, tipHead, ...tipRows);
      const hit = sv("rect", { class: "qhit", x: L, y: T, width: pw, height: ph,
        onmousemove: (e) => {
          const r = svg.getBoundingClientRect();
          const px = ((e.clientX - r.left) / r.width) * W;
          const i = Math.max(0, Math.min(n - 1, Math.round(((px - L) / pw) * (n - 1))));
          cross.setAttribute("x1", x(i)); cross.setAttribute("x2", x(i)); cross.classList.remove("hidden");
          series.forEach((s, k) => { const v = s.values[i]; dots[k].classList.toggle("hidden", v < 0); dots[k].setAttribute("cx", x(i)); dots[k].setAttribute("cy", v < 0 ? 0 : y(v)); });
          tipHead.textContent = new Date((d.start + i * d.step) * 1000).toLocaleString();
          series.forEach((s, k) => { tipRows[k].lastChild.textContent = s.label + ": " + (s.values[i] < 0 ? "no data" : s.text(i)); });
          const tx = x(i) > W / 2 ? x(i) - 260 : x(i) + 12;
          tip.setAttribute("transform", `translate(${tx} ${T + 6})`); tip.classList.remove("hidden");
        },
        onmouseleave: () => { cross.classList.add("hidden"); tip.classList.add("hidden"); } });
      svg.append(cross, ...dots, hit, tip);
      const legend = series.length > 1 || o.legend ? h("div", { class: "qlegend" }, series.map((s) => h("span", { class: "qchip " + s.cls }, h("span", { class: "sw" }), s.label))) : null;
      return h("div", { class: "card" }, h("header", { class: "bar" }, h("h2", {}, title)), h("div", { class: "body" }, legend, svg));
    }

    function draw(d) {
      const n = d.now;
      clear(status);
      if (!n.at) { clear(tiles); clear(charts).append(h("div", { class: "empty" }, "No sample yet: the daemon has only just started.")); clear(tables); return; }
      const tile = (cls, label, value, sub) => h("div", { class: "qtile " + cls }, h("div", { class: "qv" }, value), h("div", { class: "qs" }, sub || "\u00a0"), h("div", { class: "ql" }, label));
      // the fullest real disk; tiny system mounts (under 1 GiB) only count when nothing bigger exists
      const big = (n.fs || []).filter((f) => f.total >= 1 << 30);
      const topFS = (big.length ? big : n.fs || []).reduce((a, f) => (!a || f.pct > a.pct ? f : a), null);
      clear(tiles).append(
        tile("t1", "CPU", pc(n.cpu_pct), n.cores + (n.cores === 1 ? " core" : " cores") + " · load " + n.load[0].toFixed(2)),
        tile("t3", "Memory", pc(n.mem_pct), bytes(n.mem_used) + " of " + bytes(n.mem_total)),
        tile("t4", "Disk space", topFS ? pc(topFS.pct) : "–", topFS ? topFS.mount + " · " + bytes(topFS.used) + " of " + bytes(topFS.total) : ""),
        tile("t2", "Disk I/O", pc(n.io_pct), "busiest disk, time busy"),
        tile("t5", "Network in", bits(n.rx_bps), n.net_util_pct >= 0 ? "busiest link " + pc(n.net_util_pct) : "link speed unknown"),
        tile("t6", "Network out", bits(n.tx_bps), ""));
      const cpuT = (i) => pc(d.cpu[i]) + " (peak " + pc(d.cpu_max[i]) + ")";
      const fsCls = ["t4", "t5", "t6", "t7", "t8", "t-other"];
      const ioT = (i) => pc(d.io[i]) + " (peak " + pc(d.io_max[i]) + ")";
      const diskSeries = [{ label: "I/O busy", cls: "t2", values: d.io, text: ioT }].concat((d.fs || []).map((f, k) => ({ label: f.mount + " used", cls: fsCls[k % fsCls.length], values: f.pct, text: (i) => pc(f.pct[i]) })));
      clear(charts).append(
        lineChart("CPU utilization", d, [{ label: "CPU", cls: "t1", values: d.cpu, text: cpuT }], { floor: 10, axis: (v) => (Number.isInteger(v) ? v : v.toFixed(1)) + "%" }),
        lineChart("Memory utilization", d, [{ label: "Memory", cls: "t3", values: d.mem, text: (i) => pc(d.mem[i]) }], { floor: 10, axis: (v) => (Number.isInteger(v) ? v : v.toFixed(1)) + "%" }),
        lineChart("Disk utilization", d, diskSeries, { fixedMax: 100, axis: (v) => (Number.isInteger(v) ? v : v.toFixed(1)) + "%", legend: true }),
        lineChart("Network utilization", d, [
          { label: "In", cls: "t5", values: d.rx, text: (i) => bits(d.rx[i]) + " (peak " + bits(d.rx_max[i]) + ")" },
          { label: "Out", cls: "t6", values: d.tx, text: (i) => bits(d.tx[i]) + " (peak " + bits(d.tx_max[i]) + ")" }], { floor: 1000, axis: (v) => bits(v) }));
      const fsRows = (n.fs || []).map((f) => h("tr", {}, h("td", { class: "mono" }, f.mount), h("td", {}, f.device + " · " + f.type), h("td", { class: "num" }, bytes(f.used) + " of " + bytes(f.total)), h("td", { class: "num" }, pc(f.pct))));
      const ifRows = (n.ifaces || []).map((i) => h("tr", { class: i.counted ? null : "muted" }, h("td", { class: "mono" }, i.name), h("td", { title: i.counted ? null : "Not in the totals" }, i.up ? "up" : "down", i.counted ? "" : " *"),
        h("td", { class: "num" }, i.speed ? i.speed + " Mb/s" : "–"), h("td", { class: "num" }, bits(i.rx_bps)), h("td", { class: "num" }, bits(i.tx_bps)), h("td", { class: "num" }, i.util_pct >= 0 ? pc(i.util_pct) : "–")));
      fill(tables, 
        h("div", { class: "card" }, h("header", { class: "bar" }, h("h2", {}, "Filesystems")), h("div", { class: "body flush" }, fsRows.length
          ? h("table", {}, h("thead", {}, h("tr", {}, h("th", {}, "Mount"), h("th", {}, "Device"), h("th", { class: "num" }, "Used"), h("th", { class: "num" }, "Use"))), h("tbody", {}, fsRows))
          : h("div", { class: "empty" }, "No disk filesystems found."))),
        h("div", { class: "card" }, h("header", { class: "bar" }, h("h2", {}, "Network interfaces")), h("div", { class: "body flush" }, ifRows.length
          ? h("div", { class: "scroll" }, h("table", {}, h("thead", {}, h("tr", {}, h("th", {}, "Interface"), h("th", {}, "State"), h("th", { class: "num" }, "Speed"), h("th", { class: "num" }, "In"), h("th", { class: "num" }, "Out"), h("th", { class: "num" }, "Link use"))), h("tbody", {}, ifRows)))
          : h("div", { class: "empty" }, "No interfaces found."))));
      clear(cnote).append(...clusterNote(d.cluster));   // which nodes are added together, when the Node menu says Cluster
      const gn = guardNote(d.guard).slice(1);   // only the memory-guard warning, when there is one (how the numbers are kept is in the help)
      clear(foot).append(...gn); foot.hidden = !gn.length;
    }

    async function load() {
      const my = ++seq;
      try {
        let from = range, to = "";
        if (range === "custom") {
          const a = new Date(fromIn.value), b = new Date(toIn.value);
          if (isNaN(a) || isNaN(b) || a >= b) throw new Error("Pick a start that is before the end.");
          from = String(Math.floor(a / 1000)); to = String(Math.floor(b / 1000));
        }
        const r = await api("GET", "/api/host?from=" + encodeURIComponent(from) + (to ? "&to=" + encodeURIComponent(to) : ""));
        if (my !== seq) return;
        lastAt = Date.now();
        draw(r.data);
      } catch (e) { fail(status)(e); }
    }

    function setRange(r) {
      range = r;
      for (const [id, b] of rangeBtns) b.setAttribute("aria-pressed", id === r ? "true" : "false");
      customRow.classList.toggle("hidden", r !== "custom");
      if (r === "custom" && !fromIn.value) {
        const f = (d) => new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
        toIn.value = f(new Date()); fromIn.value = f(new Date(Date.now() - 6 * 3600 * 1000));
      }
      if (r !== "custom") load();
    }

    return {
      mount(main) {
        status = h("div", { "aria-live": "polite" });
        rangeBtns = new Map(RANGES.map(([id, label]) => [id, h("button", { type: "button", class: "seg", "aria-pressed": id === range ? "true" : "false", onclick: () => setRange(id) }, label)]));
        fromIn = h("input", { type: "datetime-local", "aria-label": "From" });
        toIn = h("input", { type: "datetime-local", "aria-label": "To" });
        customRow = h("span", { class: "qcustom hidden" }, fromIn, " to ", toIn,
          h("button", { class: "btn", type: "button", onclick: load }, "Apply"), h("span", { class: "muted small" }, " only the last 30 days exist"));
        bar = h("div", { class: "toolbar qbar" }, h("span", { class: "segs", role: "group", "aria-label": "Time range" }, [...rangeBtns.values()]), customRow);
        tiles = h("div", { class: "qtiles" });
        charts = h("div", { class: "hostcharts" });
        tables = h("div", { class: "hosttables" });
        foot = h("p", { class: "hint" });
        cnote = h("div", {});
        main.append(status, bar, cnote, tiles, charts, tables, foot);
        setRange(range);
      },
      async poll() {
        if (range === "custom") return;
        if (Date.now() - lastAt >= REFRESH_MS) await load();
      },
    };
  })();

  // ── Log, Monitor  (CLI: --log) ───────────────────────────────────────────────
  VIEWS.log = (() => {
    let status, level, span, count, search, live, info, box, first, timer, tsBtn, tsHold = 0, seq = 0, shown = [];
    const sel = (label, opts, val, onchange) => {
      const el = h("select", { "aria-label": label, onchange }, opts.map(([v, t]) => h("option", { value: v }, t)));
      el.value = val;
      return el;
    };
    const stamp = (t) => {
      const d = new Date(t);
      if (isNaN(d)) return t || "";
      const day = d.toDateString() === new Date().toDateString() ? "" : d.toLocaleDateString([], { month: "short", day: "numeric" }) + " ";
      return day + d.toLocaleTimeString([], { hour12: false });
    };
    const words = () => search.value.trim().split(/\s+/).filter(Boolean);
    // the message with the search words marked
    const marked = (msg) => {
      const w = words();
      if (!w.length) return msg;
      const re = new RegExp("(" + w.map((x) => x.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join("|") + ")", "ig");
      return msg.split(re).map((part, i) => (i % 2 ? h("mark", {}, part) : part));
    };
    function draw(r) {
      const stick = box.scrollHeight - box.scrollTop - box.clientHeight < 40; // at the bottom: follow new lines
      shown = r.lines;
      clear(box);
      if (!r.lines.length) box.append(h("div", { class: "empty" }, r.scanned ? "No log line matches these filters." : "The log is empty."));
      for (const l of r.lines) {
        box.append(h("div", { class: "logline lv-" + l.level.toLowerCase() },
          h("span", { class: "lt" }, stamp(l.time)), h("span", { class: "ll" }, l.level), h("span", { class: "lm" }, marked(l.msg))));
      }
      if (stick || first) box.scrollTop = box.scrollHeight;
      clear(info).append(r.matched + " matching line" + (r.matched === 1 ? "" : "s") + " of " + r.scanned + " on disk" +
        (r.truncated ? "; showing the newest " + r.lines.length + " (older ones are cut: narrow the filters or raise the line count)" : "") +
        (r.oldest ? ". The log reaches back to " + when(r.oldest) + "." : "."));
    }
    async function load() {
      const my = ++seq;
      const q = new URLSearchParams({ level: level.value, q: search.value.trim(), since: span.value, n: count.value });
      try {
        const r = (await api("GET", "/api/log?" + q.toString())).data;
        if (my !== seq) return; // a newer request is on its way
        if (Date.now() > tsHold) clear(status);   // not while the troubleshooting bundle is saying something
        draw(r);
        first = false;
      } catch (e) { fail(status)(e); }
    }
    // the troubleshooting bundle: every node's logs, configuration and state in one .tgz, asked of the node signed in to
    async function tshoot() {
      tsBtn.disabled = true; tsHold = Infinity;
      say(status, "info", "Collecting from every node… this takes up to a minute.");
      try {
        await apiDownload("/api/tshoot/download?capture=1", "ddgw-tshoot.tgz");
        say(status, "info", "Downloaded. Passwords, the gateway key, tokens and join codes are removed; README.txt in it lists what it holds.");
      } catch (e) { fail(status)(e); }
      tsBtn.disabled = false; tsHold = Date.now() + 20000;
    }
    function download() {
      const text = shown.map((l) => l.raw).join("\n") + "\n";
      const a = h("a", { href: URL.createObjectURL(new Blob([text], { type: "text/plain" })), download: "ddgw-log.txt" });
      document.body.append(a); a.click(); a.remove();
      setTimeout(() => URL.revokeObjectURL(a.href), 1000);
    }
    return {
      mount(main) {
        first = true;
        status = h("div", { "aria-live": "polite" });
        level = sel("Level", [["", "All levels"], ["info", "Info and above"], ["warn", "Warnings and errors"], ["error", "Errors only"]], "", load);
        span = sel("Time range", [["15m", "Last 15 minutes"], ["1h", "Last hour"], ["6h", "Last 6 hours"], ["1d", "Last 24 hours"], ["7d", "Last 7 days"], ["all", "All of it"]], "1h", load);
        count = sel("Lines", [["200", "200 lines"], ["1000", "1000 lines"], ["5000", "5000 lines"]], "1000", load);
        search = h("input", { type: "search", placeholder: "Filter words…", "aria-label": "Filter text", spellcheck: "false", class: "grow",
          oninput: () => { clearTimeout(timer); timer = setTimeout(load, 300); } });
        live = h("input", { type: "checkbox", checked: true });
        tsBtn = h("button", { class: "btn", type: "button", onclick: tshoot }, "tshoot");
        info = h("div", { class: "hint nomargin" });
        box = h("div", { class: "logbox", tabindex: "0", role: "log", "aria-label": "Log lines" });
        main.append(status, section("Log",
          h("div", { class: "toolbar tight logbar" }, search, level, span, count,
            h("label", { class: "opt" }, live, " Live")),
          h("div", { class: "toolbar tight" },
            h("button", { class: "btn", type: "button", onclick: load }, "Refresh"),
            h("button", { class: "btn", type: "button", onclick: download }, "Download"),
            tsBtn),
          box, info));
      },
      async poll() {
        if (first || live.checked) await load();
      },
      poll_ms: 3000,
    };
  })();

  // ── Capture, Monitor  (CLI: --capture, --capture-interfaces) ────────────────────
  // One node: a tcpdump-like capture on the node picked in the Node menu, shown live and downloadable as a .tgz (one .pcap in it).
  // Cluster: the same capture on every node at once for a chosen time, one .pcap per node in a .tgz.
  VIEWS.capture = (() => {
    let status, iface, filter, info, box, startBtn, stopBtn, dlBtn, ifs = null, cursor = 0, shown = 0, last = null, running = false;
    let job = null, jobBox, durSel, goBtn, dlJob;
    const ROWS = 1500;
    const cluster = () => state.target === CLUSTER;
    const sizeText = (n) => (n < 1024 ? n + " bytes" : n < 1048576 ? Math.round(n / 1024) + " KB" : (n / 1048576).toFixed(1) + " MB");

    const ifaceSelect = () => h("select", { "aria-label": "Interface", class: "capif" });
    async function loadIfaces() {
      const r = (await api("GET", "/api/capture/interfaces")).data;
      ifs = r;
      const gw = new Set(r.gateway || []);
      const keep = iface.value;
      clear(iface).append(...r.interfaces.map((i) => h("option", { value: i.name }, i.name + (gw.has(i.name) ? " (gateway)" : "") + (i.up ? "" : " (down)"))));
      const pick = [keep, last && last.iface, ...(r.gateway || []), (r.interfaces.find((i) => i.up && i.name !== "lo") || {}).name, (r.interfaces[0] || {}).name].find((n) => n && r.interfaces.some((i) => i.name === n));
      if (pick) iface.value = pick;
    }
    function addRows(pk) {
      const stick = box.scrollHeight - box.scrollTop - box.clientHeight < 40;
      if (box.firstChild && box.firstChild.classList && box.firstChild.classList.contains("empty")) clear(box);
      for (const p of pk) {
        const dns = / DNS /.test(p.summary), arp = /^(ARP|ICMPv6 .*neighbor)/.test(p.summary) || p.summary.startsWith("ARP");
        box.append(h("div", { class: "capline" + (dns ? " dns" : "") + (arp ? " arp" : ""), title: p.len + " bytes" },
          h("span", { class: "ct" }, p.time), h("span", { class: "cs" }, p.summary)));
      }
      shown += pk.length;
      while (box.childNodes.length > ROWS) { box.removeChild(box.firstChild); }
      if (stick) box.scrollTop = box.scrollHeight;
    }
    function empty(text) { clear(box).append(h("div", { class: "empty" }, text)); shown = 0; }
    function setButtons() {
      stopBtn.disabled = !running;
      dlBtn.disabled = !(last && last.count > 0);
    }
    async function refresh(full) {
      const q = full ? 0 : cursor;
      const st = (await api("GET", "/api/capture/packets?since=" + q)).data;
      if (st.cursor < cursor || (last && st.iface !== last.iface && !full)) return refresh(true); // another capture began, or this one was reset
      if (full) { clear(box); shown = 0; }
      if (st.packets.length) addRows(st.packets);
      cursor = st.cursor;
      last = st;
      running = st.running;
      if (!shown && !box.firstChild) empty(st.iface ? "Waiting for packets on " + st.iface + (st.filter ? " matching: " + st.filter : "") + "…" : "Not capturing. Choose an interface and press Start.");
      clear(info).append(!st.iface ? "" :
        (st.running ? "Capturing on " : "Stopped on ") + st.iface + (st.filter ? " · filter: " + st.filter : "") + " · " + st.count + " packet" + (st.count === 1 ? "" : "s") + " kept" +
        (st.seen > st.matched ? " (" + st.seen + " seen, the filter turned away " + (st.seen - st.matched) + ")" : "") +
        " · " + sizeText(st.bytes) + " in the buffer, the newest 5000 or 32 MB");
      setButtons();
    }
    async function start() {
      try {
        await api("POST", "/api/capture/start", { iface: iface.value, filter: filter.value.trim() });
        say(status, "ok"); cursor = 0; last = null; empty("Waiting for packets…");
        await refresh(true);
      } catch (e) { fail(status)(e); }
    }
    async function stop() { try { await api("POST", "/api/capture/stop", {}); await refresh(false); } catch (e) { fail(status)(e); } }
    async function clearAll() { try { await api("POST", "/api/capture/clear", {}); empty(running ? "Waiting for packets…" : "Cleared."); await refresh(false); } catch (e) { fail(status)(e); } }

    // ── every node ──
    function drawJob() {
      clear(jobBox);
      if (!job || job.none) return;
      const left = Math.max(0, job.seconds - job.elapsed);
      // Nothing to say when it went well (the table does): only while it runs, and when a node could not capture
      const failed = (job.nodes || []).filter((n) => n.status !== "done").length;
      if (!job.done) jobBox.append(h("div", { class: "notice info", role: "status" }, "Capturing on " + job.iface + (job.filter ? " (" + job.filter + ")" : "") + " on every node… " + Math.ceil(left) + " s left"));
      else if (job.error) jobBox.append(h("div", { class: "notice bad", role: "status" }, job.error));
      else if (failed) jobBox.append(h("div", { class: "notice warn", role: "status" }, failed + " of " + job.nodes.length + " nodes could not capture (see the list); the others are in the download."));
      if (job.nodes && job.nodes.length) {
        jobBox.append(h("div", { class: "scroll" }, h("table", {},
          h("thead", {}, h("tr", {}, ["Node", "Interface", "Result", "Packets kept", "Seen", "Size", "Why not"].map((t, i) => h("th", { class: i >= 3 && i <= 5 ? "num" : "" }, t)))),
          h("tbody", {}, job.nodes.map((n) => h("tr", {},
            h("td", {}, n.name, n.self ? " ★" : ""), h("td", { class: "mono" }, n.iface),
            h("td", {}, pill(n.status, n.status === "done" ? "ok" : n.status === "error" ? "bad" : "")),
            h("td", { class: "num" }, n.status === "done" ? String(n.kept) : "–"), h("td", { class: "num" }, n.status === "done" ? String(n.seen) : "–"),
            h("td", { class: "num" }, n.status === "done" ? sizeText(n.bytes) : "–"),
            h("td", {}, n.error || "")))))));
      }
      dlJob.disabled = !(job.done && job.ready);
      goBtn.disabled = !job.done;
    }
    async function pollJob() {
      const r = (await api("GET", "/api/clustercapture/status")).data;
      job = r.none ? null : r;
      drawJob();
    }
    async function goAll() {
      try {
        await api("POST", "/api/clustercapture/start", { iface: iface.value, filter: filter.value.trim(), seconds: Number(durSel.value) });
        say(status, "ok");
        await pollJob();
      } catch (e) { fail(status)(e); }
    }

    return {
      async mount(main) {
        cursor = 0; shown = 0; last = null; running = false; job = null; ifs = null;
        status = h("div", { "aria-live": "polite" });
        iface = ifaceSelect();
        filter = h("input", { type: "text", class: "grow", placeholder: "Filter, e.g. host 10.20.0.205 and port 53", "aria-label": "Capture filter", spellcheck: "false", autocomplete: "off", maxlength: "300",
          onkeydown: (e) => { if (e.key === "Enter") { e.preventDefault(); (cluster() ? goAll : start)(); } } });
        if (cluster()) {
          durSel = h("select", { "aria-label": "How long" }, [["5", "5 seconds"], ["10", "10 seconds"], ["30", "30 seconds"], ["60", "60 seconds"]].map(([v, t]) => h("option", { value: v }, t)));
          durSel.value = "10";
          goBtn = h("button", { class: "btn primary", type: "button", onclick: goAll }, "Capture on all nodes");
          dlJob = h("button", { class: "btn", type: "button", disabled: true, onclick: () => apiDownload("/api/clustercapture/download", "ddgw-cluster-capture.tgz").catch(fail(status)) }, "Download .tgz");
          jobBox = h("div", {});
          main.append(status, section("Capture on every node",
            h("div", { class: "hint nomargin" }, "Runs the same capture on every node of the cluster at the same time, for the time you choose, and bundles one .pcap per node in a .tgz. Each node keeps its newest packets (about 4 MB). The capture only listens."),
            h("div", { class: "toolbar tight logbar" }, iface, filter, durSel, goBtn, dlJob), jobBox));
          await loadIfaces().catch(fail(status));
          await pollJob().catch(fail(status));
          return;
        }
        startBtn = h("button", { class: "btn primary", type: "button", onclick: start }, "Start");
        stopBtn = h("button", { class: "btn", type: "button", disabled: true, onclick: stop }, "Stop");
        dlBtn = h("button", { class: "btn", type: "button", disabled: true, onclick: () => apiDownload("/api/capture/download", "ddgw-capture.tgz").catch(fail(status)) }, "Download .tgz");
        info = h("div", { class: "hint nomargin" });
        box = h("div", { class: "logbox capbox", tabindex: "0", role: "log", "aria-label": "Captured packets" });
        main.append(status, section("Packet capture",
          h("div", { class: "toolbar tight logbar" }, iface, filter, startBtn, stopBtn,
            h("button", { class: "btn", type: "button", onclick: clearAll }, "Clear"), dlBtn),
          box, info));
        empty("Not capturing. Choose an interface and press Start.");
        try { await loadIfaces(); await refresh(true); } catch (e) { fail(status)(e); }
      },
      async poll() {
        if (cluster()) { if (job && !job.done) await pollJob(); return; }
        if (running || (last && !last.iface)) await refresh(false);
      },
      poll_ms: 1000,
    };
  })();

  // ── Anycast, Monitor  (CLI: --bgp) ──────────────────────────────────────────────
  VIEWS.anycaststatus = (() => {
    let box;
    const stateKind = (st) => (st === "Established" ? "ok" : st === "Active" || st === "Connect" || st === "OpenSent" || st === "OpenConfirm" ? "warn" : "bad");
    const bfdKind = (st) => (st === "up" ? "ok" : st === "init" ? "warn" : st === "down" ? "bad" : "");

    function draw(st) { const real = box; box = h("div", {}); try { build(st); } finally { morph(real, box); box = real; } }
    // built into a detached copy and morphed in: the page keeps its elements (and so its scroll boxes) from one poll to the next
    function build(st) {
      const c = st.config;
      if (!c.asn) {
        box.append(section("BGP", h("div", { class: "notice info", role: "status" },
          "BGP is off on this node. Set a local AS number under Configure → Anycast and the anycast addresses of the gateways are announced to your neighbors.")));
        return;
      }
      if (c.disabled) {
        box.append(section("BGP", h("div", { class: "notice info", role: "status" },
          "BGP is disabled on this node (AS " + c.asn + "). Enable it under Operate → Anycast.")));
        return;
      }
      box.append(section("Status", kv([
        ["Local AS", String(c.asn) + (c.router_id ? " · router id " + c.router_id : "")],
        ["FRR", st.installed ? pill("installed", "ok") : pill("not installed", "bad")],
        st.installed ? ["bgpd", st.running ? pill("answering", "ok") : pill("not answering", "bad")] : null,
        ["Last apply", h("span", {}, pill(st.applied.ok ? "ok" : "failed", st.applied.ok ? "ok" : "bad"), " ", st.applied.detail || "–")],
      ]), ...(st.notes || []).map((n) => h("div", { class: "notice warn", role: "status" }, n))));

      const live = new Map(st.peers.map((p) => [p.peer, p]));
      const row = (n) => {
        const p = live.get(n.peer);
        return h("tr", {}, h("td", { class: "mono" }, n.peer), h("td", {}, String(n.remote_as)), h("td", {}, n.description || ""),
          h("td", {}, n.disabled ? pill("disabled", "") : p ? pill(p.state, stateKind(p.state)) : pill(st.running ? "not known yet" : "–", "")),
          h("td", {}, p && p.bfd ? pill(p.bfd, bfdKind(p.bfd)) : "–"),
          h("td", {}, p && p.state === "Established" ? p.uptime : "–"),
          h("td", {}, p && p.state === "Established" ? String(p.sent) : "–"));
      };
      box.append(section("Neighbors", h("div", { class: "scroll" }, h("table", {},
        h("thead", {}, h("tr", {}, ["Neighbor", "AS", "Description", "BGP", "BFD", "Up for", "Prefixes sent"].map((t) => h("th", {}, t)))),
        h("tbody", {}, c.neighbors.length ? c.neighbors.map(row) : h("tr", {}, h("td", { colspan: 7, class: "empty" }, "No neighbors yet: add one under Configure → Anycast.")))))));

      box.append(section("Anycast addresses", h("div", { class: "scroll" }, h("table", {},
        h("thead", {}, h("tr", {}, ["Address", "State", "Why"].map((t) => h("th", {}, t)))),
        h("tbody", {}, st.addresses.length ? st.addresses.map((a) => h("tr", {},
          h("td", { class: "mono" }, a.addr),
          h("td", {}, a.up ? pill("announced", "ok") : pill("withdrawn", "")),
          h("td", {}, a.up ? "" : a.reason || "")))
          : h("tr", {}, h("td", { colspan: 3, class: "empty" }, "None: add an anycast address to a gateway on the Topology page.")))))));
    }

    return {
      async mount(main) {
        box = h("div", {});
        main.append(box);
        draw((await api("GET", "/api/bgp")).data);
      },
      async poll() { draw((await api("GET", "/api/bgp")).data); },
      poll_ms: 5000,
    };
  })();

  // ── Anycast, Configure  (CLI: --asn, --router-id, --bgp-neighbor-add/-del) ──────
  VIEWS.anycast = (() => {
    let status, settings, nbrs, cfg = null, sel = null, draft = null, rows = new Map(), chain = Promise.resolve();
    const clone = (o) => JSON.parse(JSON.stringify(o));

    function save(next, keepForm) {
      chain = chain.then(async () => {
        try { const st = (await api("PUT", "/api/bgp", next)).data; say(status, "ok"); cfg = st.config; return true; }
        catch (e) { fail(status)(e); if (!keepForm) await load(); return false; }
      });
      return chain;
    }

    function select(key) {
      sel = key;
      for (const [k, tr] of rows) tr.classList.toggle("sel", k === key);
    }

    function neighborRow(n, idx) {
      const as = h("input", { class: "as", type: "text", inputmode: "numeric", pattern: "[0-9]*", autocomplete: "off", value: String(n.remote_as), "aria-label": "AS of " + n.peer });
      const desc = h("input", { type: "text", value: n.description || "", maxlength: "60", "aria-label": "Description of " + n.peer });
      const pw = h("input", { type: "password", value: n.password || "", autocomplete: "new-password", "aria-label": "Password for " + n.peer });
      const mh = h("input", { class: "as", type: "text", inputmode: "numeric", pattern: "[0-9]*", autocomplete: "off", value: n.multihop > 1 ? String(n.multihop) : "", placeholder: "off", "aria-label": "Multihop of " + n.peer });
      const commit = () => {
        const next = clone(cfg);
        next.neighbors[idx] = { peer: n.peer, remote_as: Number(as.value) || 0, description: desc.value.trim(), password: pw.value, multihop: Number(mh.value) || 0 };
        save(next);
      };
      for (const el of [as, desc, pw, mh]) el.addEventListener("change", commit);
      return h("tr", { "data-peer": n.peer }, h("td", { class: "mono" }, n.peer), h("td", {}, as), h("td", {}, desc), h("td", {}, pw), h("td", {}, mh));
    }

    // the new row: becomes a neighbor once it has an address and an AS
    function draftRow() {
      const peer = h("input", { type: "text", placeholder: "192.0.2.1 or 2001:db8::1", spellcheck: "false", "aria-label": "New neighbor address" });
      const as = h("input", { class: "as", type: "text", inputmode: "numeric", pattern: "[0-9]*", autocomplete: "off", placeholder: "AS", "aria-label": "New neighbor AS" });
      const desc = h("input", { type: "text", maxlength: "60", placeholder: "optional", "aria-label": "New neighbor description" });
      const pw = h("input", { type: "password", autocomplete: "new-password", placeholder: "optional", "aria-label": "New neighbor password" });
      const mh = h("input", { class: "as", type: "text", inputmode: "numeric", pattern: "[0-9]*", autocomplete: "off", placeholder: "off", "aria-label": "New neighbor multihop" });
      const commit = async () => {
        if (!peer.value.trim() || !(Number(as.value) > 0)) return; // not complete yet
        const next = clone(cfg);
        next.neighbors.push({ peer: peer.value.trim(), remote_as: Number(as.value), description: desc.value.trim(), password: pw.value, multihop: Number(mh.value) || 0 });
        if (await save(next, true)) { draft = null; sel = peer.value.trim(); drawNeighbors(); }
      };
      for (const el of [peer, as, desc, pw, mh]) el.addEventListener("change", commit);
      draft = { peer, focus: () => peer.focus() };
      return h("tr", { "data-peer": "" }, h("td", {}, peer), h("td", {}, as), h("td", {}, desc), h("td", {}, pw), h("td", {}, mh));
    }

    function drawNeighbors() {
      rows = new Map();
      const add = h("button", { class: "btn small", type: "button", "aria-label": "Add a neighbor", title: "Add a neighbor", onclick: () => {
        if (draft) { draft.focus(); return; }
        const tr = draftRow(); body.append(tr); rows.set("", tr); select(""); draft.focus();
        const e = body.querySelector(".empty"); if (e) e.closest("tr").remove();
      } }, "+");
      const del = h("button", { class: "btn small", type: "button", "aria-label": "Remove the selected neighbor", title: "Remove the selected neighbor", onclick: () => {
        if (sel === null) { say(status, "warn", "Click a neighbor first, then remove it."); return; }
        if (sel === "") { draft = null; drawNeighbors(); return; }
        if (!confirm("Remove neighbor " + sel + "?")) return;
        const next = clone(cfg); next.neighbors = next.neighbors.filter((x) => x.peer !== sel);
        sel = null; save(next).then(drawNeighbors);
      } }, "−");
      const body = h("tbody", {});
      cfg.neighbors.forEach((n, i) => { const tr = neighborRow(n, i); body.append(tr); rows.set(n.peer, tr); });
      draft = null;
      if (!cfg.neighbors.length) body.append(h("tr", {}, h("td", { colspan: 4, class: "empty" }, "No neighbors yet. Press + to add one.")));
      for (const [k, tr] of rows) { const pick = () => select(k); tr.addEventListener("click", pick); tr.addEventListener("focusin", pick); }
      if (sel !== null && !rows.has(sel)) sel = null;
      clear(nbrs).append(section("Neighbors",
        h("div", { class: "toolbar tight" }, add, del),
        h("div", { class: "scroll" }, h("table", {},
          h("thead", {}, h("tr", {}, ["Neighbor", "AS", "Description", "Password", "Multihop"].map((t) => h("th", {}, t)))), body))));
      select(sel);
    }

    function drawSettings() {
      const asn = h("input", { type: "text", inputmode: "numeric", pattern: "[0-9]*", autocomplete: "off", value: cfg.asn ? String(cfg.asn) : "", placeholder: "64512" });
      const rid = h("input", { type: "text", value: cfg.router_id || "", placeholder: "192.0.2.1 (optional)", spellcheck: "false" });
      const secs = (v, d, label) => h("input", { class: "narrow", type: "text", inputmode: "numeric", pattern: "[0-9]*", autocomplete: "off", value: String(v || d), "aria-label": label });
      const ka = secs(cfg.keepalive, 3, "Keepalive"), hold = secs(cfg.hold, 9, "Hold time");
      const prep = h("input", { type: "checkbox", checked: !!cfg.as_prepend });
      const needAS = () => { rid.disabled = !(Number(asn.value) > 0); };   // a router ID means nothing without an AS
      needAS();
      const commit = () => {
        if (!(Number(asn.value) > 0)) rid.value = "";   // clearing the AS clears the router ID with it
        needAS();
        const next = clone(cfg);
        next.asn = Number(asn.value) || 0; next.router_id = rid.value.trim();
        next.keepalive = Number(ka.value) || 0; next.hold = Number(hold.value) || 0;
        if (prep.checked) next.as_prepend = true; else delete next.as_prepend;
        save(next);
      };
      asn.addEventListener("input", needAS);
      for (const el of [asn, rid, ka, hold, prep]) el.addEventListener("change", commit);
      clear(settings).append(section("BGP",
        h("div", { class: "bgprow" },
          h("label", { class: "f" }, h("span", {}, "Local AS number"), asn),
          h("label", { class: "f" }, h("span", {}, "Router ID"), rid),
          h("label", { class: "f" }, h("span", {}, "Keepalive"), ka),
          h("label", { class: "f" }, h("span", {}, "Hold time"), hold),
          h("label", { class: "f" }, h("span", {}, "\u00a0"), h("span", { class: "opt" }, prep, " AS Prepend")))));
    }

    async function load() {
      cfg = (await api("GET", "/api/bgp")).data.config;
      drawSettings(); drawNeighbors();
    }

    return {
      async mount(main) {
        status = h("div", { "aria-live": "polite" }); settings = h("div", {}); nbrs = h("div", {});
        sel = null; draft = null;
        main.append(status, settings, nbrs);
        try { await load(); } catch (e) { fail(status)(e); }
      },
    };
  })();

  // ── Anycast, Operate  (CLI: --bgp-disable/-enable, --bgp-neighbor-disable/-enable) ──
  VIEWS.anycastop = (() => {
    let status, box;

    async function call(body) {
      try { draw((await api("POST", "/api/bgp/operate", body)).data); clear(status); }
      catch (e) { fail(status)(e); }
    }

    function draw(st) { const real = box; box = h("div", {}); try { build(st); } finally { morph(real, box); box = real; } }
    // built into a detached copy and morphed in: the page keeps its elements (and so its scroll boxes) from one poll to the next
    function build(st) {
      const c = st.config;
      if (!c.asn) {
        box.append(section("BGP", h("div", { class: "notice info", role: "status" },
          "BGP is off on this node. Set a local AS number under Configure → Anycast first.")));
        return;
      }
      const live = new Map(st.peers.map((p) => [p.peer, p]));
      const stateOf = (n) => {
        const p = live.get(n.peer);
        if (n.disabled) return pill("disabled", "");
        if (c.disabled) return pill("–", "");
        return p ? pill(p.state, p.state === "Established" ? "ok" : p.state === "Active" || p.state === "Connect" || p.state === "OpenSent" || p.state === "OpenConfirm" ? "warn" : "bad") : pill(st.running ? "not known yet" : "–", "");
      };
      const toggleBGP = h("button", { class: c.disabled ? "btn primary" : "btn danger", type: "button", onclick: () => {
        if (!c.disabled && !confirm("Disable BGP on this node?\n\nIts sessions go down and the anycast addresses are no longer announced from this node. The settings are kept.")) return;
        call({ enabled: !!c.disabled });
      } }, c.disabled ? "Enable BGP" : "Disable BGP");
      const row = (n) => h("tr", { tabindex: "0", oncontextmenu: rowMenu([[n.disabled ? "Enable" : "Disable", () => {
          if (c.disabled) return;
          if (!n.disabled && !confirm("Disable neighbor " + n.peer + "?\n\nThe session is shut down and nothing is announced to it. The neighbor is kept.")) return;
          call({ peer: n.peer, enabled: !!n.disabled });
        }]]) }, h("td", { class: "mono" }, n.peer), h("td", {}, String(n.remote_as)), h("td", {}, n.description || ""), h("td", {}, stateOf(n)));
      box.append(section("BGP",
        kv([["Local AS", String(c.asn)], ["State", c.disabled ? pill("disabled", "") : pill("enabled", "ok")]]),
        h("div", { class: "toolbar" }, toggleBGP)));
      box.append(section("Neighbors", h("div", { class: "scroll" }, h("table", {},
        h("thead", {}, h("tr", {}, ["Neighbor", "AS", "Description", "BGP"].map((t) => h("th", {}, t)))),
        h("tbody", {}, c.neighbors.length ? c.neighbors.map(row) : h("tr", {}, h("td", { colspan: 5, class: "empty" }, "No neighbors yet: add one under Configure → Anycast.")))))));
    }

    return {
      async mount(main) {
        status = h("div", { "aria-live": "polite" }); box = h("div", {});
        main.append(status, box);
        try { draw((await api("GET", "/api/bgp")).data); } catch (e) { fail(status)(e); }
      },
      async poll() { draw((await api("GET", "/api/bgp")).data); },
      poll_ms: 5000,
    };
  })();

  // ── Users  (CLI: --users, --user-add, --user-passwd, --user-expiry, --user-del) ──
  VIEWS.users = (() => {
    // Layout: the accounts in a list on the left, the chosen one (or the "new user" / "existing account" form) on the right.
    let status, listBox, detail, filterIn, users = [], others = [], group = "", sel = null, mode = "user", otherList = null;
    const dateText = (u) => (u.expires ? new Date(u.expires * 1000).toISOString().slice(0, 10) : "never");
    const toUnix = (d) => (/^\d{4}-\d{2}-\d{2}$/.test(d) ? Math.floor(Date.UTC(+d.slice(0, 4), +d.slice(5, 7) - 1, +d.slice(8, 10), 12) / 1000) : 0);
    const me = () => (state.session && state.session.user) || "";
    const current = () => users.find((u) => u.name === sel) || null;

    async function call(path, body, done) {
      try {
        const r = (await api("POST", "/api/users/" + path, body)).data;
        if (r.partial) say(status, "warn", r.message); else clear(status); // success shows in the list; only a node that missed it is worth saying
        if (done) done();
        await VIEWS.users.poll();
      } catch (e) { fail(status)(e); }
    }

    function drawList() {
      const real = listBox; listBox = h("div", { class: "ulist" });
      try {
        const q = filterIn ? filterIn.value.trim().toLowerCase() : "";
        const shown = users.filter((u) => !q || u.name.toLowerCase().includes(q));
        if (!users.length) listBox.append(h("div", { class: "empty" }, "Nobody is in the " + group + " group, so nobody can sign in."));
        else if (!shown.length) listBox.append(h("div", { class: "empty" }, "No user matches."));
        shown.forEach((u) => {
          const on = mode === "user" && u.name === sel;
          listBox.append(h("button", { type: "button", class: "uitem" + (on ? " on" : ""), "data-user": u.name,
            onclick: () => { sel = u.name; mode = "user"; drawList(); drawDetail(); } },
            h("span", { class: "uname" }, u.name, u.name === me() ? h("span", { class: "pill info tiny" }, "you") : null),
            u.expired ? h("span", { class: "usub bad" }, "expired " + dateText(u)) : h("span", { class: "usub" }, u.expires ? "expires " + dateText(u) : "never expires")));
        });
      } finally { morph(real, listBox); listBox = real; }
    }

    let shown = "";   // what the detail panel is showing: the mode and the account
    function drawDetail() {
      shown = mode + "/" + (mode === "user" ? sel : "");
      clear(detail);
      if (mode === "new") {
        const name = h("input", { type: "text", autocomplete: "off", spellcheck: "false", autocapitalize: "none", maxlength: "32" });
        const pw = h("input", { type: "password", autocomplete: "new-password" });
        const exp = h("input", { type: "date" });
        const add = () => call("add", { username: name.value.trim(), password: pw.value, expires: toUnix(exp.value) }, () => { sel = name.value.trim(); mode = "user"; });
        pw.addEventListener("keydown", (e) => { if (e.key === "Enter") add(); });
        detail.append(h("header", {}, h("h2", {}, "New user")), h("div", { class: "body" },
          h("div", { class: "grid c3" }, h("label", { class: "f" }, "Name", name), h("label", { class: "f" }, "Password", pw), h("label", { class: "f" }, "Expires (blank = never)", exp)),
          h("div", { class: "toolbar" }, h("button", { class: "btn primary", type: "button", onclick: add }, "Add user"))));
        name.focus();
        return;
      }
      if (mode === "existing") {
        const who = h("input", { type: "text", list: "users-others", autocomplete: "off", spellcheck: "false", autocapitalize: "none", maxlength: "32" });
        const grant = () => call("grant", { username: who.value.trim() }, () => { sel = who.value.trim(); mode = "user"; });
        who.addEventListener("keydown", (e) => { if (e.key === "Enter") grant(); });
        detail.append(h("header", {}, h("h2", {}, "Add an existing account")), h("div", { class: "body" },
          h("p", { class: "muted small" }, "An account that already exists on this host can be allowed to sign in here by adding it to the " + group + " group."),
          h("div", { class: "grid c3" }, h("label", { class: "f" }, "Name", who, otherList)),
          h("div", { class: "toolbar" }, h("button", { class: "btn primary", type: "button", onclick: grant }, "Add to group"))));
        who.focus();
        return;
      }
      const u = current();
      if (!u) { detail.append(h("div", { class: "empty" }, users.length ? "Choose a user on the left." : "Add a user to get started.")); return; }
      const self = u.name === me();
      const pw = h("input", { type: "password", autocomplete: "new-password", "aria-label": "New password for " + u.name });
      const d = h("input", { type: "date", value: u.expires ? dateText(u) : "", "aria-label": "Expiry date for " + u.name });
      const note = h("span", { class: "usaved", "aria-live": "polite" });
      // Save sends what changed: a new password if one was typed, the date if it differs (blank = never expires)
      const save = async () => {
        const was = u.expires ? dateText(u) : "";
        if (!pw.value && d.value === was) { note.textContent = "Nothing to save"; return; }
        note.textContent = "";
        if (pw.value) {
          let ok = true;
          await call("password", { username: u.name, password: pw.value }, () => { pw.value = ""; });
          if (pw.value) ok = false;   // still there: the call failed and the message is shown above
          if (!ok) return;
        }
        if (d.value !== was) await call("expiry", { username: u.name, expires: d.value ? toUnix(d.value) : 0 });
        drawDetail();
        const n = detail.querySelector(".usaved"); if (n) n.textContent = "Saved";
      };
      pw.addEventListener("keydown", (e) => { if (e.key === "Enter") save(); });
      const body = h("div", { class: "body ubody" },
        h("div", { class: "urow" }, h("label", { class: "f grow" }, "New password", pw)),
        h("div", { class: "urow" }, h("label", { class: "f grow" }, "Expires (blank = never)", d)));
      const left = [];
      if (!self) {
        left.push(h("button", { class: "btn", type: "button", onclick: () => {
          if (confirm("Disable " + u.name + "?\n\nThe account is kept; it just cannot sign in here any more.")) call("revoke", { username: u.name });
        } }, "Disable"),
        h("button", { class: "btn danger", type: "button", onclick: () => {
          if (confirm("Delete the account " + u.name + "?")) call("delete", { username: u.name });
        } }, "Delete"));
      }
      body.append(h("div", { class: "urow actions" }, ...left, h("span", { class: "grow" }), note, h("button", { class: "btn primary", type: "button", onclick: save }, "Save")));
      detail.append(h("header", {}, h("h2", {}, u.name)), body);
    }

    return {
      mount(main) {
        status = h("div", { "aria-live": "polite" });
        listBox = h("div", { class: "ulist" });
        detail = h("div", { class: "card udetail" });
        otherList = h("datalist", { id: "users-others" });
        filterIn = h("input", { type: "text", class: "ufilter", placeholder: "Filter", "aria-label": "Filter users", autocomplete: "off", spellcheck: "false", oninput: drawList });
        sel = null; mode = "user";
        const left = h("div", { class: "card ulistcard" },
          h("header", { class: "bar" }, h("h2", {}, "Users"), h("span", { class: "grow" }),
            h("button", { class: "btn primary small", type: "button", onclick: () => { mode = "new"; drawList(); drawDetail(); } }, "+ New")),
          h("div", { class: "ufilterbar" }, filterIn),
          listBox,
          h("div", { class: "ufoot" }, h("button", { class: "btn", type: "button", onclick: () => { mode = "existing"; drawList(); drawDetail(); } }, "Add an existing account…")));
        main.append(status, h("div", { class: "usersplit" }, left, detail));
      },
      async poll() {
        const r = (await api("GET", "/api/users")).data;
        users = r.users || [];
        others = r.others || [];
        group = r.group;
        if (otherList) clear(otherList).append(...others.map((n) => h("option", { value: n })));
        if (mode === "user" && !current()) sel = users.length ? users[0].name : null;
        drawList();
        // the panel is left alone on a poll so what is being typed survives, and redrawn when what it should show has changed
        if (!detail.firstChild || shown !== mode + "/" + (mode === "user" ? sel : "")) drawDetail();
      },
    };
  })();

  // ── Node, Operate  (CLI: --assert-agc, --node-pause/-resume/-status, --power) ──
  VIEWS.node = (() => {
    let status, pending, act, whenSel, mins, clock, takeBtn, npBtn, npPaused = false;
    const radio = (name, value, label, checked, ...extra) => {
      const r = h("input", { type: "radio", name, value, checked: !!checked });
      return h("label", { class: "opt" }, r, " ", label, ...extra);
    };
    const val = (name) => { const r = document.querySelector('input[name="' + name + '"]:checked'); return r ? r.value : ""; };

    const showNodePause = () => {
      npBtn.textContent = npPaused ? "Resume this node" : "Pause this node";
      npBtn.className = npPaused ? "btn primary" : "btn danger";
    };
    async function takeOver() {
      if (!confirm("Make this node the gateway controller for all groups?\n\nThe node that currently answers for the shared address is asked to step down once this node has taken the role.")) return;
      takeBtn.disabled = true;
      try {
        const r = await api("POST", "/api/assert-agc", {});
        if (!r.ok) say(status, "warn", r.error || "Could not take over as the gateway controller");
        else say(status, "info", "Asked to take over: " + (r.messages || []).join(" · "));   // what happened, per group and address family
      } catch (e) { fail(status)(e); }
      finally { takeBtn.disabled = false; }
    }
    async function toggleNodePause() {
      const want = !npPaused;
      if (want && !confirm("Pause this node?\n\nAll of its gateways stop serving and the other nodes take over. Clients are not interrupted as long as another node is serving. Resume it when you are done.")) return;
      try {
        await api("POST", "/api/nodepause", { paused: want });
        npPaused = want; showNodePause();
      } catch (e) { fail(status)(e); }
    }

    async function go(req) {
      const verb = req.action === "restart" ? "restart" : "shut down";
      const whenText = req.when === "now" ? "now" : req.when === "in" ? "in " + req.minutes + " minute(s)" : "at " + req.time;
      if (!confirm(verb.charAt(0).toUpperCase() + verb.slice(1) + " the whole host " + whenText + "?\n\n" +
        (req.action === "restart" ? "The host reboots and you lose access to this page until it is back." : "The host powers off and stays off until it is powered on again.") +
        (req.when === "now" ? "" : "\n\nThe gateway is not checked for cover at the scheduled time; make sure another member is serving by then."))) return;
      try {
        const r = (await api("POST", "/api/power", req)).data;
        say(status, "info", "Host " + (req.action === "restart" ? "restart" : "shutdown") + " " + r.when + ".");
        await VIEWS.node.poll();
      } catch (e) {
        // a gateway would lose its last serving member: say so and let the admin decide
        if (/^not safe to /.test(e.message || "") && confirm(e.message.replace(/ \(to go ahead anyway.*$/, "") + "\n\nGo ahead anyway? Clients of that gateway will be interrupted.")) {
          try { const r = (await api("POST", "/api/power", { ...req, force: true })).data; say(status, "info", "Host " + (req.action === "restart" ? "restart" : "shutdown") + " " + r.when + "."); await VIEWS.node.poll(); } catch (e2) { fail(status)(e2); }
          return;
        }
        fail(status)(e);
      }
    }

    function execute() {
      const req = { action: val("power-act") || "restart", when: val("power-when") || "now" };
      if (req.when === "in") {
        req.minutes = parseInt(mins.value, 10);
        if (!(req.minutes >= 1 && req.minutes <= 10080)) { say(status, "warn", "Enter a number of minutes between 1 and 10080."); return; }
      } else if (req.when === "at") {
        req.time = clock.value;
        if (!/^\d{2}:\d{2}$/.test(req.time)) { say(status, "warn", "Choose a time of day."); return; }
      }
      go(req);
    }

    return {
      mount(main) {
        status = h("div", { "aria-live": "polite" });
        pending = h("div", {});
        mins = h("input", { type: "number", min: "1", max: "10080", value: "5", class: "narrow", "aria-label": "Minutes", onfocus: () => { document.querySelector('input[name="power-when"][value="in"]').checked = true; } });
        clock = h("input", { type: "time", "aria-label": "Time of day", onfocus: () => { document.querySelector('input[name="power-when"][value="at"]').checked = true; } });
        takeBtn = h("button", { class: "btn warn", type: "button", onclick: takeOver }, "Make this node the gateway controller");
        npBtn = h("button", { class: "btn", type: "button", onclick: toggleNodePause }, "…");
        main.append(status, pending,
          section("Gateway controller", h("div", { class: "toolbar" }, takeBtn)),
          section("Maintenance", h("div", { class: "toolbar" }, npBtn)),
          section("Host",
            h("fieldset", { class: "radios" }, h("legend", {}, "Action"),
              radio("power-act", "restart", "Restart host", true), radio("power-act", "shutdown", "Shut down host", false)),
            h("fieldset", { class: "radios" }, h("legend", {}, "When"),
              radio("power-when", "now", "Now", true),
              radio("power-when", "in", "In ", false, mins, " minutes"),
              radio("power-when", "at", "At ", false, clock, " (24-hour)")),
            h("div", { class: "toolbar" },
              h("button", { class: "btn danger", type: "button", onclick: execute }, "Execute"))));
      },
      async poll() {
        try { npPaused = !!(await api("GET", "/api/nodepause")).data.paused; showNodePause(); } catch (_) { /* keep the last state */ }
        const p = (await api("GET", "/api/power")).data;
        clear(pending);
        if (p.scheduled) {
          pending.append(h("div", { class: "notice warn", role: "status" },
            "A " + p.action + " is scheduled for " + when(p.at) + ". ",
            h("button", { class: "btn", type: "button", onclick: async () => {
              try { await api("POST", "/api/power", { action: "cancel" }); say(status, "info", "The scheduled " + p.action + " is cancelled."); await VIEWS.node.poll(); } catch (e) { fail(status)(e); }
            } }, "Cancel it")));
        }
      },
      poll_ms: 5000,
    };
  })();

  // ── boot ──────────────────────────────────────────────────────────────────
  (async () => {
    try {
      state.session = await api("GET", "/api/session");
      showShell();
    } catch (e) {
      if (e.message !== "unauthenticated") showLogin();
    }
  })();
})();
