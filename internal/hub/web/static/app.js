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

  // Auto-refresh on pages that ask for it (overview every 60 s, approvals
  // every 10 s). Pages with a [data-live] list (approvals) are checked in the
  // background and only reloaded when the list of pending requests changed;
  // if you have already ticked boxes or opened a menu, a banner appears
  // instead so your selection is not thrown away. Reloads are plain GETs, so a
  // page shown after submitting a form is never re-submitted.
  var r = parseInt(document.body.getAttribute("data-refresh") || "0", 10);
  if (r > 0) {
    var touched = false, bannerShown = false;
    document.addEventListener("change", function () { touched = true; }, true);
    document.addEventListener("toggle", function () { touched = true; }, true);
    var typing = function () {
      var a = document.activeElement;
      return a && ((a.tagName === "INPUT" && a.type !== "checkbox") || a.tagName === "TEXTAREA" || a.tagName === "SELECT");
    };
    var reload = function () { location.replace(location.pathname + location.search); };
    var ids = function (root) {
      return Array.prototype.map.call(root.querySelectorAll("[data-live] input[name=id]"), function (i) { return i.value; }).sort().join(",");
    };
    var live = document.querySelector("[data-live]");
    var current = ids(document);
    setInterval(function () {
      if (document.hidden || bannerShown) return;
      if (!live) { if (!typing()) reload(); return; }
      fetch(location.pathname + location.search, { credentials: "same-origin" })
        .then(function (res) { return res.ok ? res.text() : null; })
        .then(function (html) {
          if (!html) return;
          var doc = new DOMParser().parseFromString(html, "text/html");
          if (!doc.querySelector("[data-live]")) return; // e.g. the login page after the session ended
          if (ids(doc) === current) return;
          if (!touched && !typing()) { reload(); return; }
          bannerShown = true;
          var b = document.createElement("a");
          b.href = location.pathname + location.search;
          b.className = "notice ok";
          b.style.display = "block";
          b.textContent = "The list of requests changed. Click to refresh (your current selection will be reset).";
          live.parentNode.insertBefore(b, live);
        })
        .catch(function () {});
    }, r * 1000);
  }
})();
