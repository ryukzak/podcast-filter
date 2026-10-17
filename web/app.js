"use strict";

const $ = (id) => document.getElementById(id);

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const DEBOUNCE_MS = 350;

const el = {
  importInput: $("import"),
  importBtn: $("import-btn"),
  importStatus: $("import-status"),
  feed: $("feed"),
  title: $("title"),
  minMinutes: $("min-minutes"),
  maxMinutes: $("max-minutes"),
  lengthError: $("length-error"),
  rules: $("rules"),
  rulesEmpty: $("rules-empty"),
  addRule: $("add-rule"),
  items: $("items"),
  summary: $("preview-summary"),
  viewRadios: document.querySelectorAll('input[name="view"]'),
  previewError: $("preview-error"),
  inlineUrl: $("inline-url"),
  inlineSize: $("inline-size"),
  inlineHint: $("inline-hint"),
  copyInline: $("copy-inline"),
  inlineEdit: $("inline-edit"),
  shortSection: $("short-section"),
  password: $("password"),
  createShort: $("create-short"),
  shortStatus: $("short-status"),
  storedSection: $("stored-section"),
  storedId: $("stored-id"),
  storedHint: $("stored-hint"),
  updateUrl: $("update-url"),
  copyStored: $("copy-stored"),
  storedEdit: $("stored-edit"),
  updateRow: $("update-row"),
  updatePassword: $("update-password"),
  updateShort: $("update-short"),
  updateStatus: $("update-status"),
  ruleTemplate: $("rule-template"),
  itemTemplate: $("item-template"),
};

const state = {
  maxInlineUrlLength: 512,
  stored: null, // { id, mutable } once a short URL is created or loaded
  // Per-rule compile problems from the last preview, used to mark the chips of
  // rules that did not take part in the verdict.
  ruleErrors: [],
  // What the preview list currently shows, so that a filter edit patches the
  // existing rows instead of rebuilding (and so resetting) the scroll box.
  rendered: { feed: null, titles: [] },
};

// Latest item/filter per row, so an open detail panel can be rebuilt on a
// filter change. A WeakMap keeps nothing alive once the row is replaced.
const rowState = new WeakMap();

let previewTimer = null;
let previewAbort = null;

/* ---------- filter model ---------- */

function currentFilter() {
  return {
    feed: el.feed.value.trim(),
    title: el.title.value.trim(),
    minMinutes: readMinutes(el.minMinutes),
    maxMinutes: readMinutes(el.maxMinutes),
    rules: [...el.rules.querySelectorAll(".rule")].map((rule) => ({
      re: rule.querySelector(".rule-re").value,
      neg: rule.querySelector(".rule-mode").value === "drop",
    })),
  };
}

// readMinutes treats an empty or partial field as "no bound".
function readMinutes(input) {
  const value = Number.parseInt(input.value, 10);
  return Number.isNaN(value) ? 0 : value;
}

// cleanFilter drops the rows the user has not filled in yet. The preview keeps
// every row so that chip indexes line up with the form, but a URL or a stored
// filter should only carry real patterns — an empty pattern matches everything
// and would silently change the result.
function cleanFilter() {
  const filter = currentFilter();
  filter.rules = filter.rules.filter((rule) => rule.re.trim() !== "");
  return filter;
}

// buildInlineUrl produces the same query layout as the server: neg is emitted
// for every rule or for none. The returned string is fully percent-encoded, so
// its length is the length a podcast client will actually see. With path "/"
// the same query opens the builder with this filter loaded.
function buildInlineUrl(filter, path = "/filter") {
  const params = new URLSearchParams();
  params.set("feed", filter.feed);
  if (filter.title) params.set("title", filter.title);

  const anyNegative = filter.rules.some((rule) => rule.neg);
  for (const rule of filter.rules) {
    params.append("re", rule.re);
    if (anyNegative) params.append("neg", String(rule.neg));
  }
  if (filter.minMinutes > 0) params.set("min", String(filter.minMinutes));
  if (filter.maxMinutes > 0) params.set("max", String(filter.maxMinutes));

  return `${location.origin}${path}?${params.toString()}`;
}

/* ---------- rules ---------- */

function addRule(rule = { re: "", neg: false }) {
  const node = el.ruleTemplate.content.cloneNode(true);
  const item = node.querySelector(".rule");

  item.querySelector(".rule-re").value = rule.re;
  item.querySelector(".rule-mode").value = rule.neg ? "drop" : "keep";
  item.querySelector(".rule-remove").addEventListener("click", () => {
    item.remove();
    renumberRules();
    onFilterChange();
  });

  el.rules.appendChild(node);
  renumberRules();
}

function setRules(rules) {
  el.rules.replaceChildren();
  rules.forEach(addRule);
}

function renumberRules() {
  const rules = [...el.rules.querySelectorAll(".rule")];
  rules.forEach((rule, i) => {
    rule.querySelector(".rule-index").textContent = `#${i + 1}`;
  });
  el.rulesEmpty.hidden = rules.length > 0;
}

function showRuleErrors(errors) {
  [...el.rules.querySelectorAll(".rule")].forEach((rule, i) => {
    // An untouched row is obviously incomplete; only report real syntax errors.
    const blank = rule.querySelector(".rule-re").value.trim() === "";
    const problem = blank ? "" : (errors?.[i] ?? "");
    const line = rule.querySelector(".rule-error");
    line.textContent = problem;
    line.hidden = problem === "";
  });
}

/* ---------- preview ---------- */

function onFilterChange() {
  updateUrls();
  schedulePreview();
}

function schedulePreview() {
  clearTimeout(previewTimer);
  previewTimer = setTimeout(runPreview, DEBOUNCE_MS);
}

async function runPreview() {
  const filter = currentFilter();
  if (!filter.feed) {
    el.summary.textContent = "Enter a feed URL to see its episodes.";
    el.previewError.hidden = true;
    el.items.replaceChildren();
    state.rendered = { feed: null, titles: [] };
    return;
  }

  previewAbort?.abort();
  previewAbort = new AbortController();

  try {
    const resp = await fetch("/api/preview", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ filter }),
      signal: previewAbort.signal,
    });
    const data = await resp.json();

    if (!resp.ok) {
      setStatus(el.previewError, data.error || `request failed (${resp.status})`, "error");
      return;
    }

    el.previewError.hidden = true;
    showRuleErrors(data.ruleErrors);
    if (data.lengthError) {
      setStatus(el.lengthError, `${data.lengthError}, so it is ignored`, "error");
    } else {
      el.lengthError.hidden = true;
    }
    state.ruleErrors = data.ruleErrors || [];
    renderPreview(filter, data);
  } catch (err) {
    if (err.name === "AbortError") return;
    setStatus(el.previewError, String(err), "error");
  }
}

function renderPreview(filter, data) {
  el.summary.textContent =
    `${data.kept} pass · ${data.total - data.kept} removed · ${data.total} in the feed` +
    (data.truncated ? ` · only the first ${data.items.length} listed` : "");

  const titles = data.items.map((item) => item.title);
  const sameList =
    state.rendered.feed === filter.feed &&
    state.rendered.titles.length === titles.length &&
    state.rendered.titles.every((title, i) => title === titles[i]);

  if (!sameList) {
    el.items.replaceChildren(...data.items.map(buildRow));
    state.rendered = { feed: filter.feed, titles };
  }

  // Patching in place is what keeps the scroll position pinned while the user
  // edits a pattern: nothing is detached and no row changes height.
  const rows = el.items.children;
  data.items.forEach((item, i) => {
    paintRow(rows[i], item, filter);
  });
}

function buildRow(item) {
  const row = el.itemTemplate.content.cloneNode(true).querySelector(".item");

  row.querySelector(".item-title").textContent = item.title || "(no title)";
  row.querySelector(".item-date").textContent = formatDate(item.pubDate);
  row.querySelector(".item-len").textContent = item.length || "—";
  row.querySelector(".item-line").addEventListener("click", () => toggleRow(row));

  return row;
}

// paintRow carries the verdict with colour alone; the reasoning lives in the
// details panel, which is only rebuilt while it is actually open.
function paintRow(row, item, filter) {
  if (!row) return;

  row.classList.toggle("dropped", !item.passed);
  row.querySelector(".item-len").classList.toggle("culprit", Boolean(item.lengthVerdict));
  row.dataset.state = item.passed ? "passes" : "dropped";
  rowState.set(row, { item, filter });

  row.querySelector(".chips").replaceChildren(
    ...item.matches.map((matched, i) => {
      const chip = document.createElement("span");
      chip.className = "chip";
      if (ruleIsUnused(i)) chip.classList.add("broken");
      else if (matched) chip.classList.add("matched");
      if (i === item.rejectedBy) chip.classList.add("culprit");
      chip.textContent = String(i + 1);
      chip.title = describeRule(filter.rules[i], i, matched, i === item.rejectedBy);
      // No keep rule matched, so they are collectively why it is out.
      if (!item.passed && !item.lengthVerdict && item.rejectedBy === -1 && !filter.rules[i].neg) {
        chip.classList.add("unmet");
      }
      return chip;
    }),
  );

  if (row.classList.contains("open")) renderDetails(row);
}

function toggleRow(row) {
  const open = row.classList.toggle("open");
  row.querySelector(".item-line").setAttribute("aria-expanded", String(open));
  row.querySelector(".item-details").hidden = !open;
  if (open) renderDetails(row);
}

function renderDetails(row) {
  const entry = rowState.get(row);
  if (!entry) return;
  const { item, filter } = entry;

  const details = row.querySelector(".item-details");
  details.replaceChildren();

  addDetail(
    details,
    "Verdict",
    verdictText(item, filter),
    item.passed ? "verdict-keep" : "verdict-drop",
  );
  addDetail(details, "Title", item.title || "(no title)");
  if (item.pubDate) addDetail(details, "Published", item.pubDate);
  if (item.length)
    addDetail(details, "Length", item.length, item.lengthVerdict ? "verdict-drop" : "");
  if (item.author) addDetail(details, "Author", item.author);
  if (item.subtitle) addDetail(details, "About", item.subtitle);

  if (item.link) {
    const link = document.createElement("a");
    link.href = item.link;
    link.target = "_blank";
    link.rel = "noreferrer";
    link.textContent = item.link;
    addDetail(details, "Link", link);
  }

  if (filter.rules.length > 0) {
    const list = document.createElement("ul");
    list.className = "rule-list";
    item.matches.forEach((matched, i) => {
      const entryLine = document.createElement("li");
      if (matched && !ruleIsUnused(i)) entryLine.classList.add("matched");
      if (i === item.rejectedBy) entryLine.classList.add("culprit");
      entryLine.textContent = describeRule(filter.rules[i], i, matched, i === item.rejectedBy);
      list.append(entryLine);
    });
    addDetail(details, "Filters", list);
  }
}

// A rule that is blank or does not compile took no part in the verdict, and
// saying so is more useful than reporting it as a pattern that did not match.
function ruleIsUnused(index) {
  return Boolean(state.ruleErrors[index]);
}

// An episode is out for one of two reasons: a drop filter excluded it, or no
// keep filter ever took it.
function verdictText(item, filter) {
  if (item.passed) return "passes";
  if (item.lengthVerdict === "short") return `shorter than ${filter.minMinutes} min`;
  if (item.lengthVerdict === "long") return `longer than ${filter.maxMinutes} min`;
  if (item.rejectedBy >= 0) return `dropped by filter #${item.rejectedBy + 1}`;
  return "no keep filter matched it";
}

function describeRule(rule, index, matched, culprit) {
  if (ruleIsUnused(index)) {
    const reason = rule.re.trim() === "" ? "not set yet" : state.ruleErrors[index];
    return `#${index + 1} — ${reason}, so it is ignored`;
  }

  const action = rule.neg ? "drop if matching" : "keep if matching";
  const result = matched ? "matched" : "did not match";
  return `#${index + 1} ${action} /${rule.re}/ — ${result}${culprit ? ", so this episode is out" : ""}`;
}

function addDetail(details, label, value, className) {
  const dt = document.createElement("dt");
  dt.textContent = label;

  const dd = document.createElement("dd");
  if (className) dd.className = className;
  if (typeof value === "string") dd.textContent = value;
  else dd.append(value);

  details.append(dt, dd);
}

// Dates are a narrow column, so the year is only spelled out when it is not
// the current one.
function formatDate(pubDate) {
  if (!pubDate) return "—";

  const parsed = new Date(pubDate);
  if (Number.isNaN(parsed.getTime())) return pubDate;

  const iso = parsed.toISOString().slice(0, 10);
  return iso.startsWith(`${new Date().getUTCFullYear()}-`) ? iso.slice(5) : iso;
}

/* ---------- result URLs ---------- */

// syncPageUrl keeps the address bar on the builder link for what is on the
// screen, so a reload or a bookmark comes back to it. A stored link keeps its
// id while it is loaded, since that is what the update form works on.
function syncPageUrl() {
  const filter = cleanFilter();
  let url = `${location.origin}/`;
  if (state.stored) url += `?${new URLSearchParams({ id: state.stored.id })}`;
  else if (filter.feed) url = buildInlineUrl(filter, "/");
  if (url !== location.href) history.replaceState(null, "", url);
}

function updateUrls() {
  syncPageUrl();
  const filter = cleanFilter();

  if (!filter.feed) {
    el.inlineUrl.value = "";
    el.inlineSize.textContent = "";
    el.inlineSize.classList.remove("over");
    el.inlineHint.textContent = "Enter a feed URL first.";
    el.inlineEdit.parentElement.hidden = true;
    el.shortSection.classList.remove("recommended");
    el.createShort.disabled = true;
    return;
  }

  const url = buildInlineUrl(filter);
  const tooLong = url.length > state.maxInlineUrlLength;

  el.inlineUrl.value = url;
  el.inlineSize.textContent = `${url.length} / ${state.maxInlineUrlLength} chars`;
  el.inlineSize.classList.toggle("over", tooLong);
  el.inlineHint.textContent = tooLong
    ? "This URL is longer than most podcast clients handle reliably — use a short URL below instead."
    : "Short enough to paste straight into a podcast client. No server-side state involved.";

  el.inlineEdit.href = buildInlineUrl(filter, "/");
  el.inlineEdit.parentElement.hidden = false;

  el.shortSection.classList.toggle("recommended", tooLong);
  el.createShort.disabled = false;
}

function showStored(link, note, kind) {
  state.stored = { id: link.id, mutable: link.mutable };
  syncPageUrl();

  el.storedSection.hidden = false;
  el.storedId.textContent = link.id;
  el.updateUrl.value = location.origin + link.path;
  el.storedEdit.href = `${location.origin}/?${new URLSearchParams({ id: link.id })}`;
  el.updateRow.hidden = !link.mutable;
  el.storedHint.textContent = link.mutable
    ? "Password protected: edit the filter above and press «Update this URL» to change what this URL serves."
    : "Created without a password, so it is immutable — create a new short URL to change the filter.";

  if (note) setStatus(el.updateStatus, note, kind);
  else el.updateStatus.hidden = true;
}

async function createShortUrl() {
  const filter = cleanFilter();
  el.createShort.disabled = true;
  try {
    const link = await callLinkAPI("/api/links", "POST", {
      filter,
      password: el.password.value,
    });
    el.password.value = "";
    el.shortStatus.hidden = true;
    showStored(link, "Short URL created.", "ok");
    el.updateUrl.scrollIntoView({ behavior: "smooth", block: "center" });
  } catch (err) {
    setStatus(el.shortStatus, err.message, "error");
  } finally {
    el.createShort.disabled = false;
  }
}

async function updateShortUrl() {
  el.updateShort.disabled = true;
  try {
    const link = await callLinkAPI(`/api/links/${encodeURIComponent(state.stored.id)}`, "PUT", {
      filter: cleanFilter(),
      password: el.updatePassword.value,
    });
    showStored(link, `Updated — this URL now serves the filter above.`, "ok");
  } catch (err) {
    setStatus(el.updateStatus, err.message, "error");
  } finally {
    el.updateShort.disabled = false;
  }
}

async function callLinkAPI(path, method, body) {
  const resp = await fetch(path, {
    method,
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  const data = await resp.json().catch(() => ({}));
  if (!resp.ok) throw new Error(data.error || `request failed (${resp.status})`);
  return data;
}

/* ---------- importing an existing link ---------- */

// importLink accepts the three things a user is likely to paste: a stored link
// (or a bare id), one of our own /filter URLs, or just a podcast feed URL.
async function importLink() {
  const raw = el.importInput.value.trim();
  if (!raw) return;

  if (await loadFrom(raw)) el.importInput.value = "";
}

async function loadFrom(raw) {
  try {
    const id = extractLinkID(raw);
    if (id) {
      const link = await callLinkAPI(`/api/links/${encodeURIComponent(id)}`, "GET", undefined);
      applyFilter(link.filter);
      showStored(link, null);
      setStatus(el.importStatus, `Loaded stored filter ${link.id}.`, "ok");
    } else if (isPlainFeedURL(raw)) {
      // Nothing to import, so keep whatever filters are already in the form.
      el.feed.value = raw;
      reloadPreview();
      setStatus(el.importStatus, "Loaded the feed URL — your filters were kept.", "ok");
    } else {
      const filter = parseInlineFilter(raw);
      el.storedSection.hidden = true;
      state.stored = null;
      applyFilter(filter);
      setStatus(el.importStatus, "Loaded the filter from the URL.", "ok");
    }
    return true;
  } catch (err) {
    setStatus(el.importStatus, err.message, "error");
    return false;
  }
}

// loadFromPageURL opens the builder on the filter in the page's own query:
// /?id=<guid> for a stored link, or /?feed=…&re=… for an inline one.
async function loadFromPageURL() {
  const params = new URLSearchParams(location.search);
  if (!params.get("id") && !params.get("feed")) return false;
  await loadFrom(location.href);
  return true;
}

// A URL with no filter in it is an origin feed, not a filtered feed.
function isPlainFeedURL(raw) {
  if (!/^https?:\/\//i.test(raw)) return false;
  try {
    const params = new URL(raw).searchParams;
    return (
      !params.get("feed") &&
      params.getAll("re").length === 0 &&
      !params.get("min") &&
      !params.get("max")
    );
  } catch {
    return false;
  }
}

function extractLinkID(raw) {
  if (UUID_RE.test(raw)) return raw;
  try {
    return new URL(raw, location.origin).searchParams.get("id");
  } catch {
    return null;
  }
}

function parseInlineFilter(raw) {
  let params;
  try {
    params = new URL(raw, location.origin).searchParams;
  } catch {
    throw new Error("that does not look like a URL or a link id");
  }

  const res = params.getAll("re");
  const negs = params.getAll("neg");
  if (negs.length > 0 && negs.length !== res.length) {
    throw new Error("the URL has a different number of re and neg parameters");
  }
  if (!params.get("feed")) {
    throw new Error("that does not look like a feed URL, a filtered URL, or a link id");
  }

  return {
    feed: params.get("feed"),
    title: params.get("title") || "",
    minMinutes: Number.parseInt(params.get("min"), 10) || 0,
    maxMinutes: Number.parseInt(params.get("max"), 10) || 0,
    rules: res.map((re, i) => ({ re, neg: negs.length > 0 && negs[i] === "true" })),
  };
}

function applyFilter(filter) {
  el.feed.value = filter.feed || "";
  el.title.value = filter.title || "";
  el.minMinutes.value = filter.minMinutes || "";
  el.maxMinutes.value = filter.maxMinutes || "";
  setRules(filter.rules || []);
  // A different feed means a different episode list, so drop the patching cache.
  reloadPreview();
}

// reloadPreview drops the row cache and previews right away, skipping the
// debounce that only exists to absorb typing.
function reloadPreview() {
  state.rendered = { feed: null, titles: [] };
  updateUrls();
  clearTimeout(previewTimer);
  runPreview();
}

/* ---------- misc ---------- */

function applyView(view) {
  el.items.classList.toggle("view-passing", view === "passing");
  el.items.classList.toggle("view-removed", view === "removed");
}

function setStatus(node, text, kind) {
  node.textContent = text;
  node.classList.toggle("error", kind === "error");
  node.classList.toggle("ok", kind === "ok");
  node.hidden = false;
}

async function copyFrom(input, button) {
  if (!input.value) return;
  const original = button.textContent;
  try {
    await navigator.clipboard.writeText(input.value);
  } catch {
    input.select();
    document.execCommand("copy");
  }
  button.textContent = "Copied";
  setTimeout(() => (button.textContent = original), 1200);
}

async function loadConfig() {
  try {
    const resp = await fetch("/api/config");
    if (!resp.ok) return;
    const config = await resp.json();
    if (config.maxInlineUrlLength) state.maxInlineUrlLength = config.maxInlineUrlLength;
  } catch {
    // The built-in default is good enough if the server did not answer.
  }
}

function wire() {
  el.addRule.addEventListener("click", () => {
    addRule();
    el.rules.querySelector(".rule:last-child .rule-re")?.focus();
    onFilterChange();
  });

  // One listener on the container covers the rule inputs added later on.
  el.rules.addEventListener("input", onFilterChange);
  el.rules.addEventListener("change", onFilterChange);

  el.feed.addEventListener("input", onFilterChange);
  el.title.addEventListener("input", onFilterChange);
  el.minMinutes.addEventListener("input", onFilterChange);
  el.maxMinutes.addEventListener("input", onFilterChange);

  el.importBtn.addEventListener("click", importLink);
  el.importInput.addEventListener("keydown", (e) => {
    if (e.key === "Enter") importLink();
  });

  el.createShort.addEventListener("click", createShortUrl);
  el.updateShort.addEventListener("click", updateShortUrl);
  for (const radio of el.viewRadios) {
    radio.addEventListener("change", () => {
      if (radio.checked) applyView(radio.value);
    });
  }

  el.copyInline.addEventListener("click", () => copyFrom(el.inlineUrl, el.copyInline));
  el.copyStored.addEventListener("click", () => copyFrom(el.updateUrl, el.copyStored));
}

async function main() {
  wire();
  await loadConfig();
  if (await loadFromPageURL()) return;
  // Start with one empty filter so the shape of the form is obvious.
  addRule();
  updateUrls();
}

main();
