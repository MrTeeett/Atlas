import { api } from "../api.js";
import { el } from "../dom.js";
import { t } from "../i18n.js";
import { state } from "../state.js";

function pill(text) {
  return el("span", { class: "pill" }, text);
}

function dangerText(text) {
  return el("div", { style: "color:var(--danger); margin-top:10px;" }, text);
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

function parsePortsInput(s) {
  s = String(s || "").trim();
  if (!s) return null;
  if (/^\d+$/.test(s)) return s;
  if (/^\d+\s*-\s*\d+$/.test(s)) return s.replace(/\s+/g, "");
  return null;
}

export async function renderFirewall(root) {
  const wrap = el("div", { class: "card" });
  const head = el("div", { class: "pm-head" },
    el("div", { class: "pm-title" }, t("firewall.title")),
    pill(`${t("firewall.web")}: ${state.me || "—"}`),
    el("span", { class: "pm-spacer" }),
  );
  const body = el("div");
  wrap.append(head, body);
  root.append(wrap);

  async function load() {
    body.replaceChildren(el("div", { class: "path" }, t("common.loading")));
    const [st, rules] = await Promise.all([
      api("api/firewall/status"),
      api("api/firewall/rules").catch(() => ({ enabled: false, rules: [] })),
    ]);
    render(st, rules);
  }

  function renderStatus(st) {
    const enabled = !!st.db_enabled;
    const active = !!st.active;
    const tool = st.tool || "—";
    const extTool = st.external_tool || "";
    const isSystemTool = tool === "firewall-cmd" || tool === "ufw";

    const statusRow = el("div", { class: "toolbar" },
      pill(`${t("firewall.tool")}: ${tool}`),
      pill(`${t(isSystemTool ? "firewall.systemActive" : "firewall.active")}: ${active ? t("common.yes") : t("common.no")}`),
      pill(`${t(isSystemTool ? "firewall.atlasEnabled" : "firewall.enabled")}: ${enabled ? t("common.yes") : t("common.no")}`),
      pill(`${t("firewall.euid")}: ${st.euid}`),
      pill(st.has_sudo ? `${t("firewall.sudo")}: ${t("common.yes")}` : `${t("firewall.sudo")}: ${t("common.no")}`),
      el("span", { class: "pm-spacer" }),
      el("button", {
        class: enabled ? "danger" : "",
        onclick: async () => {
          await api("api/firewall/enabled", {
            method: "POST",
            headers: { "content-type": "application/json" },
            body: JSON.stringify({ enabled: !enabled }),
          });
          await load();
        },
      }, enabled
        ? (isSystemTool ? t("firewall.disableAtlas") : t("firewall.disable"))
        : (isSystemTool ? t("firewall.enableAtlas") : t("firewall.enable"))),
      el("button", {
        class: "secondary",
        onclick: async () => { await api("api/firewall/apply", { method: "POST" }); await load(); },
        disabled: !enabled ? "disabled" : null,
      }, t("firewall.apply")),
      el("button", { class: "secondary", onclick: () => load() }, t("common.refresh")),
    );

    const notes = [];
    if (!st.config_enabled) notes.push(dangerText(t("firewall.configDisabled")));
    if (st.error) notes.push(dangerText(st.error));
    if (isSystemTool) {
      notes.push(el("div", { class: "path" }, t("firewall.atlasNote", { tool })));
    }
    if (extTool) {
      if (st.external_error) {
        notes.push(dangerText(t("firewall.externalError", { tool: extTool, err: st.external_error })));
      } else {
        notes.push(el("div", { class: "path" }, t("firewall.externalActive", { tool: extTool })));
      }
    }

    return el("div", { class: "card", style: "margin-top:12px;" },
      el("div", { class: "path" }, t("firewall.statusTitle")),
      statusRow,
      notes.length ? el("div", {}, ...notes) : el("div", { style: "height:0px;" }),
    );
  }

  function ruleRow(r, onToggle, onEdit, onDelete, onPortLookup) {
    const hasService = !!(r.service && String(r.service).trim());
    const ports = r.port_from === r.port_to ? String(r.port_from) : `${r.port_from}-${r.port_to}`;
    const descr = hasService
      ? `service:${r.service}`
      : (r.type === "redirect" ? `${ports} → ${r.to_port}` : ports);
    return el("tr", {},
      el("td", {}, el("input", {
        type: "checkbox",
        checked: !!r.enabled,
        onchange: (e) => onToggle(!!e.target.checked),
      })),
      el("td", { class: "mono" }, r.type),
      el("td", { class: "mono" }, r.proto),
      el("td", { class: "mono" }, descr),
      el("td", {}, r.comment || ""),
      el("td", { style: "text-align:right; white-space:nowrap;" },
        hasService ? null : el("button", { class: "secondary", onclick: () => onPortLookup(r.type === "redirect" ? r.to_port : r.port_from, r.proto) }, t("firewall.whoUsesPort")),
        hasService ? null : " ",
        el("button", { class: "secondary", onclick: onEdit }, t("common.edit")),
        " ",
        el("button", { class: "danger", onclick: onDelete }, t("common.delete")),
      ),
    );
  }

  function renderExternalRules(rulesResp) {
    const tool = rulesResp.external_tool;
    if (!tool) return null;
    const rules = rulesResp.external_rules || [];
    const title = t("firewall.externalTitle", { tool });

    let note = null;
    if (rulesResp.external_error) {
      note = dangerText(t("firewall.externalError", { tool, err: rulesResp.external_error }));
    } else if (!rulesResp.external_active) {
      note = el("div", { class: "path" }, t("firewall.externalInactive", { tool }));
    } else if (!rules.length) {
      note = el("div", { class: "path" }, t("firewall.externalEmpty", { tool }));
    }

    const table = el("table", {},
      el("thead", {}, el("tr", {},
        el("th", {}, t("firewall.externalThTo")),
        el("th", {}, t("firewall.externalThAction")),
        el("th", {}, t("firewall.externalThFrom")),
        el("th", { style: "text-align:right" }, t("firewall.externalThV6")),
      )),
    );
    const tbody = el("tbody");
    table.append(tbody);
    for (const r of rules) {
      tbody.append(el("tr", {},
        el("td", { class: "mono" }, r.to || ""),
        el("td", {}, r.action || ""),
        el("td", { class: "mono" }, r.from || ""),
        el("td", { style: "text-align:right" }, r.v6 ? "v6" : ""),
      ));
    }

    return el("div", { class: "card", style: "margin-top:12px;" },
      el("div", { class: "path" }, title),
      note || el("div", { style: "height:0px;" }),
      rules.length ? table : null,
    );
  }

  function renderRules(st, rulesResp) {
    const enabled = !!rulesResp.enabled;
    const rules = rulesResp.rules || [];
    const tool = st.tool || "";
    const isSystemTool = tool === "firewall-cmd" || tool === "ufw";
    const allowService = tool === "firewall-cmd" || tool === "ufw";

    const addBtn = el("button", {
      onclick: () => openAddEdit(),
      disabled: !st.config_enabled ? "disabled" : null,
    }, t("firewall.addRule"));

    const table = el("table", {},
      el("thead", {}, el("tr", {},
        el("th", {}, t("firewall.thOn")),
        el("th", {}, t("firewall.thType")),
        el("th", {}, t("firewall.thProto")),
        el("th", {}, t("firewall.thPorts")),
        el("th", {}, t("firewall.thComment")),
        el("th", { style: "text-align:right" }, t("firewall.thActions")),
      )),
    );
    const tbody = el("tbody");
    table.append(tbody);

    async function toggleRule(rule, value) {
      await api(`api/firewall/rules/${encodeURIComponent(rule.id)}/toggle`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ enabled: value }),
      });
      await load();
    }

    async function deleteRule(rule) {
      if (!confirm(t("firewall.deleteRuleConfirm", { id: rule.id }))) return;
      await api(`api/firewall/rules/${encodeURIComponent(rule.id)}`, { method: "DELETE" });
      await load();
    }

    async function editRule(rule, data) {
      await api(`api/firewall/rules/${encodeURIComponent(rule.id)}`, {
        method: "PUT",
        headers: { "content-type": "application/json" },
        body: JSON.stringify(data),
      });
      await load();
    }

    function openAddEdit(rule) {
      const typeSel = el("select");
      for (const v of ["allow", "deny", "redirect"]) typeSel.append(el("option", { value: v }, v));
      const protoSel = el("select");
      for (const v of ["tcp", "udp"]) protoSel.append(el("option", { value: v }, v));
      const portsIn = el("input", { class: "mono", placeholder: t("firewall.portsPlaceholder") });
      const toPortIn = el("input", { class: "mono", type: "number", placeholder: t("firewall.toPortPlaceholder"), min: "1", max: "65535" });
      const serviceIn = el("input", { class: "mono", placeholder: t("firewall.servicePlaceholder") });
      const enabledIn = el("input", { type: "checkbox" });
      const commentIn = el("input", { placeholder: t("firewall.commentPlaceholder") });

      function syncVisibility() {
        const useService = allowService && !!serviceIn.value.trim();
        portsIn.disabled = useService;
        toPortIn.style.display = typeSel.value === "redirect" && !useService ? "" : "none";
      }
      typeSel.addEventListener("change", syncVisibility);
      serviceIn.addEventListener("input", syncVisibility);

      if (rule) {
        typeSel.value = rule.type || "allow";
        protoSel.value = rule.proto || "tcp";
        if (rule.service) {
          portsIn.value = "";
          serviceIn.value = rule.service;
        } else {
          portsIn.value = rule.port_from === rule.port_to ? String(rule.port_from) : `${rule.port_from}-${rule.port_to}`;
        }
        toPortIn.value = rule.to_port || "";
        enabledIn.checked = !!rule.enabled;
        commentIn.value = rule.comment || "";
      } else {
        typeSel.value = "allow";
        protoSel.value = "tcp";
        enabledIn.checked = true;
      }
      if (!allowService) {
        serviceIn.disabled = true;
        serviceIn.value = "";
      }
      syncVisibility();

      const form = el("div", { class: "split" },
        el("div", {},
          el("div", { class: "path" }, t("firewall.ruleTitle")),
          el("div", { class: "toolbar" }, el("span", { class: "path" }, t("firewall.type")), typeSel),
          el("div", { class: "toolbar" }, el("span", { class: "path" }, t("firewall.proto")), protoSel),
          el("div", { class: "toolbar" }, el("span", { class: "path" }, t("firewall.ports")), portsIn),
          el("div", { class: "toolbar" }, el("span", { class: "path" }, t("firewall.redirectTo")), toPortIn),
          el("div", { class: "toolbar" }, el("span", { class: "path" }, t("firewall.serviceLabel")), serviceIn),
        ),
        el("div", {},
          el("div", { class: "path" }, t("firewall.optionsTitle")),
          el("div", { class: "toolbar" }, enabledIn, el("span", { class: "path" }, t("firewall.enabledLabel"))),
          el("div", { class: "toolbar" }, el("span", { class: "path" }, t("firewall.commentLabel")), commentIn),
          el("div", { class: "path" }, t("firewall.portsHelp")),
        ),
      );

      const m = modal(rule ? t("firewall.editRuleTitle") : t("firewall.addRuleTitle"), [form], [
        el("button", { class: "secondary", onclick: () => m.close() }, t("common.cancel")),
        el("button", {
          onclick: async () => {
            const service = serviceIn.value.trim();
            if (service && typeSel.value === "redirect") {
              alert(t("firewall.badServiceRedirect"));
              return;
            }
            let ports = "";
            if (!service) {
              ports = parsePortsInput(portsIn.value);
              if (!ports) { alert(t("firewall.badPorts")); return; }
            }
            const payload = {
              type: typeSel.value,
              proto: protoSel.value,
              ports,
              to_port: Number(toPortIn.value || 0),
              service,
              comment: commentIn.value || "",
            };
            try {
              if (rule) {
                await editRule(rule, payload);
                // enabled toggle is separate
                if (!!enabledIn.checked !== !!rule.enabled) await toggleRule(rule, !!enabledIn.checked);
              } else {
                await api("api/firewall/rules", {
                  method: "POST",
                  headers: { "content-type": "application/json" },
                  body: JSON.stringify({ ...payload, enabled: !!enabledIn.checked, position: -1 }),
                });
              }
              m.close();
              await load();
            } catch (e) {
              alert(e.message || String(e));
            }
          },
        }, rule ? t("common.save") : t("common.add")),
      ]);
      portsIn.focus();
    }

    function openPortLookup(port, proto) {
      const portIn = el("input", { class: "mono", type: "number", min: "1", max: "65535", value: String(port || "") });
      const protoSel = el("select");
      for (const v of ["tcp", "udp", "any"]) protoSel.append(el("option", { value: v }, v));
      protoSel.value = proto || "tcp";
      const out = el("div", { style: "margin-top:10px;" }, "");
      const m = modal(t("firewall.portUsageTitle"), [
        el("div", { class: "toolbar" },
          el("span", { class: "path" }, t("firewall.port")), portIn,
          el("span", { class: "path" }, t("firewall.proto")), protoSel,
          el("button", {
            class: "secondary",
            onclick: async () => {
              out.replaceChildren(el("div", { class: "path" }, t("common.loading")));
              try {
                const res = await api(`api/ports/usage?port=${encodeURIComponent(portIn.value)}&proto=${encodeURIComponent(protoSel.value)}`);
                if (res.error) {
                  out.replaceChildren(dangerText(res.error));
                  return;
                }
                const tbl = el("table", {},
                  el("thead", {}, el("tr", {},
                    el("th", {}, t("firewall.thProto")),
                    el("th", {}, t("firewall.thLocal")),
                    el("th", {}, t("firewall.thPID")),
                    el("th", {}, t("firewall.thProcess")),
                  )),
                );
                const tb = el("tbody");
                for (const it of res.items || []) {
                  tb.append(el("tr", {},
                    el("td", { class: "mono" }, it.proto || ""),
                    el("td", { class: "mono" }, it.local || ""),
                    el("td", { class: "mono" }, it.pid ? String(it.pid) : "—"),
                    el("td", { class: "mono" }, it.process || "—"),
                  ));
                }
                tbl.append(tb);
                out.replaceChildren(tbl);
              } catch (e) {
                out.replaceChildren(dangerText(e.message || String(e)));
              }
            },
          }, t("firewall.check")),
        ),
        out,
      ], [
        el("button", { class: "secondary", onclick: () => m.close() }, t("common.close")),
      ]);
      // Auto-run
      setTimeout(() => m.card.querySelector("button.secondary")?.click(), 10);
    }

    for (const r of rules) {
      tbody.append(ruleRow(
        r,
        (v) => toggleRule(r, v).catch(e => alert(e.message || String(e))),
        () => openAddEdit(r),
        () => deleteRule(r).catch(e => alert(e.message || String(e))),
        (p, proto) => openPortLookup(p, proto),
      ));
    }

    return el("div", { class: "card", style: "margin-top:12px;" },
      el("div", { class: "path" }, t("firewall.rulesTitle")),
      el("div", { class: "toolbar" },
        addBtn,
        pill(t("firewall.count", { n: rules.length })),
        pill(t(isSystemTool ? "firewall.atlasEnabledShort" : "firewall.firewallEnabled", { enabled: enabled ? t("common.yes") : t("common.no") })),
      ),
      table,
    );
  }

  function render(st, rules) {
    body.replaceChildren(
      renderStatus(st),
      renderExternalRules(rules),
      renderRules(st, rules),
    );
  }

  await load();
}
