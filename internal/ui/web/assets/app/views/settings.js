import { api } from "../api.js";
import { el } from "../dom.js";
import { state } from "../state.js";
import { getLang, LANGS, setLang, t } from "../i18n.js";
import { applyTheme, getTheme } from "../theme.js";

function row(label, node) {
  return el("div", { class: "kv" },
    el("div", { class: "k" }, label),
    el("div", {}, node),
  );
}

export async function renderSettings(root) {
  const wrap = el("div", {});

  // Theme
  const themeSel = el("select");
  themeSel.append(
    el("option", { value: "dark" }, t("settings.themeDark")),
    el("option", { value: "light" }, t("settings.themeLight")),
  );
  themeSel.value = getTheme();
  themeSel.addEventListener("change", () => applyTheme(themeSel.value));

  const langSel = el("select");
  for (const id of LANGS) langSel.append(el("option", { value: id }, t(`languages.${id}`)));
  langSel.value = getLang();
  langSel.addEventListener("change", () => setLang(langSel.value));

  const themeCard = el("div", { class: "card" },
    el("div", { class: "path" }, t("settings.title")),
    el("div", { class: "toolbar" },
      row(t("settings.themeMode"), themeSel),
      row(t("settings.language"), langSel),
    ),
  );

  // HTTPS (admin)
  const tlsCard = el("div", { class: "card", style: "margin-top:12px;" },
    el("div", { class: "path" }, t("settings.httpsTitle")),
  );

  const autostartCard = el("div", { class: "card", style: "margin-top:12px;" },
    el("div", { class: "path" }, t("settings.autostartTitle")),
  );

  const uninstallCard = el("div", { class: "card", style: "margin-top:12px;" },
    el("div", { class: "path" }, t("settings.uninstallTitle")),
  );

  if (!state.isAdmin) {
    tlsCard.append(el("div", { class: "path" }, t("settings.httpsOnlyAdmin")));
    autostartCard.append(el("div", { class: "path" }, t("settings.httpsOnlyAdmin")));
    uninstallCard.append(el("div", { class: "path" }, t("settings.httpsOnlyAdmin")));
  } else {
    let current = null;
    try {
      current = await api("api/admin/config");
    } catch {}

    const certPath = el("input", { class: "mono", placeholder: "/etc/ssl/.../fullchain.pem", style: "width:100%;" });
    const keyPath = el("input", { class: "mono", placeholder: "/etc/ssl/.../privkey.pem", style: "width:100%;" });

    const cert = el("textarea", { class: "editor mono", style: "height:160px; min-height:160px;" });
    const key = el("textarea", { class: "editor mono", style: "height:160px; min-height:160px; margin-top:10px;" });
    const status = el("div", { class: "path", style: "margin-top:10px;" },
      (current?.config?.tls_cert_file && current?.config?.tls_key_file)
        ? t("settings.httpsStatusEnabled", { cert: current.config.tls_cert_file, key: current.config.tls_key_file })
        : t("settings.httpsStatusDisabled"),
    );

    const btnEnable = el("button", {
      onclick: async () => {
        const cp = (certPath.value || "").trim();
        const kp = (keyPath.value || "").trim();
        const certPem = (cert.value || "").trim();
        const keyPem = (key.value || "").trim();

        const usingPaths = !!(cp || kp);
        if (usingPaths) {
          if (!cp || !kp) {
            alert(t("settings.httpsPathsRequired"));
            return;
          }
          if (!cp.startsWith("/") || !kp.startsWith("/")) {
            alert(t("settings.httpsPathsAbs"));
            return;
          }
        } else if (!certPem || !keyPem) {
          alert(t("settings.httpsRequired"));
          return;
        }
        btnEnable.disabled = true;
        try {
          await api("api/admin/tls", {
            method: "POST",
            headers: { "content-type": "application/json" },
            body: JSON.stringify(usingPaths
              ? { cert_path: cp, key_path: kp }
              : { cert_pem: certPem, key_pem: keyPem }),
          });
          status.textContent = t("settings.httpsSavedRestart");
          if (usingPaths) {
            // Keep paths as-is.
            cert.value = "";
            key.value = "";
          } else {
            cert.value = "";
            key.value = "";
          }
        } catch (e) {
          alert(e.message || String(e));
        } finally {
          btnEnable.disabled = false;
        }
      },
    }, t("settings.httpsEnable"));

    tlsCard.append(
      el("div", { class: "path" }, t("settings.httpsHelp")),
      el("div", { class: "path", style: "margin-top:10px;" }, t("settings.httpsCertPath")),
      certPath,
      el("div", { class: "path", style: "margin-top:10px;" }, t("settings.httpsKeyPath")),
      keyPath,
      el("div", { class: "path", style: "margin-top:10px;" }, t("settings.httpsOrPaste")),
      el("div", { class: "path", style: "margin-top:10px;" }, t("settings.httpsCert")),
      cert,
      el("div", { class: "path", style: "margin-top:10px;" }, t("settings.httpsKey")),
      key,
      el("div", { class: "toolbar", style: "margin-top:10px;" }, btnEnable),
      status,
    );

    // Autostart (systemd)
    const autoStatus = el("div", { class: "path", style: "margin-top:10px;" }, t("common.loading"));
    const autoHint = el("div", { class: "path", style: "margin-top:6px;" }, "");
    const autoBtn = el("button", { class: "secondary", disabled: "disabled" }, t("settings.autostartEnable"));

    async function reloadAutostart() {
      autoStatus.textContent = t("common.loading");
      autoHint.textContent = "";
      autoBtn.disabled = true;
      autoBtn.className = "secondary";
      try {
        const st = await api("api/admin/autostart");
        if (!st.supported) {
          autoStatus.textContent = t("settings.autostartUnsupported");
          autoHint.textContent = st.message || "";
          autoBtn.disabled = true;
          return;
        }
        if (!st.actions_enabled) {
          autoStatus.textContent = t("settings.autostartDisabledByConfig");
          autoHint.textContent = t("settings.autostartEnableHint");
          autoBtn.disabled = true;
          return;
        }

        const enabled = !!st.enabled;
        const active = !!st.active;
        const unitExists = !!st.unit_exists;

        autoStatus.textContent = t("settings.autostartStatus", {
          enabled: enabled ? t("common.yes") : t("common.no"),
          active: active ? t("common.yes") : t("common.no"),
          unit: st.unit_name || "",
          unit_exists: unitExists ? t("common.yes") : t("common.no"),
        });
        autoHint.textContent = st.message || "";
        autoBtn.disabled = false;
        autoBtn.textContent = enabled ? t("settings.autostartDisable") : t("settings.autostartEnable");
        autoBtn.className = enabled ? "danger" : "secondary";
        autoBtn.onclick = async () => {
          autoBtn.disabled = true;
          try {
            await api("api/admin/autostart", {
              method: "POST",
              headers: { "content-type": "application/json" },
              body: JSON.stringify({ enabled: !enabled }),
            });
          } catch (e) {
            alert(e.message || String(e));
          } finally {
            await reloadAutostart();
          }
        };
      } catch (e) {
        autoStatus.textContent = t("settings.autostartError");
        autoHint.textContent = e.message || String(e);
      }
    }

    autostartCard.append(
      el("div", { class: "path" }, t("settings.autostartHelp")),
      el("div", { class: "toolbar", style: "margin-top:10px;" }, autoBtn),
      autoStatus,
      autoHint,
    );
    await reloadAutostart();

    // Uninstall
    const uninstallHint = el("div", { class: "path" }, t("settings.uninstallHelp"));
    const uninstallBtn = el("button", {
      class: "danger",
      disabled: !current?.enable_admin_actions ? "disabled" : null,
      onclick: async () => {
        const token = prompt(t("settings.uninstallPrompt"), "");
        if (!token) return;
        if (String(token).trim().toUpperCase() !== "DELETE") {
          alert(t("settings.uninstallConfirmMismatch"));
          return;
        }
        uninstallBtn.disabled = true;
        try {
          const res = await api("api/admin/uninstall", {
            method: "POST",
            headers: { "content-type": "application/json" },
            body: JSON.stringify({ confirm: "DELETE" }),
          });
          const files = Array.isArray(res.files) ? res.files : [];
          alert(`${t("settings.uninstallStarted")}\n\n${files.join("\n")}`);
        } catch (e) {
          alert(e.message || String(e));
        } finally {
          uninstallBtn.disabled = false;
        }
      },
    }, t("settings.uninstallButton"));

    if (!current?.enable_admin_actions) {
      uninstallCard.append(el("div", { class: "path" }, t("settings.autostartEnableHint")));
    }
    uninstallCard.append(
      uninstallHint,
      el("div", { class: "toolbar", style: "margin-top:10px;" }, uninstallBtn),
    );
  }

  // Update (stub)
  const btnUpdate = el("button", { class: "secondary", onclick: () => alert(t("settings.updateTodo")) }, t("settings.updateStub"));
  const updCard = el("div", { class: "card", style: "margin-top:12px;" },
    el("div", { class: "path" }, t("settings.updateTitle")),
    el("div", { class: "toolbar" }, btnUpdate),
  );

  wrap.append(themeCard, tlsCard, autostartCard, uninstallCard, updCard);
  root.append(wrap);
}
