/** Pure gate for "should this card start loading its thumbnail now".
 *
 * The fetch itself is intentionally not queued: the browser and the server own
 * connection and generation limits. This gate only answers whether starting a
 * request now is useful, so a fling does not fan out requests for rows that are
 * already behind the user.
 *
 * Values are provisional: see checks/thumbnail-prefetch-* evidence before
 * treating them as UX guarantees.
 */
export const SLOW_SCROLL_PX_PER_MS = 0.35;

export function shouldLoadThumbnail({
  inBand,
  wanted,
  interactionActive = false,
  scrolling = false,
  velocity = 0,
  slowThreshold = SLOW_SCROLL_PX_PER_MS,
}) {
  if (!inBand || wanted || interactionActive) return false;
  // While the user is still moving, only a slow browse may start new work.
  // There is no center-band restriction: the 600px prefetch region is the
  // whole admission test.
  if (scrolling && velocity > slowThreshold) return false;
  return true;
}
