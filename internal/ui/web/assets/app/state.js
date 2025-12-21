export const state = {
  csrf: "",
  me: "",
  role: "",
  isAdmin: false,
  canExec: false,
  canProcs: false,
  canFW: false,
  view: "dashboard",
};

export const views = [
  { id: "dashboard", titleKey: "tabs.dashboard" },
  { id: "files", titleKey: "tabs.files" },
  { id: "terminal", titleKey: "tabs.terminal", requiresExec: true },
  { id: "processes", titleKey: "tabs.processes" },
  { id: "firewall", titleKey: "tabs.firewall", requiresFW: true },
  { id: "settings", titleKey: "tabs.settings" },
  { id: "admin", titleKey: "tabs.admin", requiresAdmin: true },
];
