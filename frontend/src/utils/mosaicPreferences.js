// Quantum does not index image dimensions. Cache oriented preview ratios by source/path/mtime.
export function readMosaicRatios(username) {
  try {
    const value = JSON.parse(localStorage.getItem(`quantumMosaicRatios:${username}`) || '{}');
    return Object.fromEntries(Object.entries(value).filter(([,ratio]) => Number.isFinite(ratio) && ratio > 0).slice(-2048));
  } catch { return {}; }
}
export function saveMosaicRatios(username, ratios) {
  try {
    localStorage.setItem(`quantumMosaicRatios:${username}`, JSON.stringify(Object.fromEntries(Object.entries(ratios).slice(-2048))));
  } catch { /* Cache loss must not affect file access. */ }
}
