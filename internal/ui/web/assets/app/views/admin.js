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
    { id: "users", titleKey: "admin.users" },
  ];
  const navNodes = new Map();
  let page = "server";

  for (const p of pages) {
    const n = el("div", { class: "item", tabindex: "0", onclick: () => setPage(p.id) }, t(p.titleKey));
    nav.append(n);
    navNodes.set(p.id, n);
  }

  function setPage(id) {
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
    else await renderUsers();
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
        el("button", { class: "secondary", onclick: () => editConfig(cfg.config) }, t("admin.editJson")),
      ),
      el("div", { class: "path" }, t("admin.configRestartHint")),
    );

    function editConfig(currentCfg) {
      const ta = el("textarea", { class: "editor mono" }, JSON.stringify(currentCfg, null, 2));
      const m = modal(t("admin.editConfigTitle"), [ta], [
        el("button", { class: "secondary", onclick: () => m.close() }, t("common.cancel")),
        el("button", {
          onclick: async () => {
            let next;
            try { next = JSON.parse(ta.value || "{}"); } catch (e) { alert(t("admin.badJson")); return; }
            await api("api/admin/config", {
              method: "PUT",
              headers: { "content-type": "application/json" },
              body: JSON.stringify(next),
            });
            m.close();
            await render();
          },
        }, t("common.save")),
      ]);
      ta.focus();
    }

    replaceMain(head, sys, actionsCard, cfgCard);
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

  setPage(page);
}
