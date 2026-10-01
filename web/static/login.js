document.addEventListener('DOMContentLoaded', function () {
  var button = document.querySelector('.toggle-visibility');
  var password = document.getElementById('password');
  if (!button || !password) return;
  button.addEventListener('click', function () {
    var showing = password.type === 'text';
    password.type = showing ? 'password' : 'text';
    button.textContent = showing ? 'Show' : 'Hide';
    button.setAttribute('aria-label', showing ? 'Show password' : 'Hide password');
  });
});
