// Claude Server Agent control panel: progressive enhancements only. Every
// page works without this file.
(function () {
  "use strict";
  var root = document.documentElement;

  // Theme (dark default, light optional), remembered per browser.
  function applyTheme(t) { if (t === "light") root.setAttribute("data-theme", "light"); else root.removeAttribute("data-theme"); }
  applyTheme(localStorage.getItem("csa:theme"));
  document.addEventListener("click", function (e) {
    var t = e.target.closest("[data-theme-toggle]");
    if (t) {
      var next = root.getAttribute("data-theme") === "light" ? "dark" : "light";
      localStorage.setItem("csa:theme", next); applyTheme(next); e.preventDefault();
    }
    var m = e.target.closest("[data-menu-toggle]");
    if (m || e.target.closest(".sidebar-backdrop")) {
      document.querySelector(".app-sidebar").classList.toggle("open");
      document.querySelector(".sidebar-backdrop").classList.toggle("open");
    }
    var c = e.target.closest("[data-copy]");
    if (c) {
      var el = document.getElementById(c.getAttribute("data-copy"));
      if (el && navigator.clipboard) { navigator.clipboard.writeText(el.textContent.trim()); var old = c.textContent; c.textContent = "Copied"; setTimeout(function () { c.textContent = old; }, 1500); }
    }
  });

  // Show times in the viewer's local timezone.
  function fmt(d, mode) {
    if (mode === "date") return d.toLocaleDateString();
    if (mode === "rel") {
      var s = Math.round((Date.now() - d.getTime()) / 1000);
      if (s < 60) return "just now";
      if (s < 3600) return Math.round(s / 60) + " min ago";
      if (s < 86400) return Math.round(s / 3600) + " h ago";
      return Math.round(s / 86400) + " d ago";
    }
    return d.toLocaleString([], { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" });
  }
  document.querySelectorAll("time[datetime]").forEach(function (t) {
    var d = new Date(t.getAttribute("datetime"));
    if (!isNaN(d)) { t.title = d.toLocaleString(); t.textContent = fmt(d, t.getAttribute("data-fmt")); }
  });

  // Confirm dangerous buttons.
  document.querySelectorAll("form[data-confirm]").forEach(function (f) {
    f.addEventListener("submit", function (e) { if (!confirm(f.getAttribute("data-confirm"))) e.preventDefault(); });
  });

  // Select-all checkbox for approvals.
  document.querySelectorAll("[data-check-all]").forEach(function (box) {
    box.addEventListener("change", function () {
      document.querySelectorAll("input[name=id]").forEach(function (c) { c.checked = box.checked; });
    });
  });

  // Instant table filter (the server-side search still works without JS).
  document.querySelectorAll("input[data-filter]").forEach(function (inp) {
    var table = document.getElementById(inp.getAttribute("data-filter"));
    if (!table) return;
    inp.addEventListener("input", function () {
      var q = inp.value.toLowerCase();
      table.querySelectorAll("tbody tr").forEach(function (tr) { tr.style.display = tr.textContent.toLowerCase().indexOf(q) >= 0 ? "" : "none"; });
    });
  });

  // Gentle auto-refresh on pages that ask for it (overview, approvals) unless the user is typing.
  var r = document.body.getAttribute("data-refresh");
  if (r) setInterval(function () {
    var a = document.activeElement;
    if (!a || (a.tagName !== "INPUT" && a.tagName !== "TEXTAREA" && a.tagName !== "SELECT")) location.reload();
  }, parseInt(r, 10) * 1000);
})();
