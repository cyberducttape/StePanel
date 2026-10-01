(() => {
  'use strict';
  window.StepanelUI = Object.freeze({
    setStatus(element, message, state = '') {
      if (!element) return;
      element.textContent = message;
      if (state) element.dataset.state = state;
      else delete element.dataset.state;
    },
  });
})();
