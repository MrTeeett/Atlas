import { api } from "../api.js";
import { el, svg } from "../dom.js";
import { fmtBytes, fmtDate } from "../format.js";
import { t } from "../i18n.js";
import { state } from "../state.js";
import { icons } from "../icons.js";

export async function renderFiles(root) {
  const fm = {
    path: "/",
    back: [],
    forward: [],
    entries: [],
    selected: new Set(),
    lastIndex: null,
    view: localStorage.getItem("atlas.fm.view") || "grid",
    search: "",
    addressEdit: false,
    ctx: null,
    modal: null,
    fsUser: localStorage.getItem("atlas.fm.fsuser") || "self",
    fsSelfName: "self",
    fsAny: false,
    fsAllowed: ["self"],
  };

  const sidebar = el("aside", { class: "fm-sidebar" });
  const main = el("div", { class: "fm-main" });
  const layout = el("div", { class: "fm" }, sidebar, main);
  root.append(layout);

  const btnBack = iconBtn(t("files.backTitle"), icons.back, () => goBack());
  const btnForward = iconBtn(t("files.forwardTitle"), icons.forward, () => goForward());
  const btnUp = iconBtn(t("files.upTitle"), icons.up, () => goUp());
  const btnRefresh = iconBtn(t("files.refreshTitle"), icons.refresh, () => refresh());

  const addressWrap = el("div", { class: "fm-address" });
  const crumbs = el("div", { class: "fm-breadcrumbs" });
  const pathInput = el("input", { class: "fm-path-input", style: "display:none", value: "/" });
  addressWrap.append(crumbs, pathInput);

  const filePicker = el("input", { type: "file", multiple: "multiple" });
  const btnNewFolder = iconBtn(t("files.newFolderTitle"), icons.plus, () => newFolder());
  const btnNewFile = iconBtn(t("files.newFileTitle"), icons.filePlus, () => newFile());
  const btnUpload = iconBtn(t("files.uploadTitle"), icons.upload, () => filePicker.click());
  const btnRename = iconBtn(t("files.renameTitle"), icons.edit, () => renameSelected());
  const btnDelete = iconBtn(t("files.deleteTitle"), icons.trash, () => deleteSelected());
  const btnView = iconBtn(t("files.viewTitle"), icons.grid, () => toggleView());

  const search = el("input", { class: "fm-search", placeholder: t("files.searchPlaceholder"), value: "" });
  const searchIcon = el("span", { class: "pill", title: t("files.searchTitle") }, svg(icons.search));

  const fsUserLabel = el("span", { class: "pill", title: t("files.fsContextTitle") }, "FS");
  const fsUserSelect = el("select", { title: t("files.fsUserSelectTitle") });

  const toolbar = el(
    "div",
    { class: "fm-toolbar" },
    btnBack,
    btnForward,
    btnUp,
    btnRefresh,
    addressWrap,
    searchIcon,
    search,
    el("span", { class: "pill", title: t("files.atlasRootHint") }, "ATLAS_ROOT"),
    el("span", { style: "flex:1" }),
    fsUserLabel,
    fsUserSelect,
    btnNewFolder,
    btnNewFile,
    btnUpload,
    btnRename,
    btnDelete,
    btnView,
    filePicker,
  );

  const content = el("div", { class: "fm-content" });
  const drop = el("div", { class: "fm-drop" }, el("span", {}, t("files.dropHint")));
  content.append(drop);
  const status = el("div", { class: "fm-status" });

  main.append(toolbar, content, status);

  const places = [
    { titleKey: "files.placeHome", path: "/home" },
    { titleKey: "files.placeRoot", path: "/" },
    { titleKey: "files.placeEtc", path: "/etc" },
    { titleKey: "files.placeVar", path: "/var" },
    { titleKey: "files.placeOpt", path: "/opt" },
    { titleKey: "files.placeTmp", path: "/tmp" },
  ];
  sidebar.append(el("div", { class: "fm-title" }, t("files.entrypoints")));
  const placeNodes = places.map((p) =>
    el(
      "div",
      { class: "fm-place", tabindex: "0", onclick: () => navigate(p.path, { push: true }) },
      el("span", { class: "fm-smallico" }, svg(icons.folder)),
      el("span", {}, t(p.titleKey)),
    ),
  );
  sidebar.append(...placeNodes);

  function fsUserDisplay() {
    if (fm.fsUser === "self") return fm.fsSelfName || "self";
    return fm.fsUser || "self";
  }

  function setFSUser(user) {
    fm.fsUser = user || "self";
    localStorage.setItem("atlas.fm.fsuser", fm.fsUser);
    updateStatus();
  }

  async function loadIdentities() {
    const info = await api("api/fs/identities");
    fm.fsSelfName = info.self || "self";
    fm.fsAllowed = Array.isArray(info.allowed) ? info.allowed : ["self"];
    fm.fsAny = fm.fsAllowed.includes("*");

    const allowedUsers = new Set(fm.fsAllowed);
    fsUserSelect.replaceChildren();
    fsUserSelect.append(el("option", { value: "self" }, `self (${fm.fsSelfName})`));
    for (const u of fm.fsAllowed) {
      if (u === "self" || u === "*") continue;
      fsUserSelect.append(el("option", { value: u }, u));
    }

    if (fm.fsUser !== "self" && !allowedUsers.has(fm.fsUser)) {
      fsUserSelect.append(el("option", { value: fm.fsUser }, fm.fsUser));
    }
    if (fm.fsAny) fsUserSelect.append(el("option", { value: "__other__" }, t("files.other")));
    fsUserSelect.value = fm.fsUser;
    updateStatus();
  }

  fsUserSelect.addEventListener("change", async () => {
    if (fsUserSelect.value === "__other__") {
      const u = prompt(t("files.linuxUserPrompt"), "");
      if (!u) {
        fsUserSelect.value = fm.fsUser;
        return;
      }
      setFSUser(u.trim());
      await loadIdentities();
      fsUserSelect.value = fm.fsUser;
      await refresh();
      return;
    }
    setFSUser(fsUserSelect.value);
    await refresh();
  });

  function fsApi(path, options = {}) {
    const headers = new Headers(options.headers || {});
    headers.set("X-Atlas-FS-User", fm.fsUser || "self");
    return api(path, { ...options, headers });
  }

  filePicker.addEventListener("change", async () => {
    const files = filePicker.files ? Array.from(filePicker.files) : [];
    filePicker.value = "";
    if (!files.length) return;
    await uploadFiles(files);
  });

  search.addEventListener("input", () => {
    fm.search = search.value || "";
    renderContent();
    updateStatus();
  });

  pathInput.addEventListener("keydown", async (e) => {
    if (e.key === "Enter") {
      fm.addressEdit = false;
      await navigate(pathInput.value || "/", { push: true });
      renderAddress();
    }
    if (e.key === "Escape") {
      fm.addressEdit = false;
      renderAddress();
    }
  });

  content.addEventListener("dragover", (e) => {
    e.preventDefault();
    drop.classList.add("visible");
  });
  content.addEventListener("dragleave", () => drop.classList.remove("visible"));
  content.addEventListener("drop", async (e) => {
    e.preventDefault();
    drop.classList.remove("visible");
    const files = Array.from(e.dataTransfer?.files || []);
    if (!files.length) return;
    await uploadFiles(files);
  });

  content.addEventListener("contextmenu", (e) => {
    if (e.target.closest(".fm-item,.fm-row")) return;
    if (e.target === content || e.target === drop || e.target.closest(".fm-grid,table")) {
      e.preventDefault();
      showContextMenu(e.clientX, e.clientY, [
        { label: t("files.cmNewFolder"), action: () => newFolder() },
        { label: t("files.cmNewFile"), action: () => newFile() },
        { label: t("files.cmUpload"), action: () => filePicker.click() },
        { sep: true },
        { label: t("common.refresh"), action: () => refresh() },
      ]);
    }
  });

  content.addEventListener("click", (e) => {
    if (e.target.closest(".fm-item,.fm-row")) return;
    clearSelected();
  });

  function iconBtn(title, iconHTML, onClick) {
    return el("button", { class: "iconbtn", title, type: "button", onclick: onClick }, svg(iconHTML));
  }

  function normalizePath(p) {
    if (!p) return "/";
    if (!p.startsWith("/")) p = `/${p}`;
    p = p.replaceAll("\\", "/");
    p = p.replace(/\/+/g, "/");
    if (p.length > 1 && p.endsWith("/")) p = p.slice(0, -1);
    return p;
  }

  function parentPath(p) {
    p = normalizePath(p);
    if (p === "/") return "/";
    const idx = p.lastIndexOf("/");
    return idx <= 0 ? "/" : p.slice(0, idx);
  }

  function currentEntries() {
    const q = (fm.search || "").toLowerCase();
    const base = fm.entries || [];
    if (!q) return base;
    return base.filter((e) => e.name.toLowerCase().includes(q));
  }

  function updateButtons() {
    btnRename.disabled = fm.selected.size !== 1;
    btnDelete.disabled = fm.selected.size === 0;
  }

  function updateSelectionUI() {
    const selected = fm.selected;
    for (const node of content.querySelectorAll("[data-path]")) {
      const p = node.getAttribute("data-path");
      node.classList.toggle("selected", p && selected.has(p));
    }
  }

  function setSelected(paths) {
    fm.selected = new Set(paths);
    updateButtons();
    updateSelectionUI();
    updateStatus();
  }

  function clearSelected() {
    fm.selected.clear();
    fm.lastIndex = null;
    updateButtons();
    updateSelectionUI();
    updateStatus();
  }

  function toggleView() {
    fm.view = fm.view === "grid" ? "list" : "grid";
    localStorage.setItem("atlas.fm.view", fm.view);
    btnView.replaceChildren(svg(fm.view === "grid" ? icons.grid : icons.list));
    renderContent();
  }

  async function refresh() {
    await load(fm.path);
  }

  async function goBack() {
    const p = fm.back.pop();
    if (!p) return;
    fm.forward.push(fm.path);
    await load(p);
  }

  async function goForward() {
    const p = fm.forward.pop();
    if (!p) return;
    fm.back.push(fm.path);
    await load(p);
  }

  async function goUp() {
    if (fm.path === "/") return;
    await navigate(parentPath(fm.path), { push: true });
  }

  async function navigate(path, { push }) {
    path = normalizePath(path);
    if (push) {
      fm.back.push(fm.path);
      fm.forward = [];
    }
    await load(path);
  }

  async function load(path) {
    path = normalizePath(path);
    try {
      const data = await fsApi(`api/fs/list?path=${encodeURIComponent(path)}`);
      fm.path = normalizePath(data.path);
      fm.entries = data.entries || [];
      clearSelected();
      renderAddress();
      updatePlaces();
      renderContent();
      updateNavButtons();
      updateStatus();
    } catch (e) {
      showModal(t("common.error"), `${e.message}`);
    }
  }

  function updatePlaces() {
    for (const n of placeNodes) n.classList.remove("active");
    for (let i = 0; i < places.length; i++) {
      if (normalizePath(places[i].path) === fm.path) placeNodes[i].classList.add("active");
    }
  }

  function updateNavButtons() {
    btnBack.disabled = fm.back.length === 0;
    btnForward.disabled = fm.forward.length === 0;
    btnUp.disabled = fm.path === "/";
  }

  function renderAddress() {
    crumbs.replaceChildren();
    pathInput.style.display = fm.addressEdit ? "block" : "none";
    crumbs.style.display = fm.addressEdit ? "none" : "flex";
    pathInput.value = fm.path;

    const parts = fm.path.split("/").filter(Boolean);
    const items = [{ label: "/", path: "/" }];
    let acc = "";
    for (const p of parts) {
      acc += `/${p}`;
      items.push({ label: p, path: acc });
    }
    items.forEach((it, idx) => {
      if (idx > 0) crumbs.append(el("span", { class: "crumb sep" }, "›"));
      crumbs.append(el("button", { class: "crumb", type: "button", onclick: () => navigate(it.path, { push: true }) }, it.label));
    });
    crumbs.addEventListener("dblclick", () => {
      fm.addressEdit = true;
      renderAddress();
      pathInput.focus();
      pathInput.select();
    }, { once: true });
  }

  function entryIcon(ent, small) {
    const html = ent.is_dir ? icons.folder : icons.file;
    const node = svg(html);
    if (small) return el("span", { class: "fm-smallico" }, node);
    return el("div", { class: "fm-icon" }, node);
  }

  function isSpecialUp(ent) {
    return ent && ent.is_dir && ent.name === "..";
  }

  function onSelect(ent, index, e) {
    if (isSpecialUp(ent)) {
      clearSelected();
      return;
    }
    const all = currentEntries().filter((x) => !isSpecialUp(x));
    const idx = all.findIndex((x) => x.path === ent.path);
    const additive = e.ctrlKey || e.metaKey;
    const range = e.shiftKey && fm.lastIndex !== null && fm.lastIndex !== undefined;
    if (range) {
      const a = Math.min(fm.lastIndex, idx);
      const b = Math.max(fm.lastIndex, idx);
      const next = additive ? new Set(fm.selected) : new Set();
      for (let i = a; i <= b; i++) next.add(all[i].path);
      fm.selected = next;
    } else if (additive) {
      if (fm.selected.has(ent.path)) fm.selected.delete(ent.path);
      else fm.selected.add(ent.path);
      fm.lastIndex = idx;
    } else {
      fm.selected = new Set([ent.path]);
      fm.lastIndex = idx;
    }
    updateButtons();
    updateSelectionUI();
    updateStatus();
  }

  async function openEntry(ent) {
    if (isSpecialUp(ent)) { await goUp(); return; }
    if (ent.is_dir) { await navigate(ent.path, { push: true }); return; }
    await viewFile(ent.path);
  }

  async function viewFile(path) {
    const text = await fsApi(`api/fs/read?path=${encodeURIComponent(path)}&limit=262144`);
    const head = el(
      "div",
      { class: "toolbar", style: "margin-bottom:10px;" },
      el("span", { class: "path mono", style: "flex:1; overflow:hidden; text-overflow:ellipsis; white-space:nowrap;" }, path),
      el("button", { class: "secondary", onclick: () => editFile(path) }, t("common.edit")),
      el("a", { class: "link", href: `api/fs/download?path=${encodeURIComponent(path)}&as=${encodeURIComponent(fm.fsUser || "self")}` }, t("files.download")),
      el("button", { class: "secondary", onclick: () => closeModal() }, t("common.close")),
    );
    const card = el("div", { class: "card" }, head, el("pre", { class: "terminal mono" }, text));
    showModalNode(card);
  }

  async function editFile(path) {
    closeContextMenu();
    const text = await fsApi(`api/fs/read?path=${encodeURIComponent(path)}&limit=1048576`);
    if (text.includes("\u0000")) {
      showModal(t("common.error"), t("files.binaryDisabled"));
      return;
    }
    const textarea = el("textarea", { class: "editor", spellcheck: "false" }, text);
    const btnSave = el("button", { onclick: () => save() }, t("common.save"));
    const btnCancel = el("button", { class: "secondary", onclick: () => closeModal() }, t("common.cancel"));
    const head = el(
      "div",
      { class: "toolbar", style: "margin-bottom:10px;" },
      el("span", { class: "path mono", style: "flex:1; overflow:hidden; text-overflow:ellipsis; white-space:nowrap;" }, path),
      btnSave,
      btnCancel,
    );
    const card = el("div", { class: "card" }, head, textarea);
    showModalNode(card);

    async function save() {
      btnSave.disabled = true;
      try {
        await fsApi("api/fs/write", {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: JSON.stringify({ path, content: textarea.value }),
        });
        closeModal();
        await refresh();
      } catch (e) {
        btnSave.disabled = false;
        showModal(t("common.error"), e.message);
      }
    }
  }

  async function uploadFiles(files) {
    const form = new FormData();
    for (const f of files) form.append("file", f, f.name);
    await fsApi(`api/fs/upload?path=${encodeURIComponent(fm.path)}`, { method: "POST", body: form });
    await refresh();
  }

  async function newFolder() {
    const name = prompt(t("files.folderNamePrompt"));
    if (!name) return;
    await fsApi(`api/fs/mkdir?path=${encodeURIComponent(fm.path)}&name=${encodeURIComponent(name)}`, { method: "POST" });
    await refresh();
  }

  async function newFile() {
    const name = prompt(t("files.fileNamePrompt"));
    if (!name) return;
    await fsApi(`api/fs/touch?path=${encodeURIComponent(fm.path)}&name=${encodeURIComponent(name)}`, { method: "POST" });
    await refresh();
  }

  async function renameSelected() {
    const p = Array.from(fm.selected)[0];
    if (!p) return;
    const ent = fm.entries.find((e) => e.path === p);
    if (!ent || isSpecialUp(ent)) return;
    const name = prompt(t("files.renamePrompt"), ent.name);
    if (!name || name === ent.name) return;
    await fsApi("api/fs/rename", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ from: ent.path, to: name }),
    });
    await refresh();
  }

  async function deleteSelected() {
    const paths = Array.from(fm.selected);
    if (!paths.length) return;
    const ok = confirm(t("files.deleteConfirm", { n: paths.length }));
    if (!ok) return;
    await fsApi("api/fs/delete", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ paths }),
    });
    await refresh();
  }

  function renderContent() {
    closeContextMenu();
    const entries = currentEntries();
    const selected = fm.selected;
    const view = fm.view;
    btnView.replaceChildren(svg(view === "grid" ? icons.grid : icons.list));

    if (view === "grid") {
      const st = content.scrollTop;
      const sl = content.scrollLeft;
      const grid = el("div", { class: "fm-grid" });
      for (let i = 0; i < entries.length; i++) {
        const ent = entries[i];
        const item = el(
          "div",
          { class: "fm-item", tabindex: "0", "data-path": ent.path },
          entryIcon(ent, false),
          el("div", { class: "fm-name", title: ent.name }, ent.name),
          el("div", { class: "fm-meta" }, ent.is_dir ? t("files.folder") : fmtBytes(ent.size)),
        );
        item.addEventListener("click", (e) => onSelect(ent, i, e));
        item.addEventListener("dblclick", () => openEntry(ent));
        item.addEventListener("contextmenu", (e) => {
          e.preventDefault();
          e.stopPropagation();
          if (!selected.has(ent.path) && !isSpecialUp(ent)) setSelected([ent.path]);
          showEntryContextMenu(e.clientX, e.clientY, ent);
        });
        grid.append(item);
      }
      content.replaceChildren(drop, grid);
      updateSelectionUI();
      content.scrollTop = st;
      content.scrollLeft = sl;
      return;
    }

    const st = content.scrollTop;
    const sl = content.scrollLeft;
    const table = el(
      "table",
      { class: "fm-list" },
      el("thead", {}, el("tr", {},
        el("th", {}, t("files.thName")),
        el("th", {}, t("files.thSize")),
        el("th", {}, t("files.thModified")),
      )),
    );
    const tbody = el("tbody");
    for (let i = 0; i < entries.length; i++) {
      const ent = entries[i];
      const tr = el(
        "tr",
        { class: "fm-row", "data-path": ent.path },
        el("td", {}, el("div", { class: "fm-namecell" }, entryIcon(ent, true), el("span", { class: "mono" }, ent.name))),
        el("td", {}, ent.is_dir ? "—" : fmtBytes(ent.size)),
        el("td", {}, fmtDate(ent.mod_unix)),
      );
      tr.addEventListener("click", (e) => onSelect(ent, i, e));
      tr.addEventListener("dblclick", () => openEntry(ent));
      tr.addEventListener("contextmenu", (e) => {
        e.preventDefault();
        e.stopPropagation();
        if (!selected.has(ent.path) && !isSpecialUp(ent)) setSelected([ent.path]);
        showEntryContextMenu(e.clientX, e.clientY, ent);
      });
      tbody.append(tr);
    }
    table.append(tbody);
    content.replaceChildren(drop, table);
    updateSelectionUI();
    content.scrollTop = st;
    content.scrollLeft = sl;
  }

  function updateStatus() {
    const entries = currentEntries().filter((e) => !isSpecialUp(e));
    const selected = Array.from(fm.selected).map((p) => fm.entries.find((e) => e.path === p)).filter(Boolean);
    const selectedCount = selected.length;
    const selectedBytes = selected.reduce((sum, e) => sum + (e.is_dir ? 0 : Number(e.size) || 0), 0);
    status.replaceChildren(
      el("span", {}, t("files.statusUser", { user: state.me || "—" })),
      el("span", {}, t("files.statusFs", { fs: fsUserDisplay() })),
      el("span", {}, t("files.statusItems", { n: entries.length })),
      el("span", {}, selectedCount ? t("files.statusSelected", { n: selectedCount, size: fmtBytes(selectedBytes) }) : " "),
      el("span", { class: "mono" }, fm.path),
    );
  }

  function closeContextMenu() {
    if (!fm.ctx) return;
    fm.ctx.remove();
    fm.ctx = null;
  }

  function showContextMenu(x, y, items) {
    closeContextMenu();
    const menu = el("div", { class: "cm" });
    for (const it of items) {
      if (it.sep) { menu.append(el("hr")); continue; }
      menu.append(el("button", { type: "button", onclick: () => { closeContextMenu(); it.action(); } }, it.label));
    }
    const pad = 8;
    menu.style.left = `${Math.max(pad, x)}px`;
    menu.style.top = `${Math.max(pad, y)}px`;
    document.body.append(menu);
    fm.ctx = menu;
    const rect = menu.getBoundingClientRect();
    let left = x, top = y;
    if (left + rect.width > window.innerWidth - pad) left = window.innerWidth - rect.width - pad;
    if (top + rect.height > window.innerHeight - pad) top = window.innerHeight - rect.height - pad;
    if (left < pad) left = pad;
    if (top < pad) top = pad;
    menu.style.left = `${left}px`;
    menu.style.top = `${top}px`;
  }

  function showEntryContextMenu(x, y, ent) {
    if (isSpecialUp(ent)) {
      showContextMenu(x, y, [{ label: t("files.cmUp"), action: () => goUp() }]);
      return;
    }
    const sel = Array.from(fm.selected);
    const single = sel.length === 1;
    const only = single ? fm.entries.find((e) => e.path === sel[0]) : null;
    const items = [{ label: ent.is_dir ? t("files.cmOpen") : t("files.cmView"), action: () => openEntry(ent) }];
    if (!ent.is_dir) items.push({ label: t("files.cmEdit"), action: () => editFile(ent.path) });
    if (!ent.is_dir) items.push({ label: t("files.download"), action: () => (window.location.href = `api/fs/download?path=${encodeURIComponent(ent.path)}&as=${encodeURIComponent(fm.fsUser || "self")}`) });
    items.push({ sep: true });
    items.push({ label: t("files.cmNewFolder"), action: () => newFolder() });
    items.push({ label: t("files.cmNewFile"), action: () => newFile() });
    items.push({ label: t("files.cmUpload"), action: () => filePicker.click() });
    items.push({ sep: true });
    if (single && only) items.push({ label: t("files.cmRename"), action: () => renameSelected() });
    items.push({ label: t("files.cmDelete"), action: () => deleteSelected() });
    showContextMenu(x, y, items);
  }

  function closeModal() {
    if (!fm.modal) return;
    fm.modal.remove();
    fm.modal = null;
  }

  function showModal(title, text) {
    const card = el("div", { class: "card" }, el("div", { class: "path" }, title), el("pre", { class: "terminal mono" }, text));
    showModalNode(card);
  }

  function showModalNode(card) {
    closeModal();
    const m = el("div", { class: "modal", onclick: (e) => { if (e.target === m) closeModal(); } }, card);
    document.body.append(m);
    fm.modal = m;
  }

  function onGlobalKeydown(e) {
    if (e.defaultPrevented) return;
    const ae = document.activeElement;
    const isInput = ae && (ae.tagName === "INPUT" || ae.tagName === "TEXTAREA");

    if (e.key === "Escape") {
      closeContextMenu();
      closeModal();
      if (fm.addressEdit) {
        fm.addressEdit = false;
        renderAddress();
      }
      return;
    }
    if (isInput && ae !== pathInput) return;

    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "l") {
      e.preventDefault();
      fm.addressEdit = true;
      renderAddress();
      pathInput.focus();
      pathInput.select();
      return;
    }
    if (e.key === "Backspace") {
      e.preventDefault();
      goUp();
      return;
    }
    if (e.key === "Enter") {
      const only = fm.selected.size === 1 ? fm.entries.find((x) => x.path === Array.from(fm.selected)[0]) : null;
      if (only) openEntry(only);
      return;
    }
    if (e.key === "Delete") {
      deleteSelected();
      return;
    }
    if (e.key === "F2") {
      renameSelected();
      return;
    }
    if (e.altKey && e.key === "ArrowLeft") {
      goBack();
      return;
    }
    if (e.altKey && e.key === "ArrowRight") {
      goForward();
      return;
    }
  }

  document.addEventListener("click", closeContextMenu);
  window.addEventListener("keydown", onGlobalKeydown);
  root._cleanup = () => {
    document.removeEventListener("click", closeContextMenu);
    window.removeEventListener("keydown", onGlobalKeydown);
    closeContextMenu();
    closeModal();
  };

  btnRename.disabled = true;
  btnDelete.disabled = true;
  updateButtons();
  btnView.replaceChildren(svg(fm.view === "grid" ? icons.grid : icons.list));
  await loadIdentities();
  await load("/");
}
