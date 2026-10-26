(function () {
  "use strict";

  var supported = ["ru", "en"];
  var state = {
    lang: "ru",
    content: null
  };

  function getPath(object, path) {
    return path.split(".").reduce(function (value, key) {
      if (value === null || value === undefined) return undefined;
      return value[key];
    }, object);
  }

  function lookup(path) {
    if (!state.content) return undefined;
    if (path.indexOf("shared.") === 0) {
      return getPath(state.content.shared, path.slice("shared.".length));
    }
    return getPath(state.content[state.lang], path);
  }

  function text(value) {
    return typeof value === "string" ? value : "";
  }

  function applyTranslations() {
    document.documentElement.lang = state.lang;
    document.documentElement.dataset.lang = state.lang;
    document.body.dataset.lang = state.lang;

    var page = document.body.dataset.page || "index";
    var title = lookup(page + ".meta.title");
    if (title) document.title = title;

    var description = lookup(page + ".meta.description");
    var meta = document.querySelector('meta[name="description"]');
    if (description && meta) meta.setAttribute("content", description);

    document.querySelectorAll("[data-i18n]").forEach(function (node) {
      var value = lookup(node.dataset.i18n);
      if (typeof value === "string") node.innerHTML = value;
    });

    document.querySelectorAll("[data-i18n-text]").forEach(function (node) {
      var value = lookup(node.dataset.i18nText);
      if (typeof value === "string") node.textContent = value;
    });

    document.querySelectorAll("[data-i18n-placeholder]").forEach(function (node) {
      var value = lookup(node.dataset.i18nPlaceholder);
      if (typeof value === "string") node.setAttribute("placeholder", value);
    });

    document.querySelectorAll("[data-i18n-aria-label]").forEach(function (node) {
      var value = lookup(node.dataset.i18nAriaLabel);
      if (typeof value === "string") node.setAttribute("aria-label", value);
    });

    document.querySelectorAll("[data-i18n-title]").forEach(function (node) {
      var value = lookup(node.dataset.i18nTitle);
      if (typeof value === "string") node.setAttribute("title", value);
    });

    document.querySelectorAll("button[data-lang]").forEach(function (button) {
      var active = button.dataset.lang === state.lang;
      button.setAttribute("aria-pressed", active ? "true" : "false");
      button.classList.toggle("is-active", active);
    });

    document.querySelectorAll('input[name="language"]').forEach(function (input) {
      input.value = state.lang;
    });

    renderDownloads();
  }

  function renderDownloads() {
    var grid = document.getElementById("download-grid");
    if (!grid || !state.content) return;

    var groups = lookup("index.downloads.groups");
    var apps = lookup("index.downloads.apps");
    var getLabel = text(lookup("index.downloads.get"));
    var downloads = state.content.shared.downloads;

    if (!Array.isArray(groups) || !Array.isArray(apps)) return;

    grid.innerHTML = groups.map(function (group) {
      var links = apps.map(function (app) {
        var url = downloads[group.key] && downloads[group.key][app.key];
        if (!url) return "";
        return [
          '<a class="download-link" href="' + url + '" rel="noopener">',
          '<span class="download-app">' + text(app.name) + "</span>",
          '<span class="download-app-note">' + text(app.note) + "</span>",
          '<span class="download-action">' + getLabel + " ↓</span>",
          "</a>"
        ].join("");
      }).join("");

      return [
        '<article class="download-card">',
        '<div class="download-os"><span>' + text(group.os) + '</span><span class="download-arch">' + text(group.arch) + "</span></div>",
        '<p class="download-note">' + text(group.note) + "</p>",
        '<div class="download-links">' + links + "</div>",
        "</article>"
      ].join("");
    }).join("");
  }

  function setLanguage(lang) {
    if (supported.indexOf(lang) === -1) return;
    state.lang = lang;

    try {
      window.localStorage.setItem("noesis.lang", lang);
    } catch (error) {
      /* Storage can be unavailable in private mode; the query param still works. */
    }

    try {
      var url = new URL(window.location.href);
      if (lang === "ru") url.searchParams.delete("lang");
      else url.searchParams.set("lang", lang);
      window.history.replaceState(null, "", url.toString());
    } catch (error) {
      /* ignore */
    }

    applyTranslations();
  }

  function initialLanguage() {
    var fromQuery = new URLSearchParams(window.location.search).get("lang");
    if (supported.indexOf(fromQuery) !== -1) return fromQuery;

    try {
      var saved = window.localStorage.getItem("noesis.lang");
      if (supported.indexOf(saved) !== -1) return saved;
    } catch (error) {
      /* ignore */
    }

    return (navigator.language || "").toLowerCase().indexOf("en") === 0 ? "en" : "ru";
  }

  function showLoadError(error) {
    console.error("noesis: could not load content.json", error);
    var box = document.createElement("div");
    box.className = "i18n-error";
    box.innerHTML = "Не удалось загрузить <code>content.json</code>. Откройте сайт через HTTP-сервер, а не через <code>file://</code>.";
    document.body.prepend(box);
  }

  function init() {
    fetch("content.json", { cache: "no-cache" })
      .then(function (response) {
        if (!response.ok) throw new Error("HTTP " + response.status);
        return response.json();
      })
      .then(function (content) {
        state.content = content;
        state.lang = initialLanguage();
        applyTranslations();

        document.querySelectorAll("button[data-lang]").forEach(function (button) {
          button.addEventListener("click", function () {
            setLanguage(button.dataset.lang);
          });
        });
      })
      .catch(showLoadError);
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
