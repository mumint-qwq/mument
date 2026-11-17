import { reactive } from 'vue';

const sources = reactive(new Map());
const prefix = 'quantumMosaicImageSource:';
export function normalizeMosaicImageSource(value) {
  return value === 'original' ? 'original' : 'thumbnail';
}
export function getMosaicImageSource(username) {
  const user = username || '';
  if (!sources.has(user)) {
    let value;
    try { value = localStorage.getItem(prefix + user); } catch { /* Private browsing. */ }
    sources.set(user, normalizeMosaicImageSource(value));
  }
  return sources.get(user);
}
export function setMosaicImageSource(username, value) {
  const user = username || '';
  const source = normalizeMosaicImageSource(value);
  sources.set(user, source);
  try { localStorage.setItem(prefix + user, source); } catch { /* The live selection still works. */ }
}
window.addEventListener('storage', event => {
  if (event.key?.startsWith(prefix)) {
    sources.set(event.key.slice(prefix.length), normalizeMosaicImageSource(event.newValue));
  } else if (event.key === null) {
    sources.clear();
  }
});
