import { reactive } from 'vue';
import { normalizeMosaicDensity } from './mosaicLayout.js';

const values = reactive(new Map());
const prefix = 'quantumMosaicDensity:v1:';
const quantize = value => Math.round(normalizeMosaicDensity(value) * 100) / 100;
function read(value) {
  return value === null || value === '' ? 1 : quantize(value);
}
export function getMosaicDensity(username) {
  const user = username || '';
  if (!values.has(user)) {
    let value = null;
    try { value = localStorage.getItem(prefix + user); } catch { /* Local-only preference. */ }
    values.set(user, read(value));
  }
  return values.get(user);
}
export function setMosaicDensity(username, density) {
  const user = username || '';
  const value = quantize(density);
  values.set(user, value);
  try { localStorage.setItem(prefix + user, String(value)); } catch { /* Keep the live setting. */ }
}
window.addEventListener('storage', event => {
  if (event.key?.startsWith(prefix)) values.set(event.key.slice(prefix.length), read(event.newValue));
  else if (event.key === null) values.clear();
});
