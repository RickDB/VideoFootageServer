// Shared UI helpers: fuzzy-matching timezone combobox and a generic
// client-side paginated/filterable table, reused across the dashboard,
// site form, export wizard, and the Uptime Kuma import wizard.
(function () {
  "use strict";

  function escRe(s) {
    return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  }

  // Substring match first, then a `*` wildcard pattern, then a loose
  // subsequence fuzzy match so "ams" still finds "Europe/Amsterdam".
  function fuzzyMatch(hay, query) {
    if (!query) return true;
    const h = String(hay).toLowerCase();
    const q = String(query).toLowerCase().trim();
    if (!q) return true;
    if (h.includes(q)) return true;
    if (q.includes("*")) {
      try {
        if (new RegExp("^" + q.split("*").map(escRe).join(".*") + "$", "i").test(hay)) return true;
      } catch (e) {
        /* ignore bad pattern */
      }
    }
    let qi = 0;
    for (let i = 0; i < h.length && qi < q.length; i++) {
      if (h[i] === q[qi]) qi++;
    }
    return qi === q.length;
  }

  // ---------- Timezone combobox ----------
  let tzPromise = null;
  function loadTimezones() {
    if (!tzPromise) {
      tzPromise = fetch("/api/timezones")
        .then((r) => r.json())
        .catch(() => ["UTC"]);
    }
    return tzPromise;
  }

  function initTimezoneCombo(input) {
    if (!input || input.dataset.hfInit) return;
    input.dataset.hfInit = "1";
    input.setAttribute("autocomplete", "off");
    const wrap = document.createElement("div");
    wrap.className = "hf-combo";
    input.parentNode.insertBefore(wrap, input);
    wrap.appendChild(input);
    const list = document.createElement("div");
    list.className = "hf-combo-list";
    list.hidden = true;
    wrap.appendChild(list);
    let zones = [];
    loadTimezones().then((z) => (zones = z));

    function render() {
      const q = input.value;
      const matches = (q ? zones.filter((z) => fuzzyMatch(z, q)) : zones).slice(0, 60);
      list.innerHTML = "";
      if (!matches.length) {
        list.hidden = true;
        return;
      }
      for (const z of matches) {
        const opt = document.createElement("div");
        opt.className = "hf-combo-opt";
        opt.textContent = z;
        opt.onmousedown = (e) => {
          e.preventDefault();
          input.value = z;
          list.hidden = true;
          input.dispatchEvent(new Event("change"));
        };
        list.appendChild(opt);
      }
      list.hidden = false;
    }
    input.addEventListener("focus", render);
    input.addEventListener("input", render);
    input.addEventListener("blur", () => setTimeout(() => (list.hidden = true), 150));
  }

  function initAllTimezonePickers(root) {
    (root || document).querySelectorAll("[data-tz-picker]").forEach(initTimezoneCombo);
  }

  // ---------- Paginated / filterable table ----------
  // opts: {
  //   root: element, rows: [{..., _id}], columns: [{key,label,render?(row)}],
  //   pageSize, searchKeys: [...], searchPlaceholder,
  //   filters: [{key,label,options:[{value,label}],match(row,value)}],
  //   selection: 'none'|'single'|'multi', selected: Set, name,
  //   autoSelectSingle: true selects the lone row of a single-select table
  //   with nothing else to choose from, without waiting for a click,
  //   onSelectionChange(idsArray), rowHref(row), emptyText
  // }
  function HFTable(opts) {
    // A Go nil slice marshals to JSON `null`, not `[]` — an empty table
    // (no jobs yet, no recorders yet, …) must not crash the renderer.
    // A row also needs a stable _id for selection (radio/checkbox) mode to
    // track and report which rows are picked — callers that already carry
    // one (e.g. the Uptime Kuma import wizard's synthetic rows) keep it,
    // everyone else gets it derived from the row's own id/ID field (Go
    // JSON marshals a struct's ID field as "ID", so that's the common
    // case — a raw site/NVR/job row straight off the wire).
    opts.rows = (opts.rows || []).map((r) => (r._id !== undefined ? r : { ...r, _id: r.ID ?? r.id }));
    const root = opts.root;
    root.innerHTML = "";
    root.classList.add("hf-table");
    const bar = document.createElement("div");
    bar.className = "hf-table-bar";
    const search = document.createElement("input");
    search.type = "search";
    search.placeholder = opts.searchPlaceholder || "Filter…";
    bar.appendChild(search);
    const filterEls = [];
    (opts.filters || []).forEach((f) => {
      const sel = document.createElement("select");
      const optAll = document.createElement("option");
      optAll.value = "";
      optAll.textContent = f.label;
      sel.appendChild(optAll);
      f.options.forEach((o) => {
        const oe = document.createElement("option");
        oe.value = o.value;
        oe.textContent = o.label;
        sel.appendChild(oe);
      });
      sel.dataset.key = f.key;
      bar.appendChild(sel);
      filterEls.push({ sel, f });
    });
    root.appendChild(bar);
    const wrap = document.createElement("div");
    wrap.className = "table-wrap";
    const table = document.createElement("table");
    const thead = document.createElement("thead");
    const trh = document.createElement("tr");
    const hasSelect = opts.selection && opts.selection !== "none";
    if (hasSelect) {
      const th = document.createElement("th");
      th.style.width = "1%";
      trh.appendChild(th);
    }
    opts.columns.forEach((c) => {
      const th = document.createElement("th");
      th.textContent = c.label;
      trh.appendChild(th);
    });
    thead.appendChild(trh);
    table.appendChild(thead);
    const tbody = document.createElement("tbody");
    table.appendChild(tbody);
    wrap.appendChild(table);
    root.appendChild(wrap);
    const pager = document.createElement("div");
    pager.className = "hf-pager";
    root.appendChild(pager);

    let page = 0;
    const pageSize = opts.pageSize || 10;
    const selected = opts.selected || new Set();

    function filteredRows() {
      let rows = opts.rows;
      const q = search.value.trim();
      if (q) rows = rows.filter((r) => (opts.searchKeys || []).some((k) => fuzzyMatch(r[k] ?? "", q)));
      filterEls.forEach(({ sel, f }) => {
        if (sel.value) rows = rows.filter((r) => f.match(r, sel.value));
      });
      return rows;
    }

    function renderRows() {
      const rows = filteredRows();
      const totalPages = Math.max(1, Math.ceil(rows.length / pageSize));
      if (page >= totalPages) page = totalPages - 1;
      if (page < 0) page = 0;
      const slice = rows.slice(page * pageSize, (page + 1) * pageSize);
      tbody.innerHTML = "";
      if (!slice.length) {
        const tr = document.createElement("tr");
        const td = document.createElement("td");
        td.colSpan = opts.columns.length + (hasSelect ? 1 : 0);
        td.className = "hf-empty";
        td.textContent = opts.emptyText || "No results.";
        tr.appendChild(td);
        tbody.appendChild(tr);
      }
      for (const row of slice) {
        const tr = document.createElement("tr");
        let input = null;
        if (hasSelect) {
          const td = document.createElement("td");
          input = document.createElement("input");
          input.type = opts.selection === "single" ? "radio" : "checkbox";
          if (opts.selection === "single") input.name = opts.name || "hf-select";
          input.checked = selected.has(row._id);
          input.onchange = () => {
            if (opts.selection === "single") selected.clear();
            if (input.checked) selected.add(row._id);
            else selected.delete(row._id);
            renderRows();
            if (opts.onSelectionChange) opts.onSelectionChange([...selected]);
          };
          td.appendChild(input);
          tr.appendChild(td);
          tr.classList.add("hf-row-link");
          tr.onclick = (e) => {
            if (e.target === input || e.target.closest("a,button,form,select,textarea,label")) return;
            input.checked = opts.selection === "single" ? true : !input.checked;
            input.dispatchEvent(new Event("change"));
          };
        } else if (opts.rowHref) {
          tr.classList.add("hf-row-link");
          tr.onclick = (e) => {
            if (e.target.closest("a,button,form,select,textarea,label,input")) return;
            window.location.href = opts.rowHref(row);
          };
        }
        opts.columns.forEach((c) => {
          const td = document.createElement("td");
          if (c.render) td.innerHTML = c.render(row);
          else td.textContent = row[c.key] ?? "";
          tr.appendChild(td);
        });
        if (selected.has(row._id)) tr.classList.add("hf-selected");
        tbody.appendChild(tr);
      }
      pager.innerHTML = "";
      const info = document.createElement("span");
      info.className = "hf-pager-info";
      info.textContent = rows.length ? `${page * pageSize + 1}–${Math.min(rows.length, (page + 1) * pageSize)} of ${rows.length}` : "0 of 0";
      pager.appendChild(info);
      const prev = document.createElement("button");
      prev.type = "button";
      prev.className = "tiny";
      prev.textContent = "← Prev";
      prev.disabled = page <= 0;
      prev.onclick = () => {
        page--;
        renderRows();
      };
      const next = document.createElement("button");
      next.type = "button";
      next.className = "tiny";
      next.textContent = "Next →";
      next.disabled = page >= totalPages - 1;
      next.onclick = () => {
        page++;
        renderRows();
      };
      pager.appendChild(prev);
      pager.appendChild(next);
    }
    search.oninput = () => {
      page = 0;
      renderRows();
    };
    filterEls.forEach(({ sel }) => (sel.onchange = () => {
      page = 0;
      renderRows();
    }));
    // A single-select table with exactly one row to choose from (e.g. an
    // account limited to one site) shouldn't force a click just to advance
    // past a foregone conclusion — pick it for the user up front.
    if (opts.selection === "single" && opts.autoSelectSingle && opts.rows.length === 1 && !selected.size) {
      selected.add(opts.rows[0]._id);
      if (opts.onSelectionChange) opts.onSelectionChange([...selected]);
    }
    renderRows();
    return {
      refresh: renderRows,
      getSelected: () => [...selected],
      setRows: (rows) => {
        opts.rows = (rows || []).map((r) => (r._id !== undefined ? r : { ...r, _id: r.ID ?? r.id }));
        renderRows();
      },
    };
  }

  // ---------- Simple tabs ----------
  function initTabs(root) {
    const buttons = [...root.querySelectorAll("[data-tab]")];
    const panels = [...root.querySelectorAll("[data-tab-panel]")];
    function activate(name) {
      buttons.forEach((b) => b.classList.toggle("active", b.dataset.tab === name));
      panels.forEach((p) => (p.hidden = p.dataset.tabPanel !== name));
    }
    buttons.forEach((b) => (b.onclick = () => activate(b.dataset.tab)));
    if (buttons.length) activate(buttons[0].dataset.tab);
  }
  function initAllTabs(root) {
    (root || document).querySelectorAll("[data-tabs]").forEach(initTabs);
  }

  // ---------- Camera snapshot thumbnails ----------
  // A small preview keeps the camera picker from being overcrowded; the
  // full-size image is only ever a click away in a modal. Thumbnails for a
  // form are all requested in parallel (independent fetches, so one slow or
  // offline camera never blocks the others) and the server caches each
  // image for 4 hours, so re-opening the same form is instant.
  function snapshotURL(cameraId) {
    return "/api/cameras/" + encodeURIComponent(cameraId) + "/snapshot";
  }
  function itemThumbURL(itemId) {
    return "/api/job-items/" + encodeURIComponent(itemId) + "/thumbnail";
  }

  function ensureSnapshotModal() {
    let modal = document.querySelector("#hf-snap-modal");
    if (modal) return modal;
    modal = document.createElement("div");
    modal.id = "hf-snap-modal";
    modal.className = "hf-snap-modal";
    modal.innerHTML =
      '<div class="hf-snap-modal-backdrop"></div><div class="hf-snap-modal-body"><button type="button" class="hf-snap-modal-close" aria-label="Close preview">&times;</button><img alt=""><div class="hf-snap-modal-caption"></div></div>';
    document.body.appendChild(modal);
    const close = () => modal.classList.remove("open");
    modal.querySelector(".hf-snap-modal-backdrop").onclick = close;
    modal.querySelector(".hf-snap-modal-close").onclick = close;
    document.addEventListener("keydown", (e) => {
      if (e.key === "Escape") close();
    });
    return modal;
  }

  function openSnapshot(url, caption) {
    if (!url) return;
    const modal = ensureSnapshotModal();
    modal.querySelector("img").src = url;
    modal.querySelector(".hf-snap-modal-caption").textContent = caption || "";
    modal.classList.add("open");
  }

  // Returns a small <span> wrapping a not-yet-loaded thumbnail <img>; insert
  // it next to a camera's label, then call loadSnapshots() once every
  // thumbnail for the form is in the DOM.
  function cameraThumb(cameraId, caption) {
    const wrap = document.createElement("span");
    wrap.className = "cam-thumb-wrap";
    const img = document.createElement("img");
    img.className = "cam-thumb hf-snap-loading";
    img.alt = caption ? caption + " preview" : "Camera preview";
    img.dataset.cameraId = cameraId;
    img.dataset.caption = caption || "";
    wrap.appendChild(img);
    return wrap;
  }

  // Same small click-to-enlarge thumbnail, sourced from the first frame of
  // a completed job's secured output file instead of a live recorder
  // snapshot — used on the job and Downloads pages.
  function jobItemThumb(itemId, caption) {
    const wrap = document.createElement("span");
    wrap.className = "cam-thumb-wrap";
    const img = document.createElement("img");
    img.className = "cam-thumb hf-snap-loading";
    img.alt = caption ? caption + " preview" : "Secured footage preview";
    img.dataset.kind = "item";
    img.dataset.itemId = itemId;
    img.dataset.caption = caption || "";
    wrap.appendChild(img);
    return wrap;
  }

  function loadSnapshots(root) {
    const imgs = [...(root || document).querySelectorAll("img.cam-thumb.hf-snap-loading")];
    return Promise.all(
      imgs.map(async (img) => {
        try {
          const fetchURL = img.dataset.kind === "item" ? itemThumbURL(img.dataset.itemId) : snapshotURL(img.dataset.cameraId);
          const res = await fetch(fetchURL);
          if (!res.ok) throw new Error("preview unavailable");
          const blob = await res.blob();
          const url = URL.createObjectURL(blob);
          img.src = url;
          img.classList.remove("hf-snap-loading");
          img.onclick = () => openSnapshot(url, img.dataset.caption);
        } catch (e) {
          // A 1x1 transparent gif so the browser has something to decode
          // successfully — otherwise a missing/broken src draws its own
          // fallback icon plus the alt text, which isn't clipped to the
          // thumbnail box and can overflow into neighboring rows.
          img.src = "data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBTAA7";
          img.classList.remove("hf-snap-loading");
          img.classList.add("hf-snap-error");
        }
      })
    );
  }

  // ---------- Camera picker (tile grid, paginated) ----------
  // Renders cameras as bigger click-anywhere tiles (checkbox + a real-sized
  // preview + name) instead of a cramped checkbox list, and paginates so a
  // site with a full 32-camera recorder stays usable instead of turning
  // into one long scroll. Selection state survives paging, since re-picking
  // a page rebuilds only that page's DOM.
  function cameraPickerEsc(v) {
    const d = document.createElement("div");
    d.textContent = v ?? "";
    return d.innerHTML;
  }

  function cameraPicker(container, cameras, opts) {
    opts = opts || {};
    const pageSize = opts.pageSize || 16;
    const key = opts.key || ((c) => String(c.id));
    const thumbId = opts.thumbId || ((c) => c.id);
    const title = opts.title || ((c) => c.name);
    const subtitle = opts.subtitle || null;
    const onChange = opts.onChange || function () {};
    const checked = new Set(opts.initialChecked || []);
    const pages = Math.max(1, Math.ceil(cameras.length / pageSize));
    let page = 0;

    function render() {
      container.innerHTML = "";
      if (!cameras.length) {
        container.innerHTML = '<p class="muted">No cameras are configured for this site yet.</p>';
        return;
      }
      const grid = document.createElement("div");
      grid.className = "cam-tile-grid";
      const start = page * pageSize;
      for (const c of cameras.slice(start, start + pageSize)) {
        const k = key(c);
        const tile = document.createElement("label");
        tile.className = "cam-tile";
        const media = document.createElement("span");
        media.className = "cam-tile-media";
        media.appendChild(cameraThumb(thumbId(c), title(c)));
        const cb = document.createElement("input");
        cb.type = "checkbox";
        cb.className = "cam-tile-check";
        cb.dataset.key = k;
        cb.checked = checked.has(k);
        cb.onchange = () => {
          if (cb.checked) checked.add(k);
          else checked.delete(k);
          tile.classList.toggle("checked", cb.checked);
          onChange();
        };
        tile.classList.toggle("checked", cb.checked);
        const info = document.createElement("span");
        info.className = "cam-tile-info";
        info.innerHTML = `<b>${cameraPickerEsc(title(c))}</b>${subtitle ? `<small>${cameraPickerEsc(subtitle(c))}</small>` : ""}`;
        tile.append(media, cb, info);
        grid.appendChild(tile);
      }
      container.appendChild(grid);
      if (pages > 1) {
        const pager = document.createElement("div");
        pager.className = "cam-pager";
        const prev = document.createElement("button");
        prev.type = "button";
        prev.className = "tiny";
        prev.textContent = "← Prev";
        prev.disabled = page === 0;
        prev.onclick = () => {
          page--;
          render();
        };
        const label = document.createElement("span");
        label.textContent = `Page ${page + 1} of ${pages} · ${cameras.length} cameras`;
        const next = document.createElement("button");
        next.type = "button";
        next.className = "tiny";
        next.textContent = "Next →";
        next.disabled = page >= pages - 1;
        next.onclick = () => {
          page++;
          render();
        };
        pager.append(prev, label, next);
        container.appendChild(pager);
      }
      loadSnapshots(container);
    }
    render();
    return {
      selected: () => cameras.filter((c) => checked.has(key(c))),
      selectedKeys: () => [...checked],
    };
  }

  // ---------- Status badges ----------
  // A job's two terminal states are shown as a themed check or cross rather
  // than spelling the word out: the pill's color already carries the
  // meaning (green/red), so "completed"/"failed" repeated down a whole
  // column of jobs reads as cramped, noisy text. Non-terminal states
  // (queued, downloading, verifying, starting) keep their word — there's
  // no single obvious icon for "downloading" — and unrelated reuses of the
  // same CSS classes elsewhere (recorder online/offline, request status,
  // ad-hoc wizard messages) are untouched since this only ever renders a
  // job's own status value.
  const JOB_STATUS_ICON = { completed: "✓", failed: "✗" };
  function jobStatusBadge(status) {
    const s = String(status || "");
    const icon = JOB_STATUS_ICON[s];
    const label = s.charAt(0).toUpperCase() + s.slice(1);
    if (icon) return `<span class="status ${s} status-icon" title="${label}" aria-label="${label}">${icon}</span>`;
    return `<span class="status ${s}">${s}</span>`;
  }

  // ---------- Nav dropdown ----------
  function initDropdowns() {
    document.querySelectorAll(".nav-dropdown").forEach((dd) => {
      const btn = dd.querySelector(".nav-dd-btn");
      if (!btn) return;
      btn.onclick = (e) => {
        e.stopPropagation();
        document.querySelectorAll(".nav-dropdown.open").forEach((o) => {
          if (o !== dd) o.classList.remove("open");
        });
        dd.classList.toggle("open");
      };
    });
    document.addEventListener("click", () => document.querySelectorAll(".nav-dropdown.open").forEach((d) => d.classList.remove("open")));
  }

  window.HF = { fuzzyMatch, initTimezoneCombo, initAllTimezonePickers, Table: HFTable, initTabs, initAllTabs, cameraThumb, jobItemThumb, loadSnapshots, openSnapshot, cameraPicker, jobStatusBadge };
  document.addEventListener("DOMContentLoaded", () => {
    initAllTimezonePickers();
    initAllTabs();
    initDropdowns();
  });
})();
