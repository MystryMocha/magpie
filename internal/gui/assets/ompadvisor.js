// omp's advisor beyond its role (#5019): advisor.* in config.yml, the
// roster in WATCHDOG.yml, the notes in WATCHDOG.md, each subagent's
// task.agentAdvisor and retry.fallbackChains.advisor, at the user's level
// or a project's (agent/omp_advisor.go). A sheet of settings rows as an
// agent's own settings are (openAgentPrefs): each written as it is picked,
// with the version of the files it read; what takes more room (an
// advisor, the notes, the subagents, the fallback chain, the rest of the
// settings, what is in effect) on a page of its own. app.js imports this
// when omp's row opens, so it weighs nothing on the core.

// ompSaid is what magpie says of the advisor's files (agent.OmpSay) in the
// reader's language: its file first, when it is about one
const ompSaid = (s) => (s.file ? s.file + ": " : "") + t(s.key, s.vars);

// oaApi is api for the Advisor's routes, whose errors come with the words
// to translate (say)
async function oaApi(path, body) {
  const res = await fetch("/api/agents/omp-advisor/" + path, {
    method: body === undefined ? "GET" : "POST",
    headers: { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const data = await res.json().catch(() => null);
  if (!res.ok) throw new Error(data?.say ? ompSaid(data.say) : data?.error || `${res.status} ${res.statusText}`);
  return data;
}

// ompAdvisorEdit puts Edit › on omp's Advisor row (#5019): connectPanel in
// app.js draws the row, its value v holding the line, and imports this
// file for the rest, which keeps it off the core
window.ompAdvisorEdit = function (a, v) {
  const b = el("button", "ag-quiet");
  b.type = "button";
  b.append(el("span", "", t("Edit")), el("span", "chev", svg(CHEV_R, 10, 1.6)));
  b.onclick = () => ompAdvisorOpen(a);
  v.firstChild.append(b);
};

// the sheet open: whose, the view the server last gave, the level shown
// (user or project), the page (main, or one of OA_PAGES) and its argument,
// and the advisor of the roster opened out under its line (open)
let oa = null;

// ompAdvisorOpen reads the advisor's settings for a (at project, a folder
// as omp spells it, or the user's level) and opens them in a sheet.
window.ompAdvisorOpen = async function (a, project = "", where = project ? "project" : "user") {
  let view;
  try {
    view = await oaApi(encodeURIComponent(a.id) + (project ? "?project=" + encodeURIComponent(project) : ""));
  } catch (e) { return statusErr(e); }
  oa = { a, view, where, page: "main" };
  oaDraw();
};

const oaLayer = () => oa.where === "project" ? oa.view.project : oa.view.user;
// a page of its own starts at its top, not where the last was scrolled
const oaGo = (page) => {
  oa.page = page;
  oaDraw();
  const body = $("#modal .ebody");
  if (body) body.scrollTop = 0;
};

// oaWrite saves ch at the level shown, with the version read: a file
// changed since is a 409 and nothing is written. The sheet is drawn again
// from what the server has either way, so a refused pick doesn't stay.
async function oaWrite(ch) {
  const { a, view } = oa;
  if (oa.where === "project") ch.project = view.projectDir;
  ch.version = oaLayer().version;
  let ok = true;
  try {
    oa.view = await oaApi(encodeURIComponent(a.id), ch);
    // the disconnect's list of what it puts back has these now, and the
    // row's line says what is set
    delete previews[a.id];
    api("state").then((s) => { state = s; renderAgents(); }, () => {});
  } catch (e) {
    ok = false;
    status(e.message, "err");
  }
  oaDraw();
  return ok;
}
// oaRoster changes the roster shown (fn on a copy) and writes it
function oaRoster(fn) {
  const w = structuredClone(oaLayer().watchdog || {});
  w.advisors ||= [];
  fn(w);
  return oaWrite({ watchdog: w });
}

// omp's thinking levels, which a model of the advisor's may carry after
// a colon (provider/model:high), as omp's model patterns take them
const OMP_LEVELS = ["off", "minimal", "low", "medium", "high", "xhigh", "max"];
const ompLevelOf = (v) => {
  const at = (v || "").lastIndexOf(":");
  return at > 0 && OMP_LEVELS.includes(v.slice(at + 1)) ? [v.slice(0, at), v.slice(at + 1)] : [v || "", ""];
};
// the levels a model may be given: those omp offers a model of magpie's
// (models.yml's), and off; every one of omp's for a model of omp's own
const ompLevelsFor = (id) => { const l = oa.view.levels?.[id]; return l ? ["off", ...l] : OMP_LEVELS; };
// a model as the advisor role's picker names it, its level after it
const ompModelName = (a, v) => {
  const [id, lvl] = ompLevelOf(v);
  const role = a.fields.find((f) => f.label === "advisor");
  const name = (role && optionFor(role, id)?.label) || id;
  return lvl ? `${name} (${lvl})` : name;
};
const oaSetting = (key) => oa.view.settings.find((s) => s.key === key);
// advisor.maxNotesPerUpdate as it is in effect at this level
const oaNotesEach = () => { const k = "advisor.maxNotesPerUpdate"; return oaLayer().settings?.[k] || oa.view.user.settings?.[k] || oaSetting(k)?.default || ""; };
// a value as the reader says it: On/Off, an option's label, the number
const oaSaid = (s, v) => s.kind === "bool" ? t(v === "true" ? "On" : "Off") : s.kind === "enum" ? t(s.options.find((o) => o.value === v)?.label || v) : v;
// an advisor's cadence in a few words
const oaWhen = (r) => r.reviewMode === "agent-end" ? t("when the agent ends") : r.reviewInterval > 1 ? t("every {n} turns", { n: r.reviewInterval }) : t("every turn");

// the pieces of a page: a row of the settings list, a chip that opens a
// menu, a row that opens a page of its own
function oaRow(name, sub, ...ctl) {
  const r = el("div", "row pref");
  const who = el("div", "who");
  who.append(el("div", "name", name));
  if (sub) who.append(el("div", "sub", sub));
  r.append(who, ...ctl);
  return r;
}
function oaChip(text, set) {
  const b = el("button", "proto pick" + (set ? " set" : ""));
  b.type = "button";
  b.append(el("span", "", text), svg(CHEV, 11, 1.6));
  return b;
}
const oaMenu = (b, opts, value, choose, head) => (ev) => {
  ev.stopPropagation();
  if (b.classList.contains("open")) return closeProtoMenu();
  openProtoMenu(b, opts, value, choose, head, "oa-menu");
};
function oaNav(name, value, page) {
  const r = el("button", "row pref oa-nav");
  r.type = "button";
  r.dataset.page = page;
  r.append(el("span", "name", name), el("span", "oa-v", value), el("span", "chev", svg(CHEV_R, 10, 1.6)));
  r.title = name + ": " + value;
  r.onclick = () => oaGo(page);
  return r;
}
const oaList = (...rows) => {
  const l = el("div", "list prefs");
  l.append(...rows);
  return l;
};
// the advisor role's models (magpie's, as its picker lists them), for an
// advisor of the roster, a subagent or the fallback chain
const oaPickModel = (b, value, unset, onPick) => (ev) => {
  const role = oa.a.fields.find((f) => f.label === "advisor");
  openPicker(oa.a, {
    key: "omp-advisor", label: "advisor", value,
    options: [...(unset ? [{ value: "", label: t("Not set"), note: unset, icon: oa.a.icon, reset: true }] : []), ...(role?.options || [])],
    onPick,
  }, b, ev);
};

// oaSettingRow is one of the advisor.* settings: Off/On or two options as
// segs, more as a menu, a number typed; at a project's level unset is as
// for every project. omp's words for it are its tooltip.
function oaSettingRow(s) {
  const layer = oaLayer(), user = oa.where === "user";
  const now = layer.settings?.[s.key] || "";
  const inherited = user ? s.default : oa.view.user.settings?.[s.key] || s.default;
  // a pick of what is in effect unset takes the key out
  const write = (v) => oaWrite({ settings: { [s.key]: user && v === s.default ? "" : v } });
  const opts = s.kind === "bool" ? [["false", t("Off")], ["true", t("On")]] : (s.options || []).map((o) => [o.value, t(o.label)]);
  let c;
  if (s.kind === "number") {
    c = el("input", "words compact-num");
    c.type = "number";
    c.min = s.min;
    if (s.max) c.max = s.max;
    c.value = now;
    c.placeholder = inherited;
    c.setAttribute("aria-label", t(s.label));
    c.onchange = () => {
      const v = c.value.trim();
      if (v && (+v < s.min || (s.max && +v > s.max) || !Number.isInteger(+v))) { c.value = now; return status(t("{field} takes a whole number from {min} to {max}", { field: t(s.label), min: s.min, max: s.max }), "err"); }
      write(v);
    };
  } else if (opts.length <= 2) {
    c = segs([...(user ? [] : [["", t("As for every project")]]), ...opts], user ? now || s.default : now, write);
  } else {
    c = oaChip(oaSaid(s, now || inherited), !!now);
    c.onclick = oaMenu(c, [{ v: "", name: user ? "Default" : "As for every project", note: oaSaid(s, inherited) },
      ...s.options.map((o) => ({ v: o.value, name: o.label, note: o.description || "" }))], now, write, s.label);
  }
  const r = oaRow(t(s.label), "", c);
  r.dataset.key = s.key;
  r.querySelector(".who").title = t(s.description);
  return r;
}

const OA_FIRST = ["advisor.enabled", "advisor.reviewMode"];

// oaDraw draws the page the sheet is on, in place: a pick or a page of
// its own is the same sheet, without the dialog's soft refresh (openModal)
function oaDraw() {
  const { a, where, page } = oa;
  const layer = oaLayer();
  const ed = el("div", "editor agent-prefs omp-adv");
  const head = el("div", "ehead");
  const bar = el("div", "bar");
  const done = el("button", "primary", t("Done"));
  done.type = "button";
  done.onclick = () => closeModal();
  // the folder page is how a project gets a layer, so it needs none
  if (page !== "main" && OA_PAGES[page] && (layer || page === "folder")) {
    const back = el("button", "oa-back");
    back.type = "button";
    back.setAttribute("aria-label", t("Back"));
    back.append(svg(CHEV_R, 12, 1.8));
    back.onclick = () => oaGo("main");
    head.append(back);
    // a page with nothing to show (an advisor since removed) is the first
    if (OA_PAGES[page](ed, head, bar, done) === false) return oaGo("main");
  } else {
    oa.page = "main";
    head.append(el("b", "", t("{agent}'s advisor", { agent: a.name })));
    if (where === "project") head.append(oaProjectChip());
    // where: the user's files, or a project's over them
    const there = segs([["user", t("For every project")], ["project", t("In a project")]], where, (v) => { Object.assign(oa, { where: v, open: null }); oaDraw(); });
    there.classList.add("oa-where");
    head.append(there);
    oaMain(ed, bar);
    bar.append(el("span", "grow"), done);
  }
  ed.prepend(head);
  ed.append(bar);
  openModal(ed, true);
}

// the project in the head: a folder omp was run in, one chosen, or typed
function oaProjectChip() {
  const { a, view } = oa;
  const dir = view.projectDir || "";
  const go = (d) => d && ompAdvisorOpen(a, d, "project");
  const pick = oaChip(dir ? dir.split(/[\\/]/).filter(Boolean).pop() || dir : t("Pick a folder"), !!dir);
  pick.title = view.project ? dir + "\n" + t("omp reads {file} over the user's, in this folder only", { file: view.project.config }) : dir;
  pick.onclick = oaMenu(pick, [
    ...(view.projects || []).map((p) => ({ v: p.dir, name: p.dir, note: ago(p.last), literalName: true })),
    { v: "\x01", name: "Choose…", note: "" }, { v: "\x02", name: "Type a folder…", note: "" },
  ], dir, async (v) => {
    if (v === "\x02") return oaGo("folder");
    if (v !== "\x01") return go(v);
    try { go((await oaApi(encodeURIComponent(a.id) + "/choose", {})).dir); } catch (e) { statusErr(e); }
  }, "Folders omp was run in");
  return pick;
}

// the first page: the switch and when it reviews, the roster, and a line
// for each of the rest
function oaMain(ed, bar) {
  const { a, view, where } = oa;
  const layer = oaLayer();
  // no project yet, or one omp reads nothing in: said where the rows go
  if (where === "project" && (!layer || view.projectHome)) {
    ed.append(oaList(oaRow(t("Project"), view.projectHome ? t("This is your home folder: omp reads no project settings here") : t("Pick a folder omp was run in, or choose one"))));
  }
  if (!layer) return;
  if (layer.configError) ed.append(el("p", "oa-warn", t("{file} can't be read: {err}", { file: layer.config, err: layer.configError })));

  // the switch and when it reviews; a roster runs nothing while it is off
  const first = oaList(...OA_FIRST.map((k) => oaSettingRow(oaSetting(k))));
  const enabled = layer.settings?.["advisor.enabled"] || (where === "user" ? "" : view.user.settings?.["advisor.enabled"]);
  const role = a.fields.find((f) => f.label === "advisor");
  const off = enabled !== "true" && (view.effective.roster?.length ? "Off, so omp runs none of WATCHDOG.yml's advisors"
    : role?.value ? "Off, so the advisor role's model runs nothing" : "");
  if (off) first.firstChild.querySelector(".who").append(el("div", "sub oa-off", t(off)));
  ed.append(first);

  // WATCHDOG.yml: an advisor a line, each its own page
  const wd = layer.watchdog || {};
  const advisors = wd.advisors || [];
  const cap = el("div", "oa-cap");
  cap.append(el("b", "", t("Advisors")), el("span", "oa-file", layer.watchdogFile));
  const roster = oaList();
  roster.classList.add("oa-roster");
  if (layer.watchdogError?.length) {
    // magpie writes no roster it can't read whole
    const r = oaRow(t("magpie leaves {file} as it is: fix it by hand first", { file: layer.watchdogFile }), "");
    r.querySelector(".who").append(...layer.watchdogError.map((e) => el("div", "sub oa-err", ompSaid(e))));
    roster.append(r);
  } else {
    const add = el("button", "text", "+ " + t("Add an advisor"));
    add.type = "button";
    // a new one opens out, its name ready to be typed over
    add.onclick = async () => {
      let n = advisors.length + 1;
      while (advisors.some((x) => x.name === "advisor-" + n)) n++;
      oa.open = advisors.length;
      if (!await oaRoster((w) => w.advisors.push({ name: "advisor-" + n, tools: null }))) return;
      $("#modal .oa-adv-body input.words")?.select();
    };
    cap.append(add);
    if (!advisors.length) roster.append(oaRow(t("None"), t("None: omp runs one advisor on the advisor role's model, by the settings above")));
    advisors.forEach((adv, i) => {
      const [mid, lvl] = ompLevelOf(adv.model);
      const model = oaChip(mid ? ompModelName(a, adv.model) : t("the advisor role's model"), !!mid);
      model.onclick = oaPickModel(model, mid, t("the advisor role's model"), (m) =>
        // its level goes with it to a model that offers it
        oaRoster((w) => { w.advisors[i].model = m && lvl && !ompLevelOf(m)[1] && ompLevelsFor(m).includes(lvl) ? m + ":" + lvl : m || undefined; }));
      const r = oaRow(adv.name || "—", oaWhen(adv) + (adv.enabled === false ? " · " + t("paused") : ""), model, el("span", "chev", svg(CHEV_R, 10, 1.6)));
      const open = oa.open === i;
      r.classList.add("oa-adv");
      r.classList.toggle("paused", adv.enabled === false);
      r.classList.toggle("open", open);
      r.tabIndex = 0;
      r.setAttribute("role", "button");
      r.setAttribute("aria-expanded", open);
      // its line opens it out under it, or folds it back
      const fold = () => { oa.open = open ? null : i; oaDraw(); };
      r.onclick = (ev) => { if (!ev.target.closest(".proto")) fold(); };
      r.onkeydown = (ev) => { if (ev.target === r && (ev.key === "Enter" || ev.key === " ")) { ev.preventDefault(); fold(); } };
      roster.append(r);
      if (open) roster.append(oaAdvBody(adv, i));
    });
  }
  ed.append(cap, roster);

  // the rest, a line each
  const subs = Object.entries(layer.subagents || {}).filter(([, v]) => v);
  const chain = layer.fallback || [];
  const more = view.settings.filter((s) => !OA_FIRST.includes(s.key) && layer.settings?.[s.key]).length;
  const unset = t(where === "user" ? "Default" : "As for every project");
  const onOff = (v) => v === "on" || v === "true" ? t("On") : v === "off" || v === "false" ? t("Off") : ompModelName(a, v);
  ed.append(oaList(
    oaNav(t("Notes for the advisors"), layer.notesError ? t("can't be read") : (layer.notes || wd.instructions || "").trim().split("\n")[0] || t("None"), "notes"),
    oaNav(t("Subagents"), subs.length ? subs.map(([k, v]) => k + ": " + onOff(v)).join(" · ") : unset, "subs"),
    oaNav(t("Fallback models"), chain.length ? chain.map((m) => ompModelName(a, m)).join(" → ") : t("None"), "chain"),
    oaNav(t("More settings"), more ? t("{n} set", { n: more }) : unset, "more"),
  ));

  // what is in effect here: a line in the bar, the files on its page
  const eff = view.effective;
  const files = new Set([...Object.values(eff.settings || {}), ...Object.values(eff.subagents || {}), eff.fallback || {}, ...(eff.roster || [])].map((x) => x.from).filter(Boolean));
  for (const n of eff.notes || []) files.add(n);
  const shown = el("button", "text oa-eff-open");
  shown.type = "button";
  shown.append(el("span", "", t("In effect: {n} advisors · from {files} files", { n: eff.roster?.length || 0, files: files.size }) + (eff.warnings?.length ? " · ⚠" : "")), el("span", "chev", svg(CHEV_R, 10, 1.6)));
  shown.onclick = () => oaGo("effective");
  bar.append(shown);
}

// oaAdvBody is an advisor of the roster opened out under its line, its
// model being on the line: each of the rest written as it is picked, a
// word typed once it is left, as the rest of the sheet is
function oaAdvBody(adv, i) {
  const layer = oaLayer();
  const put = (k, v) => oaRoster((w) => { const x = w.advisors[i]; if (v === "" || v == null) delete x[k]; else x[k] = v; });
  const opt = (key) => (oaSetting("advisor." + key)?.options || []).map((o) => ({ v: o.value, name: o.label, note: o.description || "" }));

  const name = el("input", "words");
  name.value = adv.name;
  name.setAttribute("aria-label", t("Name"));
  // omp needs a name: none typed keeps the one it has
  name.onchange = () => { const v = name.value.trim(); if (!v) name.value = adv.name; else if (v !== adv.name) put("name", v); };
  // the thinking level after its model (provider/model:high)
  const [mid, mlvl] = ompLevelOf(adv.model);
  // the model's own levels, and one set it doesn't offer, to be seen
  const levels = mid ? ompLevelsFor(mid) : [];
  const lvl = oaChip(mlvl ? t(mlvl) : t("Default"), !!mlvl);
  lvl.disabled = !mid || levels.length < 2 && !mlvl;
  lvl.onclick = oaMenu(lvl, [{ v: "", name: "Default", note: "medium, as omp gives an advisor" },
    ...(mlvl && !levels.includes(mlvl) ? [mlvl] : []).concat(levels).map((l) => ({ v: l, name: l, note: "" }))], mlvl, (v) => put("model", mid + (v ? ":" + v : "")), "Thinking level");
  const runs = segs([["on", t("On")], ["off", t("Paused")]], adv.enabled === false ? "off" : "on", (v) => put("enabled", v === "off" ? false : null));
  // when it reviews: each turn, every few turns, or once the agent ends
  const when = el("div", "oa-ctl");
  if (adv.reviewMode !== "agent-end") {
    const every = el("input", "words compact-num");
    every.type = "number";
    every.min = 1;
    every.value = adv.reviewInterval || "";
    every.placeholder = "1";
    every.title = t("Every how many turns");
    every.setAttribute("aria-label", every.title);
    every.onchange = () => put("reviewInterval", Math.max(0, parseInt(every.value, 10) || 0) || null);
    when.append(every);
  }
  when.append(segs([["turn", t("Every turn")], ["agent-end", t("Agent end")]], adv.reviewMode === "agent-end" ? "agent-end" : "turn", (v) => put("reviewMode", v === "turn" ? "" : v)));
  const choice = (key, opts, unsetNote, headWord) => {
    const v = adv[key] == null ? "" : String(adv[key]);
    const b = oaChip(v ? t(opts.find((o) => o.v === v)?.name || v) : t(unsetNote), !!v);
    b.onclick = oaMenu(b, [{ v: "", name: "Default", note: unsetNote }, ...opts], v, (x) => put(key, key === "includeReasoning" ? (x ? x === "true" : null) : x), headWord);
    return b;
  };
  const notes = el("input", "words compact-num");
  notes.type = "number";
  notes.min = 1;
  notes.max = 32;
  notes.value = adv.maxNotesPerUpdate || "";
  // empty: the roster's count, or advisor.maxNotesPerUpdate's
  notes.placeholder = layer.watchdog.maxNotesPerUpdate || oaNotesEach();
  notes.title = t("as every advisor's");
  notes.setAttribute("aria-label", t("Notes per update"));
  notes.onchange = () => put("maxNotesPerUpdate", Math.max(0, Math.min(32, parseInt(notes.value, 10) || 0)) || null);
  // its tools: omp's default (read, grep, glob, recall), or those picked,
  // written once the menu is shut
  const tools = oaChip(adv.tools?.length ? adv.tools.join(", ") : t("read, grep, glob and recall"), !!adv.tools?.length);
  tools.onclick = (ev) => {
    ev.stopPropagation();
    if (tools.classList.contains("open")) return closeProtoMenu();
    openProtoMenu(tools, [{ v: "", name: "Default", note: "read, grep, glob and recall" }, ...oa.view.tools.map((x) => ({ v: x, name: x, note: "", literalName: true }))],
      adv.tools || [], (list) => put("tools", list.length ? list : null), "Tools it may use", "oa-menu");
  };
  const say = el("textarea", "oa-text");
  say.rows = 3;
  say.value = adv.instructions || "";
  say.placeholder = t("What this advisor looks for");
  say.setAttribute("aria-label", t("Instructions"));
  say.onchange = () => put("instructions", say.value.trim() ? say.value : "");
  const text = oaRow(t("Instructions"), "", say);
  text.classList.add("oa-wide");
  const rm = el("button", "text danger", t("Remove"));
  rm.type = "button";
  rm.onclick = () => { oa.open = null; oaRoster((w) => w.advisors.splice(i, 1)); };
  const end = el("div", "row oa-adv-end");
  end.append(rm);

  const b = el("div", "oa-adv-body");
  b.append(
    oaRow(t("Name"), "", name),
    oaRow(t("Thinking level"), "", lvl),
    oaRow(t("Runs"), "", runs),
    oaRow(t("Reviews"), "", when),
    oaRow(t("Sees"), "", choice("scope", [{ v: "main", name: "The main agent only", note: "" }, { v: "subagent", name: "Subagents only", note: "" }, { v: "all", name: "The main agent and its subagents", note: "" }], "the main agent and its subagents", "What it reviews")),
    oaRow(t("Backlog sync"), "", choice("syncBacklog", opt("syncBacklog"), "as advisor.syncBacklog", "Advisor Sync Backlog")),
    oaRow(t("Reasoning"), "", choice("includeReasoning", [{ v: "true", name: "Sees the reasoning", note: "" }, { v: "false", name: "Doesn't see the reasoning", note: "" }], "sees the reasoning", "Reasoning")),
    oaRow(t("Notes per update"), "", notes),
    oaRow(t("Tools"), "", tools),
    text,
    end,
  );
  return b;
}

// the pages of their own, each given the sheet, its head, its bar and Done
const OA_PAGES = {
  // WATCHDOG.md, and what WATCHDOG.yml tells every advisor
  notes(ed, head, bar) {
    const layer = oaLayer(), wd = layer.watchdog || {};
    head.append(el("b", "", t("Notes for the advisors")));
    const md = el("textarea", "oa-text");
    md.rows = 6;
    md.value = layer.notes || "";
    md.placeholder = t("What the advisors should especially pay attention to, in Markdown. Empty: no file");
    md.setAttribute("aria-label", "WATCHDOG.md");
    // one there that can't be read isn't written over
    md.disabled = !!layer.notesError;
    const rows = [oaRow("WATCHDOG.md", layer.notesError ? t("{file} can't be read: {err}", { file: layer.notesFile, err: layer.notesError })
      : t("omp reads every WATCHDOG.md, from yours to the project's innermost, and gives the advisors all of them"), md)];
    const shared = el("textarea", "oa-text");
    shared.rows = 3;
    shared.value = wd.instructions || "";
    shared.placeholder = t("What every advisor is told, before its own instructions");
    shared.setAttribute("aria-label", t("Every advisor's instructions"));
    const count = el("input", "words compact-num");
    count.type = "number";
    count.min = 1;
    count.max = 32;
    count.value = wd.maxNotesPerUpdate || "";
    count.placeholder = oaNotesEach();
    count.title = t("as advisor.maxNotesPerUpdate");
    count.setAttribute("aria-label", t("Notes per update, for each advisor"));
    // the roster's shared words, while magpie can write the roster
    const roster = !layer.watchdogError?.length;
    if (roster) rows.push(oaRow(t("Every advisor's instructions"), layer.watchdogFile, shared));
    for (const r of rows) r.classList.add("oa-wide");
    if (roster) rows.push(oaRow(t("Notes per update, for each advisor"), "", count));
    ed.append(oaList(...rows));
    const save = el("button", "primary", t("Save"));
    save.type = "button";
    save.onclick = async () => {
      const ch = {};
      if (!layer.notesError && md.value !== (layer.notes || "")) ch.notes = md.value;
      const n = Math.max(0, Math.min(32, parseInt(count.value, 10) || 0));
      if (roster && (shared.value !== (wd.instructions || "") || n !== (wd.maxNotesPerUpdate || 0))) {
        ch.watchdog = { ...structuredClone(wd), instructions: shared.value || undefined, maxNotesPerUpdate: n || undefined };
        ch.watchdog.advisors ||= [];
      }
      if (!Object.keys(ch).length || await oaWrite(ch)) oaGo("main");
    };
    bar.append(el("span", "grow"), save);
  },

  // task.agentAdvisor: an advisor for each subagent, or not
  subs(ed, head, bar, done) {
    const layer = oaLayer(), user = oa.where === "user";
    head.append(el("b", "", t("Subagents")), el("span", "oa-file", layer.config));
    ed.append(oaList(...oa.view.subagents.map((name) => {
      const now = layer.subagents?.[name] || "";
      const shown = now === "on" || now === "true" ? t("On") : now === "off" || now === "false" ? t("Off") : now ? ompModelName(oa.a, now) : t(user ? "Default" : "As for every project");
      const b = oaChip(shown, !!now);
      b.onclick = oaMenu(b, [{ v: "", name: user ? "Default" : "As for every project", note: user ? "omp's default" : "" },
        { v: "on", name: "On", note: "the advisor's model" }, { v: "off", name: "Off", note: "no advisor for this subagent" },
        { v: "\x01", name: "A model of its own…", note: "" }], ["on", "off", ""].includes(now) ? now : "\x01", (v, ev) => {
        if (v !== "\x01") return oaWrite({ subagents: { [name]: v } });
        oaPickModel(b, now, "", (m) => oaWrite({ subagents: { [name]: m } }))(ev);
      }, "Advisor for this subagent");
      const r = oaRow(name, "", b);
      r.querySelector(".who").title = t("task.agentAdvisor: on, off, or a model for that subagent's advisor");
      return r;
    })));
    bar.append(el("span", "grow"), done);
  },

  // retry.fallbackChains.advisor: what the advisor tries when its model fails
  chain(ed, head, bar, done) {
    const layer = oaLayer(), chain = layer.fallback || [];
    head.append(el("b", "", t("Fallback models")), el("span", "oa-file", layer.config));
    const rows = chain.map((m, i) => {
      const x = el("button", "text", t("Remove"));
      x.type = "button";
      x.onclick = () => oaWrite({ fallback: chain.filter((_, j) => j !== i) });
      return oaRow((i + 1) + ". " + ompModelName(oa.a, m), "", x);
    });
    const add = oaChip(t("Pick"), false);
    add.onclick = oaPickModel(add, "", "", (m) => m && !chain.includes(m) && oaWrite({ fallback: [...chain, m] }));
    rows.push(oaRow(t("Add a model"), t("retry.fallbackChains.advisor: tried in this order when the advisor's model fails"), add));
    ed.append(oaList(...rows));
    bar.append(el("span", "grow"), done);
  },

  // the advisor.* settings the first page doesn't hold
  more(ed, head, bar, done) {
    head.append(el("b", "", t("More settings")), el("span", "oa-file", oaLayer().config));
    ed.append(oaList(...oa.view.settings.filter((s) => !OA_FIRST.includes(s.key)).map(oaSettingRow)));
    bar.append(el("span", "grow"), done);
  },

  // a project folder typed, as omp spells it
  folder(ed, head, bar) {
    head.append(el("b", "", t("Project")));
    const dir = el("input", "words");
    dir.value = oa.view.projectDir || "";
    dir.placeholder = t("A project folder, as omp sees it");
    dir.setAttribute("aria-label", t("Project"));
    const go = () => dir.value.trim() && ompAdvisorOpen(oa.a, dir.value.trim(), "project");
    dir.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") go(); };
    const r = oaRow(t("Project"), "", dir);
    r.classList.add("oa-wide");
    ed.append(oaList(r));
    const open = el("button", "primary", t("Open"));
    open.type = "button";
    open.onclick = go;
    bar.append(el("span", "grow"), open);
  },

  // what is in effect here, each from its file
  effective(ed, head, bar, done) {
    const { a, view } = oa, eff = view.effective;
    head.append(el("b", "", t(view.project ? "In effect in this project" : "In effect")));
    const line = (k, v, from) => oaRow(k, v + " · " + (from || t("omp's default")));
    const rows = view.settings.map((s) => line(t(s.label), oaSaid(s, eff.settings?.[s.key]?.value || s.default), eff.settings?.[s.key]?.from));
    for (const r of eff.roster || []) {
      const bits = [r.model ? ompModelName(a, r.model) : t("the advisor role's model"), oaWhen(r)];
      if (r.enabled === false) bits.push(t("paused"));
      rows.push(line(t("Advisor {name}", { name: r.name }), bits.join(" · "), r.from));
    }
    const onOff = (v) => ["on", "true"].includes(v) ? t("On") : ["off", "false"].includes(v) ? t("Off") : ompModelName(a, v);
    for (const [name, e] of Object.entries(eff.subagents || {})) rows.push(line(t("Advisor for {agent}", { agent: name }), onOff(e.value), e.from));
    if (eff.fallback?.value?.length) rows.push(line(t("Fallback models"), eff.fallback.value.map((m) => ompModelName(a, m)).join(" → "), eff.fallback.from));
    for (const n of eff.notes || []) rows.push(line(t("Notes"), "WATCHDOG.md", n));
    for (const w of eff.warnings || []) ed.append(el("p", "oa-warn", ompSaid(w)));
    ed.append(oaList(...rows));
    bar.append(el("span", "grow"), done);
  },
};
