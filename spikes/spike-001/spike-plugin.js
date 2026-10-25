// DISPOSABLE SPIKE — NOT PRODUCTION CODE (SPIKE-001): proves whether a same-origin custom plugin
// listed in the user's settings is loaded when ALLOW_CUSTOM_PLUGINS is set in PreConfig.js.
Draw.loadPlugin(function (ui) {
  window.__spikePluginLoaded = true;
  window.__spikePluginUi = ui;
});
