import { api } from "../api.js";
import { el } from "../dom.js";
import { t } from "../i18n.js";
import { state } from "../state.js";

function pill(text) {
  return el("span", { class: "pill" }, text);
}

function modal(title, bodyNodes, actions) {
  const card = el("div", { class: "card" },
    el("div", { class: "pm-title" }, title),
    ...bodyNodes,
    el("div", { class: "toolbar", style: "margin-top:10px; justify-content:flex-end;" }, ...actions),
  );
  const wrap = el("div", { class: "modal", onclick: (e) => { if (e.target === wrap) close(); } }, card);
  function close() { wrap.remove(); }
  document.body.append(wrap);
  return { close, wrap, card };
}

function csvToArr(s) {
  return String(s || "").split(",").map(x => x.trim()).filter(Boolean);
}

function arrToCSV(a) {
  return (a || []).join(",");
}

function parseListen(addr) {
  const raw = String(addr || "").trim();
  if (!raw) return { host: "0.0.0.0", port: "" };
  if (raw.startsWith("[")) {
    const idx = raw.indexOf("]");
    if (idx > 0) {
      const host = raw.slice(1, idx);
      const port = raw.slice(idx + 1).replace(/^:/, "");
      return { host: host || "0.0.0.0", port };
    }
  }
  const last = raw.lastIndexOf(":");
  if (last > 0 && raw.indexOf(":") === last) {
    return { host: raw.slice(0, last) || "0.0.0.0", port: raw.slice(last + 1) };
  }
  if (/^\d+$/.test(raw)) return { host: "0.0.0.0", port: raw };
  return { host: raw, port: "" };
}

function buildListen(host, port) {
  const h = String(host || "").trim() || "0.0.0.0";
  const p = String(port || "").trim();
  if (!p) return "";
  const needsBrackets = h.includes(":") && !h.startsWith("[");
  return `${needsBrackets ? `[${h}]` : h}:${p}`;
}

export async function renderAdmin(root) {
  if (!state.isAdmin) {
    root.append(el("div", { class: "card" }, el("div", { class: "path" }, t("common.forbidden"))));
    return;
  }

  function yesNo(v) {
    return v ? t("common.yes") : t("common.no");
  }

  function roleLabel(role) {
    const r = String(role || "user").toLowerCase();
    if (r === "admin") return t("admin.roleAdmin");
    if (r === "user") return t("admin.roleUser");
    return role || "user";
  }

  const nav = el("aside", { class: "sm-nav" });
  const main = el("div", { class: "sm-main" });
  const layout = el("div", { class: "sm" }, nav, main);
  root.append(layout);

  const pages = [
    { id: "server", titleKey: "admin.server" },
    { id: "config", titleKey: "admin.config" },
    { id: "users", titleKey: "admin.users" },
    { id: "sudo", titleKey: "admin.sudo" },
    { id: "logs", titleKey: "admin.logs" },
  ];
  const navNodes = new Map();
  let page = "server";
  let cleanup = null;

  for (const p of pages) {
    const n = el("div", { class: "item", tabindex: "0", onclick: () => setPage(p.id) }, t(p.titleKey));
    nav.append(n);
    navNodes.set(p.id, n);
  }

  function setPage(id) {
    try { cleanup && cleanup(); } catch {}
    cleanup = null;
    page = id;
    for (const p of pages) navNodes.get(p.id)?.classList.toggle("active", p.id === id);
    render();
  }

  function replaceMain(...nodes) {
    main.replaceChildren(...nodes);
  }

  async function render() {
    replaceMain(el("div", { class: "path" }, t("common.loading")));
    if (page === "server") await renderServer();
    else if (page === "config") await renderConfig();
    else if (page === "users") await renderUsers();
    else if (page === "sudo") await renderSudo();
    else await renderLogs();
  }

  function fieldRow(label, control, hint) {
    return el("div", { class: "form-row" },
      el("div", { class: "form-label" }, label),
      el("div", { class: "form-control" }, control, hint ? el("div", { class: "form-hint" }, hint) : null),
    );
  }

  function section(title, ...rows) {
    return el("div", { class: "form-section" },
      el("div", { class: "path" }, title),
      el("div", { class: "form-grid" }, ...rows),
    );
  }

  function checkControl(input) {
    return el("label", { class: "form-check" }, input, el("span", {}, t("common.enabled")));
  }

  async function renderServer() {
    const [cfg, info] = await Promise.all([
      api("api/admin/config"),
      api("api/system/info").catch(() => null),
    ]);

    const head = el("div", { class: "pm-head" },
      el("div", { class: "pm-title" }, t("admin.titleServer")),
      pill(`${t("admin.web")}: ${state.me || "—"}`),
      pill(`${t("admin.role")}: ${roleLabel(state.role) || "—"}`),
      el("span", { class: "pm-spacer" }),
      el("button", { class: "secondary", onclick: () => render() }, t("common.refresh")),
    );

    const sys = el("div", { class: "card", style: "margin-top:12px;" },
      el("div", { class: "path" }, t("admin.system")),
      el("div", { class: "kv" },
        el("div", { class: "k" }, t("monitor.hostname")), el("div", {}, info?.hostname || "—"),
        el("div", { class: "k" }, t("monitor.os")), el("div", {}, info?.os || "—"),
        el("div", { class: "k" }, t("monitor.kernel")), el("div", {}, info?.kernel || "—"),
        el("div", { class: "k" }, t("monitor.uptime")), el("div", {}, info?.uptime_seconds != null ? `${Math.floor(info.uptime_seconds)}${t("common.secondsShort")}` : "—"),
        el("div", { class: "k" }, t("monitor.load")), el("div", {}, info ? `${(info.load1 || 0).toFixed(2)} ${(info.load5 || 0).toFixed(2)} ${(info.load15 || 0).toFixed(2)}` : "—"),
      ),
    );

    const actionsEnabled = !!cfg.enable_admin_actions;
    const actionsCard = el("div", { class: "card", style: "margin-top:12px;" },
      el("div", { class: "path" }, t("admin.actions")),
      el("div", { class: "toolbar" },
        pill(actionsEnabled ? t("common.enabled") : t("common.disabled")),
        pill(`${t("admin.service")}: ${cfg.service_name || "—"}`),
        pill(`${t("admin.configPath")}: ${cfg.config_path || "—"}`),
        el("span", { class: "pm-spacer" }),
        el("button", {
          class: "secondary",
          disabled: !actionsEnabled ? "disabled" : null,
          onclick: () => confirmAction("restart", cfg.service_name),
        }, t("admin.restartService")),
        el("button", {
          class: "danger",
          disabled: !actionsEnabled ? "disabled" : null,
          onclick: () => confirmAction("reboot"),
        }, t("admin.reboot")),
        el("button", {
          class: "danger",
          disabled: !actionsEnabled ? "disabled" : null,
          onclick: () => confirmAction("shutdown"),
        }, t("admin.shutdown")),
      ),
      !actionsEnabled
        ? el("div", { class: "path" }, t("admin.enableActionsHint"))
        : el("div", { style: "height:0px;" }),
    );

    function confirmAction(action) {
      const token = action.toUpperCase();
      const input = el("input", { class: "mono", placeholder: t("admin.typeToConfirm", { token }) });
      const msg = el("div", { class: "path" }, t("admin.confirmDanger"));
      const actionLabel = t(`admin.action.${action}`);
      const m = modal(t("admin.confirmActionTitle", { action: actionLabel || action }), [msg, input], [
        el("button", { class: "secondary", onclick: () => m.close() }, t("common.cancel")),
        el("button", {
          class: "danger",
          onclick: async () => {
            if ((input.value || "").trim().toUpperCase() !== token) {
              alert(t("admin.typeToken", { token }));
              return;
            }
            await api("api/admin/action", {
              method: "POST",
              headers: { "content-type": "application/json" },
              body: JSON.stringify({ action, confirm: token }),
            });
            m.close();
          },
        }, t("admin.run")),
      ]);
      input.focus();
    }

    const cfgCard = el("div", { class: "card", style: "margin-top:12px;" },
      el("div", { class: "path" }, t("admin.configTitle")),
      el("div", { class: "toolbar" },
        pill(`${t("admin.listen")}: ${cfg.config.listen}`),
        pill(`${t("admin.root")}: ${cfg.config.root}`),
        pill(`${t("admin.exec")}: ${cfg.config.enable_exec ? t("common.on") : t("common.off")}`),
        pill(`${t("admin.firewall")}: ${cfg.config.enable_firewall ? t("common.on") : t("common.off")}`),
        el("span", { class: "pm-spacer" }),
        el("button", { class: "secondary", onclick: () => setPage("config") }, t("admin.openSettings")),
      ),
      el("div", { class: "path" }, t("admin.configRestartHint")),
    );

    replaceMain(head, sys, actionsCard, cfgCard);
  }

  async function renderConfig() {
    const cfg = await api("api/admin/config");
    const currentCfg = cfg.config;
    const listen = parseListen(currentCfg.listen);
    const hostIn = el("input", { class: "mono", value: listen.host || "0.0.0.0" });
    const portIn = el("input", { class: "mono", type: "number", min: "1", max: "65535", value: listen.port || "" });
    const rootIn = el("input", { class: "mono", value: currentCfg.root || "/" });
    const basePathIn = el("input", { class: "mono", value: currentCfg.base_path || "/" });
    const tlsCertIn = el("input", { class: "mono", value: currentCfg.tls_cert_file || "" });
    const tlsKeyIn = el("input", { class: "mono", value: currentCfg.tls_key_file || "" });

    const cookieSecureIn = el("input", { type: "checkbox", checked: !!currentCfg.cookie_secure });
    const execIn = el("input", { type: "checkbox", checked: !!currentCfg.enable_exec });
    const fwIn = el("input", { type: "checkbox", checked: !!currentCfg.enable_firewall });
    const adminActionsIn = el("input", { type: "checkbox", checked: !!currentCfg.enable_admin_actions });
    const daemonizeIn = el("input", { type: "checkbox", checked: !!currentCfg.daemonize });
    const logStdoutIn = el("input", { type: "checkbox", checked: !!currentCfg.log_stdout });
    const fsSudoIn = el("input", { type: "checkbox", checked: !!currentCfg.fs_sudo });

    const serviceNameIn = el("input", { class: "mono", value: currentCfg.service_name || "" });
    const logFileIn = el("input", { class: "mono", value: currentCfg.log_file || "" });
    const updateRepoIn = el("input", { class: "mono", value: currentCfg.update_repo || "" });
    const fsUsersIn = el("input", { class: "mono", value: arrToCSV(currentCfg.fs_users || []) });
    const masterKeyIn = el("input", { class: "mono", value: currentCfg.master_key_file || "" });
    const userDBIn = el("input", { class: "mono", value: currentCfg.user_db_path || "" });
    const fwDBIn = el("input", { class: "mono", value: currentCfg.firewall_db_path || "" });

    const logLevelIn = el("select");
    for (const v of ["debug", "info", "warn", "error", "off"]) {
      logLevelIn.append(el("option", { value: v }, v));
    }
    logLevelIn.value = currentCfg.log_level || "info";

    const updateChannelIn = el("select");
    for (const v of ["auto", "stable", "dev"]) {
      updateChannelIn.append(el("option", { value: v }, v));
    }
    updateChannelIn.value = currentCfg.update_channel || "auto";

    const head = el("div", { class: "pm-head" },
      el("div", { class: "pm-title" }, t("admin.titleConfig")),
      pill(`${t("admin.configPath")}: ${cfg.config_path || "—"}`),
      el("span", { class: "pm-spacer" }),
      el("button", { class: "secondary", onclick: () => renderConfig() }, t("common.refresh")),
      el("button", { onclick: () => saveConfig() }, t("common.save")),
    );

    const form = el("div", {},
      section(t("admin.sectionNetwork"),
        fieldRow(t("admin.labelListenHost"), hostIn, t("admin.listenHostHint")),
        fieldRow(t("admin.labelListenPort"), portIn, t("admin.listenPortHint")),
        fieldRow(t("admin.labelRoot"), rootIn),
        fieldRow(t("admin.labelBasePath"), basePathIn, t("admin.basePathHint")),
      ),
      section(t("admin.sectionTLS"),
        fieldRow(t("admin.labelTLSCert"), tlsCertIn),
        fieldRow(t("admin.labelTLSKey"), tlsKeyIn),
      ),
      section(t("admin.sectionFeatures"),
        fieldRow(t("admin.labelCookieSecure"), checkControl(cookieSecureIn)),
        fieldRow(t("admin.labelEnableExec"), checkControl(execIn)),
        fieldRow(t("admin.labelEnableFirewall"), checkControl(fwIn)),
        fieldRow(t("admin.labelEnableAdminActions"), checkControl(adminActionsIn)),
        fieldRow(t("admin.labelDaemonize"), checkControl(daemonizeIn)),
      ),
      section(t("admin.sectionLogging"),
        fieldRow(t("admin.labelLogLevel"), logLevelIn),
        fieldRow(t("admin.labelLogFile"), logFileIn),
        fieldRow(t("admin.labelLogStdout"), checkControl(logStdoutIn)),
      ),
      section(t("admin.sectionUpdate"),
        fieldRow(t("admin.labelUpdateRepo"), updateRepoIn),
        fieldRow(t("admin.labelUpdateChannel"), updateChannelIn),
      ),
      section(t("admin.sectionFS"),
        fieldRow(t("admin.labelFSSudo"), checkControl(fsSudoIn)),
        fieldRow(t("admin.labelFSUsers"), fsUsersIn, t("admin.fsUsersHint")),
      ),
      section(t("admin.sectionPaths"),
        fieldRow(t("admin.labelServiceName"), serviceNameIn),
        fieldRow(t("admin.labelMasterKeyFile"), masterKeyIn),
        fieldRow(t("admin.labelUserDBPath"), userDBIn),
        fieldRow(t("admin.labelFWDBPath"), fwDBIn),
      ),
      el("div", { class: "path", style: "margin-top:12px;" }, t("admin.configRestartHint")),
    );

    async function saveConfig() {
      const listenPort = String(portIn.value || "").trim();
      if (!/^\d+$/.test(listenPort)) {
        alert(t("admin.listenPortBad"));
        return;
      }
      const portNum = Number(listenPort);
      if (portNum <= 0 || portNum > 65535) {
        alert(t("admin.listenPortBad"));
        return;
      }
      const next = { ...currentCfg };
      next.listen = buildListen(hostIn.value, listenPort);
      next.root = String(rootIn.value || "").trim() || "/";
      next.base_path = String(basePathIn.value || "").trim() || "/";
      next.tls_cert_file = String(tlsCertIn.value || "").trim();
      next.tls_key_file = String(tlsKeyIn.value || "").trim();
      next.cookie_secure = !!cookieSecureIn.checked;
      next.enable_exec = !!execIn.checked;
      next.enable_firewall = !!fwIn.checked;
      next.enable_admin_actions = !!adminActionsIn.checked;
      next.service_name = String(serviceNameIn.value || "").trim();
      next.daemonize = !!daemonizeIn.checked;
      next.log_level = String(logLevelIn.value || "").trim();
      next.log_file = String(logFileIn.value || "").trim();
      next.log_stdout = !!logStdoutIn.checked;
      next.update_repo = String(updateRepoIn.value || "").trim();
      next.update_channel = String(updateChannelIn.value || "").trim();
      next.fs_sudo = !!fsSudoIn.checked;
      next.fs_users = csvToArr(fsUsersIn.value || "");
      next.master_key_file = String(masterKeyIn.value || "").trim();
      next.user_db_path = String(userDBIn.value || "").trim();
      next.firewall_db_path = String(fwDBIn.value || "").trim();
      await api("api/admin/config", {
        method: "PUT",
        headers: { "content-type": "application/json" },
        body: JSON.stringify(next),
      });
      await renderConfig();
    }

    replaceMain(head, form);
    hostIn.focus();
    hostIn.select();
  }

  async function renderUsers() {
    const res = await api("api/admin/users");
    const users = res.users || [];

    const head = el("div", { class: "pm-head" },
      el("div", { class: "pm-title" }, t("admin.titleUsers")),
      pill(t("admin.count", { n: users.length })),
      el("span", { class: "pm-spacer" }),
      el("button", { onclick: () => openUserModal(null) }, t("admin.addUser")),
      el("button", { class: "secondary", onclick: () => render() }, t("common.refresh")),
    );

    const table = el("table", {},
      el("thead", {}, el("tr", {},
        el("th", {}, t("admin.thUser")),
        el("th", {}, t("admin.thRole")),
        el("th", {}, t("admin.thExec")),
        el("th", {}, t("admin.thProcs")),
        el("th", {}, t("admin.thFW")),
        el("th", {}, t("admin.thFSSudo")),
        el("th", {}, t("admin.thFSUsers")),
        el("th", { style: "text-align:right" }, t("admin.thActions")),
      )),
    );
    const tbody = el("tbody");
    for (const u of users) {
      tbody.append(el("tr", {},
        el("td", { class: "mono" }, u.user),
        el("td", { class: "mono" }, roleLabel(u.role || "user")),
        el("td", {}, yesNo(u.can_exec)),
        el("td", {}, yesNo(u.can_procs)),
        el("td", {}, yesNo(u.can_fw)),
        el("td", {}, yesNo(u.fs_sudo)),
        el("td", { class: "mono" }, (u.fs_any ? "*" : (u.fs_users || []).join(",")) || "—"),
        el("td", { style: "text-align:right; white-space:nowrap;" },
          el("button", { class: "secondary", onclick: () => openUserModal(u) }, t("common.edit")),
          " ",
          el("button", { class: "danger", onclick: () => deleteUser(u.user) }, t("common.delete")),
        ),
      ));
    }
    table.append(tbody);

    async function deleteUser(user) {
      if (!confirm(t("admin.deleteUserConfirm", { user }))) return;
      await api(`api/admin/users/${encodeURIComponent(user)}`, { method: "DELETE" });
      await render();
    }

    function openUserModal(user) {
      const isEdit = !!user;
      const userIn = el("input", { class: "mono", placeholder: t("admin.usernamePlaceholder"), value: user?.user || "", disabled: isEdit ? "disabled" : null });
      const passIn = el("input", { class: "mono", placeholder: isEdit ? t("admin.newPasswordOptional") : t("admin.passwordPlaceholder"), type: "password" });
      const roleSel = el("select");
      for (const v of ["user", "admin"]) {
        roleSel.append(el("option", { value: v }, v === "admin" ? t("admin.roleAdmin") : t("admin.roleUser")));
      }
      roleSel.value = (user?.role || "user").toLowerCase() === "admin" ? "admin" : "user";

      const canExec = el("input", { type: "checkbox", checked: !!user?.can_exec });
      const canProcs = el("input", { type: "checkbox", checked: !!user?.can_procs });
      const canFW = el("input", { type: "checkbox", checked: !!user?.can_fw });
      const fsSudo = el("input", { type: "checkbox", checked: !!user?.fs_sudo });
      const fsAny = el("input", { type: "checkbox", checked: !!user?.fs_any });
      const fsUsers = el("input", { class: "mono", placeholder: t("admin.fsUsersCsvPlaceholder"), value: arrToCSV(user?.fs_users || []) });

      const form = el("div", { class: "split" },
        el("div", {},
          el("div", { class: "path" }, t("admin.account")),
          el("div", { class: "toolbar" }, el("span", { class: "path" }, t("admin.user")), userIn),
          el("div", { class: "toolbar" }, el("span", { class: "path" }, t("admin.role")), roleSel),
          el("div", { class: "toolbar" }, el("span", { class: "path" }, t("admin.password")), passIn),
        ),
        el("div", {},
          el("div", { class: "path" }, t("admin.permissions")),
          el("div", { class: "toolbar" }, canExec, el("span", { class: "path" }, t("admin.permExec"))),
          el("div", { class: "toolbar" }, canProcs, el("span", { class: "path" }, t("admin.permProcs"))),
          el("div", { class: "toolbar" }, canFW, el("span", { class: "path" }, t("admin.permFW"))),
          el("div", { class: "toolbar" }, fsSudo, el("span", { class: "path" }, t("admin.permFSSudo"))),
          el("div", { class: "toolbar" }, fsAny, el("span", { class: "path" }, t("admin.permFSAny"))),
          el("div", { class: "toolbar" }, el("span", { class: "path" }, t("admin.permFSUsers")), fsUsers),
        ),
      );

      const m = modal(isEdit ? t("admin.editUserTitle") : t("admin.addUserTitle"), [form], [
        el("button", { class: "secondary", onclick: () => m.close() }, t("common.cancel")),
        el("button", {
          onclick: async () => {
            const payload = {
              user: userIn.value,
              pass: passIn.value,
              role: roleSel.value,
              can_exec: !!canExec.checked,
              can_procs: !!canProcs.checked,
              can_fw: !!canFW.checked,
              fs_sudo: !!fsSudo.checked,
              fs_any: !!fsAny.checked,
              fs_users: csvToArr(fsUsers.value),
            };
            if (!payload.user) { alert(t("admin.userRequired")); return; }
            if (!isEdit && !payload.pass) { alert(t("admin.passwordRequired")); return; }
            if (isEdit) {
              await api(`api/admin/users/${encodeURIComponent(user.user)}`, {
                method: "PUT",
                headers: { "content-type": "application/json" },
                body: JSON.stringify(payload),
              });
            } else {
              await api("api/admin/users", {
                method: "POST",
                headers: { "content-type": "application/json" },
                body: JSON.stringify(payload),
              });
            }
            m.close();
            await render();
          },
        }, isEdit ? t("common.save") : t("common.add")),
      ]);
      userIn.focus();
    }

    replaceMain(head, el("div", { class: "card", style: "margin-top:12px;" }, table));
  }

  async function renderSudo() {
    const head = el("div", { class: "pm-head" },
      el("div", { class: "pm-title" }, t("admin.titleSudo")),
      pill(`${t("admin.web")}: ${state.me || "—"}`),
      el("span", { class: "pm-spacer" }),
      el("button", { class: "secondary", onclick: () => render() }, t("common.refresh")),
    );

    const info = await api("api/admin/sudo");
    const hasPass = !!info?.has_password;

    const status = pill(hasPass ? t("admin.sudoSet") : t("admin.sudoNotSet"));
    const input = el("input", { type: "password", class: "mono", placeholder: t("admin.sudoPlaceholder") });
    const saveBtn = el("button", {
      class: "secondary",
      onclick: async () => {
        if (!input.value) { alert(t("admin.sudoRequired")); return; }
        await api("api/admin/sudo", {
          method: "PUT",
          headers: { "content-type": "application/json" },
          body: JSON.stringify({ password: input.value }),
        });
        input.value = "";
        await render();
      },
    }, t("admin.sudoSave"));
    const clearBtn = el("button", {
      class: "danger",
      onclick: async () => {
        if (!confirm(t("admin.sudoClearConfirm"))) return;
        await api("api/admin/sudo", {
          method: "PUT",
          headers: { "content-type": "application/json" },
          body: JSON.stringify({ clear: true }),
        });
        input.value = "";
        await render();
      },
    }, t("admin.sudoClear"));

    const card = el("div", { class: "card", style: "margin-top:12px;" },
      el("div", { class: "path" }, t("admin.sudoHint")),
      el("div", { class: "toolbar" }, status, el("span", { class: "pm-spacer" })),
      el("div", { class: "toolbar" },
        el("span", { class: "path" }, t("admin.sudoPassword")),
        input,
        saveBtn,
        clearBtn,
      ),
    );

    replaceMain(head, card);
  }

  async function renderLogs() {
    const head = el("div", { class: "pm-head" },
      el("div", { class: "pm-title" }, t("admin.titleLogs")),
      pill(`${t("admin.web")}: ${state.me || "—"}`),
      el("span", { class: "pm-spacer" }),
      el("button", { class: "secondary", onclick: () => render() }, t("common.refresh")),
    );

    const nSel = el("select", { class: "secondary" },
      el("option", { value: "200" }, "200"),
      el("option", { value: "500", selected: "selected" }, "500"),
      el("option", { value: "1000" }, "1000"),
      el("option", { value: "5000" }, "5000"),
    );
    const autoChk = el("input", { type: "checkbox" });
    let timer = null;

    const meta = el("div", { class: "path" }, "—");
    const pre = el("pre", { class: "mono", style: "margin:0; padding:10px; max-height:60vh; overflow:auto; background:rgba(0,0,0,.25); border-radius:10px; border:1px solid rgba(255,255,255,.08);" }, "");

    async function load() {
      const n = Number(nSel.value || "500") || 500;
      const res = await api(`api/admin/logs?n=${encodeURIComponent(String(n))}`);
      if (!res.enabled) {
        meta.textContent = t("admin.logsDisabled");
        pre.textContent = "";
        return;
      }
      meta.textContent = `${t("admin.logsPath")}: ${res.path || "—"} · ${t("admin.logsSize")}: ${formatBytes(res.size_bytes || 0)}${res.truncated ? ` · ${t("admin.logsTruncated")}` : ""}`;
      pre.textContent = (res.lines || []).join("\n");
      pre.scrollTop = pre.scrollHeight;
    }

    function setAuto(v) {
      if (timer) { clearInterval(timer); timer = null; }
      if (v) {
        timer = setInterval(() => load().catch(() => {}), 2000);
      }
    }

    const tools = el("div", { class: "toolbar" },
      el("span", { class: "path" }, t("admin.tail")),
      nSel,
      el("label", { class: "toolbar", style: "gap:8px;" }, autoChk, el("span", { class: "path" }, t("admin.autoRefresh"))),
      el("span", { class: "pm-spacer" }),
      el("button", { class: "secondary", onclick: () => window.open("api/admin/logs?download=1", "_blank", "noreferrer") }, t("admin.downloadLogs")),
    );

    nSel.onchange = () => load().catch((e) => alert(e.message || e));
    autoChk.onchange = () => setAuto(!!autoChk.checked);

    const card = el("div", { class: "card", style: "margin-top:12px;" }, tools, meta, pre);
    replaceMain(head, card);
    await load();

    cleanup = () => setAuto(false);
  }

  setPage(page);
}

function formatBytes(n) {
  n = Number(n || 0);
  if (!isFinite(n) || n <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let u = 0;
  while (n >= 1024 && u < units.length - 1) { n /= 1024; u++; }
  return `${n.toFixed(u === 0 ? 0 : 1)} ${units[u]}`;
}
